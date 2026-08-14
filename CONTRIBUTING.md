# Contributing

Use Go for production helpers. Keep commits reviewable and use Conventional
Commit subjects. Do not weaken a fail-closed check to make a test pass.

Before a change is ready, run:

```text
go test ./...
go test -race -count=3 ./...
go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=/usr/bin/true ./...
```

Run `actionlint` when a workflow changes. Hosted Windows tests are required
before a public release; a cross-build does not exercise Windows file locks or
atomic replacement.

Tests that mutate Git must use disposable repositories. Never require a stash,
reset, force update, remote push, or global Git configuration change.
