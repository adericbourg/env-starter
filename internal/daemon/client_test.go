package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/engine"
)

// ── Fake server helpers ───────────────────────────────────────────────────────

// fakeServer simulates the daemon over a net.Pipe pair. It handles two
// concurrent connections: the first is treated as the RPC connection, the
// second as the event-stream connection.
type fakeServer struct {
	rpcConn    net.Conn
	streamConn net.Conn
}

// newFakeServerPair creates two net.Pipe() pairs and returns (client-side
// addresses, fakeServer holding server-side ends). The caller must call
// acceptConnections before Connect.
//
// Because net.Pipe is synchronous (no listener), we instead spin up a real
// unix socket listener backed by t.TempDir so that Connect can Dial normally.
func startFakeServer(t *testing.T, snap Snapshot, extraEvents []WireEvent) string {
	t.Helper()
	dir := socketTempDir(t)
	socketPath := dir + "/fake.sock"

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("fake server listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// Accept two connections asynchronously.
	go func() {
		// First connection: RPC. Keep it alive (accept requests), send empty responses.
		rpcConn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveRPC(rpcConn)

		// Second connection: event-stream. Send the snapshot response then stream events.
		streamConn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveStream(t, streamConn, snap, extraEvents)
	}()

	return socketPath
}

// serveRPC handles the RPC connection: reads requests and writes empty
// successful responses so that RPCs from the client don't block.
func serveRPC(conn net.Conn) {
	defer conn.Close()
	scan := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for scan.Scan() {
		var req Request
		if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
			continue
		}
		// For shutdown: just respond and close.
		if req.Method == MethodShutdown {
			_ = enc.Encode(Response{})
			return
		}
		_ = enc.Encode(Response{})
	}
}

// serveStream handles the event-stream connection: reads the subscribe
// request, sends the snapshot response, then streams extra events.
func serveStream(t *testing.T, conn net.Conn, snap Snapshot, events []WireEvent) {
	t.Helper()
	defer conn.Close()

	scan := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)

	// Consume the subscribe request.
	if !scan.Scan() {
		return
	}
	var req Request
	if err := json.Unmarshal(scan.Bytes(), &req); err != nil || req.Method != MethodSubscribe {
		t.Errorf("fake server: expected subscribe, got %q", string(scan.Bytes()))
		return
	}

	// Send snapshot.
	snapBytes, _ := json.Marshal(snap)
	_ = enc.Encode(Response{Result: snapBytes})

	// Stream extra events.
	for _, ev := range events {
		_ = enc.Encode(ev)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestConnect_seedsMirrorFromSnapshot(t *testing.T) {
	// Given
	snap := Snapshot{
		EnvStates: map[string]engine.EnvState{
			"dev": engine.EnvRunning,
		},
		CmdStates: map[string]engine.CmdState{
			"api": engine.CmdHealthy,
		},
		CmdRetries: map[string][2]int{
			"api": {1, 3},
		},
		Environments: []engine.EnvInfo{
			{Name: "dev", Description: "development"},
		},
		WorkflowCmds: map[string][]string{
			"dev": {"api"},
		},
		LogPaths: map[string]string{
			"api": "/logs/api.log",
		},
	}
	socketPath := startFakeServer(t, snap, nil)

	// When
	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { ctrl.Detach() })

	// Then — mirror is seeded from the snapshot.
	if got := ctrl.EnvState("dev"); got != engine.EnvRunning {
		t.Errorf("EnvState(dev): want %q, got %q", engine.EnvRunning, got)
	}
	if got := ctrl.CmdState("api"); got != engine.CmdHealthy {
		t.Errorf("CmdState(api): want %q, got %q", engine.CmdHealthy, got)
	}
	attempts, max := ctrl.CmdRetries("api")
	if attempts != 1 || max != 3 {
		t.Errorf("CmdRetries(api): want (1,3), got (%d,%d)", attempts, max)
	}
	envs := ctrl.Environments()
	if len(envs) != 1 || envs[0].Name != "dev" {
		t.Errorf("Environments: want [{dev development}], got %v", envs)
	}
	if got := ctrl.WorkflowCommands("dev"); len(got) != 1 || got[0] != "api" {
		t.Errorf("WorkflowCommands(dev): want [api], got %v", got)
	}
	if got := ctrl.LogPath("api"); got != "/logs/api.log" {
		t.Errorf("LogPath(api): want %q, got %q", "/logs/api.log", got)
	}
}

func TestClientController_envState_returnsMirrorValue(t *testing.T) {
	// Given
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"prod": engine.EnvStopped},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "prod"}},
		WorkflowCmds: map[string][]string{},
		LogPaths:     map[string]string{},
	}
	socketPath := startFakeServer(t, snap, nil)

	// When
	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { ctrl.Detach() })

	// Then
	if got := ctrl.EnvState("prod"); got != engine.EnvStopped {
		t.Errorf("EnvState(prod): want %q, got %q", engine.EnvStopped, got)
	}
	// Unknown env returns zero value ("").
	if got := ctrl.EnvState("unknown"); got != "" {
		t.Errorf("EnvState(unknown): want empty string, got %q", got)
	}
}

