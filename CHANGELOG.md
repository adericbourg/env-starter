# Changelog

All notable changes to env-starter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Releases follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Environments can set `auto-start: true` to start automatically when the
  TUI launches. It applies only to the TUI (not `env-starter run`), and only
  when the environment isn't already running — reconnecting to a daemon that
  kept it alive across TUI restarts is a no-op. See
  `docs/configuration.md` (`environments[].auto-start`).

### Changed
- The TUI footer's shortcut hints (`s`, `x`, `R`, `Ctrl+L`) now dim when the
  shortcut would have no effect on the current selection, instead of looking
  identical to a shortcut that would actually act.
- Config reload (`c`) is now selective instead of stopping everything: only
  environments/commands that were removed or actually changed are stopped or
  restarted, and a running command untouched by the change keeps running
  across the reload. See `docs/configuration.md` (Hot-reload behavior).

### Fixed
- A daemon-attached TUI's environment/workflow list stopped updating after a
  reload added or removed an environment or command — the client only
  refreshed its local mirror from state events, which carry no topology
  information. The client now re-fetches a snapshot after a successful
  reload.
- The TUI's selected environment/command could jump to a different one after
  a reload added or removed an entry above it in the list, since the cursor
  only tracked a numeric index. It now re-anchors the selection by name.

## [1.6.3] — 2026-07-28

### Fixed
- Release signing and self-update verification broke after a Renovate
  bump to cosign v3, which replaced the raw detached `checksums.txt.sig`
  with a Sigstore bundle (JSON). `.goreleaser.yaml` now signs with
  `cosign sign-blob --bundle`, and the self-updater's `verifyChecksums`
  unwraps `messageSignature.signature` from the bundle before running the
  existing ECDSA check.

## [1.6.2] — 2026-07-28

### Added
- The TUI footer now shows the running build's version (e.g. `v1.2.3`) at
  the bottom right, alongside the shortcut hints.

## [1.6.1] — 2026-07-22

### Changed
- The env inspector's flat list is now a searchable, filterable Key/Origin
  table (values are never shown here): a search field filters rows by
  case-insensitive key substring, and origin facets (`F5` All, `F6`
  OS/user, `F7` environment, `F8` command) narrow the table by source.
  `Enter` on a row opens a details screen with its Value, Origin, and
  Overrides — still masked by default, `Space` reveals. See
  `docs/usage.md` ("Env inspector").

### Fixed
- The env inspector details screen no longer shifts position when
  toggling reveal: the block is rendered at a fixed, clamped width before
  centering, so a long value wraps onto extra lines instead of widening
  (and re-centering) the block.
- The TUI footer shortcut label for the env inspector was corrected from
  "e env" to "e env inspector".

## [1.6.0] — 2026-07-22

### Changed
- `--config-overlay` now field-merges a same-named command/environment onto
  its base counterpart instead of replacing it wholesale: each field the
  overlay sets wins, fields it omits are inherited from the base, and `env`
  maps merge key-by-key (overlay wins per key). This lets a secrets overlay
  declare only `{name, env}` and still inherit the base entry's
  `run`/`source`/`workflow`/etc. — see `docs/configuration.md`
  ("Secrets and overrides").

### Added
- New TUI env inspector (`e` key, from the Environments/Commands/Logs pane):
  a read-only overlay listing every environment variable visible to the
  selected environment or command, with its provenance (`OS`, `environment`,
  `command`). Values are masked by default; revealing (`Enter`/`Space`)
  shows only the selected row's value — never the whole list — along with
  any lower-priority value it overrides, and re-masks when the selection
  moves or the overlay closes. See `docs/usage.md` ("Env inspector").
- Environments can now declare `env`, applied to every command in their
  `workflow`. For a command shared by several environments, the effective env
  is the union of the sharing environments' `env`, overridden by the command's
  own `env`. Two sharing environments setting the same key to different values
  is rejected at load time (see `docs/configuration.md`).
- Commands with a `readiness` probe now check it once *before* spawning
  anything. If it already passes, the command is adopted as healthy but
  **unmanaged** instead of launching a duplicate process: a warning is logged
  and the TUI shows `(unmanaged)` next to the command name. env-starter keeps
  watching it in the background and takes over with a normal managed start
  the moment the probe fails.

