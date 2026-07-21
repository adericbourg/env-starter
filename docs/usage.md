# Usage

## Configuration file location

By default, `env-starter` looks for its config at:

```
$XDG_CONFIG_HOME/env-starter/config.yaml
```

If `$XDG_CONFIG_HOME` is not set, the fallback is:

```
~/.config/env-starter/config.yaml
```

See the [configuration reference](configuration.md) for the full YAML schema.

## Background daemon

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

## Flags

| Flag | Description |
|------|-------------|
| `--config FILE` | Replace the default config entirely with `FILE`. |
| `--config-overlay FILE` | Merge `FILE` on top of the default config (entries keyed by `name`; overlay wins on conflicts). **Overlay files are trusted as code** — a malicious overlay can replace any command's `run`/`setup`/`teardown` fields. Only use overlays from sources you control. |
| `--version` | Print the version and exit. |

## Running

```sh
env-starter                         # use default config path
env-starter --config ~/my/config.yaml
env-starter --config-overlay ~/overrides.yaml
```

The TUI launches with the list of environments on the left. Select one and press `s` to start it.

## Commands

| Command | Description |
|---------|-------------|
| `env-starter` | Open the TUI to manage environments (default) |
| `env-starter run <env>` | Start an environment and wait for it to be ready (exit 0), failed (exit 1), usage error (exit 2), or timed out (exit 3) |
| `env-starter stop <env>` | Stop a running environment |
| `env-starter list` | List all configured environments (reads local config, no daemon needed) |
| `env-starter ps` | Show currently running environments and their command states |
| `env-starter command list` | Show started commands and their state (needs a running daemon) |
| `env-starter command restart <name>` | Restart a single command in place, waiting until it is healthy (exit 0), failed (exit 1), usage error (exit 2), or timed out (exit 3) — needs a running daemon |
| `env-starter allow` | Review a config's `run`/`setup`/`teardown`/`readiness.shell` commands and approve it; `--print` previews without approving, `--yes` approves without prompting |
| `env-starter shutdown` | Stop all environments and shut down the daemon |
| `env-starter update` | Update env-starter to the latest version |
| `env-starter help` | Show help |

**Example — start an environment from a script:**
```sh
env-starter run connect-order && echo "ready"
```

## Navigating the TUI

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate items in the focused pane |
| `Tab` / `←` / `→` | Switch between panes |
| `s` | Start the selected environment (Environments pane only) |
| `x` | Stop the selected environment (Environments pane only) |
| `R` (Shift+R) | Restart the selected command (Commands pane only) |
| `r` | Refresh logs (Logs pane only) |
| `Ctrl+L` | Open the selected command's log file in the default application (Logs pane only) |
| `Ctrl+C` | First press shows confirmation; a second `Ctrl+C` within 3 seconds performs a graceful shutdown — stops all environments **and shuts down the daemon** |
| `Ctrl+D` | Detach — exits the TUI immediately while leaving all environments running in the daemon. The daemon keeps running; run `env-starter` again to reconnect, or `env-starter shutdown` to stop everything. |

## Shell completion

Tab completion for subcommands, flags, and environment names is available for
bash, zsh, and fish. See [completion.md](completion.md) for install
instructions.

## Logs & cache

**Log files** are written to:

```
<os.UserCacheDir()>/env-starter/logs/<command>.log
```

On Linux this is typically `~/.cache/env-starter/logs/`. On macOS it is `~/Library/Caches/env-starter/logs/`.

Log files are created with `0600` permissions (owner read/write only). They are **not redacted**: if a command echoes tokens, passwords, or other secrets to stdout/stderr, those appear verbatim in the log file. Avoid running commands that print secrets, or rotate/delete log files after use.

**Daemon socket and lock** are also stored under the cache root:

```
<os.UserCacheDir()>/env-starter/daemon.sock
<os.UserCacheDir()>/env-starter/daemon.lock
<os.UserCacheDir()>/env-starter/daemon.log   (daemon startup errors)
```

Both files are managed automatically; do not edit them by hand.

**Source cache** (downloaded/cloned sources) lives under the same cache root. Sources are always refreshed before a run; a `url` source with a `checksum` re-verifies on every refresh. Each cached source has a sibling `<name>.lock` file (e.g. `github-owner-name-branch.lock`) used to serialize access to it across processes; these are managed automatically.
