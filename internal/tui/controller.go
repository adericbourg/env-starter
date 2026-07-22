// Package tui provides a Bubble Tea terminal UI over the orchestration engine.
package tui

import (
	"context"

	"github.com/adericbourg/env-starter/internal/engine"
)

// Controller is the subset of the engine API that the UI requires.
type Controller interface {
	Environments() []engine.EnvInfo
	WorkflowCommands(env string) []string
	EnvState(env string) engine.EnvState
	CmdState(command string) engine.CmdState
	// CmdRetries returns the number of restart attempts consumed in the current
	// cycle and the configured maximum. Both are 0 for commands that are not
	// (or cannot be) auto-restarted.
	CmdRetries(command string) (attempts, max int)
	// IsUnmanaged reports whether command is currently healthy only because an
	// external process already satisfied its readiness probe before
	// env-starter spawned anything.
	IsUnmanaged(command string) bool
	Logs(command string) []string
	LogPath(command string) string
	// ResolveEnv resolves the env variables visible to the given environment or
	// command (exactly one should be non-empty), with full layer provenance.
	// Returns nil for an unknown name.
	ResolveEnv(envName, command string) []engine.ResolvedEnvVar
	StartEnvironment(env string) error
	StopEnvironment(env string) error
	// RestartCommand restarts a single running command in place, preserving its
	// environment holders. Returns an error if the command is not running.
	RestartCommand(command string) error
	Events() <-chan engine.Event
	// StoppingCommands lists the commands currently being torn down, with elapsed
	// and grace durations, for the shutdown screen.
	StoppingCommands() []engine.StoppingCommand
	// Shutdown gracefully stops every running command, respecting ctx as the
	// overall deadline. It is invoked from the TUI so the "shutting down" screen
	// stays visible while teardown runs.
	Shutdown(ctx context.Context)
	// ConfigChanged reports the current state of the on-disk config relative to the
	// running engine. It returns:
	//   (true,  nil) — file changed, valid, and semantically different: reload available.
	//   (false, err) — file changed but cannot be parsed: show error, reload blocked.
	//   (false, nil) — no actionable change (file unchanged, or valid but equal to running).
	// The error is preserved across scans while the file remains unmodified, so the
	// caller does not need to cache it separately.
	ConfigChanged() (bool, error)
	// Reload tears down the running engine, re-loads config from disk, and builds a
	// fresh engine. Returns an error without teardown when loading or building the
	// new engine fails, leaving the current engine running.
	Reload(ctx context.Context) error
	// Detach signals the controller to release any client-side resources (e.g.
	// close the socket connection to the daemon). It is a no-op for controllers
	// that own the engine directly.
	Detach()
}
