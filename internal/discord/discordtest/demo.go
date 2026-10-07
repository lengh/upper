package discordtest

import (
	"context"
	"math/rand/v2"
	"strconv"
	"time"
)

// Demo account IDs.
const (
	demoBob   = "100000000000000003"
	demoCarol = "100000000000000004"
	demoDave  = "100000000000000005"
	demoBot   = "100000000000000006"

	demoGophers  = "210000000000000001"
	demoWelcome  = "310000000000000001"
	demoGeneral  = "310000000000000002"
	demoHelp     = "310000000000000003"
	demoOffTopic = "310000000000000004"
	demoThread   = "310000000000000005"
	demoTextCat  = "310000000000000006"
	demoVoiceCat = "310000000000000007"

	demoCafe   = "220000000000000001"
	demoLounge = "320000000000000001"
	demoMusic  = "320000000000000002"
	demoArt    = "320000000000000003"

	demoDMAlice = "410000000000000001"
	demoDMBob   = "410000000000000002"
	demoGroup   = "410000000000000003"

	demoRoleCore = "510000000000000001"
	demoRoleBot  = "510000000000000002"
)

func user(id, name, global string) map[string]any {
	u := map[string]any{"id": id, "username": name}
	if global != "" {
		u["global_name"] = global
	}
	return u
}

var demoUsers = map[string]map[string]any{
	MeID:      user(MeID, "you", "You"),
	FriendID:  user(FriendID, "alice", "Alice"),
	demoBob:   user(demoBob, "bob", "Bob"),
	demoCarol: user(demoCarol, "carol", "Carol"),
	demoDave:  user(demoDave, "dave", ""),
	demoBot:   {"id": demoBot, "username": "Dyno", "bot": true},
}

// msg builds a message at a given age.
func (s *Server) demoMsg(ch, author, content string, age time.Duration, extra map[string]any) map[string]any {
	id := strconv.FormatUint((uint64(time.Now().Add(-age).UnixMilli())-1420070400000)<<22|s.nextID.Add(1)&0x3fffff, 10)
	m := map[string]any{
		"id": id, "channel_id": ch, "content": content, "type": 0,
		"timestamp": time.Now().Add(-age).UTC().Format(time.RFC3339Nano),
		"author":    demoUsers[author],
	}
	for k, v := range extra {
		m[k] = v
	}
	s.mu.Lock()
	s.messages[ch] = append(s.messages[ch], m)
	s.mu.Unlock()
	return m
}

