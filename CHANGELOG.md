# Changelog

All notable changes to env-starter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Releases follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security
- Config files (base and overlay) now require explicit approval before
  env-starter will load them: each file's sha256 is checked against a trust
  store, and approval is invalidated the instant the file's content changes.
  Defends against a config that was tampered with or slipped in after the
  fact — `run`/`setup`/`teardown`/`readiness.shell` are still executed
  verbatim as shell scripts by design. Review and approve with the new
  `env-starter allow` subcommand (`--print` to preview only, `--yes` to skip
  the prompt); the preview also shows each command's `source` as a browsable
  URL or path, so you can go inspect the code behind a command before
  approving it. See SECURITY.md ("Config trust").
- Self-update now **fails closed** when `cosignPublicKeyPEM` is not configured:
  `Apply` returns an error instead of warning and proceeding with TLS-only
  integrity. Self-update is disabled until the maintainer embeds the cosign
  public key and cuts a signed release.

### Changed
- CI test step runs with `-race` to catch data races in the concurrent
  supervisor and daemon hub.

### Added
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
- golangci-lint (`staticcheck`, `errcheck`, `gosec`, `ineffassign`) and
  govulncheck steps added to CI.
- Releases now include an SPDX SBOM per archive and a Sigstore build-provenance
  attestation, verifiable with `gh attestation verify`.

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

[Unreleased]: https://github.com/adericbourg/env-starter/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/adericbourg/env-starter/compare/v0.4.1...v1.0.0
[0.4.1]: https://github.com/adericbourg/env-starter/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/adericbourg/env-starter/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/adericbourg/env-starter/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/adericbourg/env-starter/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/adericbourg/env-starter/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/adericbourg/env-starter/releases/tag/v0.1.0
