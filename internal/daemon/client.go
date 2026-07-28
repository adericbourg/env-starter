// Package daemon — client-side controller over a Unix socket connection to the
// daemon. ClientController implements tui.Controller by maintaining a local
// mirror seeded from the subscribe snapshot and kept up-to-date by the
// event-stream goroutine.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/engine"
)

const (
	// clientEventBuffer is the size of the events channel exposed to callers.
	clientEventBuffer = 256
)

// ClientController implements tui.Controller over a pair of Unix socket
// connections to the daemon.
//
// It maintains a local mirror of all engine state so that hot-path TUI reads
// (Environments, EnvState, CmdState, …) never incur a round-trip.
type ClientController struct {
	rpcConn    net.Conn // guarded by rpcMu; reused for all RPC calls
	streamConn net.Conn // event-stream connection; owned by runEventStream

	rpcMu   sync.Mutex
	rpcEnc  *json.Encoder
	rpcScan *bufio.Scanner

	// mirror — all fields guarded by mirrorMu
	mirrorMu     sync.RWMutex
	envStates    map[string]engine.EnvState
	cmdStates    map[string]engine.CmdState
	cmdRetries   map[string][2]int
	cmdUnmanaged map[string]bool
	environments []engine.EnvInfo
	workflowCmds map[string][]string
	logPaths     map[string]string

	events chan engine.Event
}

// Connect dials the daemon Unix socket at socketPath, performs the subscribe
// handshake on the event-stream connection to seed the local mirror, and
// starts the background event-stream goroutine. Returns a ready-to-use
// ClientController.
func Connect(socketPath string) (*ClientController, error) {
	// 1. Dial the RPC connection.
	rpcConn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("daemon client: dial rpc: %w", err)
	}

	// 2. Dial the event-stream connection.
	streamConn, err := net.Dial("unix", socketPath)
	if err != nil {
		_ = rpcConn.Close()
		return nil, fmt.Errorf("daemon client: dial stream: %w", err)
	}

	// 3. Send MethodSubscribe on the event-stream connection.
	streamEnc := json.NewEncoder(streamConn)
	if err := streamEnc.Encode(Request{Method: MethodSubscribe}); err != nil {
		_ = rpcConn.Close()
		_ = streamConn.Close()
		return nil, fmt.Errorf("daemon client: subscribe: %w", err)
	}

	// 4. Read the snapshot response.
	streamScan := bufio.NewScanner(streamConn)
	streamScan.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB; prevents truncation of large event payloads
	if !streamScan.Scan() {
		err := streamScan.Err()
		if err == nil {
			err = errors.New("connection closed before snapshot")
		}
		_ = rpcConn.Close()
		_ = streamConn.Close()
		return nil, fmt.Errorf("daemon client: read snapshot: %w", err)
	}
	var snapResp Response
	if err := json.Unmarshal(streamScan.Bytes(), &snapResp); err != nil {
		_ = rpcConn.Close()
		_ = streamConn.Close()
		return nil, fmt.Errorf("daemon client: decode snapshot response: %w", err)
	}
	if snapResp.Error != "" {
		_ = rpcConn.Close()
		_ = streamConn.Close()
		return nil, fmt.Errorf("daemon client: subscribe error: %s", snapResp.Error)
	}
	var snap Snapshot
	if err := json.Unmarshal(snapResp.Result, &snap); err != nil {
		_ = rpcConn.Close()
		_ = streamConn.Close()
		return nil, fmt.Errorf("daemon client: decode snapshot: %w", err)
	}

	// 5. Seed the mirror from the snapshot.
	rpcScan := bufio.NewScanner(rpcConn)
	rpcScan.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB; prevents truncation of large log responses
	c := &ClientController{
		rpcConn:    rpcConn,
		streamConn: streamConn,
		rpcEnc:     json.NewEncoder(rpcConn),
		rpcScan:    rpcScan,
		events:     make(chan engine.Event, clientEventBuffer),
	}
	c.seedMirror(snap)

	// 6. Start the event-stream goroutine.
	go c.runEventStream(streamScan)

	return c, nil
}

