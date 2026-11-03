// Package signal sends events to signal.nick.org, which routes them to
// Pushover and Gmail. Events carry an ID, so a retried or repeated send is
// delivered once. A client may set a Fallback for the rare caller that must
// reach Nick even when signal is down; a fallback delivery is reported back
// to signal so its record stays complete.
package signal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultURL is the signal endpoint.
const DefaultURL = "https://signal.nick.org"

// Event is one notification. Event and Title are required. App, Priority,
// Sound, Retry and Expire are hints: signal uses them unless a rule in
// signal.json routes the event differently.
type Event struct {
	ID       string `json:"id,omitempty"` // set by Send when empty
	Source   string `json:"source,omitempty"`
	Event    string `json:"event"`
	Title    string `json:"title"`
	Message  string `json:"message,omitempty"`
	URL      string `json:"url,omitempty"`
	URLTitle string `json:"url_title,omitempty"`
	RunURL   string `json:"run_url,omitempty"`
	App      string `json:"app,omitempty"`      // deployments, 24, home, panic
	Priority *int   `json:"priority,omitempty"` // -2..2; use signal.P(n)
	Sound    string `json:"sound,omitempty"`
	Retry    int    `json:"retry,omitempty"`  // priority 2: seconds between repeats
	Expire   int    `json:"expire,omitempty"` // priority 2: seconds until it gives up
	Reason   string `json:"reason,omitempty"` // fallback reports only
}

// P returns a pointer to a priority.
func P(n int) *int { return &n }

// Result is signal's answer, plus Via: "signal", "direct" (fallback) or "".
type Result struct {
	ID        string `json:"id"`
	Status    string `json:"status"` // delivered, dropped, duplicate, rate_limited, failed
	Delivered bool   `json:"delivered"`
	Rule      string `json:"rule,omitempty"`
	App       string `json:"app,omitempty"`
	Priority  int    `json:"priority"`
	Error     string `json:"error,omitempty"`
	Via       string `json:"via,omitempty"`
}

// TokenSource returns a bearer token for signal.
type TokenSource func(ctx context.Context) (string, error)

// StaticToken is a fixed bearer token from signal's SIGNAL_TOKENS.
func StaticToken(tok string) TokenSource {
	return func(context.Context) (string, error) { return tok, nil }
}

// GoogleIDToken fetches an ID token for audience from the GCP metadata
// server, for Cloud Run services whose account is in SIGNAL_GOOGLE.
func GoogleIDToken(audience string) TokenSource {
	hc := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) (string, error) {
		u := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=" + audience
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("metadata identity: %w", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("metadata identity: %s", resp.Status)
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// Client sends events. The zero value is not usable; use New or NewFromEnv.
type Client struct {
	URL    string
	Source string // default source for events that don't set one
	Token  TokenSource
	HTTP   *http.Client
	// Fallback delivers an event directly when signal can't be reached or
	// answers 5xx. Leave nil for callers that don't need it.
	Fallback func(ctx context.Context, ev Event) error
	Log      *slog.Logger
}

// New returns a client for url with the given token source.
func New(url string, token TokenSource) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}, Log: slog.Default()}
}

// NewFromEnv reads SIGNAL_URL (default DefaultURL), SIGNAL_SOURCE and
// SIGNAL_TOKEN; with no token it uses a Google ID token from the metadata
// server, so it works unconfigured on Cloud Run.
func NewFromEnv() *Client {
	url := strings.TrimSpace(os.Getenv("SIGNAL_URL"))
	if url == "" {
		url = DefaultURL
	}
	ts := GoogleIDToken(url)
	if tok := strings.TrimSpace(os.Getenv("SIGNAL_TOKEN")); tok != "" {
		ts = StaticToken(tok)
	}
	c := New(url, ts)
	c.Source = strings.TrimSpace(os.Getenv("SIGNAL_SOURCE"))
	return c
}

// errRetryable marks failures where signal may not have delivered: network
// errors and 5xx. 4xx and rate limits are final and never fall back.
var errRetryable = errors.New("signal unavailable")

// Send delivers ev through signal, retrying once (safe: the ID dedupes),
// then through Fallback if set. It logs one line per event saying how it went.
func (c *Client) Send(ctx context.Context, ev Event) (Result, error) {
	if ev.ID == "" {
		ev.ID = NewID()
	}
	if ev.Source == "" {
		ev.Source = c.Source
	}
	start := time.Now()
	res, err := c.post(ctx, "/v1/events", ev)
	if errors.Is(err, errRetryable) && ctx.Err() == nil {
		select {
		case <-time.After(2 * time.Second):
			res, err = c.post(ctx, "/v1/events", ev)
		case <-ctx.Done():
		}
	}
	if err == nil {
		res.Via = "signal"
		c.logf(slog.LevelInfo, ev, "via", "signal", "status", res.Status, "rule", res.Rule, "app", res.App, "priority", res.Priority, "ms", time.Since(start).Milliseconds())
		return res, nil
	}
	if !errors.Is(err, errRetryable) || c.Fallback == nil {
		c.logf(slog.LevelError, ev, "via", "none", "status", "failed", "err", err.Error())
		return res, err
	}
	ferr := c.Fallback(ctx, ev)
	if ferr != nil {
		c.logf(slog.LevelError, ev, "via", "none", "status", "failed", "err", err.Error(), "fallback_err", ferr.Error())
		return res, errors.Join(err, ferr)
	}
	c.logf(slog.LevelWarn, ev, "via", "direct", "status", "delivered", "reason", err.Error())
	// Tell signal, best effort, so its record shows the direct delivery.
	rep := ev
	rep.Reason = err.Error()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, rerr := c.post(rctx, "/v1/events/fallback", rep); rerr != nil {
		c.logf(slog.LevelWarn, ev, "fallback_report", "failed", "err", rerr.Error())
	}
	return Result{ID: ev.ID, Status: "delivered", Delivered: true, Via: "direct"}, nil
}

func (c *Client) post(ctx context.Context, path string, ev Event) (Result, error) {
	var res Result
	tok, err := c.Token(ctx)
	if err != nil {
		return res, fmt.Errorf("%w: token: %v", errRetryable, err)
	}
	body, _ := json.Marshal(ev)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return res, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	json.Unmarshal(b, &res)
	switch {
	case resp.StatusCode >= 500:
		return res, fmt.Errorf("%w: %s %s", errRetryable, resp.Status, res.Error)
	case resp.StatusCode >= 400:
		return res, fmt.Errorf("signal: %s %s", resp.Status, res.Error)
	}
	return res, nil
}

func (c *Client) logf(level slog.Level, ev Event, attrs ...any) {
	lg := c.Log
	if lg == nil {
		lg = slog.Default()
	}
	lg.Log(context.Background(), level, "notify", append([]any{"id", ev.ID, "source", ev.Source, "event", ev.Event, "title", ev.Title}, attrs...)...)
}

// NewID returns a random event ID. Callers that may send the same
// notification twice (retries, restarts) should derive a stable ID instead.
func NewID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
