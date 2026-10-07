package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/discord/discordtest"
)

const token = "test-token"

func start(t *testing.T, srv *discordtest.Server, tok string) (*discord.Gateway, chan error) {
	t.Helper()
	discord.GatewayURL = srv.GatewayURL()
	discord.APIBase = srv.APIURL()
	gw := discord.NewGateway(tok)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- gw.Run(ctx) }()
	return gw, done
}

func waitEvent(t *testing.T, gw *discord.Gateway, typ string) discord.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-gw.Events:
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

func TestGatewayReadyAndDispatch(t *testing.T) {
	srv := discordtest.New(token)
	defer srv.Close()
	gw, _ := start(t, srv, token)

	ev := waitEvent(t, gw, "READY")
	var r discord.Ready
	if err := json.Unmarshal(ev.Data, &r); err != nil {
		t.Fatal(err)
	}
	if r.User.Username != "me" || len(r.Guilds) != 1 || len(r.PrivateChannels) != 1 {
		t.Fatalf("unexpected READY: %+v", r)
	}

	// Many payloads through one shared zlib stream must all decode.
	for i := 0; i < 200; i++ {
		srv.Post(discordtest.GeneralID, discordtest.GuildID, discordtest.FriendID, "alice", "hello")
	}
	for i := 0; i < 200; i++ {
		waitEvent(t, gw, "MESSAGE_CREATE")
	}
}

func TestGatewayResumesAfterDrop(t *testing.T) {
	srv := discordtest.New(token)
	defer srv.Close()
	gw, _ := start(t, srv, token)
	waitEvent(t, gw, "READY")

	srv.DropConnections()
	waitEvent(t, gw, "RESUMED")
	if got := srv.Identifies.Load(); got != 1 {
		t.Fatalf("identified %d times, want 1 (should resume)", got)
	}
	if got := srv.Resumes.Load(); got != 1 {
		t.Fatalf("resumed %d times, want 1", got)
	}

	// Events still flow on the resumed connection.
	srv.Post(discordtest.DMID, "", discordtest.FriendID, "alice", "still here?")
	waitEvent(t, gw, "MESSAGE_CREATE")
}

func TestGatewayHeartbeats(t *testing.T) {
	srv := discordtest.New(token)
	srv.HeartbeatInterval = 30 * time.Millisecond
	defer srv.Close()
	gw, _ := start(t, srv, token)
	waitEvent(t, gw, "READY")

	// Survive many heartbeat intervals without reconnecting.
	time.Sleep(500 * time.Millisecond)
	if srv.Resumes.Load() != 0 || srv.Identifies.Load() != 1 {
		t.Fatalf("connection churned: identifies=%d resumes=%d", srv.Identifies.Load(), srv.Resumes.Load())
	}
}

func TestGatewayBadToken(t *testing.T) {
	srv := discordtest.New(token)
	defer srv.Close()
	_, done := start(t, srv, "wrong")
	select {
	case err := <-done:
		if !errors.Is(err, discord.ErrAuth) {
			t.Fatalf("got %v, want ErrAuth", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give up on a bad token")
	}
}

func TestGatewaySubscribeCommand(t *testing.T) {
	srv := discordtest.New(token)
	defer srv.Close()
	gw, _ := start(t, srv, token)
	waitEvent(t, gw, "READY")

	id, _ := discord.ParseSnowflake(discordtest.GuildID)
	gw.Subscribe([]discord.Snowflake{id})
	select {
	case cmd := <-srv.Commands:
		if cmd["op"].(float64) != 37 {
			t.Fatalf("op = %v, want 37", cmd["op"])
		}
		subs := cmd["d"].(map[string]any)["subscriptions"].(map[string]any)
		if _, ok := subs[discordtest.GuildID]; !ok {
			t.Fatalf("guild missing from subscription: %v", subs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no subscription command received")
	}
}