// SeedDemo replaces the account with a lively demo one: two servers, DMs
// with people in every presence state, and history with replies, code,
// reactions, link previews and an unread marker. Call before connecting.
func (s *Server) SeedDemo() {
	const view, send, history = 1 << 10, 1 << 11, 1 << 16
	everyone := strconv.Itoa(view | send | history)
	ch := func(id, name string, typ, pos int, parent, topic string) map[string]any {
		c := map[string]any{"id": id, "type": typ, "name": name, "position": pos}
		if parent != "" {
			c["parent_id"] = parent
		}
		if topic != "" {
			c["topic"] = topic
		}
		return c
	}
	member := func(id string, nick string, roles ...string) map[string]any {
		m := map[string]any{"user": demoUsers[id], "roles": roles}
		if nick != "" {
			m["nick"] = nick
		}
		return m
	}

	h, m := time.Hour, time.Minute
	day := 24 * h

	// #general: yesterday's tail, then today's conversation.
	s.demoMsg(demoGeneral, demoBob, "anyone else still on 1.22? thinking about bumping the toolchain", day+3*h, nil)
	s.demoMsg(demoGeneral, FriendID, "we moved to 1.24 last week, iterators alone were worth it", day+3*h-2*m, nil)
	s.demoMsg(demoGeneral, demoBot, "", day+2*h, map[string]any{"type": 7, "author": demoUsers[demoDave]})
	s.demoMsg(demoGeneral, demoDave, "hi all 👋 found this place through the meetup", day+2*h-m, nil)
	q := s.demoMsg(demoGeneral, demoCarol, "welcome dave! grab a sticker from #welcome", day+2*h-2*m, nil)
	s.demoMsg(demoGeneral, MeID, "morning! did anyone figure out the flaky CI job?", 3*h, nil)
	s.demoMsg(demoGeneral, demoBob, "it's the race in the cache warmer. repro:", 3*h-m, nil)
	s.demoMsg(demoGeneral, demoBob, "```go\nfor i := range workers {\n\tgo warm(cache, i) // shares buf!\n}\n```", 3*h-m-10*time.Second, nil)
	fix := s.demoMsg(demoGeneral, FriendID, "oh nice catch. `go test -race` flags it immediately too", 3*h-2*m, map[string]any{
		"reactions": []any{
			map[string]any{"emoji": map[string]any{"name": "🎯"}, "count": 3, "me": true},
			map[string]any{"emoji": map[string]any{"name": "👀"}, "count": 1},
		}})
	s.demoMsg(demoGeneral, MeID, "I'll send a patch after lunch", 3*h-3*m, map[string]any{
		"type": 19, "referenced_message": fix, "message_reference": map[string]any{"message_id": fix["id"]},
		"edited_timestamp": time.Now().Add(-3*h + 2*m).UTC().Format(time.RFC3339Nano),
	})
	s.demoMsg(demoGeneral, demoCarol, "related reading for anyone curious:", 2*h, map[string]any{
		"embeds": []any{map[string]any{
			"type": "article", "title": "Data Race Detector",
			"description": "Data races are among the most common and hardest to debug types of bugs in concurrent systems.",
			"url":         "https://go.dev/doc/articles/race_detector",
			"provider":    map[string]any{"name": "go.dev"}, "color": 0x00add8,
		}}})
	lastRead := s.demoMsg(demoGeneral, demoDave, "thanks, reading now", 2*h-m, nil)
	s.demoMsg(demoGeneral, FriendID, "<@"+MeID+"> patch looks good, approved ✅ merging when CI is green", 25*m,
		map[string]any{"mentions": []any{demoUsers[MeID]}})
	s.demoMsg(demoGeneral, demoBob, "**release notes** are drafted in the thread, please skim them _before_ friday ||there are memes||", 20*m, map[string]any{
		"reactions": []any{map[string]any{"emoji": map[string]any{"name": "🚀"}, "count": 4}},
	})
	_ = q

	s.demoMsg(demoThread, demoBob, "draft:\n- faster startup\n- `--demo` mode\n- fewer allocations in the renderer", 30*m, nil)
	s.demoMsg(demoHelp, demoDave, "how do I get my editor to run gofmt on save?", 5*h, nil)
	s.demoMsg(demoHelp, demoCarol, "> how do I get my editor to run gofmt on save?\ngopls does it: set `formatting.gofumpt` if you like it stricter", 5*h-m, nil)
	s.demoMsg(demoOffTopic, FriendID, "coffee or tea, final answer", 6*h, nil)
	s.demoMsg(demoWelcome, demoBot, "Welcome to **Gophers**! Read the rules, then say hi in <#"+demoGeneral+">.", 30*day, nil)
	s.demoMsg(demoLounge, demoCarol, "new album recs? something for late night coding", 50*m, nil)
	s.demoMsg(demoLounge, FriendID, "anything by Com Truise", 48*m, nil)
	s.demoMsg(demoMusic, demoBob, "🎶 now playing: Midnight City", 3*h, nil)

	s.demoMsg(demoDMAlice, FriendID, "are you around later? want to pair on the parser", 4*h, nil)
	s.demoMsg(demoDMAlice, MeID, "yes! after 3 works", 4*h-m, nil)
	s.demoMsg(demoDMAlice, FriendID, "perfect, I'll send an invite", 4*h-2*m, nil)
	s.demoMsg(demoDMBob, demoBob, "lmk when the patch is up", 40*m, nil)
	s.demoMsg(demoGroup, demoCarol, "saturday hike still on?", 2*day, nil)

	last := func(ch string) string {
		ms := s.messages[ch]
		if len(ms) == 0 {
			return "0"
		}
		return ms[len(ms)-1]["id"].(string)
	}
	readUpTo := func(ch string) map[string]any {
		return map[string]any{"id": ch, "last_message_id": last(ch), "mention_count": 0}
	}

	ready := map[string]any{
		"v": 10, "user": demoUsers[MeID],
		"guilds": []any{
			map[string]any{
				"id": demoGophers, "name": "Gophers", "owner_id": FriendID, "member_count": 120,
				"roles": []any{
					map[string]any{"id": demoGophers, "name": "@everyone", "permissions": everyone},
					map[string]any{"id": demoRoleCore, "name": "core", "position": 2, "color": 0x5865f2, "permissions": "0"},
					map[string]any{"id": demoRoleBot, "name": "bots", "position": 1, "color": 0x2ecc71, "permissions": "0"},
				},
				"members": []any{
					member(MeID, ""), member(FriendID, "", demoRoleCore), member(demoBob, "bobby"),
					member(demoCarol, "", demoRoleCore), member(demoDave, ""), member(demoBot, "", demoRoleBot),
				},
				"channels": []any{
					ch(demoTextCat, "Text", 4, 0, "", ""),
					ch(demoWelcome, "welcome", 5, 0, demoTextCat, "Start here"),
					ch(demoGeneral, "general", 0, 1, demoTextCat, "Everything Go · be kind · no recruiters"),
					ch(demoHelp, "help", 0, 2, demoTextCat, "Ask anything, show your code"),
					ch(demoOffTopic, "off-topic", 0, 3, demoTextCat, ""),
					ch(demoVoiceCat, "Voice", 4, 1, "", ""),
					ch("310000000000000099", "Lounge", 2, 0, demoVoiceCat, ""),
				},
				"threads": []any{map[string]any{"id": demoThread, "type": 11, "name": "release-planning", "parent_id": demoGeneral}},
			},
			map[string]any{
				"id": demoCafe, "name": "Synthwave Café", "owner_id": demoCarol, "member_count": 4200, "large": true,
				"roles":   []any{map[string]any{"id": demoCafe, "name": "@everyone", "permissions": everyone}},
				"members": []any{member(MeID, "")},
				"channels": []any{
					ch(demoLounge, "lounge", 0, 0, "", "Late night conversations"),
					ch(demoMusic, "now-playing", 0, 1, "", ""),
					ch(demoArt, "art", 0, 2, "", ""),
				},
			},
		},
		"private_channels": []any{
			map[string]any{"id": demoDMAlice, "type": 1, "last_message_id": last(demoDMAlice), "recipients": []any{demoUsers[FriendID]}},
			map[string]any{"id": demoDMBob, "type": 1, "last_message_id": last(demoDMBob), "recipients": []any{demoUsers[demoBob]}},
			map[string]any{"id": demoGroup, "type": 3, "name": "weekend plans", "last_message_id": last(demoGroup),
				"recipients": []any{demoUsers[FriendID], demoUsers[demoCarol]}},
		},
		"relationships": []any{
			map[string]any{"type": 1, "user": demoUsers[FriendID]},
			map[string]any{"type": 1, "user": demoUsers[demoBob]},
			map[string]any{"type": 1, "user": demoUsers[demoCarol]},
		},
		"presences": []any{
			map[string]any{"user": map[string]any{"id": FriendID}, "status": "online"},
			map[string]any{"user": map[string]any{"id": demoBob}, "status": "idle"},
			map[string]any{"user": map[string]any{"id": demoCarol}, "status": "dnd"},
		},
		"read_state": []any{
			map[string]any{"id": demoGeneral, "last_message_id": lastRead["id"], "mention_count": 1},
			readUpTo(demoDMAlice), readUpTo(demoGroup), readUpTo(demoHelp), readUpTo(demoWelcome),
			readUpTo(demoOffTopic), readUpTo(demoThread), readUpTo(demoMusic), readUpTo(demoArt),
			map[string]any{"id": demoDMBob, "last_message_id": "0", "mention_count": 1},
			map[string]any{"id": demoLounge, "last_message_id": "0", "mention_count": 0},
		},
		"user_guild_settings": []any{map[string]any{
			"guild_id": demoCafe, "channel_overrides": []any{map[string]any{"channel_id": demoMusic, "muted": true}},
		}},
	}
	// Channels' last_message_id drive unread markers.
	for _, g := range ready["guilds"].([]any) {
		for _, c := range g.(map[string]any)["channels"].([]any) {
			cm := c.(map[string]any)
			cm["last_message_id"] = last(cm["id"].(string))
		}
		threads, _ := g.(map[string]any)["threads"].([]any)
		for _, c := range threads {
			cm := c.(map[string]any)
			cm["last_message_id"] = last(cm["id"].(string))
		}
	}
	s.SetReady(ready)
}

