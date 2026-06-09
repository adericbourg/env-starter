# env-starter

`env-starter` is a single-binary, keyboard-driven terminal (TUI) meta-launcher for developer environments. Given a declarative YAML configuration, it starts a named environment's commands in dependency order — waiting for each dependency to become *healthy* before starting its dependents — so you stop manually juggling a dozen terminal tabs every time you sit down to code.

---

## Features

- **Dependency-aware startup** — commands start only after their declared dependencies pass a readiness probe.
- **Two command types** — long-running **services** (optionally run a `teardown` to stop a backing resource, then signalled) and **tasks** (run to completion with an optional `teardown`).
- **Sequential setup steps** — an optional `setup` list runs prep commands (e.g. `yarn install`) one by one before the main `run` command starts; the first failure aborts the command.
- **Pluggable readiness probes** — `tcp` (port accepts a connection) and `shell` (command exits 0).
- **Multiple source types** — pull scripts or binaries from `github`, a `url` (with optional sha256 checksum), or a `local` path.
- **Config overlays** — `--config-overlay` merges a second file on top of the base config (keyed by name), so you can layer team defaults with personal overrides.
- **Shared command reference-counting** — a command shared across environments runs once and stops only when all environments using it stop.
- **Background daemon** — a persistent supervisor runs independently of the TUI; environments keep running after the TUI closes and are visible from any number of TUI or CLI instances.
- **Per-command logs** — live ring-buffer view in the TUI, plus file tee for post-mortem inspection.

---

## Install

### Homebrew (recommended on macOS)

```sh
brew install adericbourg/tap/env-starter
brew trust --tap adericbourg/tap/env-starter
```

This is the recommended path on macOS: Homebrew strips the `com.apple.quarantine` attribute on
install, so the binary runs without the "unidentified developer" Gatekeeper warning.

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

> **macOS Gatekeeper warning?** If you downloaded the archive via a browser and macOS blocks the
> binary, run: `xattr -dr com.apple.quarantine env-starter` before moving it into place.
> Alternatively, use the Homebrew path above.

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

### Background daemon

`env-starter` runs a background daemon that owns all environments and their processes. The TUI and CLI subcommands are thin clients that connect to this daemon over a local unix socket.

- **Auto-started** — the first invocation (TUI or `run`) starts the daemon automatically.
- **Persists across client exits** — closing the TUI or a `run` command does not stop the environments.
- **Synchronized** — multiple TUI instances and headless `run` commands all connect to the same daemon and see the same state.
- **Config identity** — the daemon adopts the `--config`/`--config-overlay` flags from the first client that spawns it. Subsequent clients with different flags will connect to the running daemon unchanged; use `env-starter shutdown` first to restart with different config flags.

The daemon socket, spawn-lock, and startup log live under the OS cache directory:

```
<os.UserCacheDir()>/env-starter/daemon.sock
<os.UserCacheDir()>/env-starter/daemon.lock
<os.UserCacheDir()>/env-starter/daemon.log   (daemon startup errors)
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

### Commands

| Command | Description |
|---------|-------------|
| `env-starter` | Open the TUI to manage environments (default) |
| `env-starter run <env>` | Start an environment and wait for it to be ready (exit 0), failed (exit 1), usage error (exit 2), or timed out (exit 3) |
| `env-starter stop <env>` | Stop a running environment |
| `env-starter list` | List all configured environments (reads local config, no daemon needed) |
| `env-starter ps` | Show currently running environments and their command states |
| `env-starter shutdown` | Stop all environments and shut down the daemon |
| `env-starter update` | Update env-starter to the latest version |
| `env-starter help` | Show help |

**Example — start an environment from a script:**
```sh
env-starter run connect-order && echo "ready"
```

### Navigating the TUI

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate items in the focused pane |
| `Tab` / `←` / `→` | Switch between panes |
| `s` | Start the selected environment |
| `x` | Stop the selected environment |
| `l` | Focus/scroll the logs pane |
| `r` | Refresh |
| `Ctrl+L` | Open the selected command's log file in the default application |
| `Ctrl+C` | First press shows confirmation; a second `Ctrl+C` within 3 seconds performs a graceful shutdown — stops all environments **and shuts down the daemon** |
| `Ctrl+D` | Detach — exits the TUI immediately while leaving all environments running in the daemon. The daemon keeps running; run `env-starter` again to reconnect, or `env-starter shutdown` to stop everything. |

---

## Shell completion

Tab completion for subcommands, flags, and environment names is available for
bash, zsh, and fish. See [docs/completion.md](docs/completion.md) for install
instructions.

## Updating

env-starter can keep itself up to date.

### Startup prompt

When a newer release is available, env-starter prints a prompt before launching
the TUI:

```
env-starter v1.3.0 is available (current: 1.2.0). Update now? [y/N]:
```

- Answering **`y`** (or `yes`) downloads, verifies, and installs the new binary,
  then re-launches the app automatically.
- Any other answer (or pressing Enter) continues with the current version.
- The check is bounded by a 3-second timeout and silently skipped on any network
  error, so it never delays startup.
- **Dev builds** (`version = "dev"`) skip the check entirely.

### Manual update

```sh
env-starter update
```

Downloads and installs the latest release if a newer version is available;
prints `env-starter <version> is already up to date` and exits otherwise.

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
      teardown: docker compose down   # stops containers gracefully before killing the client
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

    - name: frontend
      type: service
      source:
        local: /home/user/projects/frontend
      setup:
        - yarn install   # runs first, must exit 0
        - yarn build     # runs second, must exit 0
      run: yarn start    # the long-running process; readiness probes apply here
      readiness:
        tcp: "localhost:3000"

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
<os.UserCacheDir()>/env-starter/logs/<command>.log
```

On Linux this is typically `~/.cache/env-starter/logs/`. On macOS it is `~/Library/Caches/env-starter/logs/`.

**Daemon socket and lock** are also stored under the cache root:

```
<os.UserCacheDir()>/env-starter/daemon.sock
<os.UserCacheDir()>/env-starter/daemon.lock
<os.UserCacheDir()>/env-starter/daemon.log   (daemon startup errors)
```

Both files are managed automatically; do not edit them by hand.

**Source cache** (downloaded/cloned sources) lives under the same cache root. Sources are always refreshed before a run; a `url` source with a `checksum` re-verifies on every refresh.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and Git hooks setup.

---

## License

GPL-3.0 — see [LICENSE](LICENSE).
