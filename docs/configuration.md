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

> **Security note — configs are trusted as code.**
> A base config or overlay can define any command's `run`, `setup`, and
> `teardown` fields, which are executed verbatim as shell commands. A
> malicious or compromised config is equivalent to arbitrary code execution
> under your user account. See [Approving configs](#approving-configs) below
> for the approval gate that defends against a config being tampered with or
> slipped in after the fact — it does not make these fields any safer to
> author, so only write or approve commands you'd run yourself.

### Approving configs

Every config file (base and overlay) must be explicitly approved before
env-starter will load it. Approval is a sha256 hash of the file's exact
bytes, recorded in a trust store under the owner-only cache directory; it is
invalidated the instant the file's content changes, so any later edit —
intentional or not — requires a fresh review.

```sh
env-starter allow                       # preview every run/setup/teardown/readiness.shell command, then prompt
env-starter allow --print               # preview only, approves nothing
env-starter allow --yes                 # approve without prompting (e.g. in scripts)
env-starter allow --config-overlay FILE # review an overlay alongside the base config
```

The preview shows each command's [`source`](#source) as a browsable URL or path
(e.g. `https://github.com/<repo>/tree/<branch>/<subdir>` for `github`, the exact
URL for `url`, or the filesystem path for `local`), so you can go inspect the
actual code behind a `run`/`setup`/`teardown` command before approving it.

An unapproved or changed config is refused at load time, with a message
pointing back to `env-starter allow`. While a daemon is running, editing a
watched config file to something unapproved blocks the hot-reload — the
currently running environment keeps running unaffected until the file is
reviewed and re-approved.

See [SECURITY.md](../SECURITY.md#config-trust-approval-on-first-use) for the
full trust model.

### Top-level wrapper key

The entire configuration must be nested under the key `env-starter:`:

```yaml
env-starter:
  commands: [...]
  environments: [...]
```

This prevents accidental collisions when you use overlays or include the file in a larger YAML document.

### `require-checksums`

| | |
|---|---|
| Type | bool |
| Required | no |
| Default | `false` |

When `true`, a `url` source without a `checksum` is a **validation error** instead of a startup
warning. Recommended for shared or team configs: without a checksum the downloaded file — which is
then executed — is trusted on TLS alone (see [`source.url`](#sourceurl)).

When configs are merged with `--config-overlay`, `require-checksums` is never relaxed: if either
the base or the overlay sets it, the merged config enforces it.

```yaml
env-starter:
  require-checksums: true
  commands: [...]
```

---

## `commands[]`

A list of named, runnable units. Each entry has the following fields.

### `name`

| | |
|---|---|
| Type | string |
| Required | yes |

Unique identifier for the command. Referenced by `workflow[].command` and `workflow[].depends-on`.

The name must start with a letter or digit and may contain only letters, digits, `.`, `_`, `-` and
spaces. This is enforced because the name is also used as the command's log file name
(`<name>.log`); path separators or leading dots/dashes are rejected so a name can never point
outside the logs directory.

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
- **`task`** — runs to completion. A non-zero exit marks the command `error` and blocks its dependents. Stopped by running its `teardown` command (if declared). A task with no readiness probe is considered healthy (`done`) when it exits 0. A task with a readiness probe is considered healthy when its process exits 0 **and** the probe passes — useful for tasks that background a side effect (e.g. a tunnel) and return immediately.

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

### `interactive-auth`

| | |
|---|---|
| Type | bool |
| Required | no |
| Default | `false` |

Set to `true` when `run` performs an interactive, browser-based login — for
example `tsh login` (Teleport), or any command that pops open a browser tab for
SSO through a provider like JumpCloud, Okta, or Google. Most SSO providers
reject **parallel** login attempts: if two such commands launch at the same
time, only one succeeds and the rest fail.

env-starter serializes every command flagged `interactive-auth: true`: it holds
a single global lock from just before the command launches until it becomes
healthy/done, so their logins never overlap, no matter how many environments or
independent workflow branches reference them. Commands without the flag are
unaffected and keep starting concurrently as usual.

Notes:

- The login must happen in `run`, not `setup` — only the `run` launch is gated.
- Non-overlap is guaranteed, but the **order** between independent
  `interactive-auth` commands is not. If you need a specific order, add a
  `depends-on` between them in the workflow.

```yaml
interactive-auth: true
```

### `source`

Required. See the [`source`](#source) section below.

### `readiness`

| | |
|---|---|
| Type | object |
| Required | no |

Optional probe used to determine when the command is healthy. See the [`readiness`](#readiness) section below.

### `restart`

| | |
|---|---|
| Type | object |
| Required | no |
| Applies to | `service` only |

Controls automatic restart behaviour when a service becomes unhealthy. See the [`restart`](#restart) section below.

---

## `source`

Describes where the command's working directory comes from. Exactly one of `github`, `url`, or `local` must be set — specifying none or more than one is a validation error.

Sources are **always refreshed before a run**. For `github`, an existing clone is fetched and **hard-reset to the tip of the configured `branch` or tag** — any local divergence (a force-pushed remote, a dirty cache, a diverged clone) is discarded rather than causing the refresh to fail. `url` sources are re-downloaded. An optional `subdir` selects a sub-path within the fetched source as the working directory.

Cached content lives under the OS cache directory (e.g. `~/.cache/env-starter/` on Linux).

### `source.github`

Clone a GitHub repository, or refresh an existing clone to the top of its ref.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `repo` | string | yes | — | Repository in `owner/name` form, e.g. `acme/infra`. |
| `branch` | string | no | `main` | Branch **or tag** to check out. |
| `method` | string | no | auto | Transport: `ssh`, `https`, or `gh`. When unset, tries `ssh` → `gh` → `https` in order. |

Commands that share the same `repo` and `branch` reuse a single cached clone (safe for concurrent startup). Commands that share the same `repo` but use different values for `branch` each get their own separate clone. Access to each cached clone is serialized by a sibling `<clone-dir>.lock` file, so concurrent `env-starter` processes sharing the same cache never race on it.

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
| `url` | string (scalar) | yes | — | URL to download. **Must use `https`** — plaintext `http` is rejected at config load. |
| `checksum` | object | no (unless `require-checksums` is set) | — | If set, verification is mandatory. Mismatch is a hard failure — the command will not start. |
| `checksum.alg` | string | yes (if checksum set) | — | Hash algorithm. Only `sha256` is supported; anything else is rejected at config load. |
| `checksum.value` | string | yes (if checksum set) | — | Expected digest: exactly 64 hex characters, validated at config load. |

Note: `url` is a **scalar string** at the top level of the `source` mapping, not a nested object. `checksum` is its optional sibling.

> **Strongly recommended: set a `checksum`.** Without one, the downloaded file
> (which is then executed) is trusted on the TLS connection alone — a compromised
> or swapped upstream artifact would not be detected. env-starter prints a
> startup warning for any `url` source that omits a checksum; set
> [`require-checksums: true`](#require-checksums) at the top level to turn the
> warning into a hard error. The download is always served over `https`, capped
> at 2 GiB, and only follows `https` redirects.

```yaml
source:
  url: https://releases.example.com/auth-gateway/bin
  checksum:
    alg: sha256
    value: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
```

### `source.local`

Use a directory already present on disk. The path is used as-is; no caching or refresh occurs — even if the path is itself a git working copy, `env-starter` never runs git against it.

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
- A **task** is considered healthy (`done`) when it exits 0.

If set on a **task**: after the process exits 0, the probe is run to confirm the side effect is ready (e.g. a background tunnel is accepting connections). The task is marked `healthy` when the probe passes, and its dependents unblock. If `restart` is also configured, the same probe is used as the liveness check.

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

Maximum time to wait for the readiness probe to succeed. If the probe does not pass within this window, the command is marked `timeout`.

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

## `restart`

Optional. Configures automatic restart behaviour for a command.

- **Services**: auto-restart is on by default. No `restart` block is needed to enable it; add one only to tune the settings or disable it.
- **Tasks**: opt-in. A `restart` block must be declared, **and the task must have a `readiness` probe** (the liveness probe is the only failure signal — the process has already exited). A task without a readiness probe cannot use restart; validation rejects this combination.

**What triggers a restart:**

For services, either of the following:

1. The process exits unexpectedly (after it was healthy).
2. The liveness probe fails — the readiness probe is re-run on the configured interval after the command is healthy. This detects the case where a process stays alive but stops working (e.g. a Teleport tunnel with expired authentication).

For tasks, only liveness probe failure (item 2 above). Task processes are expected to exit — exit 0 is not a crash. If the process exits non-zero *after* becoming healthy, this is currently not a restart trigger (configure the `run` command to keep a foreground process alive if crash-restart is needed).

If the command has no `readiness` probe, only crash-based restart is active for services (no liveness checking). Tasks with no probe cannot use `restart` at all.

### `restart.enabled`

| | |
|---|---|
| Type | bool |
| Required | no |
| Default | `true` when a `restart` block is present |

Set to `false` to disable auto-restart for a specific command while keeping the block for other settings.

```yaml
restart:
  enabled: false
```

### `restart.max-retries`

| | |
|---|---|
| Type | integer (≥ 0) |
| Required | no |
| Default | `3` |

Maximum number of restart attempts before the command is marked `error`. Negative values are rejected.

```yaml
restart:
  max-retries: 5
```

### `restart.backoff-base`

| | |
|---|---|
| Type | Go duration string |
| Required | no |
| Default | `1s` |

The delay before the first retry. Each subsequent attempt doubles this value (exponential backoff). With the default of `1s` the delays are 1s, 2s, 4s.

```yaml
restart:
  backoff-base: 500ms
```

### `restart.check-interval`

| | |
|---|---|
| Type | Go duration string |
| Required | no |
| Default | `10s` |

How often the readiness probe is re-run after the command is healthy (liveness check). Set to `0` to disable liveness checking while still allowing crash-based restart. Has no effect if the command has no `readiness` probe.

```yaml
restart:
  check-interval: 30s
```

**Full example — service:**

```yaml
commands:
  - name: teleport
    type: service
    source:
      local: /usr/local/bin
    run: tsh proxy ssh ...
    # tsh may open a browser for SSO login (on first run or when the session
    # expires and restart re-runs it) — serialize it against other logins.
    interactive-auth: true
    readiness:
      shell: tsh status
    restart:
      max-retries: 5
      backoff-base: 2s
      check-interval: 30s
```

**Full example — task (tunnel that backgrounds itself):**

```yaml
commands:
  - name: tunnel
    type: task
    source:
      local: /usr/local/bin
    # Opens a tunnel in the background and returns exit 0.
    run: open-tunnel.sh
    readiness:
      tcp: "localhost:2222"
      timeout: 30s
    restart:
      max-retries: 3
      backoff-base: 2s
      check-interval: 15s
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
| `healthy` | Readiness probe passed. For services: the process is running and the probe passed. For tasks with a readiness probe: the process exited 0 and the probe passed (e.g. a tunnel is accepting connections). |
| `restarting` | Command was healthy but became unhealthy; currently tearing down and relaunching. The TUI shows `(retry N/max)` next to the command name. |
| `done` | Task exited with code 0. |
| `error` | Process exited non-zero, or failed to become healthy, or exhausted all restart attempts. The TUI shows `(failed after N retries)` when retries were involved. |
| `timeout` | Readiness probe did not pass within `readiness.timeout`. |
| `stopped` | Stopped explicitly (teardown run if declared, then signal for service; teardown run for task). |

---

## Behavior notes

### Shared commands

When the same command name appears in multiple environments, it runs as a single process. `env-starter` reference-counts it: the process starts when the first environment that needs it starts, and stops only when the last environment using it stops.

### Foreground supervision

All launched processes are children of the `env-starter` TUI process. Quitting the TUI (or sending SIGINT/SIGTERM to `env-starter`) triggers a graceful shutdown of all running commands.

### Auto-restart

Commands can be configured to restart automatically when they become unhealthy after being healthy. The behaviour differs by type:

**Services** restart by default. Two signals trigger a restart:

1. **Crash**: the process exits unexpectedly.
2. **Liveness failure**: the readiness probe fails during the periodic liveness check (every `restart.check-interval`, default 10 s). This catches cases where a process stays alive but stops working — for example, a Teleport tunnel whose session token expires.

**Tasks** restart only on liveness failure (item 2 above). They must opt in by declaring a `restart` block and providing a `readiness` probe. Because a task's process is expected to exit on success, process exit is not a restart trigger. On each restart attempt the task process is re-run and the probe is re-checked to confirm the side effect is healthy again.

For both types, each restart attempt is preceded by a teardown of the unhealthy resource (teardown script, if any, then for services: SIGINT → grace period → SIGKILL). Failed attempts are retried with exponential backoff (`restart.backoff-base`, doubling each time). After `restart.max-retries` failed attempts the command is marked `error` and no further restarts are attempted.

The TUI shows `(retry N/max)` during a restart cycle and `(failed after N retries)` once all attempts are exhausted.

To disable auto-restart for a specific command, set `restart.enabled: false`.

### Logs

Each command's output is captured in two places:

- **In-memory ring buffer** — shown in the live TUI log pane.
- **File tee** — written to `<os.UserCacheDir()>/env-starter/logs/<command>.log` for post-mortem inspection.

---

## Validation rules

The following conditions cause `env-starter` to fail at startup with a descriptive error:

| Rule | Error condition |
|------|----------------|
| `command.name` is required | A command entry has no `name`. |
| `command.name` must be a safe file name | Names must start with a letter or digit and contain only letters, digits, `.`, `_`, `-` and spaces (the name is used as the log file name). |
| `command.type` is required | A command entry has no `type`. |
| `command.type` must be `service` or `task` | Any other value is rejected. |
| `command.run` is required | A command entry has no `run`. |
| `source` must specify exactly one variant | `github`, `url`, and `local` are mutually exclusive; having none or more than one is rejected. |
| `checksum` must be well-formed | `alg` must be `sha256` and `value` exactly 64 hex characters. |
| `url` source needs a `checksum` when `require-checksums` is set | With top-level `require-checksums: true`, a `url` source without a checksum is rejected. |
| `readiness` probe must be `tcp` or `shell` | `http` and `log` probes are not yet supported. Specifying more than one of `tcp`/`shell` is also rejected. |
| `restart` on a task requires a `readiness` probe | A task with a `restart` block (and restart not explicitly disabled) must also declare a `readiness` probe. Without a probe, liveness monitoring is impossible for a task. |
| `restart.max-retries` must not be negative | Negative values are rejected. |
| `environment.name` is required | An environment entry has no `name`. |
| `environment.workflow` must be non-empty | An environment with an empty workflow list is rejected. |
| `workflow[].command` must reference a defined command | Unknown command names in a workflow are rejected. |
| `workflow[].depends-on` entries must be in the same workflow | Referencing a command not present in the same environment's workflow is rejected. |
| No dependency cycles | Circular `depends-on` chains are detected by DFS and rejected. |
