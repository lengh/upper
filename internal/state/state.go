// Package state holds upper's in-memory view of the account: guilds, channels,
// DMs, read states and a bounded window of recent messages per channel.
//
// It is fed by gateway events and REST responses and queried by the UI. All
// methods are safe for concurrent use. Memory stays bounded no matter how
// many guilds or busy channels the account has: only channels that were
// opened keep message history, each capped at MaxMessages, and the least
// recently viewed histories are evicted beyond MaxChannels.
package state

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lengh/upper/internal/discord"
)

type Snowflake = discord.Snowflake

// Limits for the message cache.
var (
	MaxMessages = 400
	MaxChannels = 64
)

type guild struct {
	discord.Guild
	roles   map[Snowflake]discord.Role
	myRoles []Snowflake
	members map[Snowflake]memberInfo // nick/role cache from seen messages
}

type memberInfo struct {
	nick  string
	roles []Snowflake
}

type history struct {
	msgs     []discord.Message // ascending by ID
	complete bool              // reached the first message of the channel
	loaded   bool              // initial page fetched
	used     time.Time
}

type readState struct {
	lastAcked Snowflake
	mentions  int
}

type State struct {
	mu sync.RWMutex

	me         discord.User
	guilds     map[Snowflake]*guild
	guildOrder []Snowflake
	channels   map[Snowflake]*discord.Channel
	byGuild    map[Snowflake]map[Snowflake]*discord.Channel
	users      map[Snowflake]discord.User
	friends    []Snowflake
	reads      map[Snowflake]*readState
	histories  map[Snowflake]*history
	typing     map[Snowflake]map[Snowflake]time.Time

	// Notification settings: muted guilds/channels (channel mutes may name a
	// category, muting its children) and guilds suppressing @everyone.
	mutedGuilds   map[Snowflake]bool
	mutedChannels map[Snowflake]bool
	noEveryone    map[Snowflake]bool
}

func New() *State {
	s := &State{}
	s.reset()
	return s
}

func (s *State) reset() {
	s.guilds = map[Snowflake]*guild{}
	s.guildOrder = nil
	s.channels = map[Snowflake]*discord.Channel{}
	s.byGuild = map[Snowflake]map[Snowflake]*discord.Channel{}
	s.users = map[Snowflake]discord.User{}
	s.friends = nil
	s.reads = map[Snowflake]*readState{}
	s.typing = map[Snowflake]map[Snowflake]time.Time{}
	s.mutedGuilds = map[Snowflake]bool{}
	s.mutedChannels = map[Snowflake]bool{}
	s.noEveryone = map[Snowflake]bool{}
	if s.histories == nil {
		s.histories = map[Snowflake]*history{}
	}
}

// Change describes what an event touched so the UI redraws only that.
type Change struct {
	Tree      bool      // channel list / unread markers changed
	Channel   Snowflake // messages of this channel changed
	Typing    Snowflake // typing indicator of this channel changed
	Mention   bool      // a new message mentions the user
	Ready     bool      // full state (re)loaded
	Guild     Snowflake // guild whose channel set changed
	NewMsg    *discord.Message
	Confirmed Snowflake // nonce of one of our own messages that arrived
}

