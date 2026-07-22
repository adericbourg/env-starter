package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/adericbourg/env-starter/internal/engine"
)

// ── WireEvent round-trip ──────────────────────────────────────────────────────

func TestEventToWire_ofCommandEventWithError_setsAllFields(t *testing.T) {
	// Given
	ev := engine.Event{
		Kind:     "command",
		Command:  "api",
		CmdState: engine.CmdError,
		Err:      errors.New("exit status 1"),
	}

	// When
	w := EventToWire(ev, 2, 3, false)

	// Then
	if w.Kind != "command" {
		t.Errorf("Kind: want %q, got %q", "command", w.Kind)
	}
	if w.Command != "api" {
		t.Errorf("Command: want %q, got %q", "api", w.Command)
	}
	if w.CmdState != engine.CmdError {
		t.Errorf("CmdState: want %q, got %q", engine.CmdError, w.CmdState)
	}
	if w.Err != "exit status 1" {
		t.Errorf("Err: want %q, got %q", "exit status 1", w.Err)
	}
	if w.RetryAttempts != 2 {
		t.Errorf("RetryAttempts: want 2, got %d", w.RetryAttempts)
	}
	if w.RetryMax != 3 {
		t.Errorf("RetryMax: want 3, got %d", w.RetryMax)
	}
	if w.Unmanaged {
		t.Error("Unmanaged: want false, got true")
	}
}

func TestEventToWire_ofUnmanagedCommandEvent_setsUnmanaged(t *testing.T) {
	// Given
	ev := engine.Event{
		Kind:     "command",
		Command:  "api",
		CmdState: engine.CmdHealthy,
	}

	// When
	w := EventToWire(ev, 0, 0, true)

	// Then
	if !w.Unmanaged {
		t.Error("Unmanaged: want true, got false")
	}
}

func TestEventToWire_whenNoError_errFieldEmpty(t *testing.T) {
	// Given
	ev := engine.Event{
		Kind:     "command",
		Command:  "db",
		CmdState: engine.CmdHealthy,
	}

	// When
	w := EventToWire(ev, 0, 3, false)

	// Then
	if w.Err != "" {
		t.Errorf("expected empty Err for nil error, got %q", w.Err)
	}
}

func TestWireToEvent_ofCommandEventWithError_roundTrip(t *testing.T) {
	// Given — a command event with an error, converted to wire and back.
	original := engine.Event{
		Kind:     "command",
		Command:  "api",
		CmdState: engine.CmdError,
		Err:      errors.New("exit status 1"),
	}
	wire := EventToWire(original, 2, 3, false)

	// When
	recovered := WireToEvent(wire)

	// Then
	if recovered.Kind != original.Kind {
		t.Errorf("Kind: want %q, got %q", original.Kind, recovered.Kind)
	}
	if recovered.Command != original.Command {
		t.Errorf("Command: want %q, got %q", original.Command, recovered.Command)
	}
	if recovered.CmdState != original.CmdState {
		t.Errorf("CmdState: want %q, got %q", original.CmdState, recovered.CmdState)
	}
	if recovered.Err == nil {
		t.Fatal("expected a non-nil error after round-trip")
	}
	if recovered.Err.Error() != original.Err.Error() {
		t.Errorf("Err.Error(): want %q, got %q", original.Err.Error(), recovered.Err.Error())
	}
}

func TestWireToEvent_whenErrEmpty_nilError(t *testing.T) {
	// Given
	w := WireEvent{
		Kind:     "environment",
		EnvState: engine.EnvRunning,
	}

	// When
	ev := WireToEvent(w)

	// Then
	if ev.Err != nil {
		t.Errorf("expected nil Err for empty wire Err, got %v", ev.Err)
	}
}

func TestWireEvent_jsonRoundTrip(t *testing.T) {
	// Given
	ev := engine.Event{
		Kind:     "command",
		Command:  "worker",
		CmdState: engine.CmdRestarting,
		Err:      errors.New("signal: killed"),
	}
	w := EventToWire(ev, 1, 3, true)

	// When — marshal then unmarshal.
	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded WireEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Then
	if decoded.Kind != w.Kind {
		t.Errorf("Kind: want %q, got %q", w.Kind, decoded.Kind)
	}
	if decoded.Command != w.Command {
		t.Errorf("Command: want %q, got %q", w.Command, decoded.Command)
	}
	if decoded.CmdState != w.CmdState {
		t.Errorf("CmdState: want %q, got %q", w.CmdState, decoded.CmdState)
	}
	if decoded.Err != w.Err {
		t.Errorf("Err: want %q, got %q", w.Err, decoded.Err)
	}
	if decoded.RetryAttempts != w.RetryAttempts {
		t.Errorf("RetryAttempts: want %d, got %d", w.RetryAttempts, decoded.RetryAttempts)
	}
	if decoded.RetryMax != w.RetryMax {
		t.Errorf("RetryMax: want %d, got %d", w.RetryMax, decoded.RetryMax)
	}
	if decoded.Unmanaged != w.Unmanaged {
		t.Errorf("Unmanaged: want %v, got %v", w.Unmanaged, decoded.Unmanaged)
	}
}

// ── Snapshot construction and JSON round-trip ─────────────────────────────────

