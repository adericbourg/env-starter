// Package daemon implements the background daemon server for env-starter.
//
// The daemon listens on a Unix socket and serves two modes of operation:
//   - RPC mode: request/response JSON lines for querying and controlling the engine.
//   - Event-stream mode: a unidirectional stream of WireEvent JSON lines sent after
//     a subscribe request, letting clients stay synchronized with engine state.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/fsutil"
	"github.com/adericbourg/env-starter/internal/tui"
)

const (
	// eventHubBuffer is the per-subscriber channel buffer size. Events are dropped
	// (non-blocking send) when a subscriber's buffer is full.
	eventHubBuffer = 256
)

// SwappableController extends tui.Controller with the ability to register a
// callback that fires after an engine hot-reload swap. The daemon uses this to
// re-subscribe the event hub to the new engine's Events() channel.
type SwappableController interface {
	tui.Controller
	SetOnSwap(fn func())
}

// hubOpKind is the discriminant for hubCmd.
type hubOpKind int

const (
	hubOpSubscribe   hubOpKind = iota
	hubOpUnsubscribe hubOpKind = iota
)

// hubCmd is an internal message sent to the event hub goroutine.
type hubCmd struct {
	op  hubOpKind
	sub chan WireEvent
}

// server holds the daemon's runtime state.
type server struct {
	ctrl       SwappableController
	socketPath string

	// hub channels
	hubCmds chan hubCmd

	// done is closed when the server has finished shutting down.
	done chan struct{}

	// shutdownOnce ensures shutdown is executed exactly once, preventing
	// double-close panics when both a signal and an RPC trigger shutdown.
	shutdownOnce sync.Once
}

// Serve starts the daemon on socketPath and blocks until shutdown.
//
// Shutdown is triggered by:
//   - A shutdown RPC from any connected client.
//   - SIGINT or SIGTERM from the OS.
//
// The ctrl argument must implement SwappableController so the event hub can
// re-subscribe after a hot-reload.
func Serve(ctx context.Context, socketPath string, ctrl SwappableController) error {
	s := &server{
		ctrl:       ctrl,
		socketPath: socketPath,
		hubCmds:    make(chan hubCmd, 16),
		done:       make(chan struct{}),
	}

	// The socket dir must be owner-only before the socket exists: it is the
	// primary barrier keeping other local users from ever reaching the socket.
	// Enforced here (not only in EnsureDaemon) so a directly-invoked __daemon
	// gets the same guarantee.
	if err := fsutil.EnsureOwnerOnlyDir(filepath.Dir(socketPath)); err != nil {
		return fmt.Errorf("daemon: socket dir: %w", err)
	}

	// Remove any stale socket file from a previous run.
	_ = os.Remove(socketPath)

	// Create the socket file with owner-only permissions from the first instant:
	// net.Listen creates it 0777&^umask, which would otherwise leave a brief
	// window where another local user could connect before the chmod below.
	restoreUmask := restrictUmask()
	ln, err := net.Listen("unix", socketPath)
	restoreUmask()
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("daemon: chmod socket: %w", err)
	}

	// Start the event hub goroutine. It fans out engine events to subscribers.
	// The hub also registers the onSwap callback with ctrl so it can re-subscribe
	// to the new engine's Events() channel after a hot-reload.
	go s.runHub(ctx, ln, ctrl.Events())

	// Handle OS signals for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			s.shutdown(ctx, ln)
		case <-s.done:
		}
	}()

	// Accept loop: each connection gets its own goroutine.
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A closed listener error is expected on shutdown.
			select {
			case <-s.done:
				return nil
			default:
				return fmt.Errorf("daemon: accept: %w", err)
			}
		}
		// Defence in depth on top of the socket file permissions: only peers
		// running as the daemon's own user may issue commands.
		if err := checkPeer(conn); err != nil {
			log.Printf("daemon: %v", err)
			_ = conn.Close()
			continue
		}
		go s.handleConn(ctx, conn, ln)
	}
}