// Apply updates the state from a dispatch event.
func (s *State) Apply(ev discord.Event) (Change, error) {
	var c Change
	if ev.Type == "READY" {
		// READY can be megabytes; decode it before taking the lock so the
		// UI keeps reading the old state meanwhile.
		var r discord.Ready
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			return c, err
		}
		s.mu.Lock()
		s.loadReady(&r)
		s.mu.Unlock()
		c.Ready, c.Tree = true, true
		return c, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Type {

	case "GUILD_CREATE", "GUILD_UPDATE":
		var g discord.Guild
		if err := json.Unmarshal(ev.Data, &g); err != nil {
			return c, err
		}
		s.addGuild(g, ev.Type == "GUILD_UPDATE")
		c.Tree, c.Guild = true, g.ID

	case "GUILD_DELETE":
		var d discord.GuildDelete
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return c, err
		}
		if d.Unavailable {
			if g := s.guilds[d.ID]; g != nil {
				g.Unavailable = true
			}
		} else {
			s.removeGuild(d.ID)
		}
		c.Tree, c.Guild = true, d.ID

	case "GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE":
		var r discord.GuildRoleEvent
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			return c, err
		}
		if g := s.guilds[r.GuildID]; g != nil {
			g.roles[r.Role.ID] = r.Role
		}
		c.Tree, c.Guild = true, r.GuildID

	case "GUILD_ROLE_DELETE":
		var r discord.GuildRoleEvent
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			return c, err
		}
		if g := s.guilds[r.GuildID]; g != nil {
			delete(g.roles, r.RoleID)
		}
		c.Tree, c.Guild = true, r.GuildID

	case "GUILD_MEMBER_UPDATE":
		var m discord.GuildMemberUpdate
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			return c, err
		}
		if g := s.guilds[m.GuildID]; g != nil {
			g.members[m.User.ID] = memberInfo{nick: m.Nick, roles: m.Roles}
			if m.User.ID == s.me.ID {
				g.myRoles = m.Roles
				c.Tree, c.Guild = true, m.GuildID
			}
		}

	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "THREAD_CREATE", "THREAD_UPDATE":
		var ch discord.Channel
		if err := json.Unmarshal(ev.Data, &ch); err != nil {
			return c, err
		}
		s.addChannel(ch)
		c.Tree, c.Guild = true, ch.GuildID

	case "CHANNEL_DELETE", "THREAD_DELETE":
		var ch discord.Channel
		if err := json.Unmarshal(ev.Data, &ch); err != nil {
			return c, err
		}
		s.removeChannel(ch.ID)
		c.Tree, c.Guild = true, ch.GuildID

	case "CHANNEL_UNREAD_UPDATE":
		// Unread markers for large guilds we aren't subscribed to.
		var u struct {
			Updates []struct {
				ID            Snowflake `json:"id"`
				LastMessageID Snowflake `json:"last_message_id"`
			} `json:"channel_unread_updates"`
		}
		if err := json.Unmarshal(ev.Data, &u); err != nil {
			return c, err
		}
		for _, up := range u.Updates {
			if ch := s.channels[up.ID]; ch != nil && up.LastMessageID > ch.LastMessageID {
				ch.LastMessageID = up.LastMessageID
			}
		}
		c.Tree = true

	case "MESSAGE_CREATE":
		var m discord.Message
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			return c, err
		}
		c = s.addMessage(m)

	case "MESSAGE_UPDATE":
		c.Channel = s.updateMessage(ev.Data)

	case "MESSAGE_DELETE":
		var d discord.MessageDelete
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return c, err
		}
		if s.deleteMessages(d.ChannelID, d.ID) {
			c.Channel = d.ChannelID
		}

	case "MESSAGE_DELETE_BULK":
		var d discord.MessageDeleteBulk
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return c, err
		}
		if s.deleteMessages(d.ChannelID, d.IDs...) {
			c.Channel = d.ChannelID
		}

	case "MESSAGE_ACK":
		var a discord.MessageAck
		if err := json.Unmarshal(ev.Data, &a); err != nil {
			return c, err
		}
		rs := s.read(a.ChannelID)
		if a.MessageID > rs.lastAcked {
			rs.lastAcked = a.MessageID
		}
		rs.mentions = 0
		c.Tree = true

	case "USER_GUILD_SETTINGS_UPDATE":
		var gs discord.GuildSettings
		if err := json.Unmarshal(ev.Data, &gs); err != nil {
			return c, err
		}
		s.applySettings(gs)
		c.Tree = true

	case "TYPING_START":
		var t discord.TypingStart
		if err := json.Unmarshal(ev.Data, &t); err != nil {
			return c, err
		}
		if t.UserID == s.me.ID {
			break
		}
		if t.Member != nil && t.Member.User != nil {
			s.users[t.UserID] = *t.Member.User
			if g := s.guilds[t.GuildID]; g != nil {
				g.members[t.UserID] = memberInfo{nick: t.Member.Nick, roles: t.Member.Roles}
			}
		}
		m := s.typing[t.ChannelID]
		if m == nil {
			m = map[Snowflake]time.Time{}
			s.typing[t.ChannelID] = m
		}
		m[t.UserID] = time.Now().Add(10 * time.Second)
		c.Typing = t.ChannelID
	}
	return c, nil
}