func TestSnapshot_jsonRoundTrip(t *testing.T) {
	// Given
	snap := Snapshot{
		EnvStates: map[string]engine.EnvState{
			"dev":  engine.EnvRunning,
			"test": engine.EnvStopped,
		},
		CmdStates: map[string]engine.CmdState{
			"api": engine.CmdHealthy,
			"db":  engine.CmdStarting,
		},
		CmdRetries: map[string][2]int{
			"api": {1, 3},
			"db":  {0, 3},
		},
		CmdUnmanaged: map[string]bool{
			"api": true,
			"db":  false,
		},
	}

	// When
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal Snapshot: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal Snapshot: %v", err)
	}

	// Then — environment states are preserved.
	if decoded.EnvStates["dev"] != engine.EnvRunning {
		t.Errorf("EnvStates[dev]: want %q, got %q", engine.EnvRunning, decoded.EnvStates["dev"])
	}
	if decoded.EnvStates["test"] != engine.EnvStopped {
		t.Errorf("EnvStates[test]: want %q, got %q", engine.EnvStopped, decoded.EnvStates["test"])
	}

	// Then — command states are preserved.
	if decoded.CmdStates["api"] != engine.CmdHealthy {
		t.Errorf("CmdStates[api]: want %q, got %q", engine.CmdHealthy, decoded.CmdStates["api"])
	}
	if decoded.CmdStates["db"] != engine.CmdStarting {
		t.Errorf("CmdStates[db]: want %q, got %q", engine.CmdStarting, decoded.CmdStates["db"])
	}

	// Then — retry counters are preserved.
	if decoded.CmdRetries["api"] != [2]int{1, 3} {
		t.Errorf("CmdRetries[api]: want [1 3], got %v", decoded.CmdRetries["api"])
	}
	if decoded.CmdRetries["db"] != [2]int{0, 3} {
		t.Errorf("CmdRetries[db]: want [0 3], got %v", decoded.CmdRetries["db"])
	}

	// Then — unmanaged flags are preserved.
	if decoded.CmdUnmanaged["api"] != true {
		t.Errorf("CmdUnmanaged[api]: want true, got %v", decoded.CmdUnmanaged["api"])
	}
	if decoded.CmdUnmanaged["db"] != false {
		t.Errorf("CmdUnmanaged[db]: want false, got %v", decoded.CmdUnmanaged["db"])
	}
}

func TestSnapshot_emptyMaps_marshalWithoutError(t *testing.T) {
	// Given
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		CmdUnmanaged: map[string]bool{},
		Environments: []engine.EnvInfo{},
		WorkflowCmds: map[string][]string{},
		LogPaths:     map[string]string{},
	}

	// When
	data, err := json.Marshal(snap)

	// Then
	if err != nil {
		t.Fatalf("json.Marshal empty Snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON output")
	}
	if !strings.Contains(string(data), `"envStates":{}`) {
		t.Errorf("expected JSON to contain %q, got: %s", `"envStates":{}`, data)
	}
}

// ── Environment event round-trip ──────────────────────────────────────────────

func TestEventToWire_ofEnvironmentEvent_setsAllFields(t *testing.T) {
	// Given — an environment event with no Command and a non-zero EnvState.
	ev := engine.Event{
		Kind:        "environment",
		Environment: "dev",
		EnvState:    engine.EnvRunning,
	}

	// When
	w := EventToWire(ev, 0, 0, false)

	// Then — environment fields are propagated.
	if w.Kind != "environment" {
		t.Errorf("Kind: want %q, got %q", "environment", w.Kind)
	}
	if w.Environment != "dev" {
		t.Errorf("Environment: want %q, got %q", "dev", w.Environment)
	}
	if w.EnvState != engine.EnvRunning {
		t.Errorf("EnvState: want %q, got %q", engine.EnvRunning, w.EnvState)
	}

	// Then — command fields are absent for an environment event.
	if w.Command != "" {
		t.Errorf("Command: want empty, got %q", w.Command)
	}
	if w.CmdState != "" {
		t.Errorf("CmdState: want empty, got %q", w.CmdState)
	}

	// When — convert back to an engine.Event.
	recovered := WireToEvent(w)

	// Then — round-trip is lossless.
	if recovered.Kind != ev.Kind {
		t.Errorf("round-trip Kind: want %q, got %q", ev.Kind, recovered.Kind)
	}
	if recovered.Environment != ev.Environment {
		t.Errorf("round-trip Environment: want %q, got %q", ev.Environment, recovered.Environment)
	}
	if recovered.EnvState != ev.EnvState {
		t.Errorf("round-trip EnvState: want %q, got %q", ev.EnvState, recovered.EnvState)
	}
	if recovered.Err != nil {
		t.Errorf("round-trip Err: want nil, got %v", recovered.Err)
	}
}

// ── WireStoppingCommand JSON round-trip ───────────────────────────────────────

func TestWireStoppingCommand_jsonRoundTrip_preservesNanoseconds(t *testing.T) {
	// Given — a WireStoppingCommand with non-trivial nanosecond durations.
	const elapsedNs int64 = 5_123_456_789 // ~5.12 seconds
	const graceNs int64 = 30_000_000_000  // 30 seconds exactly
	orig := WireStoppingCommand{
		Command: "worker",
		Elapsed: elapsedNs,
		Grace:   graceNs,
	}

	// When — marshal then unmarshal.
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded WireStoppingCommand
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Then — all fields including nanosecond precision are preserved.
	if decoded.Command != orig.Command {
		t.Errorf("Command: want %q, got %q", orig.Command, decoded.Command)
	}
	if decoded.Elapsed != orig.Elapsed {
		t.Errorf("Elapsed: want %d ns, got %d ns", orig.Elapsed, decoded.Elapsed)
	}
	if decoded.Grace != orig.Grace {
		t.Errorf("Grace: want %d ns, got %d ns", orig.Grace, decoded.Grace)
	}
}
