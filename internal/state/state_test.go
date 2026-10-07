package state

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/discord/discordtest"
)

func sf(s string) Snowflake {
	id, err := discord.ParseSnowflake(s)
	if err != nil {
		panic(err)
	}
	return id
}

func event(t *testing.T, typ string, d any) discord.Event {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return discord.Event{Type: typ, Data: b}
}

func ready(t *testing.T) *State {
	t.Helper()
	s := New()
	if _, err := s.Apply(event(t, "READY", discordtest.DefaultReady())); err != nil {
		t.Fatal(err)
	}
	return s
}

func msg(id uint64, ch, author, content string) map[string]any {
	return map[string]any{
		"id": fmt.Sprint(id << 22), "channel_id": ch, "content": content,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"author":    map[string]any{"id": author, "username": "x"},
	}
}

func TestReadyAndPermissions(t *testing.T) {
	s := ready(t)
	if s.Me().Username != "me" {
		t.Fatalf("me = %+v", s.Me())
	}
	gs := s.Guilds()
	if len(gs) != 1 || gs[0].Name != "Test Server" || !gs[0].Large {
		t.Fatalf("guilds = %+v", gs)
	}
	var names []string
	for _, c := range s.GuildChannels(gs[0].ID) {
		names = append(names, c.Name)
	}
	if fmt.Sprint(names) != "[general random]" {
		t.Fatalf("channels = %v (secret must be hidden, order by position)", names)
	}
	dms := s.PrivateChannels()
	if len(dms) != 1 || dms[0].Name != "Alice" {
		t.Fatalf("dms = %+v", dms)
	}
	if !s.CanSend(sf(discordtest.GeneralID)) {
		t.Fatal("should be able to send in #general")
	}
	if s.CanManage(sf(discordtest.GeneralID)) {
		t.Fatal("should not be able to manage messages")
	}
}

func TestOwnerSeesEverything(t *testing.T) {
	r := discordtest.DefaultReady()
	g := r["guilds"].([]any)[0].(map[string]any)
	g["owner_id"] = discordtest.MeID
	s := New()
	if _, err := s.Apply(event(t, "READY", r)); err != nil {
		t.Fatal(err)
	}
	if n := len(s.GuildChannels(sf(discordtest.GuildID))); n != 3 {
		t.Fatalf("owner sees %d channels, want 3", n)
	}
}

func TestRoleOverwriteGrantsAccess(t *testing.T) {
	r := discordtest.DefaultReady()
	g := r["guilds"].([]any)[0].(map[string]any)
	g["members"].([]any)[0].(map[string]any)["roles"] = []any{discordtest.ModRoleID}
	secret := g["channels"].([]any)[3].(map[string]any)
	secret["permission_overwrites"] = append(secret["permission_overwrites"].([]any),
		map[string]any{"id": discordtest.ModRoleID, "type": 0, "allow": fmt.Sprint(1 << 10), "deny": "0"})
	s := New()
	if _, err := s.Apply(event(t, "READY", r)); err != nil {
		t.Fatal(err)
	}
	if n := len(s.GuildChannels(sf(discordtest.GuildID))); n != 3 {
		t.Fatalf("mod sees %d channels, want 3", n)
	}
}

func TestUnreadAndMentions(t *testing.T) {
	s := ready(t)
	general := sf(discordtest.GeneralID)

	m := msg(10, discordtest.GeneralID, discordtest.FriendID, "hi <@"+discordtest.MeID+">")
	m["guild_id"] = discordtest.GuildID
	m["mentions"] = []any{map[string]any{"id": discordtest.MeID, "username": "me"}}
	c, err := s.Apply(event(t, "MESSAGE_CREATE", m))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Mention || !c.Tree {
		t.Fatalf("change = %+v", c)
	}
	g := s.Guilds()[0]
	if !g.Unread || g.Mentions != 1 {
		t.Fatalf("guild = %+v", g)
	}
	if id, ok := s.NextUnread(0); !ok || id != general {
		t.Fatalf("next unread = %v %v", id, ok)
	}
	if s.MarkRead(general) == 0 {
		t.Fatal("MarkRead returned 0")
	}
	if g := s.Guilds()[0]; g.Unread || g.Mentions != 0 {
		t.Fatalf("after read: %+v", g)
	}
	if _, ok := s.NextUnread(0); ok {
		t.Fatal("still unread")
	}
}

