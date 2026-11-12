package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adericbourg/env-starter/internal/daemon"
	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/tui"
)

// mcpTestConfig has one service that prints a few lines, including a secret
// from its env, then stays up.
const mcpTestConfig = `
env-starter:
  commands:
    - name: ticker
      type: service
      source:
        local: %s
      env:
        API_TOKEN: s3cr3t-value-123
      run: "for i in 1 2 3; do echo line$i; done; echo using $API_TOKEN; sleep 30"
  environments:
    - name: demo
      description: test env
      workflow:
        - command: ticker
`

// newMCPTestEnv writes and approves a config, returns an MCP server bound to
// it and a socket path under /tmp (macOS caps unix socket paths at ~104 bytes,
// see internal/daemon's socketTempDir).
func newMCPTestEnv(t *testing.T, approve bool) (mcpServer, string) {
	t.Helper()
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", fmt.Sprintf(mcpTestConfig, dir))
	if approve {
		approveConfig(t, cfgPath)
	}
	sockDir, err := os.MkdirTemp("/tmp", "esmcp")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	return mcpServer{
		socketPath: filepath.Join(sockDir, "d.sock"),
		lockPath:   filepath.Join(sockDir, "d.lock"),
		configFile: cfgPath,
	}, cfgPath
}

// serveDaemon runs an in-process daemon for cfgPath, as runDaemon does.
func serveDaemon(t *testing.T, s mcpServer) {
	t.Helper()
	cfg, loadFn, watchPaths, err := resolveConfig(s.configFile, "")
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	eng, err := engine.New(cfg)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = daemon.Serve(ctx, s.socketPath, tui.NewReloadController(eng, cfg, watchPaths, loadFn))
	}()
	// Serve only returns on a shutdown RPC (or a signal), not on ctx cancel.
	t.Cleanup(func() {
		if c, _ := daemon.DialOnly(s.socketPath); c != nil {
			c.Shutdown(ctx)
		}
		<-done
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(s.socketPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon socket never appeared")
}

func connectMCP(t *testing.T, s mcpServer) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := newMCPServer(s).Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool calls name and returns the result's text content and error flag.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): protocol error: %v", name, err)
	}
	var texts []string
	for _, c := range res.Content {
		texts = append(texts, c.(*mcp.TextContent).Text)
	}
	return strings.Join(texts, "\n"), res.IsError
}

func TestMCPServer_listTools_exposesEverythingButAllowWithHints(t *testing.T) {
	// Given
	s, _ := newMCPTestEnv(t, true)
	session := connectMCP(t, s)

	// When
	res, err := session.ListTools(context.Background(), nil)

	// Then
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	readOnly := map[string]bool{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		readOnly[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}
	slices.Sort(names)
	want := []string{"list_environments", "logs", "restart_command", "shutdown", "start_environment", "status", "stop_environment"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	for _, name := range []string{"list_environments", "logs", "status"} {
		if !readOnly[name] {
			t.Errorf("tool %s: want ReadOnlyHint", name)
		}
	}
}

func TestMCPServer_lifecycle_startStatusLogsRestartStopShutdown(t *testing.T) {
	// Given: an approved config and a running daemon
	s, _ := newMCPTestEnv(t, true)
	serveDaemon(t, s)
	session := connectMCP(t, s)

	// When / Then: list_environments reads the config
	out, isErr := callTool(t, session, "list_environments", nil)
	if isErr || !strings.Contains(out, `"name":"demo"`) || !strings.Contains(out, `"description":"test env"`) {
		t.Fatalf("list_environments = %s (isError=%v)", out, isErr)
	}

	// When / Then: start blocks until the env runs
	out, isErr = callTool(t, session, "start_environment", map[string]any{"env": "demo", "timeout_seconds": 20})
	if isErr || !strings.Contains(out, `"state":"running"`) {
		t.Fatalf("start_environment = %s (isError=%v)", out, isErr)
	}

	// When / Then: status reflects it
	out, isErr = callTool(t, session, "status", nil)
	var status statusResult
	if err := json.Unmarshal([]byte(out), &status); isErr || err != nil {
		t.Fatalf("status = %s (isError=%v, err=%v)", out, isErr, err)
	}
	if len(status.Environments) != 1 || status.Environments[0].State != engine.EnvRunning {
		t.Errorf("status = %+v, want demo running", status)
	}

	// When / Then: logs are tailed, with env values redacted
	out, isErr = callTool(t, session, "logs", map[string]any{"command": "ticker", "tail": 2})
	if isErr || !strings.Contains(out, `"lines":["line3","using ***"]`) {
		t.Errorf("logs = %s (isError=%v)", out, isErr)
	}

	// When / Then: restart waits until healthy again
	out, isErr = callTool(t, session, "restart_command", map[string]any{"command": "ticker", "timeout_seconds": 20})
	if isErr || !strings.Contains(out, `"state":"healthy"`) {
		t.Errorf("restart_command = %s (isError=%v)", out, isErr)
	}

	// When / Then: stop waits until stopped
	out, isErr = callTool(t, session, "stop_environment", map[string]any{"env": "demo"})
	if isErr || !strings.Contains(out, `"state":"stopped"`) {
		t.Errorf("stop_environment = %s (isError=%v)", out, isErr)
	}

	// When / Then: shutdown stops the daemon
	if out, isErr = callTool(t, session, "shutdown", nil); isErr {
		t.Errorf("shutdown = %s (isError=%v)", out, isErr)
	}
	out, isErr = callTool(t, session, "status", nil)
	if isErr || out != `{"environments":[]}` {
		t.Errorf("status after shutdown = %s (isError=%v), want no environments", out, isErr)
	}
}

func TestMCPServer_toolErrors_areReportedAsToolResults(t *testing.T) {
	// Given: a running daemon, then a config change left unapproved
	s, cfgPath := newMCPTestEnv(t, true)
	serveDaemon(t, s)
	session := connectMCP(t, s)

	// When / Then: unknown names are tool errors, not protocol errors
	if out, isErr := callTool(t, session, "start_environment", map[string]any{"env": "nope"}); !isErr || !strings.Contains(out, `unknown environment "nope"`) {
		t.Errorf("start_environment(nope) = %s (isError=%v)", out, isErr)
	}
	if out, isErr := callTool(t, session, "logs", map[string]any{"command": "nope"}); !isErr || !strings.Contains(out, `unknown command "nope"`) {
		t.Errorf("logs(nope) = %s (isError=%v)", out, isErr)
	}

	// When / Then: an unapproved config is refused and the agent is told to ask the user
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(mcpTestConfig, filepath.Dir(cfgPath))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, isErr := callTool(t, session, "start_environment", map[string]any{"env": "demo"}); !isErr || !strings.Contains(out, "env-starter allow") {
		t.Errorf("start_environment(unapproved) = %s (isError=%v)", out, isErr)
	}
}
