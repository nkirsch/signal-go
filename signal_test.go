package signal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func quiet(c *Client) *Client { c.Log = slog.New(slog.NewTextHandler(io.Discard, nil)); return c }

func TestSendRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		json.NewDecoder(r.Body).Decode(&ev)
		ids = append(ids, ev.ID)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"id":"` + ev.ID + `","status":"delivered","delivered":true,"rule":"hint","app":"24"}`))
	}))
	defer srv.Close()
	c := quiet(New(srv.URL, StaticToken("tok")))
	c.Source = "gymssp"
	res, err := c.Send(context.Background(), Event{Event: "x", Title: "t", App: "24", Priority: P(1)})
	if err != nil || res.Via != "signal" || res.Rule != "hint" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Errorf("retry ids = %v, want the same id twice", ids)
	}
}

func TestFallbackOnlyWhenUnavailable(t *testing.T) {
	var status atomic.Int32
	var reported atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events/fallback" {
			var ev Event
			json.NewDecoder(r.Body).Decode(&ev)
			reported.Store(ev.Reason != "")
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	var fell int
	c := quiet(New(srv.URL, StaticToken("tok")))
	c.Fallback = func(context.Context, Event) error { fell++; return nil }

	status.Store(502)
	res, err := c.Send(context.Background(), Event{Event: "panic", Title: "t"})
	if err != nil || res.Via != "direct" || fell != 1 || !reported.Load() {
		t.Fatalf("5xx: res %+v err %v fell %d reported %v", res, err, fell, reported.Load())
	}
	// A 4xx or rate limit is final: no fallback.
	for _, code := range []int{400, 429} {
		status.Store(int32(code))
		if _, err := c.Send(context.Background(), Event{Event: "panic", Title: "t"}); err == nil || fell != 1 {
			t.Errorf("%d: err %v fell %d", code, err, fell)
		}
	}
}
