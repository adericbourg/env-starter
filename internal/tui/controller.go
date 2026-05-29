// Package tui provides a Bubble Tea terminal UI over the orchestration engine.
package tui

import "github.com/adericbourg/env-starter/internal/engine"

// Controller is the subset of the engine API that the UI requires.
// *engine.Engine satisfies this interface.
type Controller interface {
	Environments() []engine.EnvInfo
	WorkflowCommands(env string) []string
	EnvState(env string) engine.EnvState
	CmdState(command string) engine.CmdState
	Logs(command string) []string
	StartEnvironment(env string) error
	StopEnvironment(env string) error
	Events() <-chan engine.Event
}
