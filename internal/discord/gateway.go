package discord

import (
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// GatewayURL is the initial Gateway endpoint (a variable for tests).
var GatewayURL = "wss://gateway.discord.gg"

// Gateway opcodes.
const (
	opDispatch          = 0
	opHeartbeat         = 1
	opIdentify          = 2
	opResume            = 6
	opReconnect         = 7
	opInvalidSession    = 9
	opHello             = 10
	opHeartbeatAck      = 11
	opGuildSubscription = 37
)

// Gateway capabilities requested at identify. upper keeps the payload shape
// simple (no deduped users, no versioned or proto settings), dropping only
// data it never shows: user notes and presences of "implicit" relationships.
const capabilities = 1<<0 | 1<<1 // LAZY_USER_NOTES | NO_AFFINE_USER_IDS

// Event is a dispatch event. Data is decoded lazily by the consumer, so events
// upper doesn't care about cost nothing beyond the JSON scan.
type Event struct {
	Type string
	Data json.RawMessage
}

// Status reports connection lifecycle changes to the UI.
type Status struct {
	Connected bool
	Message   string
}

// ErrAuth is returned by Run when Discord rejects the token.
var ErrAuth = errors.New("discord rejected the token (close 4004): log in again")

type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// Gateway is a long-lived, self-healing Gateway connection. It resumes after
// network blips and replays missed events, so the UI never needs a full
// reload unless Discord invalidates the session.
type Gateway struct {
	token  string
	Events chan Event
	Status chan Status

	seq       atomic.Int64
	sessionID string
	resumeURL string

	// out carries commands from the app to the active connection's writer.
	out chan []byte
}

func NewGateway(token string) *Gateway {
	return &Gateway{
		token: token,
		// Large buffer so bursts (e.g. replay after resume) don't stall the
		// reader; the consumer is a cheap in-memory state update.
		Events: make(chan Event, 4096),
		Status: make(chan Status, 16),
		out:    make(chan []byte, 64),
	}
}

// Send queues a command for the current connection. It never blocks; if the
// queue is full the command is dropped (commands are idempotent state syncs).
func (g *Gateway) Send(op int, d any) {
	b, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return
	}
	select {
	case g.out <- b:
	default:
	}
}

// GuildSubscription mirrors the Op 37 per-guild subscription object.
type GuildSubscription struct {
	Typing     bool `json:"typing"`
	Threads    bool `json:"threads"`
	Activities bool `json:"activities"`
}

// Subscribe asks Discord to stream events (messages, typing) for guilds that
// are above the large threshold. Smaller guilds are streamed automatically.
func (g *Gateway) Subscribe(guilds []Snowflake) {
	if len(guilds) == 0 {
		return
	}
	subs := make(map[string]GuildSubscription, len(guilds))
	for _, id := range guilds {
		// Activities would stream presences of the whole member list,
		// which upper doesn't show; typing and threads are enough.
		subs[id.String()] = GuildSubscription{Typing: true, Threads: true}
	}
	g.Send(opGuildSubscription, map[string]any{"subscriptions": subs})
}

func (g *Gateway) status(connected bool, format string, args ...any) {
	select {
	case g.Status <- Status{Connected: connected, Message: fmt.Sprintf(format, args...)}:
	default:
	}
}

// Run connects and keeps the connection alive until ctx is cancelled or the
// token is rejected.
func (g *Gateway) Run(ctx context.Context) error {
	const minBackoff = 250 * time.Millisecond
	backoff := minBackoff
	for {
		started := time.Now()
		err := g.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var fatal *FatalError
		if errors.Is(err, ErrAuth) || errors.As(err, &fatal) {
			return err
		}
		// A session that lived a while resets the backoff.
		if time.Since(started) > time.Minute {
			backoff = minBackoff
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff)))
		g.status(false, "disconnected (%v), reconnecting in %s", err, wait.Round(time.Second))
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
		backoff = min(backoff*2, 60*time.Second)
	}
}

// FatalError is a close code that reconnecting cannot fix.
type FatalError struct {
	Code int
	Text string
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("gateway closed with fatal code %d: %s", e.Code, e.Text)
}

type closeErr struct {
	resumable bool
	err       error
}

func (e *closeErr) Error() string { return e.err.Error() }