func TestClientController_eventsFromStream_updateMirror(t *testing.T) {
	// Given — stream sends an env event followed by a command event.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvStopped},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdPending},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	extraEvents := []WireEvent{
		{Kind: "environment", Environment: "dev", EnvState: engine.EnvRunning},
		{Kind: "command", Command: "api", CmdState: engine.CmdHealthy, RetryAttempts: 2, RetryMax: 5},
	}
	socketPath := startFakeServer(t, snap, extraEvents)

	// When
	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { ctrl.Detach() })

	// Collect events until we see both expected events or timeout.
	var received []engine.Event
	timeout := time.After(2 * time.Second)
	for len(received) < 2 {
		select {
		case ev, ok := <-ctrl.Events():
			if !ok {
				goto done
			}
			received = append(received, ev)
		case <-timeout:
			t.Fatal("timed out waiting for events")
		}
	}
done:

	// Then — mirror reflects the streamed events.
	if got := ctrl.EnvState("dev"); got != engine.EnvRunning {
		t.Errorf("EnvState(dev) after event: want %q, got %q", engine.EnvRunning, got)
	}
	if got := ctrl.CmdState("api"); got != engine.CmdHealthy {
		t.Errorf("CmdState(api) after event: want %q, got %q", engine.CmdHealthy, got)
	}
	attempts, max := ctrl.CmdRetries("api")
	if attempts != 2 || max != 5 {
		t.Errorf("CmdRetries(api) after event: want (2,5), got (%d,%d)", attempts, max)
	}

	// Then — events were forwarded to the channel.
	if len(received) < 2 {
		t.Fatalf("want at least 2 events, got %d", len(received))
	}
	if received[0].Kind != "environment" || received[0].EnvState != engine.EnvRunning {
		t.Errorf("received[0]: want environment/running, got %+v", received[0])
	}
	if received[1].Kind != "command" || received[1].CmdState != engine.CmdHealthy {
		t.Errorf("received[1]: want command/healthy, got %+v", received[1])
	}
}

func TestClientController_shutdown_blocksUntilStreamCloses(t *testing.T) {
	// Given — a fake server that closes the stream connection after sending the snapshot.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{},
		WorkflowCmds: map[string][]string{},
		LogPaths:     map[string]string{},
	}
	// No extra events: the stream will close immediately after the snapshot.
	socketPath := startFakeServer(t, snap, nil)

	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Wait for the event stream goroutine to close the channel (stream EOF).
	// The fake server closes the connection after sending all events (none here).
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ctrl.Shutdown(ctx)
	}()

	// When / Then — Shutdown must return within a reasonable timeout.
	select {
	case <-doneCh:
		// OK — Shutdown returned.
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after stream closed")
	}
}

func TestClientController_detach_closesConnectionsWithoutShutdown(t *testing.T) {
	// Given
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{},
		WorkflowCmds: map[string][]string{},
		LogPaths:     map[string]string{},
	}
	socketPath := startFakeServer(t, snap, nil)

	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// When — Detach should return immediately without blocking.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ctrl.Detach()
	}()

	// Then — Detach returns quickly (no shutdown RPC or wait for stream).
	select {
	case <-doneCh:
		// OK
	case <-time.After(time.Second):
		t.Fatal("Detach did not return promptly")
	}

	// Then — after Detach, further RPC attempts fail gracefully (connections closed).
	_, err = ctrl.rpc(Request{Method: MethodEnvironments})
	if err == nil {
		t.Error("expected rpc error after Detach, got nil")
	}
}

// ── WaitForEnvSettled tests ───────────────────────────────────────────────────

// buildSettledCtrl constructs a ClientController with a pre-seeded mirror for
// WaitForEnvSettled tests. It uses a net.Pipe to avoid spinning up a full
// server; the stream is closed immediately (no events) and the RPC side is
// never used.
func buildSettledCtrl(t *testing.T, snap Snapshot, streamEvents []WireEvent) *ClientController {
	t.Helper()

	// Use startFakeServer to get a real connected client.
	socketPath := startFakeServer(t, snap, streamEvents)
	ctrl, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { ctrl.Detach() })
	return ctrl
}

func TestWaitForEnvSettled_whenAlreadyRunning_returnsTrue(t *testing.T) {
	// Given — env is already running in the mirror.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvRunning},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdHealthy},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	ctrl := buildSettledCtrl(t, snap, nil)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	running, err := WaitForEnvSettled(ctx, ctrl, "dev")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !running {
		t.Error("want running=true, got false")
	}
}

