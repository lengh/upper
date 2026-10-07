package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// UserAgent identifies upper honestly to Discord.
const UserAgent = "upper/0.1 (+https://github.com/lengh/upper)"

// APIBase is the REST endpoint. It is a variable so tests can point it at a
// local server.
var APIBase = "https://discord.com/api/v10"

// HTTPError is a non-2xx response from the API.
type HTTPError struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("discord: %d %s (code %d)", e.Status, e.Message, e.Code)
	}
	return fmt.Sprintf("discord: HTTP %d", e.Status)
}

// REST is a Discord API client that honours per-route and global rate limits
// proactively (using the X-RateLimit headers) so requests wait locally rather
// than collecting 429s, which Discord penalises.
type REST struct {
	token string
	http  *http.Client

	mu      sync.Mutex
	buckets map[string]*bucket // route key -> bucket
	global  time.Time          // no requests before this instant
}

type bucket struct {
	mu        sync.Mutex
	remaining int
	reset     time.Time
}

func NewREST(token string) *REST {
	return &REST{
		token: token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
		buckets: map[string]*bucket{},
	}
}

func (r *REST) bucket(route string) *bucket {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buckets[route]
	if b == nil {
		b = &bucket{remaining: 1}
		r.buckets[route] = b
	}
	return b
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do performs a request. route identifies the rate-limit bucket and should
// contain the method and the major parameter (channel ID), e.g.
// "POST /channels/123/messages". Requests on one route are serialised.
func (r *REST) do(ctx context.Context, method, path, route string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	b := r.bucket(route)
	b.mu.Lock()
	defer b.mu.Unlock()

	for attempt := 0; ; attempt++ {
		if b.remaining <= 0 {
			if err := sleepCtx(ctx, time.Until(b.reset)); err != nil {
				return err
			}
		}
		r.mu.Lock()
		g := r.global
		r.mu.Unlock()
		if err := sleepCtx(ctx, time.Until(g)); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, method, APIBase+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", r.token)
		req.Header.Set("User-Agent", UserAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := r.http.Do(req)
		if err != nil {
			if attempt < 2 && ctx.Err() == nil {
				if err := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); err != nil {
					return err
				}
				continue
			}
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		b.update(resp.Header)

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
				Global     bool    `json:"global"`
			}
			_ = json.Unmarshal(data, &rl)
			wait := time.Duration(rl.RetryAfter*float64(time.Second)) + 50*time.Millisecond
			if rl.Global || resp.Header.Get("X-RateLimit-Global") != "" {
				r.mu.Lock()
				r.global = time.Now().Add(wait)
				r.mu.Unlock()
			} else {
				b.remaining, b.reset = 0, time.Now().Add(wait)
			}
			if attempt >= 4 {
				return &HTTPError{Status: resp.StatusCode, Message: "rate limited"}
			}
			continue
		case resp.StatusCode >= 500 && attempt < 2:
			if err := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); err != nil {
				return err
			}
			continue
		case resp.StatusCode >= 300:
			he := &HTTPError{Status: resp.StatusCode}
			_ = json.Unmarshal(data, he)
			return he
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func (b *bucket) update(h http.Header) {
	rem := h.Get("X-RateLimit-Remaining")
	after := h.Get("X-RateLimit-Reset-After")
	if rem == "" || after == "" {
		// Unknown bucket: allow the next request immediately.
		b.remaining = 1
		return
	}
	n, err1 := strconv.Atoi(rem)
	s, err2 := strconv.ParseFloat(after, 64)
	if err1 != nil || err2 != nil {
		b.remaining = 1
		return
	}
	b.remaining = n
	b.reset = time.Now().Add(time.Duration(s * float64(time.Second)))
}

// IsUnauthorized reports whether err means the token is invalid.
func IsUnauthorized(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusUnauthorized
}

// Me returns the user the token belongs to; used to validate a token.
func (r *REST) Me(ctx context.Context) (*User, error) {
	var u User
	err := r.do(ctx, "GET", "/users/@me", "GET /users/@me", nil, &u)
	return &u, err
}

// Messages fetches up to limit (max 100) messages older than before (0 means
// newest). The result is ordered newest first, as Discord returns it.
func (r *REST) Messages(ctx context.Context, channel, before Snowflake, limit int) ([]Message, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != 0 {
		q.Set("before", before.String())
	}
	var msgs []Message
	path := "/channels/" + channel.String() + "/messages"
	err := r.do(ctx, "GET", path+"?"+q.Encode(), "GET "+path, nil, &msgs)
	return msgs, err
}

type sendMessage struct {
	Content          string            `json:"content"`
	Nonce            Snowflake         `json:"nonce,omitempty"`
	MessageReference *MessageReference `json:"message_reference,omitempty"`
	AllowedMentions  *allowedMentions  `json:"allowed_mentions,omitempty"`
	Flags            int               `json:"flags"`
}

type allowedMentions struct {
	Parse       []string `json:"parse"`
	RepliedUser bool     `json:"replied_user"`
}

// Send posts a message. If replyTo is non-zero the message is a reply;
// mentionReply controls whether the replied-to author is pinged.
func (r *REST) Send(ctx context.Context, channel Snowflake, content string, nonce, replyTo Snowflake, mentionReply bool) (*Message, error) {
	body := sendMessage{Content: content, Nonce: nonce}
	if replyTo != 0 {
		body.MessageReference = &MessageReference{MessageID: replyTo, ChannelID: channel}
		body.AllowedMentions = &allowedMentions{
			Parse:       []string{"users", "roles", "everyone"},
			RepliedUser: mentionReply,
		}
	}
	var m Message
	path := "/channels/" + channel.String() + "/messages"
	err := r.do(ctx, "POST", path, "POST "+path, body, &m)
	return &m, err
}

func (r *REST) Edit(ctx context.Context, channel, id Snowflake, content string) error {
	path := "/channels/" + channel.String() + "/messages/" + id.String()
	route := "PATCH /channels/" + channel.String() + "/messages/:id"
	return r.do(ctx, "PATCH", path, route, map[string]string{"content": content}, nil)
}

func (r *REST) Delete(ctx context.Context, channel, id Snowflake) error {
	path := "/channels/" + channel.String() + "/messages/" + id.String()
	route := "DELETE /channels/" + channel.String() + "/messages/:id"
	return r.do(ctx, "DELETE", path, route, nil, nil)
}

// Typing triggers the typing indicator for ~10 seconds.
func (r *REST) Typing(ctx context.Context, channel Snowflake) error {
	path := "/channels/" + channel.String() + "/typing"
	return r.do(ctx, "POST", path, "POST "+path, nil, nil)
}

// Ack marks the channel read up to message id, syncing with other clients.
func (r *REST) Ack(ctx context.Context, channel, id Snowflake) error {
	path := "/channels/" + channel.String() + "/messages/" + id.String() + "/ack"
	return r.do(ctx, "POST", path, "POST /channels/:id/messages/:id/ack",
		map[string]any{"token": nil}, nil)
}

// OpenDM returns the DM channel with a user, creating it if needed.
func (r *REST) OpenDM(ctx context.Context, user Snowflake) (*Channel, error) {
	var c Channel
	err := r.do(ctx, "POST", "/users/@me/channels", "POST /users/@me/channels",
		map[string]any{"recipients": []Snowflake{user}}, &c)
	return &c, err
}
