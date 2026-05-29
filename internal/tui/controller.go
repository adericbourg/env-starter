// Package tui provides a Bubble Tea terminal UI over the orchestration engine.
package tui

import (
	"context"

	"github.com/adericbourg/env-starter/internal/engine"
)

// Controller is the subset of the engine API that the UI requires.
// *engine.Engine satisfies this interface.
type Controller interface {
	Environments() []engine.EnvInfo
	WorkflowCommands(env string) []string
	EnvState(env string) engine.EnvState
	CmdState(command string) engine.CmdState
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
}
