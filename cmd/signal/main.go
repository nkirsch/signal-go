// signal sends an event to signal.nick.org, or lists recent events.
//
//	signal [-e event] [-t title] [-a app] [-p priority] [-u url] [-U label] [-s source] [message...]
//	echo message | signal -t Title
//	signal list [-s source] [-e event] [-status failed] [-n 20]
//
// The token comes from SIGNAL_TOKEN or ~/.config/signal/token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	signal "github.com/nkirsch/signal-go"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	c := signal.NewFromEnv()
	if os.Getenv("SIGNAL_TOKEN") == "" {
		home, _ := os.UserHomeDir()
		b, err := os.ReadFile(filepath.Join(home, ".config", "signal", "token"))
		if err != nil {
			fatal("no SIGNAL_TOKEN and no ~/.config/signal/token")
		}
		c.Token = signal.StaticToken(strings.TrimSpace(string(b)))
	}
	if len(os.Args) > 1 && os.Args[1] == "list" {
		list(c, os.Args[2:])
		return
	}
	send(c, os.Args[1:])
}

func send(c *signal.Client, args []string) {
	fs := flag.NewFlagSet("signal", flag.ExitOnError)
	ev := fs.String("e", "note", "event name")
	title := fs.String("t", "", "title (default: hostname)")
	app := fs.String("a", "", "Pushover app hint: deployments, 24, home, panic")
	pri := fs.Int("p", 0, "priority hint, -2..2")
	u := fs.String("u", "", "url")
	ut := fs.String("U", "", "url label")
	src := fs.String("s", "", "source (default: token name)")
	id := fs.String("id", "", "event id (dedupes repeats)")
	fs.Parse(args)
	msg := strings.Join(fs.Args(), " ")
	if msg == "" {
		if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice == 0 {
			b, _ := io.ReadAll(os.Stdin)
			msg = strings.TrimSpace(string(b))
		}
	}
	if *title == "" {
		*title, _ = os.Hostname()
	}
	e := signal.Event{ID: *id, Source: *src, Event: *ev, Title: *title, Message: msg, URL: *u, URLTitle: *ut, App: *app}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "p" {
			e.Priority = signal.P(*pri)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	res, err := c.Send(ctx, e)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Printf("%s %s rule=%q app=%s priority=%d id=%s\n", res.Status, res.Via, res.Rule, res.App, res.Priority, res.ID)
}

func list(c *signal.Client, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	src := fs.String("s", "", "source glob")
	ev := fs.String("e", "", "event glob")
	status := fs.String("status", "", "status")
	n := fs.Int("n", 20, "count")
	asJSON := fs.Bool("json", false, "raw JSON")
	fs.Parse(args)
	q := url.Values{"limit": {fmt.Sprint(*n)}}
	for k, v := range map[string]string{"source": *src, "event": *ev, "status": *status} {
		if v != "" {
			q.Set(k, v)
		}
	}
	ctx := context.Background()
	tok, err := c.Token(ctx)
	if err != nil {
		fatal(err.Error())
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/v1/events?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fatal(resp.Status + " " + string(b))
	}
	if *asJSON {
		os.Stdout.Write(b)
		return
	}
	var out struct {
		Events []struct {
			Time                                         time.Time
			Source, Event, Title, Rule, App, Via, Status string
			Priority                                     int
			Reason, Error                                string
		}
	}
	json.Unmarshal(b, &out)
	for _, e := range out.Events {
		note := e.Error + e.Reason
		fmt.Printf("%s  %-10s %-6s %-12s %-24s %-8s p%-2d %-28s %s %s\n", e.Time.Local().Format("01-02 15:04:05"), e.Status, e.Via,
			e.Source, e.Event, e.App, e.Priority, trunc(e.Rule, 28), trunc(e.Title, 50), note)
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "signal:", msg)
	os.Exit(1)
}
