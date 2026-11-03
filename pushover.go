package signal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PushoverFallback returns a Fallback that posts straight to Pushover with
// one app token and user key.
func PushoverFallback(token, user string) func(context.Context, Event) error {
	hc := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, ev Event) error {
		if token == "" || user == "" {
			return fmt.Errorf("pushover fallback: no credentials")
		}
		pri := 0
		if ev.Priority != nil {
			pri = *ev.Priority
		}
		msg := ev.Message
		if msg == "" {
			msg = ev.Event
		}
		f := url.Values{"token": {token}, "user": {user}, "title": {ev.Title}, "message": {msg}, "priority": {strconv.Itoa(pri)}}
		if ev.URL != "" {
			f.Set("url", ev.URL)
		}
		if ev.URLTitle != "" {
			f.Set("url_title", ev.URLTitle)
		}
		if ev.Sound != "" {
			f.Set("sound", ev.Sound)
		}
		if pri == 2 {
			f.Set("retry", strconv.Itoa(max(ev.Retry, 30)))
			exp := ev.Expire
			if exp == 0 {
				exp = 3600
			}
			f.Set("expire", strconv.Itoa(min(exp, 10800)))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushover.net/1/messages.json", strings.NewReader(f.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("pushover: %s", resp.Status)
		}
		return nil
	}
}