// seedMirror replaces the local mirror with snap, filling any nil map with an
// empty one so mirror reads never see a nil map. Used both to seed a freshly
// connected client and to refresh the mirror's topology after Reload, since
// state events carry no information about environments/commands having been
// added or removed.
func (c *ClientController) seedMirror(snap Snapshot) {
	if snap.EnvStates == nil {
		snap.EnvStates = make(map[string]engine.EnvState)
	}
	if snap.CmdStates == nil {
		snap.CmdStates = make(map[string]engine.CmdState)
	}
	if snap.CmdRetries == nil {
		snap.CmdRetries = make(map[string][2]int)
	}
	if snap.CmdUnmanaged == nil {
		snap.CmdUnmanaged = make(map[string]bool)
	}
	if snap.WorkflowCmds == nil {
		snap.WorkflowCmds = make(map[string][]string)
	}
	if snap.LogPaths == nil {
		snap.LogPaths = make(map[string]string)
	}

	c.mirrorMu.Lock()
	c.envStates = snap.EnvStates
	c.cmdStates = snap.CmdStates
	c.cmdRetries = snap.CmdRetries
	c.cmdUnmanaged = snap.CmdUnmanaged
	c.environments = snap.Environments
	c.workflowCmds = snap.WorkflowCmds
	c.logPaths = snap.LogPaths
	c.mirrorMu.Unlock()
}

// runEventStream reads WireEvent lines continuously from the event-stream
// connection, updates the local mirror, and forwards engine.Events on the
// events channel (non-blocking drop if full). Closes the events channel when
// the stream ends.
func (c *ClientController) runEventStream(scan *bufio.Scanner) {
	defer close(c.events)
	for scan.Scan() {
		var w WireEvent
		if err := json.Unmarshal(scan.Bytes(), &w); err != nil {
			log.Printf("daemon: client: malformed event (skipping): %v", err)
			continue
		}

		// Update mirror.
		c.mirrorMu.Lock()
		if w.Kind == "environment" && w.Environment != "" {
			c.envStates[w.Environment] = w.EnvState
		}
		if w.Kind == "command" && w.Command != "" {
			c.cmdStates[w.Command] = w.CmdState
			c.cmdRetries[w.Command] = [2]int{w.RetryAttempts, w.RetryMax}
			c.cmdUnmanaged[w.Command] = w.Unmanaged
		}
		c.mirrorMu.Unlock()

		// Forward to callers (non-blocking).
		ev := WireToEvent(w)
		select {
		case c.events <- ev:
		default:
		}
	}
}

// ── Mirror reads ──────────────────────────────────────────────────────────────

// Environments returns the ordered environment list from the local mirror.
func (c *ClientController) Environments() []engine.EnvInfo {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	out := make([]engine.EnvInfo, len(c.environments))
	copy(out, c.environments)
	return out
}

// WorkflowCommands returns the command names for env from the local mirror.
func (c *ClientController) WorkflowCommands(env string) []string {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	cmds := c.workflowCmds[env]
	if cmds == nil {
		return nil
	}
	out := make([]string, len(cmds))
	copy(out, cmds)
	return out
}

// EnvState returns the environment's state from the local mirror.
func (c *ClientController) EnvState(env string) engine.EnvState {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	return c.envStates[env]
}

// CmdState returns the command's state from the local mirror.
func (c *ClientController) CmdState(command string) engine.CmdState {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	return c.cmdStates[command]
}

// CmdRetries returns the retry counters from the local mirror.
func (c *ClientController) CmdRetries(command string) (attempts, max int) {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	r := c.cmdRetries[command]
	return r[0], r[1]
}

// IsUnmanaged returns the unmanaged flag from the local mirror.
func (c *ClientController) IsUnmanaged(command string) bool {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	return c.cmdUnmanaged[command]
}

// LogPath returns the log file path from the local mirror (seeded from snapshot).
func (c *ClientController) LogPath(command string) string {
	c.mirrorMu.RLock()
	defer c.mirrorMu.RUnlock()
	return c.logPaths[command]
}

// ── Direct RPCs ───────────────────────────────────────────────────────────────

// Logs fetches log lines for command via RPC.
func (c *ClientController) Logs(command string) []string {
	params, _ := json.Marshal(LogsParams{Command: command})
	resp, err := c.rpc(Request{Method: MethodLogs, Params: params})
	if err != nil || resp.Error != "" {
		return nil
	}
	var result LogsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil
	}
	return result.Lines
}