func (s *State) loadReady(r *discord.Ready) {
	keep := s.histories
	s.reset()
	s.me = r.User
	s.users[r.User.ID] = r.User
	for _, u := range r.Users {
		s.users[u.ID] = u
	}
	for _, rel := range r.Relationships {
		if rel.User.ID != 0 {
			s.users[rel.User.ID] = rel.User
		}
		if rel.Type == 1 {
			s.friends = append(s.friends, rel.User.ID)
		}
	}
	for _, g := range r.Guilds {
		s.addGuild(g, false)
	}
	for i := range r.PrivateChannels {
		s.addChannel(r.PrivateChannels[i])
	}
	for _, gs := range r.GuildSettings() {
		s.applySettings(gs)
	}
	for _, rs := range r.ReadStates() {
		if rs.ReadStateType != 0 {
			continue
		}
		s.reads[rs.ID] = &readState{lastAcked: rs.LastMessageID, mentions: rs.MentionCount}
	}

	// Sidebar order from user settings when available.
	if us := r.UserSettings; us != nil {
		var order []Snowflake
		for _, f := range us.GuildFolders {
			order = append(order, f.GuildIDs...)
		}
		if len(order) == 0 {
			order = us.GuildPositions
		}
		if len(order) > 0 {
			s.reorderGuilds(order)
		}
	}

	// Keep cached history for channels that still exist; after a
	// re-identify the UI refetches to fill any gap.
	for id, h := range keep {
		if _, ok := s.channels[id]; ok {
			h.loaded = false
			s.histories[id] = h
		}
	}
}

func (s *State) applySettings(gs discord.GuildSettings) {
	s.mutedGuilds[gs.GuildID] = gs.Muted
	s.noEveryone[gs.GuildID] = gs.SuppressEveryone
	for _, o := range gs.ChannelOverrides {
		s.mutedChannels[o.ChannelID] = o.Muted
	}
}

func (s *State) muted(ch *discord.Channel) bool {
	if s.mutedChannels[ch.ID] || ch.GuildID != 0 && s.mutedGuilds[ch.GuildID] {
		return true
	}
	if ch.ParentID != 0 {
		if s.mutedChannels[ch.ParentID] {
			return true
		}
		// A thread's parent channel may sit in a muted category.
		if p := s.channels[ch.ParentID]; p != nil && p.ParentID != 0 && s.mutedChannels[p.ParentID] {
			return true
		}
	}
	return false
}

func (s *State) reorderGuilds(order []Snowflake) {
	pos := make(map[Snowflake]int, len(order))
	for i, id := range order {
		pos[id] = i + 1
	}
	sort.SliceStable(s.guildOrder, func(i, j int) bool {
		pi, pj := pos[s.guildOrder[i]], pos[s.guildOrder[j]]
		if pi == 0 || pj == 0 {
			return pi != 0 && pj == 0
		}
		return pi < pj
	})
}

func (s *State) addGuild(dg discord.Guild, update bool) {
	dg.Normalize()
	g := s.guilds[dg.ID]
	if g == nil {
		g = &guild{members: map[Snowflake]memberInfo{}}
		s.guilds[dg.ID] = g
		s.guildOrder = append(s.guildOrder, dg.ID)
	}
	if update {
		// GUILD_UPDATE carries guild properties and roles but no channels.
		g.Name, g.OwnerID = dg.Name, dg.OwnerID
		if dg.Roles != nil {
			g.roles = map[Snowflake]discord.Role{}
			for _, r := range dg.Roles {
				g.roles[r.ID] = r
			}
		}
		return
	}
	channels, threads, members := dg.Channels, dg.Threads, dg.Members
	dg.Channels, dg.Threads, dg.Members = nil, nil, nil
	g.Guild = dg
	g.roles = make(map[Snowflake]discord.Role, len(dg.Roles))
	for _, r := range dg.Roles {
		g.roles[r.ID] = r
	}
	g.Roles = nil
	for _, m := range members {
		if m.User != nil {
			s.users[m.User.ID] = *m.User
			g.members[m.User.ID] = memberInfo{nick: m.Nick, roles: m.Roles}
			if m.User.ID == s.me.ID {
				g.myRoles = m.Roles
			}
		} else if m.UserID == s.me.ID {
			g.myRoles = m.Roles
		}
	}
	for i := range channels {
		channels[i].GuildID = dg.ID
		s.addChannel(channels[i])
	}
	for i := range threads {
		threads[i].GuildID = dg.ID
		s.addChannel(threads[i])
	}
}

func (s *State) removeGuild(id Snowflake) {
	delete(s.guilds, id)
	s.guildOrder = slices.DeleteFunc(s.guildOrder, func(g Snowflake) bool { return g == id })
	for cid := range s.byGuild[id] {
		delete(s.channels, cid)
		delete(s.histories, cid)
	}
	delete(s.byGuild, id)
}

