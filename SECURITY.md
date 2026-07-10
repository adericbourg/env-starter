# Security Policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for a security vulnerability.

Instead, report it privately via
[GitHub Security Advisories](https://github.com/adericbourg/env-starter/security/advisories/new).
Provide as much detail as you can: affected versions, reproduction steps, and
potential impact. You will receive a response within a few business days.

## Self-update trust model

When `env-starter update` (or the startup update prompt) applies a new release,
it goes through a two-step verification chain:

1. **TLS transport integrity** — the release assets are fetched from
   `https://github.com` only; non-HTTPS redirects are rejected.
2. **Cosign signature** — `checksums.txt` is verified against a detached
   `checksums.txt.sig` using ECDSA-P256 over SHA-256. The project's public
   key is embedded in the binary at build time (`internal/update/verify.go`).
   A missing or invalid signature is a **hard failure**: the update is aborted
   and the binary is not replaced.
3. **Archive integrity** — the sha256 digest of every downloaded archive is
   verified against the entry in `checksums.txt` before extraction.
4. **Extraction safety** — zip-slip is prevented (only `filepath.Base` of the
   archive entry name is used, never the full path); the extracted binary is
   capped at 512 MiB to guard against decompression bombs.

The binary is replaced atomically (rename) so a partial update cannot leave
the tool in a broken state.

## Local trust model

env-starter is a strictly per-user tool. Everything it persists lives under
`os.UserCacheDir()/env-starter/` (`~/.cache/env-starter` on Linux), which is
enforced **owner-only** (`0700`): the directory is created private, a
pre-existing directory with looser permissions is tightened, and a directory
owned by another user is rejected outright.

Inside that boundary:

- **Daemon socket** — the background daemon listens on a unix socket
  (`daemon.sock`, `0600`) inside the owner-only directory. The socket is
  created with a restrictive umask so it is never connectable by other users,
  even briefly. As defence in depth the daemon also verifies each connecting
  peer's uid against its own (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on
  macOS) and rejects mismatches. This matters because a connected peer can
  start configured environments — i.e. run the owner's commands — and shut
  the daemon down.
- **Source cache** — downloaded url sources and GitHub clones live in
  predictable subdirectories and are executed as code, so pre-existing cache
  content is only reused after the ownership check above passes.
- **Log files** — per-command logs are `0600` in an owner-only directory and
  are never written to a shared location: if the per-user cache directory
  cannot be resolved, env-starter fails instead of falling back to a
  world-readable temp dir.

There is no setuid, no TCP listener, and no cross-user IPC of any kind.

## URL source integrity

`url` sources are downloaded over `https` only (redirects must stay on
https) and are then **executed as code**. Declare a `checksum` so a tampered
or swapped artifact at the origin is detected; without one the download is
trusted on TLS alone and env-starter prints a startup warning. Set
`require-checksums: true` at the top level of the config to make a missing
checksum a hard validation error (recommended for shared configs; an overlay
can never relax it).

## Config overlay trust

`--config-overlay` files are trusted at the same level as the base config
file. A command's `run`, `setup`, and `teardown` fields are passed directly
to `sh -c`, so **a malicious overlay file is equivalent to code execution**.
Only use overlay files from sources you control; never share or commit an
overlay that contains untrusted data.

## Command log confidentiality

Command stdout/stderr is stored in log files under
`os.UserCacheDir()/env-starter/`. These files are created with `0600`
permissions (owner read/write only). **Logs are not redacted**: if a
subprocess echoes secrets (tokens, passwords), those appear in the log file.
Protect the cache directory accordingly.