// StartEnvironment sends a startEnvironment RPC and returns any error.
func (c *ClientController) StartEnvironment(env string) error {
	params, _ := json.Marshal(EnvParam{Env: env})
	resp, err := c.rpc(Request{Method: MethodStartEnvironment, Params: params})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// StopEnvironment sends a stopEnvironment RPC and returns any error.
func (c *ClientController) StopEnvironment(env string) error {
	params, _ := json.Marshal(EnvParam{Env: env})
	resp, err := c.rpc(Request{Method: MethodStopEnvironment, Params: params})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// RestartCommand sends a restartCommand RPC and returns any error.
func (c *ClientController) RestartCommand(command string) error {
	params, _ := json.Marshal(CmdParam{Command: command})
	resp, err := c.rpc(Request{Method: MethodRestartCommand, Params: params})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

// StoppingCommands fetches the currently-stopping commands via RPC.
func (c *ClientController) StoppingCommands() []engine.StoppingCommand {
	resp, err := c.rpc(Request{Method: MethodStoppingCommands})
	if err != nil || resp.Error != "" {
		return nil
	}
	var result StoppingCommandsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil
	}
	out := make([]engine.StoppingCommand, 0, len(result.Commands))
	for _, w := range result.Commands {
		out = append(out, engine.StoppingCommand{
			Command: w.Command,
			Elapsed: time.Duration(w.Elapsed),
			Grace:   time.Duration(w.Grace),
		})
	}
	return out
}

// ResolveEnv fetches the resolved, provenance-tagged env variables for envName
// or command (exactly one should be non-empty) via RPC.
func (c *ClientController) ResolveEnv(envName, command string) []engine.ResolvedEnvVar {
	params, _ := json.Marshal(ResolveEnvParams{Env: envName, Command: command})
	resp, err := c.rpc(Request{Method: MethodResolveEnv, Params: params})
	if err != nil || resp.Error != "" {
		return nil
	}
	var result ResolveEnvResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil
	}
	return result.Vars
}

// ConfigChanged fetches the config-changed status via RPC.
func (c *ClientController) ConfigChanged() (bool, error) {
	resp, err := c.rpc(Request{Method: MethodConfigChanged})
	if err != nil {
		return false, err
	}
	if resp.Error != "" {
		return false, errors.New(resp.Error)
	}
	var result ConfigChangedResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return false, err
	}
	var maybeErr error
	if result.Err != "" {
		maybeErr = errors.New(result.Err)
	}
	return result.Changed, maybeErr
}

// Reload sends a reload RPC and returns any error.
// Note: ctx is accepted for interface compliance but cancellation is not propagated to the blocking RPC call.
//
// A reload can add or remove environments/commands, which no state event
// carries, so the mirror's topology (environments/workflows) would otherwise
// go stale. On success, Reload also re-fetches a snapshot and re-seeds the
// mirror from it. That refresh is best-effort: if it fails, Reload still
// returns nil (the reload itself succeeded) and the previous mirror is left
// in place rather than blanked out.
func (c *ClientController) Reload(ctx context.Context) error {
	resp, err := c.rpc(Request{Method: MethodReload})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}

	snapResp, err := c.rpc(Request{Method: MethodSnapshot})
	if err != nil || snapResp.Error != "" {
		return nil
	}
	var snap Snapshot
	if err := json.Unmarshal(snapResp.Result, &snap); err != nil {
		return nil
	}
	c.seedMirror(snap)

	return nil
}

// ── Special ───────────────────────────────────────────────────────────────────

// Events returns the channel fed by the event-stream goroutine. The channel is
// closed when the stream ends (daemon gone).
func (c *ClientController) Events() <-chan engine.Event {
	return c.events
}

// Shutdown sends a shutdown RPC to the daemon and then blocks until the
// event-stream connection closes (daemon fully gone). This keeps the TUI
// shutdown screen visible during daemon teardown.
func (c *ClientController) Shutdown(ctx context.Context) {
	// Send shutdown RPC (best-effort; daemon may already be going down)
	_, _ = c.rpc(Request{Method: MethodShutdown})
	// Block until the daemon closes the event stream (or context expires)
	for {
		select {
		case _, ok := <-c.events:
			if !ok {
				return // channel closed = daemon gone
			}
		case <-ctx.Done():
			return
		}
	}
}

// Detach closes both connections silently without sending a shutdown RPC to
// the daemon. It is used when the client wants to disconnect without stopping
// the daemon (e.g. detach mode).
func (c *ClientController) Detach() {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	_ = c.rpcConn.Close()
	_ = c.streamConn.Close()
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// rpc sends a single request on the RPC connection and reads the response.
// The rpcMu mutex serialises concurrent callers.
func (c *ClientController) rpc(req Request) (Response, error) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()

	if err := c.rpcEnc.Encode(req); err != nil {
		return Response{}, fmt.Errorf("daemon client: send %s: %w", req.Method, err)
	}
	if !c.rpcScan.Scan() {
		err := c.rpcScan.Err()
		if err == nil {
			err = errors.New("connection closed")
		}
		return Response{}, fmt.Errorf("daemon client: recv %s: %w", req.Method, err)
	}
	var resp Response
	if err := json.Unmarshal(c.rpcScan.Bytes(), &resp); err != nil {
		return Response{}, fmt.Errorf("daemon client: decode %s response: %w", req.Method, err)
	}
	return resp, nil
}