func (s *State) addChannel(ch discord.Channel) {
	for _, u := range ch.Recipients {
		s.users[u.ID] = u
	}
	if len(ch.RecipientIDs) == 0 {
		for _, u := range ch.Recipients {
			ch.RecipientIDs = append(ch.RecipientIDs, u.ID)
		}
	}
	ch.Recipients = nil
	if old := s.channels[ch.ID]; old != nil && old.LastMessageID > ch.LastMessageID {
		ch.LastMessageID = old.LastMessageID
	}
	s.channels[ch.ID] = &ch
	if ch.GuildID != 0 {
		m := s.byGuild[ch.GuildID]
		if m == nil {
			m = map[Snowflake]*discord.Channel{}
			s.byGuild[ch.GuildID] = m
		}
		m[ch.ID] = &ch
	}
}

func (s *State) removeChannel(id Snowflake) {
	if ch := s.channels[id]; ch != nil && ch.GuildID != 0 {
		delete(s.byGuild[ch.GuildID], id)
	}
	delete(s.channels, id)
	delete(s.histories, id)
	delete(s.reads, id)
}

func (s *State) read(ch Snowflake) *readState {
	rs := s.reads[ch]
	if rs == nil {
		rs = &readState{}
		s.reads[ch] = rs
	}
	return rs
}

func (s *State) cacheAuthor(m *discord.Message) {
	if m.Author.ID != 0 {
		s.users[m.Author.ID] = m.Author
	}
	if m.Member != nil && m.GuildID != 0 {
		if g := s.guilds[m.GuildID]; g != nil {
			g.members[m.Author.ID] = memberInfo{nick: m.Member.Nick, roles: m.Member.Roles}
		}
	}
	m.Member = nil
	for _, u := range m.Mentions {
		if _, ok := s.users[u.ID]; !ok {
			s.users[u.ID] = u
		}
	}
	if m.ReferencedMessage != nil {
		m.ReferencedMessage.ReferencedMessage = nil
	}
}

func (s *State) mentionsMe(m *discord.Message) bool {
	if m.Author.ID == s.me.ID {
		return false
	}
	if m.MentionEveryone && !s.noEveryone[m.GuildID] {
		return true
	}
	for _, u := range m.Mentions {
		if u.ID == s.me.ID {
			return true
		}
	}
	if g := s.guilds[m.GuildID]; g != nil {
		for _, r := range m.MentionRoles {
			if slices.Contains(g.myRoles, r) {
				return true
			}
		}
	}
	return false
}

func (s *State) addMessage(m discord.Message) Change {
	c := Change{Tree: true}
	ch := s.channels[m.ChannelID]
	if ch == nil {
		return Change{} // channel we can't see (e.g. an unjoined thread)
	}
	if m.GuildID == 0 {
		m.GuildID = ch.GuildID
	}
	s.cacheAuthor(&m)
	if s.reads[m.ChannelID] == nil {
		// First activity in a never-read channel: everything before this
		// message counts as read, this one as new.
		s.reads[m.ChannelID] = &readState{lastAcked: ch.LastMessageID}
	}
	if m.ID > ch.LastMessageID {
		ch.LastMessageID = m.ID
	}
	if m.Author.ID == s.me.ID {
		// Sending a message marks the channel read, like the official client.
		rs := s.read(m.ChannelID)
		rs.lastAcked, rs.mentions = m.ID, 0
	} else if s.mentionsMe(&m) || ch.Type == discord.ChannelDM && !s.muted(ch) {
		s.read(m.ChannelID).mentions++
		c.Mention = true
	}
	if t := s.typing[m.ChannelID]; t != nil {
		delete(t, m.Author.ID)
	}

	if h := s.histories[m.ChannelID]; h != nil && h.loaded {
		nonce := m.NonceValue()
		if nonce != 0 && m.Author.ID == s.me.ID {
			for i := range h.msgs {
				if h.msgs[i].Pending && h.msgs[i].NonceValue() == nonce {
					h.msgs = slices.Delete(h.msgs, i, i+1)
					c.Confirmed = nonce
					break
				}
			}
		}
		h.insert(m)
		c.Channel = m.ChannelID
	}
	c.NewMsg = &m
	return c
}

// insert adds m keeping ascending order; pending (local) messages stay last.
func (h *history) insert(m discord.Message) {
	n := len(h.msgs)
	// Fast path: newest message.
	firstPending := n
	for firstPending > 0 && h.msgs[firstPending-1].Pending {
		firstPending--
	}
	if firstPending == 0 || h.msgs[firstPending-1].ID < m.ID {
		h.msgs = slices.Insert(h.msgs, firstPending, m)
	} else {
		i := sort.Search(firstPending, func(i int) bool { return h.msgs[i].ID >= m.ID })
		if i < firstPending && h.msgs[i].ID == m.ID {
			h.msgs[i] = m
			return
		}
		h.msgs = slices.Insert(h.msgs, i, m)
	}
	if over := len(h.msgs) - MaxMessages; over > 0 {
		h.msgs = slices.Delete(h.msgs, 0, over)
		h.complete = false
	}
}

