package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/engine"
)

// ── Mock controller ───────────────────────────────────────────────────────────

// mockController is a minimal tui.Controller + SwappableController for tests.
type mockController struct {
	mu sync.Mutex

	envs         []engine.EnvInfo
	workflowCmds map[string][]string
	envStates    map[string]engine.EnvState
	cmdStates    map[string]engine.CmdState
	cmdRetries   map[string][2]int
	logPaths     map[string]string
	logs         map[string][]string
	stoppingCmds []engine.StoppingCommand

	startErr error
	stopErr  error

	// eventsWrite is the writable end; eventsCh is the receive-only view returned
	// by Events(). Keeping both lets tests inject events via sendEvent.
	eventsWrite chan engine.Event
	eventsCh    <-chan engine.Event

	onSwap func()

	shutdownCalled bool
	// shutdownFunc, if set, is called instead of the default Shutdown stub.
	shutdownHook func(context.Context)
}

func newMockController() *mockController {
	ch := make(chan engine.Event, 256)
	return &mockController{
		workflowCmds: make(map[string][]string),
		envStates:    make(map[string]engine.EnvState),
		cmdStates:    make(map[string]engine.CmdState),
		cmdRetries:   make(map[string][2]int),
		logPaths:     make(map[string]string),
		logs:         make(map[string][]string),
		eventsWrite:  ch,
		eventsCh:     ch,
	}
}

func (m *mockController) Environments() []engine.EnvInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.envs
}

func (m *mockController) WorkflowCommands(env string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workflowCmds[env]
}

func (m *mockController) EnvState(env string) engine.EnvState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.envStates[env]
}

func (m *mockController) CmdState(command string) engine.CmdState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmdStates[command]
}

func (m *mockController) CmdRetries(command string) (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.cmdRetries[command]
	return r[0], r[1]
}

func (m *mockController) Logs(command string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logs[command]
}

func (m *mockController) LogPath(command string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logPaths[command]
}

func (m *mockController) StartEnvironment(env string) error {
	return m.startErr
}

func (m *mockController) StopEnvironment(env string) error {
	return m.stopErr
}

func (m *mockController) Events() <-chan engine.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventsCh
}

func (m *mockController) StoppingCommands() []engine.StoppingCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stoppingCmds
}

func (m *mockController) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.shutdownCalled = true
	hook := m.shutdownHook
	m.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
}

func (m *mockController) ConfigChanged() (bool, error) {
	return false, nil
}

func (m *mockController) Reload(_ context.Context) error {
	return nil
}

func (m *mockController) Detach() {}

func (m *mockController) SetOnSwap(fn func()) {
	m.mu.Lock()
	m.onSwap = fn
	m.mu.Unlock()
}