// runHub is the event hub goroutine. It drains the engine event channel and fans
// out WireEvent values to all registered subscribers. Sends to full subscriber
// buffers are dropped (non-blocking). Hub registration/deregistration is handled
// via the hubCmds channel to avoid mutex contention.
//
// The hub accepts a resubscribe signal via the resubCh (a nil-send convention on
// hubCmds is not used; instead the server's onSwap calls resubscribe explicitly).
func (s *server) runHub(ctx context.Context, ln net.Listener, events <-chan engine.Event) {
	subscribers := make(map[chan<- WireEvent]struct{})

	// resubCh receives a fresh events channel when the engine is hot-reloaded.
	// Concurrent calls to this onSwap closure cannot occur: reloadController.Reload
	// holds c.mu.Lock() for the entire swap operation before invoking onSwap, so
	// only one onSwap call can be in flight at a time.
	resubCh := make(chan (<-chan engine.Event), 1)
	s.ctrl.SetOnSwap(func() {
		newEvents := s.ctrl.Events()
		select {
		case resubCh <- newEvents:
		default:
			// A previous resubscribe has not been consumed yet; overwrite it.
			// Drain and resend.
			select {
			case <-resubCh:
			default:
			}
			resubCh <- newEvents
		}
	})

	for {
		select {
		case <-ctx.Done():
			return

		case <-s.done:
			return

		case newEvents := <-resubCh:
			events = newEvents

		case cmd := <-s.hubCmds:
			switch cmd.op {
			case hubOpSubscribe:
				subscribers[cmd.sub] = struct{}{}
			case hubOpUnsubscribe:
				// Remove the subscriber; the subscriber owns the channel lifecycle
				// and will close it itself after the unsubscribe.
				delete(subscribers, cmd.sub)
			}

		case ev, ok := <-events:
			if !ok {
				// Engine closed the events channel (e.g. shutdown).
				return
			}
			attempts, max := s.ctrl.CmdRetries(ev.Command)
			wire := EventToWire(ev, attempts, max, s.ctrl.IsUnmanaged(ev.Command))
			for sub := range subscribers {
				select {
				case sub <- wire:
				default:
					// Subscriber buffer full — drop the event.
				}
			}
		}
	}
}

// handleConn reads the first request from the connection to determine the
// connection mode, then dispatches accordingly.
func (s *server) handleConn(ctx context.Context, conn net.Conn, ln net.Listener) {
	defer func() { _ = conn.Close() }()

	scanner := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)

	if !scanner.Scan() {
		return
	}

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeError(enc, "invalid request: "+err.Error())
		return
	}

	switch req.Method {
	case MethodSubscribe:
		s.handleSubscribe(ctx, conn, enc)
		return
	case MethodShutdown:
		writeResult(enc, nil)
		// Run teardown in the background so this connection keeps serving RPCs
		// (e.g. StoppingCommands polls) while the engine tears down.
		go s.shutdown(ctx, ln)
	default:
		s.handleRPC(ctx, req, enc)
	}

	// Serve additional RPC requests on the same connection until the client
	// disconnects. Shutdown runs concurrently in the background, so polls like
	// StoppingCommands are answered normally during teardown.
	for scanner.Scan() {
		var next Request
		if err := json.Unmarshal(scanner.Bytes(), &next); err != nil {
			writeError(enc, "invalid request: "+err.Error())
			continue
		}
		if next.Method == MethodShutdown {
			writeResult(enc, nil)
			go s.shutdown(ctx, ln) // idempotent via shutdownOnce
			continue
		}
		s.handleRPC(ctx, next, enc)
	}
}

// handleSubscribe switches the connection to event-stream mode. It first sends
// a Snapshot as the Response.Result so the client can render immediately, then
// streams WireEvent lines until the connection closes or the server shuts down.
//
// The hub subscription is registered BEFORE the snapshot is built and sent so
// that no events are missed: any event fired after subscription but before the
// snapshot is read by the client will be queued in subCh and delivered after
// the snapshot.
func (s *server) handleSubscribe(ctx context.Context, conn net.Conn, enc *json.Encoder) {
	// Register with the hub first so no events are missed between the snapshot
	// and the client beginning to read the event stream.
	subCh := make(chan WireEvent, eventHubBuffer)
	select {
	case s.hubCmds <- hubCmd{op: hubOpSubscribe, sub: subCh}:
	case <-s.done:
		return
	case <-ctx.Done():
		return
	}

	snap := buildSnapshot(s.ctrl)
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		writeError(enc, "snapshot marshal: "+err.Error())
		return
	}
	writeResult(enc, snapBytes)

	// Deregister when done. The subscriber owns the subCh lifecycle and closes
	// it after unsubscribing. Using a select with done/ctx guards against the
	// hub being gone (e.g. engine shutdown) so the send cannot block forever.
	defer func() {
		select {
		case s.hubCmds <- hubCmd{op: hubOpUnsubscribe, sub: subCh}:
		case <-s.done:
		case <-ctx.Done():
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case ev := <-subCh:
			if err := enc.Encode(ev); err != nil {
				return
			}
		}
	}
}

