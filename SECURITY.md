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
