# Contributing

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
| `go test ./...` | ✓ | ✓ |
