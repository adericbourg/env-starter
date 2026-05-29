# env-starter

`env-starter` is a single-binary, keyboard-driven terminal (TUI) meta-launcher for developer environments. Given a declarative YAML configuration, it starts a named environment's commands in dependency order — waiting for each dependency to become *healthy* before starting its dependents — so you stop manually juggling a dozen terminal tabs every time you sit down to code.

---

## Features

- **Dependency-aware startup** — commands start only after their declared dependencies pass a readiness probe.
- **Two command types** — long-running **services** (stopped gracefully by signal) and **tasks** (run to completion with an optional `teardown`).
- **Pluggable readiness probes** — `tcp` (port accepts a connection) and `shell` (command exits 0).
- **Multiple source types** — pull scripts or binaries from `github`, a `url` (with optional sha256 checksum), or a `local` path.
- **Config overlays** — `--config-overlay` merges a second file on top of the base config (keyed by name), so you can layer team defaults with personal overrides.
- **Shared command reference-counting** — a command shared across environments runs once and stops only when all environments using it stop.
- **Foreground supervisor** — launched processes are children of the TUI; quitting stops everything cleanly.
- **Per-command logs** — live ring-buffer view in the TUI, plus file tee for post-mortem inspection.

---

## Install

### Download a prebuilt binary

Prebuilt archives for **Linux amd64/arm64**, **macOS amd64/arm64**, and **Windows amd64** are attached to every [GitHub Release](https://github.com/adericbourg/env-starter/releases).

Each archive (`.tar.gz` on Linux/macOS, `.zip` on Windows) contains the `env-starter` binary. Checksums are in `checksums.txt` (sha256).

```sh
# Example: macOS arm64
curl -Lo env-starter.tar.gz \
  https://github.com/adericbourg/env-starter/releases/latest/download/env-starter_<version>_darwin_arm64.tar.gz
tar xf env-starter.tar.gz
# verify
sha256sum -c checksums.txt --ignore-missing
sudo mv env-starter /usr/local/bin/
```

### go install

```sh
go install github.com/adericbourg/env-starter/cmd/env-starter@latest
```

### Build from source

```sh
git clone https://github.com/adericbourg/env-starter.git
cd env-starter
go build -o env-starter ./cmd/env-starter
```

---

## Usage

### Configuration file location

By default, `env-starter` looks for its config at:

```
$XDG_CONFIG_HOME/env-starter/config.yaml
```

If `$XDG_CONFIG_HOME` is not set, the fallback is:

```
~/.config/env-starter/config.yaml
```

### Flags

| Flag | Description |
|------|-------------|
| `--config FILE` | Replace the default config entirely with `FILE`. |
| `--config-overlay FILE` | Merge `FILE` on top of the default config (entries keyed by `name`; overlay wins on conflicts). |
| `--version` | Print the version and exit. |

### Running

```sh
env-starter                         # use default config path
env-starter --config ~/my/config.yaml
env-starter --config-overlay ~/overrides.yaml
```

The TUI launches with the list of environments on the left. Select one and press `s` to start it.

### Navigating the TUI

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate items in the focused pane |
| `Tab` / `←` / `→` | Switch between panes |
| `s` | Start the selected environment |
| `x` | Stop the selected environment |
| `l` | Focus/scroll the logs pane |
| `r` | Refresh |
| `Ctrl+C` | First press shows a "Press Ctrl+C again to quit" confirmation in the footer; a second `Ctrl+C` within 3 seconds starts a graceful shutdown (all running commands are stopped before the app closes) |

---

## Configuration example

```yaml
env-starter:
  commands:
    - name: database
      type: service
      source:
        github:
          repo: acme/infra
          branch: main
          method: ssh       # optional: ssh | https | gh
        subdir: scripts/database
      run: docker compose up
      env:
        PGPORT: "5432"
      readiness:
        tcp: "localhost:5432"
        timeout: 60s
        interval: 1s

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
        checksum:
          alg: sha256
          value: "e3b0c44298fc1c149afb..."
      run: ./bin
      readiness:
        shell: "curl -sf localhost:8080/health"

  environments:
    - name: connect-order
      description: Start the connect-order stack locally
      workflow:
        - command: database
        - command: migrate
          depends-on: [database]
        - command: auth-gateway
          depends-on: [database]
```

---

## Configuration reference

See [`docs/configuration.md`](docs/configuration.md) for the full reference covering every field, validation rules, source types, readiness probes, and environment statuses.

---

## Logs & cache

**Log files** are written to:

```
<os.UserCacheDir()>/env-starter/logs/<env>/<command>.log
```

On Linux this is typically `~/.cache/env-starter/logs/`. On macOS it is `~/Library/Caches/env-starter/logs/`.

**Source cache** (downloaded/cloned sources) lives under the same cache root. Sources are always refreshed before a run; a `url` source with a `checksum` re-verifies on every refresh.

---

## License

GPL-3.0 — see [LICENSE](LICENSE).