func (s *State) updateMessage(data json.RawMessage) Snowflake {
	// MESSAGE_UPDATE may be partial (e.g. only embeds resolved); only
	// overwrite fields that are present.
	var u struct {
		ID              Snowflake             `json:"id"`
		ChannelID       Snowflake             `json:"channel_id"`
		Content         *string               `json:"content"`
		EditedTimestamp *time.Time            `json:"edited_timestamp"`
		Embeds          *[]discord.Embed      `json:"embeds"`
		Mentions        *[]discord.User       `json:"mentions"`
		Attachments     *[]discord.Attachment `json:"attachments"`
	}
	if json.Unmarshal(data, &u) != nil {
		return 0
	}
	h := s.histories[u.ChannelID]
	if h == nil {
		return 0
	}
	i := sort.Search(len(h.msgs), func(i int) bool { return h.msgs[i].ID >= u.ID })
	if i == len(h.msgs) || h.msgs[i].ID != u.ID {
		return 0
	}
	m := &h.msgs[i]
	if u.Content != nil {
		m.Content = *u.Content
	}
	if u.EditedTimestamp != nil {
		m.EditedTimestamp = u.EditedTimestamp
	}
	if u.Embeds != nil {
		m.Embeds = *u.Embeds
	}
	if u.Attachments != nil {
		m.Attachments = *u.Attachments
	}
	if u.Mentions != nil {
		m.Mentions = *u.Mentions
	}
	return u.ChannelID
}

func (s *State) deleteMessages(ch Snowflake, ids ...Snowflake) bool {
	h := s.histories[ch]
	if h == nil {
		return false
	}
	n := len(h.msgs)
	h.msgs = slices.DeleteFunc(h.msgs, func(m discord.Message) bool {
		return !m.Pending && slices.Contains(ids, m.ID)
	})
	return len(h.msgs) != n
}

// ---- Queries ----------------------------------------------------------------

func (s *State) Me() discord.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.me
}

// GuildInfo is a guild row for the sidebar.
type GuildInfo struct {
	ID       Snowflake
	Name     string
	Unread   bool
	Mentions int
	Large    bool
}

func (s *State) Guilds() []GuildInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]GuildInfo, 0, len(s.guildOrder))
	for _, id := range s.guildOrder {
		g := s.guilds[id]
		if g == nil || g.Unavailable {
			continue
		}
		gi := GuildInfo{ID: id, Name: g.Name, Large: g.Large}
		for _, ch := range s.byGuild[id] {
			if !ch.Type.IsText() || !s.canView(g, ch) {
				continue
			}
			u, m := s.unread(ch)
			gi.Unread = gi.Unread || u
			gi.Mentions += m
		}
		out = append(out, gi)
	}
	return out
}

// ChannelInfo is a channel row for the sidebar or the switcher.
type ChannelInfo struct {
	ID       Snowflake
	GuildID  Snowflake
	Name     string
	Category string
	Type     discord.ChannelType
	Depth    int // 1 for threads
	Unread   bool
	Mentions int
	Last     Snowflake
}

// GuildChannels lists the readable text channels of a guild in sidebar order:
// uncategorised channels, then each category's channels, threads under their
// parents.
func (s *State) GuildChannels(gid Snowflake) []ChannelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.guilds[gid]
	if g == nil {
		return nil
	}
	var cats, top []*discord.Channel
	children := map[Snowflake][]*discord.Channel{}
	threads := map[Snowflake][]*discord.Channel{}
	for _, ch := range s.byGuild[gid] {
		switch {
		case ch.Type == discord.ChannelCategory:
			cats = append(cats, ch)
		case !ch.Type.IsText() || !s.canView(g, ch):
		case ch.Type >= discord.ChannelAnnounceThrd && ch.Type <= discord.ChannelPrivateThread:
			threads[ch.ParentID] = append(threads[ch.ParentID], ch)
		case ch.ParentID == 0:
			top = append(top, ch)
		default:
			children[ch.ParentID] = append(children[ch.ParentID], ch)
		}
	}
	byPos := func(a, b *discord.Channel) int {
		if a.Position != b.Position {
			return a.Position - b.Position
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	}
	slices.SortFunc(cats, byPos)
	slices.SortFunc(top, byPos)

	var out []ChannelInfo
	emit := func(ch *discord.Channel, cat string, depth int) {
		u, m := s.unread(ch)
		out = append(out, ChannelInfo{
			ID: ch.ID, GuildID: gid, Name: ch.Name, Category: cat, Type: ch.Type,
			Depth: depth, Unread: u, Mentions: m, Last: ch.LastMessageID,
		})
		ts := threads[ch.ID]
		slices.SortFunc(ts, func(a, b *discord.Channel) int {
			if a.ID > b.ID {
				return -1
			}
			return 1
		})
		for _, t := range ts {
			tu, tm := s.unread(t)
			out = append(out, ChannelInfo{
				ID: t.ID, GuildID: gid, Name: t.Name, Category: cat, Type: t.Type,
				Depth: depth + 1, Unread: tu, Mentions: tm, Last: t.LastMessageID,
			})
		}
	}
	for _, ch := range top {
		emit(ch, "", 0)
	}
	for _, cat := range cats {
		kids := children[cat.ID]
		slices.SortFunc(kids, byPos)
		for _, ch := range kids {
			emit(ch, cat.Name, 0)
		}
	}
	return out
}