### Fixed
- GitHub source refresh now fetches and hard-resets to the tip of the
  configured branch/tag (`fetch` + `reset --hard FETCH_HEAD` + `clean -fd`)
  instead of `git pull --ff-only`, so a force-pushed remote, a diverged
  clone, or a dirty cache no longer breaks the refresh. A regression test
  pins that `local` sources are never git-touched.

## [1.5.2] — 2026-07-21

### Fixed
- The starting/stopping spinner now animates on its own 80ms ticker instead
  of piggybacking on the 500ms log-refresh tick, which read as sluggish (a
  full rotation took 5s).

## [1.5.1] — 2026-07-21

### Added
- The `allow` preview now shows each command's `source` as a browsable URL
  or filesystem path, so you can go inspect the code behind a command
  before approving it. See `docs/configuration.md`.

## [1.5.0] — 2026-07-21

### Security
- Config files (base and overlay) now require explicit approval before
  env-starter will load them: each file's sha256 is checked against a trust
  store, and approval is invalidated the instant the file's content changes.
  Defends against a config that was tampered with or slipped in after the
  fact — `run`/`setup`/`teardown`/`readiness.shell` are still executed
  verbatim as shell scripts by design. Review and approve with the new
  `env-starter allow` subcommand (`--print` to preview only, `--yes` to skip
  the prompt). Hot-reload now surfaces an unapproved/changed config as a
  distinct, actionable banner in the TUI footer instead of the generic
  parse-error message. See SECURITY.md ("Config trust").

### Changed
- Keyboard shortcuts are now scoped to the pane they act on instead of
  firing globally: `s`/`x` (start/stop) only in the Environments pane,
  `Shift+R` (restart) only in the Commands pane, and `r` (refresh logs) /
  `Ctrl+L` (open log file) only in the Logs pane. The redundant `l`
  jump-to-logs shortcut was removed now that focus is pane-scoped.

### Added
- CI now collects test coverage and reports it as a monitor-only breakdown
  on every run (it never fails the build); `CONTRIBUTING.md` documents the
  local coverage command and corrects the CI table.
- golangci-lint set expanded with `errorlint`, `bodyclose`, `misspell`, and
  `unconvert`.
- An end-to-end shell script (`e2e/e2e.sh`) exercises the compiled binary
  through the full config-approval and lifecycle flow; it runs in CI on
  every pull request and push to main, plus a weekly scheduled run on
  Ubuntu and macOS.

## [1.4.0] — 2026-07-21

### Added
- New `interactive-auth` command flag for commands whose `run` performs an
  interactive browser-based login (e.g. SSO). Most providers reject
  parallel login attempts, so env-starter now serializes every command
  flagged `interactive-auth: true` behind a single global lock held from
  just before launch until the command is healthy or done — logins never
  overlap, on both initial start and restart. Commands without the flag are
  unaffected. See `docs/configuration.md` ("interactive-auth").

## [1.3.0] — 2026-07-16

### Added
- Commands can now be restarted individually without restarting their
  whole environment: `Shift+R` in the TUI (Commands pane focused), or
  headlessly via `env-starter command restart <name>`. `env-starter command
  list` shows every started command and its state. The restart preserves
  environment holders and ignores the command's `restart` policy — it
  recycles even when auto-restart is disabled. Shell completion now also
  completes `command` verbs and command names. See `docs/usage.md`.

## [1.2.1] — 2026-07-16

### Fixed
- Zsh completion install instructions now recommend a dedicated
  `~/.zsh/completions` directory (added to `fpath` explicitly) instead of
  `${fpath[1]}`, which is arbitrary and depends on plugin load order. The
  embedded completion script's own header comment is updated to match.

## [1.2.0] — 2026-07-10

### Added
- `require-checksums` config option: a `url` source without a `checksum`
  becomes a validation error instead of a startup warning, and checksum
  format (`sha256`, 64 hex chars) is now validated at config load instead
  of failing at fetch time. See `docs/configuration.md`.
- Command names are now validated against a safe file-name character set
  (must start with a letter/digit; only letters, digits, `.`, `_`, `-`, and
  spaces) — a name like `../../evil` could otherwise escape the logs
  directory since names are used verbatim as `<name>.log`.
