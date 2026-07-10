# Installation & updating

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

The downloaded archive is sha256-verified against `checksums.txt`, which is
itself cosign-signed and verified against the public key embedded in the binary
before any digest is trusted — so an update cannot be installed from a tampered
release even if the connection to GitHub is compromised. See
[releasing.md](releasing.md) for the signing setup.

---

## Next steps

- [Usage](usage.md) — running env-starter, the daemon, CLI commands, and TUI keys.
- [Configuration reference](configuration.md) — every YAML field.
- [Shell completion](completion.md) — tab completion for bash/zsh/fish.