// PrivateChannels lists DMs and group DMs, most recently active first.
func (s *State) PrivateChannels() []ChannelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ChannelInfo
	for _, ch := range s.channels {
		if !ch.Type.IsPrivate() {
			continue
		}
		u, m := s.unread(ch)
		out = append(out, ChannelInfo{
			ID: ch.ID, Name: s.channelName(ch), Type: ch.Type,
			Unread: u, Mentions: m, Last: ch.LastMessageID,
		})
	}
	slices.SortFunc(out, func(a, b ChannelInfo) int {
		if a.Last > b.Last {
			return -1
		}
		if a.Last < b.Last {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// unread reports whether the channel has unseen messages and how many
// mentions it holds. Channels with no read state at all were never opened;
// treating them as read avoids lighting up every channel of a new guild.
// Muted channels never show as unread, but mentions still count, matching
// the official client.
func (s *State) unread(ch *discord.Channel) (bool, int) {
	rs := s.reads[ch.ID]
	if rs == nil {
		return false, 0
	}
	return ch.LastMessageID > rs.lastAcked && !s.muted(ch), rs.mentions
}

func (s *State) channelName(ch *discord.Channel) string {
	if ch.Name != "" || !ch.Type.IsPrivate() {
		return ch.Name
	}
	names := make([]string, 0, len(ch.RecipientIDs))
	for _, id := range ch.RecipientIDs {
		if u, ok := s.users[id]; ok {
			names = append(names, u.DisplayName())
		}
	}
	if len(names) == 0 {
		return "(empty group)"
	}
	return strings.Join(names, ", ")
}

// Channel returns a copy of a channel.
func (s *State) Channel(id Snowflake) (discord.Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channels[id]
	if !ok {
		return discord.Channel{}, false
	}
	return *ch, true
}

// ChannelTitle is a human title like "Server › #general" or "@alice".
func (s *State) ChannelTitle(id Snowflake) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch := s.channels[id]
	if ch == nil {
		return ""
	}
	switch {
	case ch.Type == discord.ChannelDM:
		return "@" + s.channelName(ch)
	case ch.Type == discord.ChannelGroupDM:
		return s.channelName(ch)
	}
	name := "#" + ch.Name
	if g := s.guilds[ch.GuildID]; g != nil {
		name = g.Name + " › " + name
	}
	return name
}

func (s *State) GuildName(id Snowflake) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if g := s.guilds[id]; g != nil {
		return g.Name
	}
	return ""
}

// IsLarge reports whether a guild needs an explicit subscription to stream
// message events.
func (s *State) IsLarge(id Snowflake) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.guilds[id]
	return g != nil && g.Large
}

// CanSend reports whether the user may post in the channel.
func (s *State) CanSend(id Snowflake) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch := s.channels[id]
	if ch == nil {
		return false
	}
	if ch.Type.IsPrivate() {
		return true
	}
	g := s.guilds[ch.GuildID]
	if g == nil {
		return false
	}
	p := s.permissions(g, ch)
	if ch.Type >= discord.ChannelAnnounceThrd && ch.Type <= discord.ChannelPrivateThread {
		return p&discord.PermSendInThreads != 0
	}
	return p&discord.PermSendMessages != 0
}

// CanManage reports whether the user may delete others' messages.
func (s *State) CanManage(id Snowflake) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch := s.channels[id]
	if ch == nil || ch.Type.IsPrivate() {
		return false
	}
	g := s.guilds[ch.GuildID]
	return g != nil && s.permissions(g, ch)&discord.PermManageMessages != 0
}

