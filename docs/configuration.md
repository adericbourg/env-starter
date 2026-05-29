# Configuration Reference

This document is the complete reference for `env-starter`'s YAML configuration.

---

## File location & resolution

### Default path

`env-starter` resolves the base config file as follows:

1. If `$XDG_CONFIG_HOME` is set: `$XDG_CONFIG_HOME/env-starter/config.yaml`
2. Otherwise: `~/.config/env-starter/config.yaml`

### CLI overrides

| Flag | Behaviour |
|------|-----------|
| `--config FILE` | Replace the default config entirely with `FILE`. The default path is not read. |
| `--config-overlay FILE` | Load `FILE` and merge it on top of the base config. Entries are keyed by `name`; the overlay wins on any conflict. Commands and environments from the overlay that share a name with the base replace their counterpart; new names are appended. |

### Top-level wrapper key

The entire configuration must be nested under the key `env-starter:`:

```yaml
env-starter:
  commands: [...]
  environments: [...]
```

This prevents accidental collisions when you use overlays or include the file in a larger YAML document.

---

## `commands[]`

A list of named, runnable units. Each entry has the following fields.

### `name`

| | |
|---|---|
| Type | string |
| Required | yes |

Unique identifier for the command. Referenced by `workflow[].command` and `workflow[].depends-on`.

```yaml
name: database
```

### `type`

| | |
|---|---|
| Type | string (`service` \| `task`) |
| Required | yes |

Controls the command's lifecycle:

- **`service`** — long-running process (e.g. `docker run`, a proxy, a server). Never expected to return on its own. When stopped, the `teardown` command (if declared) runs first so it can shut down a backing resource gracefully (e.g. `docker stop`); then the foreground process is waited on and killed if still alive (SIGINT → 30 s grace period → SIGKILL). A service with no readiness probe is considered healthy immediately after it starts.
- **`task`** — runs to completion. A non-zero exit marks the command `error` and blocks its dependents. Stopped by running its `teardown` command (if declared). A task with no readiness probe is considered healthy when it exits 0.

```yaml
type: service
```

### `setup`

| | |
|---|---|
| Type | list of strings |
| Required | no |

An ordered list of prep commands to run **before** `run`. Each command is executed sequentially via `sh -c` in the directory resolved from `source`. If any step exits non-zero the command immediately enters the `error` state and `run` is never launched.

Use this to express multi-step startup sequences (e.g. installing dependencies before starting a server) while keeping `run` as the single long-lived monitored process:

```yaml
setup:
  - yarn install
  - yarn build
run: yarn start
```

All setup steps share the command's `env` and log stream. Setup steps run while the command is in the `starting` state, so dependents correctly wait for the full sequence (setup + run + readiness probe) to complete before starting.

### `run`

| | |
|---|---|
| Type | string |
| Required | yes |

The shell command to execute. Runs inside the directory resolved from `source` (and `source.subdir` if set). Readiness probes and service health monitoring apply to this command.

```yaml
run: docker compose up
```

### `teardown`

| | |
|---|---|
| Type | string |
| Required | no |

A shell command to run when the command is stopped. Runs in the same working directory as `run`.

- **Service**: runs **before** the foreground process is signalled, allowing it to gracefully shut down a backing resource (e.g. `docker stop` a named container). After teardown completes, the process is waited on and killed if still alive.
- **Task**: runs after the task exits.

```yaml
# gracefully stop a named Docker container before killing the foreground client
teardown: docker stop mariadb-dev
```

```yaml
# or run a cleanup script for a task
teardown: ./migrate.sh down
```

### `env`

| | |
|---|---|
| Type | map of string → string |
| Required | no |

Extra environment variables injected into the process. Values must be strings.

```yaml
env:
  PGPORT: "5432"
  LOG_LEVEL: debug
```

### `source`

