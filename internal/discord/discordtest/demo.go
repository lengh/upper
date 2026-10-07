package discordtest

import (
	"context"
	"math/rand/v2"
	"time"
)

var demoLines = []string{
	"anyone around?",
	"just pushed the fix, can someone review? `go test ./...` is green",
	"**reminder:** standup in 10 minutes",
	"lol",
	"hey <@" + MeID + ">, did you see this?",
	"```\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
	"that's *probably* fine",
	"brb coffee",
}

// Demo seeds history and simulates a lively account until ctx ends, so the
// UI can be tried without a Discord account.
func (s *Server) Demo(ctx context.Context) {
	go func() {
		for range s.Sent {
		}
	}()
	go func() {
		for range s.Commands {
		}
	}()
	s.AddMessage(GeneralID, FriendID, "alice", "welcome to the **upper** demo! nothing here touches Discord")
	s.AddMessage(GeneralID, FriendID, "alice", "try Ctrl+K, Alt+U, and F1 for help")
	s.AddMessage(GeneralID, MeID, "me", "looks good in the terminal")
	s.AddMessage(RandomID, FriendID, "alice", "random thoughts go here")
	s.AddMessage(DMID, FriendID, "alice", "hey! this is a direct message")

	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(4+rand.IntN(5)) * time.Second):
		}
		ch := GeneralID
		if i%3 == 2 {
			ch = RandomID
		}
		s.Dispatch("TYPING_START", map[string]any{"channel_id": ch, "user_id": FriendID, "guild_id": GuildID})
		time.Sleep(1500 * time.Millisecond)
		s.Post(ch, GuildID, FriendID, "alice", demoLines[i%len(demoLines)])
		if i == 2 {
			s.Post(DMID, "", FriendID, "alice", "psst, check your DMs (Alt+U jumps here)")
		}
	}
}