// sendEvent injects an event into the mock's event channel.
func (m *mockController) sendEvent(ev engine.Event) {
	m.eventsWrite <- ev
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// startTestServer starts a Serve goroutine using a temp unix socket.
// It returns the socket path and a cancel function to stop the server.
func startTestServer(t *testing.T, ctrl SwappableController) (socketPath string, cancel context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	socketPath = filepath.Join(dir, "daemon.sock")

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	go func() {
		// Signal readiness once the socket is actually accepting connections.
		go func() {
			for i := 0; i < 50; i++ {
				conn, err := net.DialTimeout("unix", socketPath, 5*time.Millisecond)
				if err == nil {
					conn.Close()
					close(ready)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
		if err := Serve(ctx, socketPath, ctrl); err != nil && ctx.Err() == nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not start in time")
	}
	return socketPath, cancel
}

// dialDaemon connects to the unix socket at socketPath and returns the conn,
// a line scanner and a JSON encoder ready for use.
func dialDaemon(t *testing.T, socketPath string) (net.Conn, *bufio.Scanner, *json.Encoder) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewScanner(conn), json.NewEncoder(conn)
}

// rpc sends one Request and returns the decoded Response.
func rpc(t *testing.T, enc *json.Encoder, scanner *bufio.Scanner, req Request) Response {
	t.Helper()
	if err := enc.Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// ── RPC dispatch tests ────────────────────────────────────────────────────────

func TestServe_environments_returnsEnvList(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{
		{Name: "dev", Description: "development"},
		{Name: "test", Description: "testing"},
	}
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	// When
	resp := rpc(t, enc, scanner, Request{Method: MethodEnvironments})

	// Then
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var envs []engine.EnvInfo
	if err := json.Unmarshal(resp.Result, &envs); err != nil {
		t.Fatalf("unmarshal environments: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("want 2 envs, got %d", len(envs))
	}
	if envs[0].Name != "dev" {
		t.Errorf("envs[0].Name: want %q, got %q", "dev", envs[0].Name)
	}
	if envs[1].Name != "test" {
		t.Errorf("envs[1].Name: want %q, got %q", "test", envs[1].Name)
	}
}

func TestServe_startEnvironment_whenNoError_returnsNilResult(t *testing.T) {
	// Given
	ctrl := newMockController()
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)
	params, _ := json.Marshal(EnvParam{Env: "dev"})

	// When
	resp := rpc(t, enc, scanner, Request{Method: MethodStartEnvironment, Params: params})

	// Then
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestServe_startEnvironment_whenError_returnsError(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.startErr = fmt.Errorf("environment already starting")
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)
	params, _ := json.Marshal(EnvParam{Env: "dev"})

	// When
	resp := rpc(t, enc, scanner, Request{Method: MethodStartEnvironment, Params: params})

	// Then
	if !strings.Contains(resp.Error, "environment already starting") {
		t.Errorf("want error containing %q, got %q", "environment already starting", resp.Error)
	}
}

func TestServe_snapshot_buildsFullSnapshot(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev", Description: "dev"}}
	ctrl.envStates["dev"] = engine.EnvRunning
	ctrl.workflowCmds["dev"] = []string{"api", "db"}
	ctrl.cmdStates["api"] = engine.CmdHealthy
	ctrl.cmdStates["db"] = engine.CmdStarting
	ctrl.cmdRetries["api"] = [2]int{1, 3}
	ctrl.logPaths["api"] = "/tmp/api.log"
	ctrl.logPaths["db"] = "/tmp/db.log"

	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	// When
	resp := rpc(t, enc, scanner, Request{Method: MethodSnapshot})

	// Then
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var snap Snapshot
	if err := json.Unmarshal(resp.Result, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.EnvStates["dev"] != engine.EnvRunning {
		t.Errorf("EnvStates[dev]: want %q, got %q", engine.EnvRunning, snap.EnvStates["dev"])
	}
	if snap.CmdStates["api"] != engine.CmdHealthy {
		t.Errorf("CmdStates[api]: want %q, got %q", engine.CmdHealthy, snap.CmdStates["api"])
	}
	if snap.CmdRetries["api"] != [2]int{1, 3} {
		t.Errorf("CmdRetries[api]: want [1 3], got %v", snap.CmdRetries["api"])
	}
	if snap.LogPaths["api"] != "/tmp/api.log" {
		t.Errorf("LogPaths[api]: want %q, got %q", "/tmp/api.log", snap.LogPaths["api"])
	}
	if len(snap.Environments) != 1 || snap.Environments[0].Name != "dev" {
		t.Errorf("Environments: want [{dev dev}], got %v", snap.Environments)
	}
}

func TestServe_unknownMethod_returnsError(t *testing.T) {
	// Given
	ctrl := newMockController()
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	// When
	resp := rpc(t, enc, scanner, Request{Method: "nonExistent"})

	// Then
	if !strings.Contains(resp.Error, "unknown method") {
		t.Errorf("want error containing %q, got %q", "unknown method", resp.Error)
	}
}

func TestServe_multipleRPCsOnSameConnection(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev", Description: "dev"}}
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	// When — send two RPCs on the same connection.
	resp1 := rpc(t, enc, scanner, Request{Method: MethodEnvironments})
	resp2 := rpc(t, enc, scanner, Request{Method: MethodSnapshot})

	// Then
	if resp1.Error != "" {
		t.Fatalf("resp1 error: %s", resp1.Error)
	}
	if resp2.Error != "" {
		t.Fatalf("resp2 error: %s", resp2.Error)
	}
}

// ── Subscribe / event stream tests ───────────────────────────────────────────

func TestServe_subscribe_sendssnapshot(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev", Description: "dev"}}
	ctrl.envStates["dev"] = engine.EnvStopped
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	// When — send a subscribe request.
	if err := enc.Encode(Request{Method: MethodSubscribe}); err != nil {
		t.Fatalf("encode subscribe: %v", err)
	}

	// Then — first response must be a snapshot.
	if !scanner.Scan() {
		t.Fatalf("scan snapshot response: %v", scanner.Err())
	}
	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("snapshot error: %s", resp.Error)
	}
	var snap Snapshot
	if err := json.Unmarshal(resp.Result, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.EnvStates["dev"] != engine.EnvStopped {
		t.Errorf("snapshot EnvStates[dev]: want %q, got %q", engine.EnvStopped, snap.EnvStates["dev"])
	}
}

func TestServe_subscribe_receivesEvents(t *testing.T) {
	// Given
	ctrl := newMockController()
	socketPath, cancel := startTestServer(t, ctrl)
	defer cancel()

	_, scanner, enc := dialDaemon(t, socketPath)

	if err := enc.Encode(Request{Method: MethodSubscribe}); err != nil {
		t.Fatalf("encode subscribe: %v", err)
	}

	// Consume the initial snapshot response. handleSubscribe registers with the
	// hub BEFORE sending the snapshot, so by the time we read the snapshot the
	// subscription is already active — no sleep required.
	if !scanner.Scan() {
		t.Fatalf("scan snapshot: %v", scanner.Err())
	}

	// Then — the subscriber receives the event as a WireEvent JSON line.
	evCh := make(chan WireEvent, 1)
	go func() {
		if scanner.Scan() {
			var ev WireEvent
			if err := json.Unmarshal(scanner.Bytes(), &ev); err == nil {
				evCh <- ev
			}
		}
	}()

	// When — inject an event. The subscription is already registered (see above).
	ctrl.sendEvent(engine.Event{
		Kind:     "command",
		Command:  "api",
		CmdState: engine.CmdHealthy,
	})

	select {
	case ev := <-evCh:
		if ev.Kind != "command" {
			t.Errorf("Kind: want %q, got %q", "command", ev.Kind)
		}
		if ev.Command != "api" {
			t.Errorf("Command: want %q, got %q", "api", ev.Command)
		}
		if ev.CmdState != engine.CmdHealthy {
			t.Errorf("CmdState: want %q, got %q", engine.CmdHealthy, ev.CmdState)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive event within timeout")
	}
}

// ── Shutdown RPC test ─────────────────────────────────────────────────────────

func TestServe_shutdown_closesServer(t *testing.T) {
	// Given
	ctrl := newMockController()
	socketPath, _ := startTestServer(t, ctrl)
	// Note: we do not call cancel() here because the shutdown RPC will stop the server.

	_, scanner, enc := dialDaemon(t, socketPath)

	// When — send a shutdown request.
	resp := rpc(t, enc, scanner, Request{Method: MethodShutdown})

	// Then — response must be successful (no error).
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Then — the server stops accepting new connections within a short timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 50*time.Millisecond)
		if err != nil {
			// Server is no longer accepting — shutdown complete.
			return
		}
		conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server still accepting connections after shutdown")
}

func TestServe_shutdown_rpcStillServedDuringTeardown(t *testing.T) {
	// Given — a controller that takes a while to shut down, with a stopping command.
	ctrl := newMockController()
	ctrl.stoppingCmds = []engine.StoppingCommand{
		{Command: "svc-a", Elapsed: 3 * time.Second, Grace: 30 * time.Second},
	}

	shutdownStarted := make(chan struct{})
	shutdownBlock := make(chan struct{})
	ctrl.shutdownHook = func(_ context.Context) {
		close(shutdownStarted)
		<-shutdownBlock // block until test unblocks it
	}

	socketPath, _ := startTestServer(t, ctrl)
	_, scanner, enc := dialDaemon(t, socketPath)

	// When — send shutdown, then immediately poll StoppingCommands on the same connection.
	rpc(t, enc, scanner, Request{Method: MethodShutdown})
	<-shutdownStarted // ensure engine teardown has begun

	resp := rpc(t, enc, scanner, Request{Method: MethodStoppingCommands})

	// Then — StoppingCommands response must be valid and contain the stopping command.
	if resp.Error != "" {
		t.Fatalf("StoppingCommands after shutdown RPC returned error: %s", resp.Error)
	}
	var result StoppingCommandsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode StoppingCommandsResult: %v", err)
	}
	if len(result.Commands) != 1 || result.Commands[0].Command != "svc-a" {
		t.Errorf("expected stopping command 'svc-a', got %+v", result.Commands)
	}

	close(shutdownBlock) // let teardown finish
}

// ── Hub fan-out tests ─────────────────────────────────────────────────────────

func TestHub_fanOut_toMultipleSubscribers(t *testing.T) {
	// Given — two subscribers registered with the hub directly.
	ctrl := newMockController()

	s := &server{
		ctrl:    ctrl,
		hubCmds: make(chan hubCmd, 16),
		done:    make(chan struct{}),
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	go s.runHub(ctx, nil, ctrl.Events())

	sub1 := make(chan WireEvent, 4)
	sub2 := make(chan WireEvent, 4)

	s.hubCmds <- hubCmd{op: hubOpSubscribe, sub: sub1}
	s.hubCmds <- hubCmd{op: hubOpSubscribe, sub: sub2}
	// Give hub time to process registrations.
	time.Sleep(10 * time.Millisecond)

	// When — send one event.
	ctrl.sendEvent(engine.Event{Kind: "command", Command: "api", CmdState: engine.CmdHealthy})

	// Then — both subscribers receive it.
	assertReceivesWireEvent := func(t *testing.T, name string, ch chan WireEvent, wantKind, wantCmd string) {
		t.Helper()
		select {
		case ev := <-ch:
			if ev.Kind != wantKind {
				t.Errorf("%s Kind: want %q, got %q", name, wantKind, ev.Kind)
			}
			if ev.Command != wantCmd {
				t.Errorf("%s Command: want %q, got %q", name, wantCmd, ev.Command)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: did not receive event within timeout", name)
		}
	}

	assertReceivesWireEvent(t, "sub1", sub1, "command", "api")
	assertReceivesWireEvent(t, "sub2", sub2, "command", "api")
}

func TestHub_fanOut_dropsEventWhenSubscriberBufferFull(t *testing.T) {
	// Given — a subscriber with a buffer size of 1.
	ctrl := newMockController()

	s := &server{
		ctrl:    ctrl,
		hubCmds: make(chan hubCmd, 16),
		done:    make(chan struct{}),
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	go s.runHub(ctx, nil, ctrl.Events())

	// Tiny buffer: holds only 1 event.
	subSmall := make(chan WireEvent, 1)
	s.hubCmds <- hubCmd{op: hubOpSubscribe, sub: subSmall}
	time.Sleep(10 * time.Millisecond)

	// When — send 3 events. Only 1 can fit in the buffer; the rest must be dropped.
	for i := 0; i < 3; i++ {
		ctrl.sendEvent(engine.Event{Kind: "command", Command: fmt.Sprintf("cmd%d", i), CmdState: engine.CmdHealthy})
	}
	time.Sleep(20 * time.Millisecond)

	// Then — exactly 1 event is buffered (overflow events are dropped).
	if got := len(subSmall); got != 1 {
		t.Errorf("want 1 buffered event, got %d", got)
	}
}

func TestHub_unsubscribe_stopsDelivery(t *testing.T) {
	// Given — a subscriber that is unsubscribed before the event is sent.
	ctrl := newMockController()

	s := &server{
		ctrl:    ctrl,
		hubCmds: make(chan hubCmd, 16),
		done:    make(chan struct{}),
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	go s.runHub(ctx, nil, ctrl.Events())

	sub := make(chan WireEvent, 4)
	s.hubCmds <- hubCmd{op: hubOpSubscribe, sub: sub}
	time.Sleep(10 * time.Millisecond)

	// When — unsubscribe then send an event.
	s.hubCmds <- hubCmd{op: hubOpUnsubscribe, sub: sub}
	time.Sleep(10 * time.Millisecond)
	ctrl.sendEvent(engine.Event{Kind: "command", Command: "api", CmdState: engine.CmdHealthy})
	time.Sleep(20 * time.Millisecond)

	// Then — no event was delivered.
	if len(sub) != 0 {
		t.Errorf("want 0 events after unsubscribe, got %d", len(sub))
	}
}

// ── buildSnapshot helper test ─────────────────────────────────────────────────

func TestBuildSnapshot_ofEmptyController_returnsEmptyMaps(t *testing.T) {
	// Given
	ctrl := newMockController()

	// When
	snap := buildSnapshot(ctrl)

	// Then
	if len(snap.Environments) != 0 {
		t.Errorf("Environments: want empty, got %v", snap.Environments)
	}
	if len(snap.EnvStates) != 0 {
		t.Errorf("EnvStates: want empty, got %v", snap.EnvStates)
	}
	if len(snap.CmdStates) != 0 {
		t.Errorf("CmdStates: want empty, got %v", snap.CmdStates)
	}
}

func TestBuildSnapshot_withEnvsAndCommands_populatesAllFields(t *testing.T) {
	// Given
	ctrl := newMockController()
	ctrl.envs = []engine.EnvInfo{{Name: "dev", Description: "dev"}}
	ctrl.envStates["dev"] = engine.EnvRunning
	ctrl.workflowCmds["dev"] = []string{"api", "db"}
	ctrl.cmdStates["api"] = engine.CmdHealthy
	ctrl.cmdStates["db"] = engine.CmdStarting
	ctrl.cmdRetries["api"] = [2]int{2, 5}
	ctrl.logPaths["api"] = "/logs/api.log"
	ctrl.logPaths["db"] = "/logs/db.log"

	// When
	snap := buildSnapshot(ctrl)

	// Then — environment-level fields.
	if snap.EnvStates["dev"] != engine.EnvRunning {
		t.Errorf("EnvStates[dev]: want %q, got %q", engine.EnvRunning, snap.EnvStates["dev"])
	}
	if len(snap.WorkflowCmds["dev"]) != 2 {
		t.Errorf("WorkflowCmds[dev]: want 2 commands, got %v", snap.WorkflowCmds["dev"])
	}

	// Then — command-level fields.
	if snap.CmdStates["api"] != engine.CmdHealthy {
		t.Errorf("CmdStates[api]: want %q, got %q", engine.CmdHealthy, snap.CmdStates["api"])
	}
	if snap.CmdStates["db"] != engine.CmdStarting {
		t.Errorf("CmdStates[db]: want %q, got %q", engine.CmdStarting, snap.CmdStates["db"])
	}
	if snap.CmdRetries["api"] != [2]int{2, 5} {
		t.Errorf("CmdRetries[api]: want [2 5], got %v", snap.CmdRetries["api"])
	}
	if snap.LogPaths["api"] != "/logs/api.log" {
		t.Errorf("LogPaths[api]: want %q, got %q", "/logs/api.log", snap.LogPaths["api"])
	}
	if snap.LogPaths["db"] != "/logs/db.log" {
		t.Errorf("LogPaths[db]: want %q, got %q", "/logs/db.log", snap.LogPaths["db"])
	}
}