func TestWaitForEnvSettled_whenStartingThenRunning_returnsTrue(t *testing.T) {
	// Given — env starts in "starting" state, then transitions to "running".
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvStarting},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdStarting},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	// Stream sends command healthy then env running.
	extraEvents := []WireEvent{
		{Kind: "command", Command: "api", CmdState: engine.CmdHealthy},
		{Kind: "environment", Environment: "dev", EnvState: engine.EnvRunning},
	}
	ctrl := buildSettledCtrl(t, snap, extraEvents)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	running, err := WaitForEnvSettled(ctx, ctrl, "dev")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !running {
		t.Error("want running=true, got false")
	}
}

func TestWaitForEnvSettled_whenStoppedThenStarted_waitsForEvents(t *testing.T) {
	// Given — daemon just started: env=Stopped, commands not yet in the mirror.
	// This simulates the race where StartEnvironment is called but the EnvStarting
	// event has not yet arrived at the client when WaitForEnvSettled runs.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvStopped},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	extraEvents := []WireEvent{
		{Kind: "environment", Environment: "dev", EnvState: engine.EnvStarting},
		{Kind: "command", Command: "api", CmdState: engine.CmdStarting},
		{Kind: "command", Command: "api", CmdState: engine.CmdHealthy},
		{Kind: "environment", Environment: "dev", EnvState: engine.EnvRunning},
	}
	ctrl := buildSettledCtrl(t, snap, extraEvents)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	running, err := WaitForEnvSettled(ctx, ctrl, "dev")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !running {
		t.Error("want running=true, got false — initial check must not short-circuit on EnvStopped")
	}
}

func TestWaitForEnvSettled_whenFailsAfterRetries_returnsFalse(t *testing.T) {
	// Given — env starts, command cycles through restarting then error.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvStarting},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdStarting},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	// Simulate retries exhausted.
	extraEvents := []WireEvent{
		{Kind: "command", Command: "api", CmdState: engine.CmdRestarting, RetryAttempts: 3, RetryMax: 3},
		{Kind: "command", Command: "api", CmdState: engine.CmdError},
		{Kind: "environment", Environment: "dev", EnvState: engine.EnvError},
	}
	ctrl := buildSettledCtrl(t, snap, extraEvents)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	running, err := WaitForEnvSettled(ctx, ctrl, "dev")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if running {
		t.Error("want running=false (env errored), got true")
	}
}

func TestWaitForCmdSettled_whenTransitionsThroughRestarting_returnsHealthyTrue(t *testing.T) {
	// Given — the mirror starts out "healthy" (the pre-restart residue), then
	// genuinely transitions through restarting before becoming healthy again.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvRunning},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdHealthy},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	extraEvents := []WireEvent{
		{Kind: "command", Command: "api", CmdState: engine.CmdRestarting},
		{Kind: "command", Command: "api", CmdState: engine.CmdHealthy},
	}
	ctrl := buildSettledCtrl(t, snap, extraEvents)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	healthy, err := WaitForCmdSettled(ctx, ctrl, "api")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !healthy {
		t.Error("want healthy=true, got false")
	}
}

func TestWaitForCmdSettled_whenRestartingThenError_returnsHealthyFalse(t *testing.T) {
	// Given — the command genuinely restarts but fails to come back healthy.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvRunning},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdHealthy},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	extraEvents := []WireEvent{
		{Kind: "command", Command: "api", CmdState: engine.CmdRestarting},
		{Kind: "command", Command: "api", CmdState: engine.CmdError},
	}
	ctrl := buildSettledCtrl(t, snap, extraEvents)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	healthy, err := WaitForCmdSettled(ctx, ctrl, "api")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if healthy {
		t.Error("want healthy=false (command errored), got true")
	}
}

func TestWaitForCmdSettled_whenNoTransientObserved_trustsMirrorAfterGrace(t *testing.T) {
	// Given — the mirror already shows "healthy" and no restarting transition
	// is ever observed (the restart, if any, fully completed before this call
	// started watching). Shrink the grace window so the test stays fast.
	orig := cmdSettleGrace
	cmdSettleGrace = 20 * time.Millisecond
	defer func() { cmdSettleGrace = orig }()

	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvRunning},
		CmdStates:    map[string]engine.CmdState{"api": engine.CmdHealthy},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	ctrl := buildSettledCtrl(t, snap, nil)

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	healthy, err := WaitForCmdSettled(ctx, ctrl, "api")

	// Then
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !healthy {
		t.Error("want healthy=true after grace window elapses, got false")
	}
}

// ── Reload mirror-refresh tests ───────────────────────────────────────────────
//
// A reload can add or remove environments/commands, which no state event
// carries, so ClientController.Reload re-fetches a snapshot and re-seeds the
// mirror on success. These use the real daemon server (startTestServer,
// mockController — from server_test.go) so the snapshot actually reflects the
// mock's state at the time of the follow-up RPC, not a canned response.