// Author resolves how to display a message author in a channel.
type Author struct {
	Name  string
	Color int // RGB, 0 = default
}

func (s *State) Author(guildID Snowflake, u discord.User) Author {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.author(guildID, u)
}

func (s *State) author(guildID Snowflake, u discord.User) Author {
	a := Author{Name: u.DisplayName()}
	g := s.guilds[guildID]
	if g == nil {
		return a
	}
	mi, ok := g.members[u.ID]
	if !ok {
		return a
	}
	if mi.nick != "" {
		a.Name = mi.nick
	}
	top := -1
	for _, rid := range mi.roles {
		if r, ok := g.roles[rid]; ok && r.Color != 0 && r.Position > top {
			top, a.Color = r.Position, r.Color
		}
	}
	return a
}

// UserName resolves a user ID for mention rendering.
func (s *State) UserName(guildID, id Snowflake) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return "", false
	}
	return s.author(guildID, u).Name, true
}

func (s *State) RoleName(guildID, id Snowflake) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if g := s.guilds[guildID]; g != nil {
		if r, ok := g.roles[id]; ok {
			return r.Name, true
		}
	}
	return "", false
}

func (s *State) ChannelNameByID(id Snowflake) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ch := s.channels[id]; ch != nil {
		return s.channelName(ch), true
	}
	return "", false
}

// FindUser finds a friend or known user by username or display name.
func (s *State) FindUser(name string) (discord.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToLower(strings.TrimPrefix(name, "@"))
	var fallback *discord.User
	for _, id := range s.friends {
		u := s.users[id]
		if strings.ToLower(u.Username) == name || strings.ToLower(u.Tag()) == name {
			return u, true
		}
		if fallback == nil && strings.ToLower(u.GlobalName) == name {
			fallback = &u
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	for _, u := range s.users {
		if strings.ToLower(u.Username) == name {
			return u, true
		}
	}
	return discord.User{}, false
}

// DMWith returns the existing 1:1 DM channel with a user.
func (s *State) DMWith(user Snowflake) (Snowflake, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.channels {
		if ch.Type == discord.ChannelDM && len(ch.RecipientIDs) == 1 && ch.RecipientIDs[0] == user {
			return ch.ID, true
		}
	}
	return 0, false
}

// AddChannel registers a channel obtained over REST (e.g. a new DM).
func (s *State) AddChannel(ch discord.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addChannel(ch)
}

// Typing returns the names of users currently typing in a channel.
func (s *State) Typing(ch Snowflake) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.typing[ch]
	if len(m) == 0 {
		return nil
	}
	gid := Snowflake(0)
	if c := s.channels[ch]; c != nil {
		gid = c.GuildID
	}
	now := time.Now()
	var names []string
	for id, until := range m {
		if now.After(until) {
			delete(m, id)
			continue
		}
		if u, ok := s.users[id]; ok {
			names = append(names, s.author(gid, u).Name)
		}
	}
	sort.Strings(names)
	return names
}

// ---- Message history --------------------------------------------------------

// Messages returns a copy of the cached messages of a channel, oldest first,
// and whether older history exists beyond them.
func (s *State) Messages(ch Snowflake) (msgs []discord.Message, more bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.histories[ch]
	if h == nil {
		return nil, true
	}
	h.used = time.Now()
	return slices.Clone(h.msgs), !h.complete
}

// HistoryLoaded reports whether the latest page of a channel was fetched.
func (s *State) HistoryLoaded(ch Snowflake) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.histories[ch]
	return h != nil && h.loaded
}

// Oldest returns the ID of the oldest cached message.
func (s *State) Oldest(ch Snowflake) Snowflake {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.histories[ch]
	if h == nil {
		return 0
	}
	for _, m := range h.msgs {
		if !m.Pending {
			return m.ID
		}
	}
	return 0
}

// SetHistory stores the newest page of a channel (newest-first, as returned
// by the API), merging with messages that arrived meanwhile.
func (s *State) SetHistory(ch Snowflake, page []discord.Message, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.histories[ch]
	if h == nil {
		h = &history{}
		s.histories[ch] = h
	}
	old := h.msgs
	h.msgs = make([]discord.Message, 0, len(page)+8)
	for i := len(page) - 1; i >= 0; i-- {
		s.cacheAuthor(&page[i])
		h.msgs = append(h.msgs, page[i])
	}
	var newest Snowflake
	if n := len(h.msgs); n > 0 {
		newest = h.msgs[n-1].ID
	}
	for _, m := range old {
		if m.Pending || m.ID > newest {
			h.insert(m)
		}
	}
	h.loaded = true
	h.complete = len(page) < limit
	h.used = time.Now()
	s.evict(ch)
}