func TestHistoryPendingAndCap(t *testing.T) {
	old := MaxMessages
	MaxMessages = 5
	defer func() { MaxMessages = old }()

	s := ready(t)
	ch := sf(discordtest.DMID)

	var page []discord.Message
	for i := 3; i >= 1; i-- { // newest first
		page = append(page, discord.Message{ID: Snowflake(i << 22), ChannelID: ch, Content: fmt.Sprint(i)})
	}
	s.SetHistory(ch, page, 50)
	if msgs, more := s.Messages(ch); len(msgs) != 3 || more || msgs[0].Content != "1" {
		t.Fatalf("history = %v more=%v", msgs, more)
	}

	nonce := discord.NewNonce()
	s.AddPending(discord.Message{ChannelID: ch, Content: "mine", Author: s.Me(),
		Nonce: []byte(`"` + nonce.String() + `"`)})

	// Someone else's message lands before ours is confirmed: pending stays last.
	if _, err := s.Apply(event(t, "MESSAGE_CREATE", msg(4, discordtest.DMID, discordtest.FriendID, "4"))); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ch)
	if !msgs[len(msgs)-1].Pending {
		t.Fatalf("pending message not last: %+v", msgs)
	}

	// Our echo replaces the pending copy.
	echo := msg(5, discordtest.DMID, discordtest.MeID, "mine")
	echo["nonce"] = nonce.String()
	c, _ := s.Apply(event(t, "MESSAGE_CREATE", echo))
	if c.Confirmed != nonce {
		t.Fatalf("confirmed = %v", c.Confirmed)
	}
	msgs, _ = s.Messages(ch)
	for _, m := range msgs {
		if m.Pending {
			t.Fatal("pending copy not replaced")
		}
	}
	if len(msgs) != 5 {
		t.Fatalf("len = %d, want 5", len(msgs))
	}

	// Cap: the oldest message is dropped and history is no longer complete.
	if _, err := s.Apply(event(t, "MESSAGE_CREATE", msg(6, discordtest.DMID, discordtest.FriendID, "6"))); err != nil {
		t.Fatal(err)
	}
	msgs, more := s.Messages(ch)
	if len(msgs) != 5 || msgs[0].Content != "2" || !more {
		t.Fatalf("after cap: len=%d first=%q more=%v", len(msgs), msgs[0].Content, more)
	}

	// Partial update keeps content when only embeds change.
	if _, err := s.Apply(event(t, "MESSAGE_UPDATE", map[string]any{
		"id": fmt.Sprint(6 << 22), "channel_id": discordtest.DMID, "embeds": []any{}})); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.Messages(ch)
	if msgs[len(msgs)-1].Content != "6" {
		t.Fatal("partial update wiped content")
	}

	if _, err := s.Apply(event(t, "MESSAGE_DELETE", map[string]any{
		"id": fmt.Sprint(6 << 22), "channel_id": discordtest.DMID})); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.Messages(ch); len(msgs) != 4 {
		t.Fatalf("after delete len = %d", len(msgs))
	}
}

func TestEviction(t *testing.T) {
	old := MaxChannels
	MaxChannels = 2
	defer func() { MaxChannels = old }()
	s := ready(t)
	for _, id := range []string{discordtest.GeneralID, discordtest.RandomID, discordtest.DMID} {
		s.SetHistory(sf(id), nil, 50)
		time.Sleep(time.Millisecond)
	}
	if s.HistoryLoaded(sf(discordtest.GeneralID)) {
		t.Fatal("least recently used history was not evicted")
	}
	if !s.HistoryLoaded(sf(discordtest.DMID)) || !s.HistoryLoaded(sf(discordtest.RandomID)) {
		t.Fatal("recent histories evicted")
	}
}