func TestClientReload_whenEnvironmentAdded_appearsInEnvironments(t *testing.T) {
	// Given a connected client
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev"}}
	ctrl.workflowCmds["dev"] = []string{"api"}
	ctrl.envStates["dev"] = engine.EnvRunning
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	cc, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cc.Detach()

	// When a reload adds a new environment
	ctrl.mu.Lock()
	ctrl.envs = append(ctrl.envs, engine.EnvInfo{Name: "staging"})
	ctrl.workflowCmds["staging"] = []string{"worker"}
	ctrl.envStates["staging"] = engine.EnvStopped
	ctrl.mu.Unlock()

	if err := cc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Then the new environment appears in the mirror
	found := false
	for _, env := range cc.Environments() {
		if env.Name == "staging" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'staging' to appear after reload, got %v", cc.Environments())
	}
}

func TestClientReload_whenEnvironmentRemoved_disappearsFromEnvironments(t *testing.T) {
	// Given a connected client with two environments
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev"}, {Name: "staging"}}
	ctrl.workflowCmds["dev"] = []string{"api"}
	ctrl.workflowCmds["staging"] = []string{"worker"}
	ctrl.envStates["dev"] = engine.EnvRunning
	ctrl.envStates["staging"] = engine.EnvStopped
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	cc, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cc.Detach()

	// When a reload removes "staging"
	ctrl.mu.Lock()
	ctrl.envs = []engine.EnvInfo{{Name: "dev"}}
	delete(ctrl.workflowCmds, "staging")
	delete(ctrl.envStates, "staging")
	ctrl.mu.Unlock()

	if err := cc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Then "staging" no longer appears
	for _, env := range cc.Environments() {
		if env.Name == "staging" {
			t.Fatalf("expected 'staging' to be gone after reload, got %v", cc.Environments())
		}
	}
}

func TestClientReload_whenWorkflowChanged_workflowCommandsReflectIt(t *testing.T) {
	// Given a connected client
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev"}}
	ctrl.workflowCmds["dev"] = []string{"api"}
	ctrl.envStates["dev"] = engine.EnvRunning
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	cc, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cc.Detach()
	if got := cc.WorkflowCommands("dev"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("expected initial workflow [api], got %v", got)
	}

	// When a reload adds a step to "dev"'s workflow
	ctrl.mu.Lock()
	ctrl.workflowCmds["dev"] = []string{"api", "worker"}
	ctrl.mu.Unlock()

	if err := cc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Then WorkflowCommands reflects the new step
	got := cc.WorkflowCommands("dev")
	if len(got) != 2 || got[0] != "api" || got[1] != "worker" {
		t.Errorf("expected workflow [api worker], got %v", got)
	}
}

// startFakeServerSequencedRPC behaves like startFakeServer but responds to
// each RPC request in turn with the corresponding entry in rpcResponses
// (an empty, error-free Response once the list is exhausted).
func startFakeServerSequencedRPC(t *testing.T, snap Snapshot, rpcResponses []Response) string {
	t.Helper()
	dir := socketTempDir(t)
	socketPath := dir + "/fake.sock"

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("fake server listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		rpcConn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer rpcConn.Close()
			scan := bufio.NewScanner(rpcConn)
			enc := json.NewEncoder(rpcConn)
			for i := 0; scan.Scan(); i++ {
				resp := Response{}
				if i < len(rpcResponses) {
					resp = rpcResponses[i]
				}
				_ = enc.Encode(resp)
			}
		}()

		streamConn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveStream(t, streamConn, snap, nil)
	}()

	return socketPath
}

func TestClientReload_whenSnapshotRPCFails_keepsThePreviousMirror(t *testing.T) {
	// Given a client seeded with an initial snapshot, and a fake server that
	// succeeds the reload RPC but errors on the follow-up snapshot RPC
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{"dev": engine.EnvRunning},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: []engine.EnvInfo{{Name: "dev"}},
		WorkflowCmds: map[string][]string{"dev": {"api"}},
		LogPaths:     map[string]string{},
	}
	socketPath := startFakeServerSequencedRPC(t, snap, []Response{
		{},                              // MethodReload succeeds
		{Error: "snapshot unavailable"}, // MethodSnapshot fails
	})
	cc, err := Connect(socketPath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cc.Detach()

	// When
	if err := cc.Reload(context.Background()); err != nil {
		t.Fatalf("expected Reload to still succeed despite the snapshot refresh failing, got: %v", err)
	}

	// Then the previous mirror is left untouched
	envs := cc.Environments()
	if len(envs) != 1 || envs[0].Name != "dev" {
		t.Errorf("expected mirror to still show 'dev', got %v", envs)
	}
}
