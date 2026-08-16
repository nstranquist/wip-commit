# Contributing

Use Go for production helpers. Keep commits reviewable and use Conventional
Commit subjects. Do not weaken a fail-closed check to make a test pass.

Before a change is ready, run:

```text
GOWORK=off go mod tidy -diff
GOWORK=off go mod verify
GOWORK=off go test ./...
GOWORK=off go test -race -count=3 ./...
GOWORK=off go vet ./...
golangci-lint run ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOWORK=off go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOWORK=off go test -exec=/usr/bin/true ./...
```

Run `actionlint` when a workflow changes. Hosted Windows tests are required
before a public release. A cross-build does not exercise Windows file locks or
atomic replacement.

Tests that mutate Git must use disposable repositories. Never require a stash,
reset, force update, remote push, or global Git configuration change.

Changes to state or capture paths need adversarial tests for symlinks,
concurrent movement, exact ref preservation, and partial-operation recovery.

Read [GOVERNANCE.md](GOVERNANCE.md), [SUPPORT.md](SUPPORT.md), and
[SECURITY.md](SECURITY.md) before proposing a change. The repository owner must
approve a Code of Conduct and configure a confidential conduct-reporting path
before the project solicits public contributions. Do not put a security or
conduct report in a public issue.

Read [docs/OSS-PRACTICE-GUIDE.md](docs/OSS-PRACTICE-GUIDE.md) before changing
public scope, governance, security, dependencies, support, or release policy.
Review its official source register when a listed trigger applies.
