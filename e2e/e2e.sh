#!/usr/bin/env bash
# End-to-end test: drives the real, compiled env-starter binary through the
# full config-approval + lifecycle flow, exactly as an operator would type it.
#
# Flow: allow (base config) -> allow (+ overlay) -> run -> verify the daemon
# kept the environment alive across a separate client invocation (the
# substance of "detach" — the literal Ctrl+D keypress is a TUI action and is
# covered by internal/tui/tui_test.go) -> stop -> shutdown. Also checks that
# `run` refuses an unapproved config before any command executes.
#
# Unlike the unit suite (go test ./...), this spawns the real daemon process
# (internal/daemon/spawn.go locates it via os.Executable(), so it can only be
# reached by running the actual binary) and drives it purely through the
# documented CLI, one assertable side effect at a time. See
# docs/configuration.md and CONTRIBUTING.md for how to run this locally.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$REPO_ROOT"

# WORK is rooted directly under /tmp with a short template (rather than a
# bare `mktemp -d`, which on macOS defaults to a long $TMPDIR under
# /var/folders/...) because the daemon's unix socket lives at
# $HOME/Library/Caches/env-starter/daemon.sock on macOS (os.UserCacheDir
# ignores XDG_CACHE_HOME on darwin) — combined with a long WORK prefix that
# overflows sun_path's ~104-byte limit and fails with "bind: invalid argument".
#
# It is also resolved through pwd -P (symlinks followed) so every path we
# bake into the generated configs already matches what the binary's own
# trust store will normalize them to (internal/trust.normalize does the same
# resolution) — otherwise "allow --print" would report a different path than
# the one we grep for on a host where the temp dir is behind a symlink (e.g.
# macOS's /tmp -> /private/tmp).
WORK="$(cd "$(mktemp -d /tmp/e2e.XXXXXX)" && pwd -P)"
BIN="$WORK/env-starter"

cleanup() {
  # Best-effort: kill any daemon this run spawned so nothing leaks past the
  # script, then discard the whole scratch tree.
  "$BIN" shutdown >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_file() {
  [ -f "$1" ] || fail "expected file to exist: $1"
}

assert_no_file() {
  [ -f "$1" ] && fail "expected file to NOT exist: $1"
  return 0
}

assert_contains() {
  local haystack="$1" needle="$2" label="$3"
  case "$haystack" in
  *"$needle"*) ;;
  *) fail "$label: expected output to contain '$needle'"$'\n'"--- output ---"$'\n'"$haystack" ;;
  esac
}

assert_dead() {
  local pidfile="$1"
  local pid
  pid="$(cat "$pidfile")"
  if kill -0 "$pid" 2>/dev/null; then
    fail "expected process $pid (from $pidfile) to be gone, but it is still running"
  fi
}

echo "==> building env-starter"
# Built before isolating HOME below: go build resolves GOPATH/module cache
# from HOME, so building first keeps the host's real module cache (fast,
# already populated) instead of forcing a fresh download into the scratch dir.
go build -o "$BIN" ./cmd/env-starter

# Isolate the trust store, daemon socket/lock/log, and default config lookup
# from the developer's/runner's real ones — same technique as isolateCacheDir
# in cmd/env-starter/main_test.go (os.UserCacheDir() honors HOME/XDG_CACHE_HOME).
export HOME="$WORK/home"
export XDG_CACHE_HOME=""
export XDG_CONFIG_HOME="$WORK/xdg-config"
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

SRC_DIR="$WORK/src"
mkdir -p "$SRC_DIR"

ENV_NAME="e2e-env"
CMD_NAME="app"
BASE_CFG="$WORK/base.yaml"
OVERLAY_CFG="$WORK/overlay.yaml"
BASE_MARKER="$WORK/base-marker"
OVERLAY_MARKER="$WORK/overlay-marker"
PID_FILE="$WORK/app.pid"

# The base command is long-running (stays "running" for the whole script) and
# leaves two artifacts behind: a marker file (proves the command actually ran)
# and its own PID (proves, later, that it was actually killed — not just
# reported as stopped).
cat >"$BASE_CFG" <<EOF
env-starter:
  commands:
    - name: $CMD_NAME
      type: service
      source:
        local: $SRC_DIR
      run: 'touch "$BASE_MARKER"; echo \$\$ > "$PID_FILE"; sleep 300'
  environments:
    - name: $ENV_NAME
      workflow:
        - command: $CMD_NAME
EOF

# The overlay replaces the same command (matched by name — see
# internal/config/merge.go) with one that touches a *different* marker, so we
# can tell whether the overlay actually took effect or was silently ignored.
cat >"$OVERLAY_CFG" <<EOF
env-starter:
  commands:
    - name: $CMD_NAME
      type: service
      source:
        local: $SRC_DIR
      run: 'touch "$OVERLAY_MARKER"; echo \$\$ > "$PID_FILE"; sleep 300'
  environments: []
EOF

echo "==> run refuses an unapproved config"
# Flags must precede the positional <env> argument: Go's flag package stops
# parsing flags at the first non-flag token, so "run <env> --config X" would
# silently leave --config unparsed.
if "$BIN" run --config "$BASE_CFG" "$ENV_NAME" 2>"$WORK/deny.err"; then
  fail "run should have been refused before approval"
fi
assert_contains "$(cat "$WORK/deny.err")" "env-starter allow" "unapproved run error"
assert_no_file "$BASE_MARKER" # the command must never have executed

echo "==> allow (base config)"
"$BIN" allow --yes --config "$BASE_CFG" >/dev/null
out="$("$BIN" allow --print --config "$BASE_CFG")"
assert_contains "$out" "[approved] $BASE_CFG" "allow --print after approving base"

echo "==> allow (base + overlay)"
"$BIN" allow --yes --config "$BASE_CFG" --config-overlay "$OVERLAY_CFG" >/dev/null
out="$("$BIN" allow --print --config "$BASE_CFG" --config-overlay "$OVERLAY_CFG")"
assert_contains "$out" "[approved] $BASE_CFG" "allow --print after approving overlay (base)"
assert_contains "$out" "[approved] $OVERLAY_CFG" "allow --print after approving overlay (overlay)"

echo "==> run (base + overlay)"
"$BIN" run --config "$BASE_CFG" --config-overlay "$OVERLAY_CFG" "$ENV_NAME"
assert_file "$OVERLAY_MARKER" # the overlay's command ran...
assert_no_file "$BASE_MARKER" # ...and replaced the base's, not merged alongside it
assert_file "$PID_FILE"

echo "==> ps sees the environment across a separate client invocation"
# This second, independent process talking to the still-running daemon is the
# substance of "detach": the environment survives the first client exiting.
out="$("$BIN" ps)"
assert_contains "$out" "$ENV_NAME" "ps after run"
assert_contains "$out" "running" "ps after run"

echo "==> stop"
"$BIN" stop "$ENV_NAME"
out="$("$BIN" ps)"
assert_contains "$out" "No environments running." "ps after stop"
assert_dead "$PID_FILE" # the real process was killed, not just marked stopped

echo "==> shutdown"
"$BIN" shutdown
out="$("$BIN" ps)"
assert_contains "$out" "No daemon running." "ps after shutdown"

echo "PASS"
