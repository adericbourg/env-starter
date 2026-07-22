# env-starter

env-starter starts your local dev stack in the right order and tells you when
it's actually ready.

[![Release](https://img.shields.io/github/v/release/adericbourg/env-starter?sort=semver)](https://github.com/adericbourg/env-starter/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/adericbourg/env-starter/ci.yml?branch=main&label=CI)](https://github.com/adericbourg/env-starter/actions/workflows/ci.yml)
[![License: GPL-3.0](https://img.shields.io/github/license/adericbourg/env-starter)](LICENSE)

---

Most local stacks come with a startup ritual: start the database, wait until it
accepts connections, run the migrations, start the auth service, wait again,
then start the app. A handful of terminal tabs, from memory, every morning.

env-starter replaces the ritual with a YAML file. Describe each command once —
what it runs, what it depends on, and how to tell when it's ready — then start
the whole stack with one keystroke. Each command waits for its dependencies to
be healthy, not for a hopeful `sleep`. The ordering and the waiting stop being
your job.

## Demo

![env-starter starting a dependency-ordered environment](docs/images/demo.gif)

A `database` service (Postgres) starts, and a `migrate` task runs once it
accepts connections — the exact config below, runnable as-is. One press of
`s`.

## A minimal example

```yaml
env-starter:
  commands:
    - name: database
      type: service
      source:
        local: ./scripts/database
      run: docker compose up
      teardown: docker compose down
      readiness:
        tcp: "localhost:5432"
        timeout: 60s

    - name: migrate
      type: task
      source:
        local: ./scripts/migrate
      run: ./migrate.sh up

  environments:
    - name: my-app
      workflow:
        - command: database
        - command: migrate
          depends-on: [database]
```

```sh
env-starter run my-app && echo "ready"
```

Here `migrate` runs only once Postgres accepts connections. Commands can also
be pulled from a `github` repo or a `url` with checksum verification, and a
shared team config can be overlaid with personal overrides — the
[configuration reference](docs/configuration.md) has the full schema. This
config is also the one behind the demo above; see
[`docs/examples/demo.yaml`](docs/examples/demo.yaml) for the runnable version
— it needs Docker running and a free port `5432`.

## How it works

A background daemon owns the environments and their processes; the TUI and the
CLI are thin clients talking to it over a local unix socket. Close the TUI (or
detach with `Ctrl+D`) and everything keeps running — reopen it from any
terminal, or from several at once, and you see the same state. Commands come in
two kinds: *services* stay up and are probed for readiness (`tcp` for a port
accepting connections, `shell` for a command exiting 0), *tasks* run to
completion; both can declare a `teardown`. Every command's output streams live
in the TUI and is teed to a log file for post-mortem reading.

Nothing runs behind your back: `env-starter allow` lists every command a config
would execute, with a browsable link to its source, and asks for your approval
before anything starts.

## Get started

```sh
brew install adericbourg/tap/env-starter
brew trust --tap adericbourg/tap/env-starter
env-starter allow   # review and approve your config's commands (first run only)
env-starter
```

Prebuilt binaries, `go install`, and building from source are covered in
[Installation](docs/installation.md).

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
