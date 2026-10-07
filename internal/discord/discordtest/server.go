// Package discordtest is an in-process fake of the Discord REST API and
// Gateway, used by upper's tests. It speaks zlib-stream compressed JSON over
// a websocket exactly like the real Gateway.
package discordtest

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Server is a fake Discord. The zero value is not usable; call New.
type Server struct {
	*httptest.Server

	Token             string
	HeartbeatInterval time.Duration

	mu       sync.Mutex
	conns    []*conn
	messages map[string][]map[string]any // channel -> newest last
	nextID   atomic.Uint64
	ready    map[string]any

	Identifies atomic.Int32
	Resumes    atomic.Int32
	Acks       atomic.Int32
	Sent       chan map[string]any // REST message creates
	Commands   chan map[string]any // gateway ops other than heartbeat
}

type conn struct {
	ws  *websocket.Conn
	zw  *zlib.Writer
	buf bytes.Buffer
	mu  sync.Mutex
	seq int
}

func (c *conn) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(v)
	c.buf.Reset()
	if _, err := c.zw.Write(b); err != nil {
		return err
	}
	// Z_SYNC_FLUSH: the frame ends in 00 00 FF FF, as Discord's do.
	if err := c.zw.Flush(); err != nil {
		return err
	}
	return c.ws.WriteMessage(websocket.BinaryMessage, c.buf.Bytes())
}

func (c *conn) dispatch(t string, d any) error {
	c.mu.Lock()
	c.seq++
	s := c.seq
	c.mu.Unlock()
	return c.send(map[string]any{"op": 0, "t": t, "s": s, "d": d})
}

// New starts a fake with a small account: one guild with two channels (one
// hidden by permissions), and one DM.
func New(token string) *Server {
	s := &Server{
		Token:             token,
		HeartbeatInterval: 41250 * time.Millisecond,
		messages:          map[string][]map[string]any{},
		Sent:              make(chan map[string]any, 100),
		Commands:          make(chan map[string]any, 100),
	}
	s.ready = DefaultReady()
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// id returns a real-looking snowflake for "now", so fake messages sort and
// timestamp like Discord's.
func (s *Server) id() string {
	ms := uint64(time.Now().UnixMilli() - 1420070400000)
	return strconv.FormatUint(ms<<22|s.nextID.Add(1)&0x3fffff, 10)
}

// GatewayURL returns the ws:// URL of the fake gateway.
func (s *Server) GatewayURL() string { return "ws" + strings.TrimPrefix(s.URL, "http") + "/gateway" }

// APIURL returns the REST base URL.
func (s *Server) APIURL() string { return s.URL + "/api/v10" }

// SetReady replaces the READY payload.
func (s *Server) SetReady(r map[string]any) {
	s.mu.Lock()
	s.ready = r
	s.mu.Unlock()
}

// AddMessage seeds history for a channel.
func (s *Server) AddMessage(channel, authorID, username, content string) map[string]any {
	m := map[string]any{
		"id": s.id(), "channel_id": channel, "content": content, "type": 0,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"author":    map[string]any{"id": authorID, "username": username, "global_name": strings.ToUpper(username[:1]) + username[1:]},
	}
	s.mu.Lock()
	s.messages[channel] = append(s.messages[channel], m)
	s.mu.Unlock()
	return m
}

// Dispatch sends an event to every connected client.
func (s *Server) Dispatch(t string, d any) {
	s.mu.Lock()
	conns := append([]*conn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.dispatch(t, d)
	}
}

// DropConnections closes all gateway connections abruptly, as a network
// failure would.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.ws.Close()
	}
}

// Post creates a message from another user and dispatches it. Mentions of
// the form <@id> are listed in the message's mentions like Discord does.
func (s *Server) Post(channel, guild, authorID, username, content string) {
	m := s.AddMessage(channel, authorID, username, content)
	if guild != "" {
		m["guild_id"] = guild
	}
	if strings.Contains(content, "<@"+MeID+">") {
		m["mentions"] = []any{map[string]any{"id": MeID, "username": "me", "global_name": "Me"}}
	}
	s.Dispatch("MESSAGE_CREATE", m)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/gateway") {
		s.gateway(w, r)
		return
	}
	if r.Header.Get("Authorization") != s.Token {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"401: Unauthorized","code":0}`))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v10")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }

	switch {
	case path == "/users/@me":
		reply(s.readyUser())
	case len(parts) == 3 && parts[0] == "channels" && parts[2] == "messages" && r.Method == "GET":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
		s.mu.Lock()
		all := s.messages[parts[1]]
		var out []map[string]any
		for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
			id, _ := strconv.ParseUint(all[i]["id"].(string), 10, 64)
			if before == 0 || id < before {
				out = append(out, all[i])
			}
		}
		s.mu.Unlock()
		if out == nil {
			out = []map[string]any{}
		}
		w.Header().Set("X-RateLimit-Remaining", "4")
		w.Header().Set("X-RateLimit-Reset-After", "1")
		reply(out)
	case len(parts) == 3 && parts[0] == "channels" && parts[2] == "messages" && r.Method == "POST":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.Sent <- body
		u := s.readyUser()
		m := map[string]any{
			"id": s.id(), "channel_id": parts[1], "content": body["content"], "type": 0,
			"nonce": body["nonce"], "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"author": u,
		}
		s.mu.Lock()
		if ref, ok := body["message_reference"].(map[string]any); ok {
			m["type"] = 19
			m["message_reference"] = ref
			for _, old := range s.messages[parts[1]] {
				if old["id"] == ref["message_id"] {
					m["referenced_message"] = old
				}
			}
		}
		s.messages[parts[1]] = append(s.messages[parts[1]], m)
		s.mu.Unlock()
		s.Dispatch("MESSAGE_CREATE", m)
		reply(m)
	case len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" && r.Method == "PATCH":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		edited := map[string]any{"id": parts[3], "channel_id": parts[1], "content": body["content"],
			"edited_timestamp": time.Now().UTC().Format(time.RFC3339Nano)}
		s.Dispatch("MESSAGE_UPDATE", edited)
		reply(edited)
	case len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" && r.Method == "DELETE":
		s.Dispatch("MESSAGE_DELETE", map[string]any{"id": parts[3], "channel_id": parts[1]})
		w.WriteHeader(204)
	case len(parts) == 7 && parts[4] == "reactions":
		ev := "MESSAGE_REACTION_ADD"
		if r.Method == "DELETE" {
			ev = "MESSAGE_REACTION_REMOVE"
		}
		s.Dispatch(ev, map[string]any{"user_id": MeID, "channel_id": parts[1], "message_id": parts[3],
			"emoji": map[string]any{"name": parts[5]}})
		w.WriteHeader(204)
	case len(parts) == 5 && parts[4] == "ack":
		s.Acks.Add(1)
		reply(map[string]any{"token": nil})
	case len(parts) == 3 && parts[2] == "typing":
		w.WriteHeader(204)
	case path == "/users/@me/channels" && r.Method == "POST":
		var body struct {
			Recipients []string `json:"recipients"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(map[string]any{"id": s.id(), "type": 1,
			"recipients": []map[string]any{{"id": body.Recipients[0], "username": "newfriend"}}})
	default:
		w.WriteHeader(404)
		reply(map[string]any{"message": "404: Not Found " + path, "code": 0})
	}
}

