# signal-go

Public Go client (`signal.go`, `pushover.go`) and CLI (`cmd/signal`) for
signal.nick.org, whose server is the private `nkirsch/signal`. Stdlib only, go
1.22. Keep the `Event`/`Result` JSON in step with the server's `main.go`.
Checks: `gofmt -l .`, `go vet ./...`, `go test ./...`. Releases are semver
tags (`v0.x.y`) because Go modules require them.