// PrependHistory adds an older page (newest-first) to the front.
func (s *State) PrependHistory(ch Snowflake, page []discord.Message, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.histories[ch]
	if h == nil {
		return
	}
	older := make([]discord.Message, 0, len(page)+len(h.msgs))
	for i := len(page) - 1; i >= 0; i-- {
		if len(h.msgs) > 0 && page[i].ID >= h.msgs[0].ID {
			continue
		}
		s.cacheAuthor(&page[i])
		older = append(older, page[i])
	}
	h.msgs = append(older, h.msgs...)
	h.complete = len(page) < limit
	// Scrolling back is user-driven, so allow the window to grow beyond
	// MaxMessages while viewing; insert() trims it again on new messages.
}

// AddPending adds a locally sent message that hasn't been confirmed yet.
func (s *State) AddPending(m discord.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.histories[m.ChannelID]; h != nil {
		m.Pending = true
		h.msgs = append(h.msgs, m)
	}
}

// ResolvePending replaces a pending message with the server's copy, or marks
// it failed when sent is nil.
func (s *State) ResolvePending(ch, nonce Snowflake, sent *discord.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.histories[ch]
	if h == nil {
		return
	}
	for i := range h.msgs {
		if h.msgs[i].Pending && h.msgs[i].NonceValue() == nonce {
			if sent == nil {
				h.msgs[i].Failed = true
				return
			}
			h.msgs = slices.Delete(h.msgs, i, i+1)
			s.cacheAuthor(sent)
			h.insert(*sent)
			return
		}
	}
}

// RemoveFailed drops a failed local message.
func (s *State) RemoveFailed(ch, nonce Snowflake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.histories[ch]; h != nil {
		h.msgs = slices.DeleteFunc(h.msgs, func(m discord.Message) bool {
			return m.Pending && m.NonceValue() == nonce
		})
	}
}

// evict drops the least recently used histories beyond MaxChannels.
func (s *State) evict(keep Snowflake) {
	if len(s.histories) <= MaxChannels {
		return
	}
	type entry struct {
		id   Snowflake
		used time.Time
	}
	var es []entry
	for id, h := range s.histories {
		if id != keep {
			es = append(es, entry{id, h.used})
		}
	}
	slices.SortFunc(es, func(a, b entry) int { return a.used.Compare(b.used) })
	for _, e := range es[:len(s.histories)-MaxChannels] {
		delete(s.histories, e.id)
	}
}

// ---- Read states ------------------------------------------------------------

// MarkRead records that the channel was read up to its latest message and
// returns that message ID (0 if nothing to acknowledge).
func (s *State) MarkRead(ch Snowflake) Snowflake {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.channels[ch]
	if c == nil {
		return 0
	}
	rs := s.read(ch)
	if c.LastMessageID <= rs.lastAcked && rs.mentions == 0 {
		return 0
	}
	rs.lastAcked, rs.mentions = c.LastMessageID, 0
	return c.LastMessageID
}

// LastRead returns the last acknowledged message of a channel.
func (s *State) LastRead(ch Snowflake) Snowflake {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rs := s.reads[ch]; rs != nil {
		return rs.lastAcked
	}
	return 0
}

// NextUnread returns the next channel with mentions, or else any unread
// channel, preferring DMs.
func (s *State) NextUnread(current Snowflake) (Snowflake, bool) {
	var best ChannelInfo
	found := false
	consider := func(c ChannelInfo) {
		if c.ID == current || !c.Unread && c.Mentions == 0 {
			return
		}
		if !found || c.Mentions > 0 && best.Mentions == 0 || (c.Mentions > 0) == (best.Mentions > 0) && c.Last > best.Last {
			best, found = c, true
		}
	}
	for _, c := range s.PrivateChannels() {
		consider(c)
	}
	for _, g := range s.Guilds() {
		if !g.Unread && g.Mentions == 0 {
			continue
		}
		for _, c := range s.GuildChannels(g.ID) {
			consider(c)
		}
	}
	return best.ID, found
}

// AllChannels lists every readable channel for the quick switcher.
func (s *State) AllChannels() []ChannelInfo {
	out := s.PrivateChannels()
	for _, g := range s.Guilds() {
		for _, c := range s.GuildChannels(g.ID) {
			c.Category = g.Name
			out = append(out, c)
		}
	}
	return out
}