func (s *Server) readyUser() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready["user"].(map[string]any)
}

func (s *Server) gateway(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{ws: ws}
	c.zw = zlib.NewWriter(&c.buf)
	defer ws.Close()

	if r.URL.Query().Get("compress") != "zlib-stream" {
		_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4000, "test server needs zlib-stream"))
		return
	}
	if err := c.send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": s.HeartbeatInterval.Milliseconds()}}); err != nil {
		return
	}
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var p struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
		}
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		switch p.Op {
		case 1:
			_ = c.send(map[string]any{"op": 11})
		case 2:
			var id struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal(p.D, &id)
			if id.Token != s.Token {
				_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4004, "Authentication failed."))
				return
			}
			s.Identifies.Add(1)
			s.mu.Lock()
			ready := make(map[string]any, len(s.ready)+2)
			for k, v := range s.ready {
				ready[k] = v
			}
			s.mu.Unlock()
			ready["session_id"] = fmt.Sprintf("sess-%d", s.Identifies.Load())
			ready["resume_gateway_url"] = s.GatewayURL()
			_ = c.dispatch("READY", ready)
		case 6:
			var rs struct {
				Seq int `json:"seq"`
			}
			_ = json.Unmarshal(p.D, &rs)
			c.mu.Lock()
			c.seq = rs.Seq
			c.mu.Unlock()
			s.Resumes.Add(1)
			_ = c.dispatch("RESUMED", map[string]any{})
		default:
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			select {
			case s.Commands <- m:
			default:
			}
		}
	}
}

// IDs used by DefaultReady.
const (
	MeID       = "100000000000000001"
	FriendID   = "100000000000000002"
	GuildID    = "200000000000000001"
	GeneralID  = "300000000000000001"
	SecretID   = "300000000000000002"
	CategoryID = "300000000000000003"
	RandomID   = "300000000000000004"
	DMID       = "400000000000000001"
	ModRoleID  = "500000000000000001"
)

// DefaultReady is a READY payload shaped like a real user account's.
func DefaultReady() map[string]any {
	const viewChannel = 1 << 10
	const send = 1 << 11
	return map[string]any{
		"v":    10,
		"user": map[string]any{"id": MeID, "username": "me", "global_name": "Me"},
		"guilds": []any{map[string]any{
			"id": GuildID, "name": "Test Server", "owner_id": FriendID,
			"large": true, "member_count": 300,
			"roles": []any{
				map[string]any{"id": GuildID, "name": "@everyone", "position": 0,
					"permissions": strconv.Itoa(viewChannel | send | 1<<16)},
				map[string]any{"id": ModRoleID, "name": "Mods", "position": 1, "color": 0x3498db,
					"permissions": "0"},
			},
			"members": []any{
				map[string]any{"user": map[string]any{"id": MeID, "username": "me"}, "roles": []any{}},
				map[string]any{"user": map[string]any{"id": FriendID, "username": "alice", "global_name": "Alice"},
					"nick": "Alice (mod)", "roles": []any{ModRoleID}},
			},
			"channels": []any{
				map[string]any{"id": CategoryID, "type": 4, "name": "Text", "position": 0},
				map[string]any{"id": GeneralID, "type": 0, "name": "general", "position": 0,
					"parent_id": CategoryID, "topic": "Say hi", "last_message_id": "0"},
				map[string]any{"id": RandomID, "type": 0, "name": "random", "position": 1,
					"parent_id": CategoryID},
				map[string]any{"id": SecretID, "type": 0, "name": "secret", "position": 2,
					"parent_id": CategoryID,
					"permission_overwrites": []any{map[string]any{
						"id": GuildID, "type": 0, "allow": "0", "deny": strconv.Itoa(viewChannel)}}},
			},
		}},
		"private_channels": []any{map[string]any{
			"id": DMID, "type": 1, "last_message_id": "0",
			"recipients": []any{map[string]any{"id": FriendID, "username": "alice", "global_name": "Alice"}},
		}},
		"relationships": []any{map[string]any{"type": 1, "user": map[string]any{
			"id": FriendID, "username": "alice", "global_name": "Alice"}}},
		"read_state": []any{},
	}
}
