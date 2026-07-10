# env-starter

**A keyboard-driven terminal launcher that starts your whole dev stack, in the right order, with one keystroke.**

[![Release](https://img.shields.io/github/v/release/adericbourg/env-starter?sort=semver)](https://github.com/adericbourg/env-starter/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/adericbourg/env-starter/ci.yml?branch=main&label=CI)](https://github.com/adericbourg/env-starter/actions/workflows/ci.yml)
[![License: GPL-3.0](https://img.shields.io/github/license/adericbourg/env-starter)](LICENSE)

---

## The problem

Getting a local dev environment running usually means a dozen terminal tabs and a
memorized ritual: start the database, wait for it to accept connections, run the
migrations, start the auth service, wait again, *then* start the frontend — and hope
you didn't skip a step or start something too early. Do that daily, across several
projects, and it's real friction before you've written a line of code.

## The solution

`env-starter` replaces the ritual with a declarative YAML file. Describe your
commands once — what they are, what they depend on, and how to tell when they're
ready — and start the whole stack with a single keystroke. `env-starter` starts
each command only after its dependencies are *healthy*, so the ordering and the
waiting are no longer your job.

## Demo

![env-starter starting a dependency-ordered environment](docs/images/demo.gif)

The environment above declares `database → {migrate, auth-gateway} → frontend`.
`env-starter` starts `database` first, waits for its readiness probe, then starts
`migrate` and `auth-gateway` in parallel, and only starts `frontend` once both are
done — all from pressing `s` once.

## Example

```yaml
env-starter:
  commands:
    - name: database
      type: service
      source:
        github:
          repo: acme/infra
          subdir: scripts/database
      run: docker compose up
      teardown: docker compose down
      readiness:
        tcp: "localhost:5432"
        timeout: 60s

    - name: migrate
      type: task
      source:
        local: /home/user/scripts/migrate
      run: ./migrate.sh up
      teardown: ./migrate.sh down

    - name: auth-gateway
      type: service
      source:
        url: https://releases.example.com/auth-gateway/bin
      run: ./bin
      readiness:
        shell: "curl -sf localhost:8080/health"

  environments:
    - name: connect-order
      workflow:
        - command: database
        - command: migrate
          depends-on: [database]
        - command: auth-gateway
          depends-on: [database]
```

```sh
env-starter run connect-order && echo "ready"
```

`source` accepts a `github` repo, a `url` (with an optional checksum), or a `local`
path — see the [configuration reference](docs/configuration.md) for the full
schema. For a version you can actually run with no setup (no Docker, no network —
it's what recorded the demo above), see
[`docs/examples/demo.yaml`](docs/examples/demo.yaml).

## Features

- **Dependency-aware startup** — commands start only after their declared dependencies pass a readiness probe.
- **Two command types** — long-running **services** and run-to-completion **tasks**, each with an optional `teardown`.
- **Pluggable readiness probes** — `tcp` (port accepts a connection) and `shell` (command exits 0).
- **Multiple source types** — pull scripts or binaries from `github`, a `url` (with checksum verification), or a `local` path.
- **Config overlays** — merge personal overrides on top of shared team config.
- **Background daemon** — environments keep running after the TUI closes, and stay visible from any number of TUI or CLI instances.
- **Per-command logs** — a live view in the TUI, plus a file tee for post-mortem inspection.

## Get started

```sh
brew install adericbourg/tap/env-starter
brew trust --tap adericbourg/tap/env-starter
env-starter
```

See [Installation](docs/installation.md) for prebuilt binaries, `go install`, and building from source.

## Documentation

- [Installation & updating](docs/installation.md)
- [Usage](docs/usage.md) — the daemon, CLI commands, and TUI keys
- [Configuration reference](docs/configuration.md) — every YAML field
- [Shell completion](docs/completion.md)
- [Releasing](docs/releasing.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and Git hooks setup.

## License

GPL-3.0 — see [LICENSE](LICENSE).
