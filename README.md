# signal-go

Go client and CLI for [signal.nick.org](https://signal.nick.org), a personal
notification router. Stdlib only.

```go
c := signal.NewFromEnv() // SIGNAL_TOKEN, or a Google ID token on Cloud Run
c.Source = "gymssp"
c.Send(ctx, signal.Event{Event: "gym.waitlist_cleared", Title: "Reserved", Message: "…", App: "24", Priority: signal.P(2)})
```

`Send` sets an id when empty, retries once with the same id (signal
dedupes), and logs one `notify … via=signal|direct|none` line. Set
`c.Fallback = signal.PushoverFallback(token, user)` only for callers that must
reach a phone while signal is down; the direct delivery is reported back to
signal.

CLI: `go install github.com/nkirsch/signal-go/cmd/signal@latest`

```
signal -t Title -a home -p 1 message      # or: echo message | signal -t Title
signal list [-s source] [-e event] [-status failed] [-n 20] [-cleared]
```

The CLI reads `SIGNAL_TOKEN` or `~/.config/signal/token`.
