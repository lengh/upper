package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRESTWaitsForBucketReset(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// One request per 300ms window.
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "0.3")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	APIBase = srv.URL
	r := NewREST("t")

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := r.Messages(context.Background(), 1, 0, 50); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 550*time.Millisecond {
		t.Fatalf("3 requests with remaining=0 took %v; limiter did not wait", el)
	}
}

func TestRESTRetriesAfter429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"retry_after":0.1,"global":false}`))
			return
		}
		w.Write([]byte(`{"id":"5","username":"me"}`))
	}))
	defer srv.Close()
	APIBase = srv.URL
	u, err := NewREST("t").Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 5 || calls.Load() != 2 {
		t.Fatalf("got user %+v after %d calls", u, calls.Load())
	}
}

func TestRESTUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"401: Unauthorized","code":0}`))
	}))
	defer srv.Close()
	APIBase = srv.URL
	_, err := NewREST("bad").Me(context.Background())
	if !IsUnauthorized(err) {
		t.Fatalf("got %v, want unauthorized", err)
	}
}

func TestSnowflake(t *testing.T) {
	var s Snowflake
	if err := s.UnmarshalJSON([]byte(`"175928847299117063"`)); err != nil {
		t.Fatal(err)
	}
	if got := s.Time().UTC().Format(time.RFC3339); got != "2016-04-30T11:18:25Z" {
		t.Fatalf("time = %s", got)
	}
	b, _ := s.MarshalJSON()
	if string(b) != `"175928847299117063"` {
		t.Fatalf("marshal = %s", b)
	}
	if NewNonce() == 0 {
		t.Fatal("zero nonce")
	}
}
