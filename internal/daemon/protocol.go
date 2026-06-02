// Package daemon defines the newline-delimited JSON protocol used between the
// background daemon and its clients (TUI, CLI subcommands) over a Unix socket.
//
// Wire format:
//   - RPC: client writes one Request line; server writes one Response line.
//   - Event stream: the server writes a continuous stream of WireEvent lines;
//     no client Request is expected after the initial subscribe request.
package daemon

import (
	"encoding/json"

	"github.com/adericbourg/env-starter/internal/engine"
)

// ── Method name constants ─────────────────────────────────────────────────────

const (
	MethodEnvironments     = "environments"
	MethodStartEnvironment = "startEnvironment"
	MethodStopEnvironment  = "stopEnvironment"
	MethodSubscribe        = "subscribe"
	MethodShutdown         = "shutdown"
	MethodSnapshot         = "snapshot"
	MethodWorkflowCommands = "workflowCommands"
	MethodLogs             = "logs"
	MethodLogPath          = "logPath"
	MethodEnvState         = "envState"
	MethodCmdState         = "cmdState"
	MethodCmdRetries       = "cmdRetries"
	MethodConfigChanged    = "configChanged"
	MethodReload           = "reload"
	MethodStoppingCommands = "stoppingCommands"
)

// ── Envelope types ────────────────────────────────────────────────────────────

// Request is the client-to-server RPC envelope. Params is a raw JSON value
// whose shape depends on Method; it may be omitted for zero-argument calls.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the server-to-client RPC envelope. Exactly one of Error or
// Result is populated. Result is a raw JSON value whose shape depends on the
// Method that triggered the request.
type Response struct {
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// ── Per-RPC param/result types ────────────────────────────────────────────────

// EnvParam is a single-environment-name argument used by workflowCommands,
// envState, startEnvironment, and stopEnvironment.
type EnvParam struct {
	Env string `json:"env"`
}

// CmdParam is a single-command-name argument used by cmdState, cmdRetries,
// and logPath.
type CmdParam struct {
	Command string `json:"command"`
}

// LogsParams carries the command name for the logs RPC.
type LogsParams struct {
	Command string `json:"command"`
}

// LogsResult carries the log lines for a command.
type LogsResult struct {
	Lines []string `json:"lines"`
}

// CmdRetriesResult carries the retry counters for a command.
type CmdRetriesResult struct {
	Attempts int `json:"attempts"`
	Max      int `json:"max"`
}

// ConfigChangedResult carries the result of a configChanged RPC.
type ConfigChangedResult struct {
	Changed bool   `json:"changed"`
	Err     string `json:"err,omitempty"`
}

// WireStoppingCommand is the JSON-serializable form of engine.StoppingCommand.
// Elapsed and Grace are encoded as int64 nanoseconds for deterministic round-
// trips (time.Duration is an int64 alias, but encoding/json renders it as a
// plain number).
type WireStoppingCommand struct {
	Command string `json:"command"`
	Elapsed int64  `json:"elapsed"` // nanoseconds
	Grace   int64  `json:"grace"`   // nanoseconds
}

// StoppingCommandsResult carries all currently-stopping commands.
type StoppingCommandsResult struct {
	Commands []WireStoppingCommand `json:"commands"`
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

// Snapshot is a point-in-time view of all engine state, sent in response to
// the snapshot RPC and optionally piggy-backed onto the initial subscribe
// response so the client can render immediately without waiting for events.
type Snapshot struct {
	// EnvStates maps each environment name to its current lifecycle state.
	EnvStates map[string]engine.EnvState `json:"envStates"`
	// CmdStates maps each command name to its current lifecycle state.
	CmdStates map[string]engine.CmdState `json:"cmdStates"`
	// CmdRetries maps each command name to [attempts, max].
	CmdRetries map[string][2]int `json:"cmdRetries"`
	// Environments is the ordered list of environments known to the engine.
	// It lets a connecting client seed its local mirror without a separate
	// environments RPC.
	Environments []engine.EnvInfo `json:"environments"`
	// WorkflowCmds maps each environment name to its ordered command names.
	// It lets a connecting client seed its local mirror without separate
	// workflowCommands RPCs.
	WorkflowCmds map[string][]string `json:"workflowCmds"`
	// LogPaths maps each command name to its on-disk log file path.
	// It lets a connecting client seed its local mirror without separate
	// logPath RPCs.
	LogPaths map[string]string `json:"logPaths"`
}

// ── WireEvent ─────────────────────────────────────────────────────────────────

// WireEvent is the JSON-serializable form of engine.Event augmented with
// retry information. It is streamed line-by-line on the event-stream
// connection.
//
// Err holds the string representation of engine.Event.Err (the error
// interface cannot be marshalled to JSON directly).
type WireEvent struct {
	Kind          string          `json:"kind"`
	Environment   string          `json:"environment,omitempty"`
	Command       string          `json:"command,omitempty"`
	EnvState      engine.EnvState `json:"envState,omitempty"`
	CmdState      engine.CmdState `json:"cmdState,omitempty"`
	Err           string          `json:"err,omitempty"`
	RetryAttempts int             `json:"retryAttempts,omitempty"`
	RetryMax      int             `json:"retryMax,omitempty"`
}

// EventToWire converts an engine.Event plus retry counters to a WireEvent
// ready for JSON encoding. If ev.Err is nil the Err field is left empty.
func EventToWire(ev engine.Event, attempts, max int) WireEvent {
	w := WireEvent{
		Kind:          ev.Kind,
		Environment:   ev.Environment,
		Command:       ev.Command,
		EnvState:      ev.EnvState,
		CmdState:      ev.CmdState,
		RetryAttempts: attempts,
		RetryMax:      max,
	}
	if ev.Err != nil {
		w.Err = ev.Err.Error()
	}
	return w
}

// WireToEvent converts a WireEvent back to an engine.Event. The Err field is
// reconstructed as a simple string-wrapped error; the original error type is
// not preserved across the wire.
func WireToEvent(w WireEvent) engine.Event {
	ev := engine.Event{
		Kind:        w.Kind,
		Environment: w.Environment,
		Command:     w.Command,
		EnvState:    w.EnvState,
		CmdState:    w.CmdState,
	}
	if w.Err != "" {
		ev.Err = wireError(w.Err)
	}
	return ev
}

// wireError is a minimal error implementation used when reconstructing an
// engine.Event from its wire representation.
type wireError string

func (e wireError) Error() string { return string(e) }