func TestAuthorNickAndColor(t *testing.T) {
	s := ready(t)
	a := s.Author(sf(discordtest.GuildID), discord.User{ID: sf(discordtest.FriendID), Username: "alice"})
	if a.Name != "Alice (mod)" || a.Color != 0x3498db {
		t.Fatalf("author = %+v", a)
	}
	if u, ok := s.FindUser("alice"); !ok || u.ID != sf(discordtest.FriendID) {
		t.Fatalf("FindUser = %+v %v", u, ok)
	}
	if id, ok := s.DMWith(sf(discordtest.FriendID)); !ok || id != sf(discordtest.DMID) {
		t.Fatalf("DMWith = %v %v", id, ok)
	}
}

func TestTypingExpires(t *testing.T) {
	s := ready(t)
	if _, err := s.Apply(event(t, "TYPING_START", map[string]any{
		"channel_id": discordtest.DMID, "user_id": discordtest.FriendID})); err != nil {
		t.Fatal(err)
	}
	if names := s.Typing(sf(discordtest.DMID)); len(names) != 1 || names[0] != "Alice" {
		t.Fatalf("typing = %v", names)
	}
}

func BenchmarkGuilds(b *testing.B) {
	// 200 guilds x 60 channels: the sidebar refresh must stay cheap.
	r := discordtest.DefaultReady()
	var guilds []any
	for g := 0; g < 200; g++ {
		var chans []any
		for c := 0; c < 60; c++ {
			chans = append(chans, map[string]any{"id": fmt.Sprint(g*1000 + c + 1e9), "type": 0,
				"name": fmt.Sprint("c", c), "position": c, "last_message_id": "5"})
		}
		guilds = append(guilds, map[string]any{"id": fmt.Sprint(g + 1e6), "name": fmt.Sprint("g", g),
			"roles":    []any{map[string]any{"id": fmt.Sprint(g + 1e6), "permissions": fmt.Sprint(1 << 10)}},
			"channels": chans})
	}
	r["guilds"] = guilds
	data, _ := json.Marshal(r)
	s := New()
	if _, err := s.Apply(discord.Event{Type: "READY", Data: data}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Guilds()
	}
}

func TestMutes(t *testing.T) {
	r := discordtest.DefaultReady()
	r["user_guild_settings"] = []any{map[string]any{
		"guild_id": discordtest.GuildID, "suppress_everyone": true,
		"channel_overrides": []any{map[string]any{"channel_id": discordtest.CategoryID, "muted": true}},
	}}
	s := New()
	if _, err := s.Apply(event(t, "READY", r)); err != nil {
		t.Fatal(err)
	}
	// Activity in a channel under the muted category: no unread, no bell.
	m := msg(10, discordtest.GeneralID, discordtest.FriendID, "@everyone hi")
	m["mention_everyone"] = true
	c, _ := s.Apply(event(t, "MESSAGE_CREATE", m))
	if c.Mention {
		t.Fatal("suppressed @everyone counted as a mention")
	}
	if g := s.Guilds()[0]; g.Unread || g.Mentions != 0 {
		t.Fatalf("muted category shows unread: %+v", g)
	}
	// A direct mention still counts.
	m = msg(11, discordtest.GeneralID, discordtest.FriendID, "hi")
	m["mentions"] = []any{map[string]any{"id": discordtest.MeID}}
	if c, _ := s.Apply(event(t, "MESSAGE_CREATE", m)); !c.Mention {
		t.Fatal("direct mention in muted channel ignored")
	}
	if g := s.Guilds()[0]; g.Mentions != 1 {
		t.Fatalf("mentions = %d", g.Mentions)
	}
}