// handleRPC dispatches a single RPC request and writes the response.
func (s *server) handleRPC(ctx context.Context, req Request, enc *json.Encoder) {
	switch req.Method {
	case MethodEnvironments:
		envs := s.ctrl.Environments()
		writeResultOf(enc, envs)

	case MethodSnapshot:
		snap := buildSnapshot(s.ctrl)
		writeResultOf(enc, snap)

	case MethodWorkflowCommands:
		var p EnvParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		cmds := s.ctrl.WorkflowCommands(p.Env)
		writeResultOf(enc, cmds)

	case MethodEnvState:
		var p EnvParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		state := s.ctrl.EnvState(p.Env)
		writeResultOf(enc, state)

	case MethodCmdState:
		var p CmdParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		state := s.ctrl.CmdState(p.Command)
		writeResultOf(enc, state)

	case MethodCmdRetries:
		var p CmdParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		attempts, max := s.ctrl.CmdRetries(p.Command)
		writeResultOf(enc, CmdRetriesResult{Attempts: attempts, Max: max})

	case MethodLogs:
		var p LogsParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		lines := s.ctrl.Logs(p.Command)
		writeResultOf(enc, LogsResult{Lines: lines})

	case MethodLogPath:
		var p CmdParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		path := s.ctrl.LogPath(p.Command)
		writeResultOf(enc, path)

	case MethodStartEnvironment:
		var p EnvParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		if err := s.ctrl.StartEnvironment(p.Env); err != nil {
			writeError(enc, err.Error())
			return
		}
		writeResult(enc, nil)

	case MethodStopEnvironment:
		var p EnvParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		if err := s.ctrl.StopEnvironment(p.Env); err != nil {
			writeError(enc, err.Error())
			return
		}
		writeResult(enc, nil)

	case MethodRestartCommand:
		var p CmdParam
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, "invalid params: "+err.Error())
			return
		}
		if err := s.ctrl.RestartCommand(p.Command); err != nil {
			writeError(enc, err.Error())
			return
		}
		writeResult(enc, nil)

	case MethodStoppingCommands:
		cmds := s.ctrl.StoppingCommands()
		wire := make([]WireStoppingCommand, 0, len(cmds))
		for _, c := range cmds {
			wire = append(wire, WireStoppingCommand{
				Command: c.Command,
				Elapsed: int64(c.Elapsed),
				Grace:   int64(c.Grace),
			})
		}
		writeResultOf(enc, StoppingCommandsResult{Commands: wire})

	case MethodConfigChanged:
		changed, err := s.ctrl.ConfigChanged()
		result := ConfigChangedResult{Changed: changed}
		if err != nil {
			result.Err = err.Error()
		}
		writeResultOf(enc, result)

	case MethodReload:
		if err := s.ctrl.Reload(ctx); err != nil {
			writeError(enc, err.Error())
			return
		}
		writeResult(enc, nil)

	default:
		writeError(enc, "unknown method: "+req.Method)
	}
}

// shutdown performs a graceful shutdown: calls ctrl.Shutdown, closes the
// listener, removes the socket file, and closes the done channel.
// It is safe to call multiple times; subsequent calls are no-ops.
//
// done is closed before ln.Close() so that the accept loop's select can
// observe s.done before the Accept error is processed.
func (s *server) shutdown(ctx context.Context, ln net.Listener) {
	s.shutdownOnce.Do(func() {
		s.ctrl.Shutdown(ctx)
		close(s.done)
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("daemon: close listener: %v", err)
		}
		_ = os.Remove(s.socketPath) // best-effort
	})
}

// buildSnapshot assembles a point-in-time Snapshot from the controller.
func buildSnapshot(ctrl tui.Controller) Snapshot {
	envs := ctrl.Environments()

	envStates := make(map[string]engine.EnvState, len(envs))
	workflowCmds := make(map[string][]string, len(envs))
	logPaths := make(map[string]string)

	// Collect all unique command names to avoid redundant state calls.
	seen := make(map[string]struct{})
	for _, env := range envs {
		envStates[env.Name] = ctrl.EnvState(env.Name)
		cmds := ctrl.WorkflowCommands(env.Name)
		workflowCmds[env.Name] = cmds
		for _, cmd := range cmds {
			seen[cmd] = struct{}{}
		}
	}

	cmdStates := make(map[string]engine.CmdState, len(seen))
	cmdRetries := make(map[string][2]int, len(seen))
	cmdUnmanaged := make(map[string]bool, len(seen))
	for cmd := range seen {
		cmdStates[cmd] = ctrl.CmdState(cmd)
		attempts, max := ctrl.CmdRetries(cmd)
		cmdRetries[cmd] = [2]int{attempts, max}
		cmdUnmanaged[cmd] = ctrl.IsUnmanaged(cmd)
		logPaths[cmd] = ctrl.LogPath(cmd)
	}

	return Snapshot{
		EnvStates:    envStates,
		CmdStates:    cmdStates,
		CmdRetries:   cmdRetries,
		CmdUnmanaged: cmdUnmanaged,
		Environments: envs,
		WorkflowCmds: workflowCmds,
		LogPaths:     logPaths,
	}
}

// writeResult writes a Response with the given raw JSON result (may be nil).
func writeResult(enc *json.Encoder, result json.RawMessage) {
	if err := enc.Encode(Response{Result: result}); err != nil {
		log.Printf("daemon: write response: %v", err)
	}
}

// writeResultOf marshals v to JSON and writes it as the response result.
func writeResultOf(enc *json.Encoder, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(enc, "marshal result: "+err.Error())
		return
	}
	writeResult(enc, b)
}

// writeError writes a Response with an error message.
func writeError(enc *json.Encoder, msg string) {
	if err := enc.Encode(Response{Error: msg}); err != nil {
		log.Printf("daemon: write error response: %v", err)
	}
}