- Releases now include an SPDX SBOM per archive and a Sigstore build-
  provenance attestation, verifiable with `gh attestation verify`.
- The source cache is now serialized across processes with a sibling
  `<name>.lock` advisory file lock on top of the existing in-process mutex,
  so two `env-starter` processes sharing the OS cache never race on the
  same clone/download.

### Fixed
- Logs were silently written to a shared, world-writable temp directory
  (`os.TempDir()`) when the per-user cache directory couldn't be resolved.
  env-starter now fails to start instead.

### Security
- The daemon is now hardened against other local users: its socket
  directory is tightened to owner-only (`0700`) even if it pre-existed with
  looser permissions, the unix socket is created under a restrictive
  umask, and each connecting peer's uid is checked against the daemon's
  own (`SO_PEERCRED`/`LOCAL_PEERCRED`), rejecting mismatches. Previously
  filesystem permissions were the only barrier, and any connected peer
  could start the owner's configured commands or shut the daemon down.
- Source cache directories (GitHub clones and URL downloads) are
  predictable by name; a pre-existing directory is now only trusted after
  verifying it's owner-only and privately owned, rather than reused as-is.
- Self-update's release-tag parsing and artifact download are now as
  strict as source downloads: the tag is validated against a release-tag
  pattern before being interpolated into download URLs, and downloads
  enforce an https-only, bounded-redirect policy.
- GitHub Actions in every workflow are now pinned to a full commit SHA
  (tag kept as a trailing comment) instead of a mutable version tag,
  maintained going forward by Renovate's `helpers:pinGitHubActionDigests`
  preset.
- `SECURITY.md` documents the local, per-user trust model underlying the
  hardening above and adds a section on `url` source integrity /
  `require-checksums`.

## [1.1.1] — 2026-07-07

### Added
- golangci-lint (`staticcheck`, `errcheck`, `gosec`, `ineffassign`) and
  govulncheck steps added to CI, followed by an action upgrade (Go 1.25
  support), a migration to the v2 config schema, and a cleanup of the 50
  issues the v2 migration surfaced.

### Changed
- CI test step runs with `-race` to catch data races in the concurrent
  supervisor and daemon hub.

### Fixed
- Windows builds failed outright: a whole-file `//go:build !windows` tag
  hid daemon spawn helpers from the Windows compiler even though `main.go`
  references them unconditionally. OS-specific pieces are now isolated
  into `spawn_unix.go`/`spawn_windows.go`, and CI cross-compiles
  Windows/amd64, Darwin/arm64, and Linux/arm64 on every PR.
- Self-update's archive download could silently write a truncated or
  corrupted binary: a `Close` flush error on the destination file was
  discarded. The download now propagates it as the operation's error.

### Security
- Self-update now **fails closed** when `cosignPublicKeyPEM` is not
  configured: `Apply` returns an error instead of warning and proceeding
  with TLS-only integrity. Self-update is disabled until the maintainer
  embeds the cosign public key and cuts a signed release.
- Added `SECURITY.md` documenting the vulnerability-reporting process and
  the self-update trust model; the README and `docs/configuration.md` now
  flag that `--config-overlay` files execute as code and that command logs
  are stored unredacted (`0600`).

## [1.0.0] — 2026-06-26

### Added
- Background daemon and headless CLI subcommands (`run`, `stop`, `list`,
  `ps`, `shutdown`).
- Shell completion for bash, zsh, and fish (`env-starter completion`).
- Contextual hyperlinks extracted from recent log lines (OSC 8).
- Log screen displayed on shutdown.
- `--config-overlay` flag to merge an extra config file on top of the base
  config, keyed by command name.
- Config validation: GitHub repo name/branch format, subdir traversal guard,
  https-only URL sources, checksum warning for URL sources without a digest.
- Terminal-escape neutralization (`termsafe`) applied to all command output
  to prevent OSC-52, title injection, and similar attacks from subprocesses.
- Cosign signature verification for self-update: `checksums.txt` is verified
  against a detached `.sig` before any digest is trusted.
