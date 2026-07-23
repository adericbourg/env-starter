# Contributing

By participating in this project, you agree to abide by its
[Code of Conduct](CODE_OF_CONDUCT.md).

## Getting set up

### Prerequisites

- [Go](https://go.dev/dl/) 1.25 or later

### Build

```sh
go build ./...
```

### Test

```sh
go test ./...
```

### Vet

```sh
go vet ./...
```

### Format

```sh
gofmt -w .
```

### Coverage

```sh
go test -covermode=atomic -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
```

### End-to-end test

`e2e/e2e.sh` drives the real, compiled binary through the full
approve/run/stop/shutdown flow, exactly as an operator would type it (see the
script's own comments for why it's shell rather than a Go test). It builds
its own binary and runs fully isolated from your real trust store and
daemon, so it's safe to run locally:

```sh
./e2e/e2e.sh
```

---

## Git hooks

This repo ships a tracked `.githooks/` directory. The `pre-commit` hook
automatically formats any staged Go files with `gofmt` and re-stages them
before completing the commit — so every commit's content is already formatted.

**One-time setup per clone:**

```sh
git config core.hooksPath .githooks
```

This is enough; `gofmt` is already present wherever Go is installed.

> **On PRs**: a GitHub Actions job (`format.yml`) also runs `gofmt -w .` and
> pushes a `style: gofmt` commit back to your PR branch as a server-side
> safety net. If that job reformats your code, run `git pull` before
> continuing to keep your local branch in sync.

---

## CI

CI runs on every pull request and on pushes to `main`:

| Step | PRs | `main` push |
|---|---|---|
| Format check (`gofmt -l .`) | auto-fixed by the Format job | ✓ hard-fail |
| `go vet ./...` | ✓ | ✓ |
| `go build ./...` | ✓ | ✓ |
| `go test -race ./...` | ✓ | ✓ |
| Lint (`golangci-lint`) | ✓ | ✓ |
| Vulnerability scan (`govulncheck`) | ✓ | ✓ |
| Coverage report | informational only, never fails | informational only, also printed to the log |