var demoLines = []struct{ ch, guild, author, text string }{
	{demoGeneral, demoGophers, demoCarol, "CI is green on main again 🎉"},
	{demoLounge, demoCafe, FriendID, "this playlist is carrying my whole evening"},
	{demoGeneral, demoGophers, demoBob, "anyone up for reviewing the iterator PR? it's small, promise"},
	{demoDMAlice, "", FriendID, "sent the invite, see you at 3 🙂"},
	{demoGeneral, demoGophers, demoDave, "is there a recording of the last meetup talk?"},
	{demoHelp, demoGophers, demoCarol, "tip: `go vet ./...` before every push saves so much time"},
	{demoGeneral, demoGophers, FriendID, "<@" + MeID + "> can you take a look when you're free?"},
	{demoGroup, "", demoCarol, "forecast says sunny, I'm in ☀️"},
}

// Demo simulates a lively account until ctx ends.
func (s *Server) Demo(ctx context.Context) {
	go func() {
		for range s.Sent {
		}
	}()
	go func() {
		for range s.Commands {
		}
	}()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(6+rand.IntN(6)) * time.Second):
		}
		l := demoLines[i%len(demoLines)]
		td := map[string]any{"channel_id": l.ch, "user_id": l.author}
		if l.guild != "" {
			td["guild_id"] = l.guild
		}
		s.Dispatch("TYPING_START", td)
		time.Sleep(2500 * time.Millisecond)
		m := s.demoMsg(l.ch, l.author, l.text, 0, nil)
		if l.guild != "" {
			m["guild_id"] = l.guild
		}
		if l.author == FriendID && l.ch == demoGeneral && i%len(demoLines) == 6 {
			m["mentions"] = []any{demoUsers[MeID]}
		}
		s.Dispatch("MESSAGE_CREATE", m)
		if i%3 == 1 {
			// Someone reacts to the newest message a moment later.
			time.Sleep(1500 * time.Millisecond)
			s.Dispatch("MESSAGE_REACTION_ADD", map[string]any{
				"user_id": demoCarol, "channel_id": l.ch, "message_id": m["id"],
				"emoji": map[string]any{"name": "👍"},
			})
		}
	}
}
