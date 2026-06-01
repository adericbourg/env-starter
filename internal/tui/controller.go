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
	Logs(command string) []string
	LogPath(command string) string
	StartEnvironment(env string) error
	StopEnvironment(env string) error
	Events() <-chan engine.Event
	// StoppingCommands lists the commands currently being torn down, with elapsed
	// and grace durations, for the shutdown screen.
	StoppingCommands() []engine.StoppingCommand
	// Shutdown gracefully stops every running command, respecting ctx as the
	// overall deadline. It is invoked from the TUI so the "shutting down" screen
	// stays visible while teardown runs.
	Shutdown(ctx context.Context)
	// ConfigChanged reports whether the on-disk config file(s) differ semantically
	// from the config the running engine was built from. Once true it latches until
	// a successful Reload.
	ConfigChanged() bool
	// Reload tears down the running engine, re-loads config from disk, and builds a
	// fresh engine. Returns an error without teardown when loading or building the
	// new engine fails, leaving the current engine running.
	Reload(ctx context.Context) error
}