Required. See the [`source`](#source) section below.

### `readiness`

| | |
|---|---|
| Type | object |
| Required | no |

Optional probe used to determine when the command is healthy. See the [`readiness`](#readiness) section below.

---

## `source`

Describes where the command's working directory comes from. Exactly one of `github`, `url`, or `local` must be set — specifying none or more than one is a validation error.

Sources are **always refreshed before a run** (git pull / re-download). An optional `subdir` selects a sub-path within the fetched source as the working directory.

Cached content lives under the OS cache directory (e.g. `~/.cache/env-starter/` on Linux).

### `source.github`

Clone or pull a GitHub repository.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `repo` | string | yes | — | Repository in `owner/name` form, e.g. `acme/infra`. |
| `branch` | string | no | `main` | Branch to check out. |
| `method` | string | no | auto | Transport: `ssh`, `https`, or `gh`. When unset, tries `ssh` → `gh` → `https` in order. |

```yaml
source:
  github:
    repo: acme/infra
    branch: main
    method: ssh     # optional
  subdir: scripts/database
```

### `source.url`

Download a file from an arbitrary URL.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string (scalar) | yes | — | URL to download. |
| `checksum` | object | no | — | If set, verification is mandatory. Mismatch is a hard failure — the command will not start. |
| `checksum.alg` | string | yes (if checksum set) | — | Hash algorithm. Currently only `sha256` is used. |
| `checksum.value` | string | yes (if checksum set) | — | Expected hex digest. |

Note: `url` is a **scalar string** at the top level of the `source` mapping, not a nested object. `checksum` is its optional sibling.

```yaml
source:
  url: https://releases.example.com/auth-gateway/bin
  checksum:
    alg: sha256
    value: "e3b0c44298fc1c149afb4c8996fb92427ae41e4649b934ca495991b7852b855"
```

### `source.local`

Use a directory already present on disk. The path is used as-is; no caching or refresh occurs.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `local` | string | yes | Absolute or relative filesystem path. |

```yaml
source:
  local: /home/user/scripts/migrate
```

### `source.subdir`

| | |
|---|---|
| Type | string |
| Required | no |
| Applies to | all source types |

A path within the fetched/cloned/local source to use as the actual working directory for `run` and `teardown`.

```yaml
source:
  github:
    repo: acme/infra
  subdir: services/database
```

---

## `readiness`

Optional. Declares how to probe whether a command is healthy before its dependents are started.

If omitted:
- A **service** is considered healthy immediately after it starts.
- A **task** is considered healthy when it exits 0.

Exactly one probe type must be set (`tcp` or `shell`). Specifying more than one is a validation error.

> **Note:** `http` and `log` probe types are reserved for future support. The current version rejects them with a clear error message. Use `tcp` or `shell` in the meantime.

### `readiness.tcp`

| | |
|---|---|
| Type | string (`host:port`) |
| Required | no |

Polls the given TCP address. The command is considered healthy when a connection is accepted.

```yaml
readiness:
  tcp: "localhost:5432"
```

### `readiness.shell`

| | |
|---|---|
| Type | string |
| Required | no |

Runs the given shell command repeatedly. The command is considered healthy when this probe exits with code 0.

```yaml
readiness:
  shell: "pg_isready -h localhost -p 5432"
```

### `readiness.timeout`

| | |
|---|---|
| Type | Go duration string (e.g. `60s`, `2m`, `1m30s`) |
| Required | no |
| Default | `30s` |

Maximum time to wait for the readiness probe to succeed. If the probe does not pass within this window, the command is marked `error`.

```yaml
readiness:
  tcp: "localhost:5432"
  timeout: 60s
```

### `readiness.interval`

| | |
|---|---|
| Type | Go duration string |
| Required | no |
| Default | `1s` |

How long to wait between probe attempts.

```yaml
readiness:
  tcp: "localhost:5432"
  interval: 2s
```

---

## `environments[]`

A list of named environments, each defining an ordered workflow of commands.

### `name`

| | |
|---|---|
| Type | string |
| Required | yes |

Unique identifier for the environment, shown in the TUI.

```yaml
name: connect-order
```

### `description`

| | |
|---|---|
| Type | string |
| Required | no |

Human-readable description shown in the TUI.

```yaml
description: Start the connect-order stack locally
```

### `workflow[]`

| | |
|---|---|
| Type | list of workflow steps |
| Required | yes (non-empty) |

Ordered list of commands to include in this environment. An empty workflow is a validation error.

Each step has the following fields:

#### `workflow[].command`

| | |
|---|---|
| Type | string |
| Required | yes |

Must reference a command defined in the top-level `commands` list. An unrecognised name is a validation error.

#### `workflow[].depends-on`

| | |
|---|---|
| Type | list of strings |
| Required | no |

Names of other commands in **this workflow** that must be healthy before this command starts. All names must be present in the same workflow; forward references to commands outside the workflow are rejected.

Dependency cycles are detected and rejected at load time.

Independent branches (steps with no shared dependencies) start in parallel.

```yaml
workflow:
  - command: database
  - command: migrate
    depends-on: [database]
  - command: auth-gateway
    depends-on: [database]
  - command: app
    depends-on: [migrate, auth-gateway]
```

---

## Statuses

### Environment statuses

| Status | Meaning |
|--------|---------|
| `stopped` | No commands are running. |
| `starting` | At least one command is starting or waiting for dependencies. |
| `running` | All commands are healthy or done. |
| `degraded` | One or more commands are in error but others are still running. |
| `error` | The environment could not start successfully. |

### Command statuses

| Status | Meaning |
|--------|---------|
| `pending` | Waiting for dependencies to become healthy. |
| `starting` | Process has been spawned; readiness probe not yet passing. |
| `healthy` | Readiness probe passed (service) or the command has not yet exited (task awaiting probe). |
| `done` | Task exited with code 0. |
| `error` | Process exited non-zero or readiness timed out. |
| `stopped` | Stopped explicitly (teardown run if declared, then signal for service; teardown run for task). |

---

## Behavior notes

### Shared commands

When the same command name appears in multiple environments, it runs as a single process. `env-starter` reference-counts it: the process starts when the first environment that needs it starts, and stops only when the last environment using it stops.

### Foreground supervision

All launched processes are children of the `env-starter` TUI process. Quitting the TUI (or sending SIGINT/SIGTERM to `env-starter`) triggers a graceful shutdown of all running commands.

### Logs

Each command's output is captured in two places:

- **In-memory ring buffer** — shown in the live TUI log pane.
- **File tee** — written to `<os.UserCacheDir()>/env-starter/logs/<env>/<command>.log` for post-mortem inspection.

---

## Validation rules

The following conditions cause `env-starter` to fail at startup with a descriptive error:

| Rule | Error condition |
|------|----------------|
| `command.name` is required | A command entry has no `name`. |
| `command.type` is required | A command entry has no `type`. |
| `command.type` must be `service` or `task` | Any other value is rejected. |
| `command.run` is required | A command entry has no `run`. |
| `source` must specify exactly one variant | `github`, `url`, and `local` are mutually exclusive; having none or more than one is rejected. |
| `readiness` probe must be `tcp` or `shell` | `http` and `log` probes are not yet supported. Specifying more than one of `tcp`/`shell` is also rejected. |
| `environment.name` is required | An environment entry has no `name`. |
| `environment.workflow` must be non-empty | An environment with an empty workflow list is rejected. |
| `workflow[].command` must reference a defined command | Unknown command names in a workflow are rejected. |
| `workflow[].depends-on` entries must be in the same workflow | Referencing a command not present in the same environment's workflow is rejected. |
| No dependency cycles | Circular `depends-on` chains are detected by DFS and rejected. |