// session runs one websocket connection from dial to close.
func (g *Gateway) session(ctx context.Context) error {
	resuming := g.sessionID != ""
	base := GatewayURL
	if resuming && g.resumeURL != "" {
		base = g.resumeURL
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 15 * time.Second,
		ReadBufferSize:   64 << 10,
	}
	hdr := http.Header{"User-Agent": {UserAgent}}
	conn, _, err := dialer.DialContext(ctx, base+"/?v=10&encoding=json&compress=zlib-stream", hdr)
	if err != nil {
		return err
	}
	conn.SetReadLimit(-1)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		finalErr error
	)
	fail := func(err error) {
		errOnce.Do(func() { finalErr = err })
		cancel()
	}

	// Frames -> pipe -> one zlib stream -> JSON decoder. Discord flushes the
	// shared zlib context after every payload, so the decoder sees a plain
	// concatenation of JSON documents and needs no frame bookkeeping.
	pr, pw := io.Pipe()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(data); err != nil {
				return
			}
		}
	}()

	hello := make(chan time.Duration, 1)
	acked := atomic.Bool{}
	acked.Store(true)
	beatNow := make(chan struct{}, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		zr, err := zlib.NewReader(pr)
		if err != nil {
			fail(err)
			return
		}
		dec := json.NewDecoder(zr)
		for {
			var p payload
			if err := dec.Decode(&p); err != nil {
				fail(err)
				return
			}
			switch p.Op {
			case opHello:
				var h struct {
					Interval int `json:"heartbeat_interval"`
				}
				_ = json.Unmarshal(p.D, &h)
				hello <- time.Duration(h.Interval) * time.Millisecond
			case opHeartbeatAck:
				acked.Store(true)
			case opHeartbeat:
				select {
				case beatNow <- struct{}{}:
				default:
				}
			case opReconnect:
				fail(&closeErr{resumable: true, err: errors.New("server requested reconnect")})
				return
			case opInvalidSession:
				var resumable bool
				_ = json.Unmarshal(p.D, &resumable)
				if !resumable {
					g.sessionID, g.resumeURL = "", ""
					g.seq.Store(0)
				}
				// Discord asks for a 1-5 s pause before re-identifying.
				_ = sleepCtx(ctx, time.Second+time.Duration(rand.Int64N(int64(4*time.Second))))
				fail(&closeErr{resumable: resumable, err: errors.New("session invalidated")})
				return
			case opDispatch:
				if p.S > 0 {
					g.seq.Store(p.S)
				}
				switch p.T {
				case "READY":
					var r struct {
						SessionID string `json:"session_id"`
						ResumeURL string `json:"resume_gateway_url"`
					}
					_ = json.Unmarshal(p.D, &r)
					g.sessionID, g.resumeURL = r.SessionID, r.ResumeURL
					g.status(true, "connected")
				case "RESUMED":
					g.status(true, "resumed")
				}
				select {
				case g.Events <- Event{Type: p.T, Data: p.D}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Writer: the only goroutine that writes to conn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var interval time.Duration
		select {
		case interval = <-hello:
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
			fail(errors.New("no hello from gateway"))
			return
		}

		write := func(b []byte) error {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			return conn.WriteMessage(websocket.TextMessage, b)
		}
		heartbeat := func() error {
			s := g.seq.Load()
			d := "null"
			if s > 0 {
				d = fmt.Sprint(s)
			}
			return write([]byte(`{"op":1,"d":` + d + `}`))
		}

		var first []byte
		if resuming {
			first, _ = json.Marshal(map[string]any{"op": opResume, "d": map[string]any{
				"token": g.token, "session_id": g.sessionID, "seq": g.seq.Load(),
			}})
		} else {
			first, _ = json.Marshal(map[string]any{"op": opIdentify, "d": map[string]any{
				"token":        g.token,
				"capabilities": capabilities,
				"properties": map[string]any{
					"os": "linux", "browser": "upper", "device": "upper",
				},
				"compress": false,
				"presence": map[string]any{
					"status": "unknown", "since": 0, "activities": []any{}, "afk": false,
				},
				"client_state": map[string]any{"guild_versions": map[string]any{}},
			}})
		}
		if err := write(first); err != nil {
			fail(err)
			return
		}

		// First beat is jittered so reconnect storms spread out.
		timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
		defer timer.Stop()

		// Commands share a 120/60s budget with heartbeats; keep headroom.
		// When the budget is spent, stop reading commands (they wait in
		// g.out) but keep heartbeating until the window refills.
		const budget = 100
		tokens := budget
		window := time.NewTicker(time.Minute)
		defer window.Stop()

		for {
			out := g.out
			if tokens == 0 {
				out = nil
			}
			select {
			case <-ctx.Done():
				return
			case <-window.C:
				tokens = budget
			case <-beatNow:
				if err := heartbeat(); err != nil {
					fail(err)
					return
				}
			case <-timer.C:
				if !acked.Load() {
					fail(&closeErr{resumable: true, err: errors.New("heartbeat not acknowledged")})
					return
				}
				acked.Store(false)
				if err := heartbeat(); err != nil {
					fail(err)
					return
				}
				timer.Reset(interval)
			case b := <-out:
				tokens--
				if err := write(b); err != nil {
					fail(err)
					return
				}
			}
		}
	}()

	<-ctx.Done()

	// Close with a non-1000 code so the session stays resumable.
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(4000, "reconnecting"), time.Now().Add(time.Second))
	conn.Close()
	pw.CloseWithError(io.EOF)
	wg.Wait()

	if finalErr == nil {
		finalErr = context.Cause(ctx)
	}
	return g.classify(finalErr)
}

// classify inspects why a connection ended and resets the session when it
// can't be resumed.
func (g *Gateway) classify(err error) error {
	var ce *closeErr
	if errors.As(err, &ce) {
		if !ce.resumable {
			g.sessionID = ""
		}
		return ce.err
	}
	var we *websocket.CloseError
	if errors.As(err, &we) {
		switch we.Code {
		case 4004:
			return ErrAuth
		case 4010, 4011, 4012, 4013, 4014:
			return &FatalError{Code: we.Code, Text: we.Text}
		case 4007, 4009:
			g.sessionID, g.resumeURL = "", ""
			g.seq.Store(0)
		}
	}
	return err
}