- Windows zip archive support in self-update; extraction size capped at
  512 MiB.
- Homebrew tap distribution (`adericbourg/tap`) to avoid macOS Gatekeeper
  warnings on unsigned binaries.
- Config validation for GitHub repo/branch charset and subdir traversal.
- Streaming sha256 verification and size-capped downloads for URL sources.

### Security
- Daemon cache directory created `0700`; log files restricted to `0600`.
- Archive extraction uses `filepath.Base` to prevent zip-slip; binary read
  into memory rather than written to an attacker-controlled path.
- Download size capped at 2 GiB for URL sources; update binary capped at
  512 MiB.
- URL source HTTPS enforced at config-load time; safe redirect handling.

### Fixed
- Task teardown logs now visible in the TUI log view.
- Log files older than 30 days purged at startup.
- RPCs continue to be served during shutdown so `StoppingCommands` status
  is visible to clients.
- Source fetch output no longer leaks into the TUI.
- Concurrent source fetches serialized per cache directory.
- Missing subdir surfaces as a log error instead of a silent failure.

## [0.4.1] — 2026-06-01

### Added
- Log file purge: files older than 30 days are removed at startup.

### Fixed
- Task teardown logs shown in the TUI log view.

## [0.4.0] — 2026-06-01

### Added
- Homebrew tap distribution via `adericbourg/homebrew-tap`.

## [0.3.0] — 2026-06-01

### Fixed
- Missing subdir in a GitHub source surfaces as a log error rather than a
  silent failure.
- Self-update release lookup switched from rate-limited GitHub REST API to
  redirect-based detection.

## [0.2.1] — 2026-06-01

### Fixed
- Git/gh fetch output no longer leaks into the TUI.
- Concurrent `Fetch` calls for the same cache directory serialized to
  prevent partial-write races.

## [0.2.0] — 2026-06-01

### Added
- Auto-restart unhealthy services with exponential backoff.
- Distinct `timeout` status for readiness probe expiry.
- Opt-in health probe and restart for tasks.
- Hot-reload config on `c` key with a change-detection banner.
- Config parse errors surfaced in the TUI footer.

### Fixed
- Unstarted environments keep shared commands stopped.

## [0.1.0] — 2026-05-29

Initial release.

### Added
- TUI with keyboard-driven environment management (`↑/↓`, `s`, `x`, `l`,
  `r`, `Ctrl+C`, `Ctrl+D`).
- YAML config (`~/.config/env-starter/config.yaml`): `commands` with
  `type`, `source`, `run`, `setup`, `teardown`, `readiness`, `env`; and
  `environments` with `workflow` dependency ordering.
- TCP and shell readiness probes.
- Capped ring-buffer log per command with file tee.
- `github`, `url`, and `local` source types.
- Config overlay merge support.

[Unreleased]: https://github.com/adericbourg/env-starter/compare/v1.6.3...HEAD
[1.6.3]: https://github.com/adericbourg/env-starter/compare/v1.6.2...v1.6.3
[1.6.2]: https://github.com/adericbourg/env-starter/compare/v1.6.1...v1.6.2
[1.6.1]: https://github.com/adericbourg/env-starter/compare/v1.6.0...v1.6.1
[1.6.0]: https://github.com/adericbourg/env-starter/compare/v1.5.2...v1.6.0
[1.5.2]: https://github.com/adericbourg/env-starter/compare/v1.5.1...v1.5.2
[1.5.1]: https://github.com/adericbourg/env-starter/compare/v1.5.0...v1.5.1
[1.5.0]: https://github.com/adericbourg/env-starter/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/adericbourg/env-starter/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/adericbourg/env-starter/compare/v1.2.1...v1.3.0
[1.2.1]: https://github.com/adericbourg/env-starter/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/adericbourg/env-starter/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/adericbourg/env-starter/compare/v1.0.0...v1.1.1
[1.0.0]: https://github.com/adericbourg/env-starter/compare/v0.4.1...v1.0.0
[0.4.1]: https://github.com/adericbourg/env-starter/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/adericbourg/env-starter/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/adericbourg/env-starter/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/adericbourg/env-starter/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/adericbourg/env-starter/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/adericbourg/env-starter/releases/tag/v0.1.0