// ── Run-settling helper ───────────────────────────────────────────────────────

// WaitForEnvSettled blocks until the named env has "settled" — meaning it is
// no longer in a transient state (not starting and no commands still
// starting/restarting) — and returns true if the env is running, false if
// degraded/error. ctrl must be a *ClientController.
//
// WaitForEnvSettled must only be called when no other goroutine is consuming
// ctrl.Events() — it reads directly from the shared event channel. In practice
// this means it should only be used in headless mode (the `run` subcommand)
// where ctrl is the sole consumer.
func WaitForEnvSettled(ctx context.Context, ctrl *ClientController, envName string) (running bool, err error) {
	// settled reports whether the current mirror state for envName is stable.
	settled := func() (isSettled, isRunning bool) {
		envState := ctrl.EnvState(envName)
		if envState == engine.EnvStarting {
			return false, false
		}
		for _, cmd := range ctrl.WorkflowCommands(envName) {
			s := ctrl.CmdState(cmd)
			if s == engine.CmdStarting || s == engine.CmdRestarting {
				return false, false
			}
		}
		return true, envState == engine.EnvRunning
	}

	// 1. Return immediately only when the env is already running. For other
	// settled states (Stopped, Error, Degraded), the mirror may not yet reflect
	// a concurrent StartEnvironment call — wait for events before concluding.
	if isSettled, isRunning := settled(); isSettled && isRunning {
		return true, nil
	}

	// 2. Subscribe to the events channel and loop until settled or cancelled.
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case _, ok := <-ctrl.Events():
			if !ok {
				// Event stream closed — check final state.
				isSettled, isRunning := settled()
				if isSettled {
					return isRunning, nil
				}
				return false, errors.New("event stream closed before env settled")
			}
			if isSettled, isRunning := settled(); isSettled {
				return isRunning, nil
			}
		}
	}
}

// cmdSettleGrace bounds how long WaitForCmdSettled waits to observe the
// transient restarting phase before trusting an already-terminal reading.
// A var (not a const) so tests can shrink it.
var cmdSettleGrace = 200 * time.Millisecond

// WaitForCmdSettled blocks until the named command has "settled" after a
// restart — meaning it is no longer starting/restarting/stopping — and
// returns true if it ended healthy/done, false if it ended in
// error/timeout/stopped. ctrl must be a *ClientController.
//
// Unlike WaitForEnvSettled, an immediate "already healthy" mirror reading
// cannot be trusted as proof the restart happened: a restarted command's
// success state is indistinguishable from its state just before the
// restart, and the RestartCommand RPC response and the event that carries
// the CmdRestarting transition travel over two independent connections with
// no ordering guarantee between them. So a terminal reading is only trusted
// once we have actually observed the transient restarting phase, or once
// cmdSettleGrace has elapsed without seeing it (meaning the restart most
// likely completed before this call even started watching) — whichever
// comes first. The grace window is comfortably longer than any realistic
// local-socket propagation delay and far shorter than an actual restart
// cycle (process kill + relaunch + readiness probe).
//
// WaitForCmdSettled must only be called when no other goroutine is consuming
// ctrl.Events() — it reads directly from the shared event channel. In practice
// this means it should only be used in headless mode (the `command restart`
// subcommand) where ctrl is the sole consumer.
func WaitForCmdSettled(ctx context.Context, ctrl *ClientController, command string) (healthy bool, err error) {
	// settled reports whether the current mirror state for command is
	// terminal (isSettled), transient (isTransient), and healthy (isHealthy).
	settled := func() (isSettled, isTransient, isHealthy bool) {
		switch ctrl.CmdState(command) {
		case engine.CmdStarting, engine.CmdRestarting, engine.CmdStopping:
			return false, true, false
		case engine.CmdHealthy, engine.CmdDone:
			return true, false, true
		default:
			return true, false, false
		}
	}

	grace := time.NewTimer(cmdSettleGrace)
	defer grace.Stop()
	trusted := false

	for {
		isSettled, isTransient, isHealthy := settled()
		if isTransient {
			trusted = true // the restart has now visibly begun
		}
		if isSettled && trusted {
			return isHealthy, nil
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-grace.C:
			trusted = true
		case _, ok := <-ctrl.Events():
			if !ok {
				// Event stream closed — no further information can ever
				// arrive, so this is necessarily the final word regardless
				// of the trust gate above.
				isSettled, _, isHealthy := settled()
				if isSettled {
					return isHealthy, nil
				}
				return false, errors.New("event stream closed before command settled")
			}
		}
	}
}
