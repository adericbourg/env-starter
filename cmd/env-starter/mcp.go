package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/adericbourg/env-starter/internal/daemon"
	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/redact"
	"github.com/adericbourg/env-starter/internal/trust"
)

const (
	mcpDefaultTimeout  = 5 * time.Minute
	mcpDefaultLogLines = 200
	mcpMaxLogLines     = 1000
	// mcpFailureLogLines is how many log lines of each failed command a
	// start/restart result carries, so the agent can diagnose without a
	// second call.
	mcpFailureLogLines = 50
)

// mcpServer holds what every MCP tool needs to reach (or spawn) the daemon.
type mcpServer struct {
	socketPath    string
	lockPath      string
	configFile    string
	configOverlay string
}

type listResult struct {
	Environments []envEntry `json:"environments"`
}

type statusResult struct {
	Environments []envStatus `json:"environments"`
}

type logsArgs struct {
	Command string `json:"command" jsonschema:"command name"`
	Tail    int    `json:"tail,omitempty" jsonschema:"number of latest lines to return (default 200, max 1000)"`
}

type logsResult struct {
	Lines []string `json:"lines"`
}

type envArgs struct {
	Env            string `json:"env" jsonschema:"environment name"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"maximum seconds to wait (default 300)"`
}

type envResult struct {
	Env   string          `json:"env"`
	State engine.EnvState `json:"state"`
	// FailedLogs holds the latest log lines of each command that failed.
	FailedLogs map[string][]string `json:"failedLogs,omitempty"`
}

type cmdArgs struct {
	Command        string `json:"command" jsonschema:"command name"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"maximum seconds to wait (default 300)"`
}

type cmdResult struct {
	Command string          `json:"command"`
	State   engine.CmdState `json:"state"`
	// Logs holds the command's latest log lines when it did not come back healthy.
	Logs []string `json:"logs,omitempty"`
}

type noResult struct{}

// runMCP implements "env-starter mcp": an MCP server on stdio exposing the
// daemon's operations to AI agents. stdout carries the protocol, so nothing
// else may print to it.
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configFile := fs.String("config", "", "use FILE as the configuration")
	configOverlay := fs.String("config-overlay", "", "load FILE as an overlay on top of the base config")
	_ = fs.Parse(args)

	socketPath, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	lockPath, err := daemon.LockPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s := mcpServer{socketPath: socketPath, lockPath: lockPath, configFile: *configFile, configOverlay: *configOverlay}
	if err := newMCPServer(s).Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newMCPServer registers the tools. There is deliberately no tool to approve
// a config: approval (env-starter allow) must stay a human decision.
func newMCPServer(s mcpServer) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "env-starter", Version: version}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_environments",
		Description: "List the environments defined in the env-starter config.",
		Annotations: readOnly,
	}, s.listEnvironments)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "status",
		Description: "Show running environments and the state of each of their commands.",
		Annotations: readOnly,
	}, s.status)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "logs",
		Description: "Return the latest log lines of a command. Configured env values and common secret formats are masked as ***, best effort.",
		Annotations: readOnly,
	}, s.logs)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_environment",
		Description: "Start an environment and wait until it is running or failed. On failure, the result carries the failed commands' latest logs.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.startEnvironment)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_environment",
		Description: "Stop a running environment and wait until it is stopped.",
	}, s.stopEnvironment)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "restart_command",
		Description: "Restart a single started command and wait until it is healthy again or failed.",
	}, s.restartCommand)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "shutdown",
		Description: "Stop every environment and shut down the env-starter daemon.",
	}, s.shutdown)
	return server
}

func (s mcpServer) listEnvironments(_ context.Context, _ *mcp.CallToolRequest, _ noResult) (*mcp.CallToolResult, listResult, error) {
	cfg, _, _, err := resolveConfig(s.configFile, s.configOverlay)
	if err != nil {
		return nil, listResult{}, err
	}
	return nil, listResult{Environments: envEntries(cfg)}, nil
}

func (s mcpServer) status(_ context.Context, _ *mcp.CallToolRequest, _ noResult) (*mcp.CallToolResult, statusResult, error) {
	client, err := daemon.DialOnly(s.socketPath)
	if err != nil {
		return nil, statusResult{}, err
	}
	if client == nil {
		return nil, statusResult{Environments: []envStatus{}}, nil
	}
	defer client.Detach()
	return nil, statusResult{Environments: activeEnvironments(client)}, nil
}

func (s mcpServer) logs(_ context.Context, _ *mcp.CallToolRequest, args logsArgs) (*mcp.CallToolResult, logsResult, error) {
	tail := args.Tail
	if tail <= 0 {
		tail = mcpDefaultLogLines
	}
	client, err := s.dial()
	if err != nil {
		return nil, logsResult{}, err
	}
	defer client.Detach()
	lines, err := s.agentLogs(client, args.Command, min(tail, mcpMaxLogLines))
	return nil, logsResult{Lines: lines}, err
}

func (s mcpServer) startEnvironment(ctx context.Context, _ *mcp.CallToolRequest, args envArgs) (*mcp.CallToolResult, envResult, error) {
	var notApproved *trust.NotApprovedError
	if err := trust.Check(configPaths(s.configFile, s.configOverlay)); errors.As(err, &notApproved) {
		return nil, envResult{}, fmt.Errorf("%w; approving a config is the user's decision: ask them to run `env-starter allow`", err)
	} else if err != nil {
		return nil, envResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeoutOrDefault(args.TimeoutSeconds))
	defer cancel()
	client, err := daemon.EnsureDaemon(ctx, s.socketPath, s.lockPath, s.configFile, s.configOverlay)
	if err != nil {
		return nil, envResult{}, err
	}
	defer client.Detach()

	if !slices.ContainsFunc(client.Environments(), func(e engine.EnvInfo) bool { return e.Name == args.Env }) {
		return nil, envResult{}, fmt.Errorf("unknown environment %q", args.Env)
	}
	if err := client.StartEnvironment(args.Env); err != nil {
		return nil, envResult{}, err
	}
	running, err := daemon.WaitForEnvSettled(ctx, client, args.Env)
	if err != nil {
		return nil, envResult{}, fmt.Errorf("waiting for environment %q: %w", args.Env, err)
	}

	result := envResult{Env: args.Env, State: client.EnvState(args.Env)}
	if !running {
		result.FailedLogs = map[string][]string{}
		for _, cmd := range client.WorkflowCommands(args.Env) {
			if state := client.CmdState(cmd); state == engine.CmdError || state == engine.CmdTimeout {
				result.FailedLogs[cmd], _ = s.agentLogs(client, cmd, mcpFailureLogLines)
			}
		}
	}
	return nil, result, nil
}

func (s mcpServer) stopEnvironment(ctx context.Context, _ *mcp.CallToolRequest, args envArgs) (*mcp.CallToolResult, envResult, error) {
	client, err := s.dial()
	if err != nil {
		return nil, envResult{}, err
	}
	defer client.Detach()

	if err := client.StopEnvironment(args.Env); err != nil {
		return nil, envResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutOrDefault(args.TimeoutSeconds))
	defer cancel()
	if err := waitForEnvStopped(ctx, client, args.Env, func() {}); err != nil {
		return nil, envResult{}, fmt.Errorf("waiting for environment %q to stop: %w", args.Env, err)
	}
	return nil, envResult{Env: args.Env, State: engine.EnvStopped}, nil
}

func (s mcpServer) restartCommand(ctx context.Context, _ *mcp.CallToolRequest, args cmdArgs) (*mcp.CallToolResult, cmdResult, error) {
	client, err := s.dial()
	if err != nil {
		return nil, cmdResult{}, err
	}
	defer client.Detach()

	if err := client.RestartCommand(args.Command); err != nil {
		return nil, cmdResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutOrDefault(args.TimeoutSeconds))
	defer cancel()
	healthy, err := daemon.WaitForCmdSettled(ctx, client, args.Command)
	if err != nil {
		return nil, cmdResult{}, fmt.Errorf("waiting for command %q: %w", args.Command, err)
	}

	result := cmdResult{Command: args.Command, State: client.CmdState(args.Command)}
	if !healthy {
		result.Logs, _ = s.agentLogs(client, args.Command, mcpFailureLogLines)
	}
	return nil, result, nil
}

func (s mcpServer) shutdown(ctx context.Context, _ *mcp.CallToolRequest, _ noResult) (*mcp.CallToolResult, noResult, error) {
	client, err := daemon.DialOnly(s.socketPath)
	if err != nil || client == nil {
		return nil, noResult{}, err
	}
	client.Shutdown(ctx)
	return nil, noResult{}, nil
}

// agentLogs returns the last tail log lines of command with secrets redacted,
// since agents send them to their model provider. The whole buffer is redacted
// before tailing so a private key block cut by the tail is still masked.
func (s mcpServer) agentLogs(client commandLogSource, command string, tail int) ([]string, error) {
	lines, err := commandLogs(client, command, math.MaxInt)
	if err != nil {
		return nil, err
	}
	secrets, err := s.envValues()
	if err != nil {
		return nil, err
	}
	lines = redact.Lines(lines, secrets)
	return lines[max(0, len(lines)-tail):], nil
}

// envValues returns every env value set in the config (commands and
// environments, overlay included). OS env values are left out: masking HOME
// or USER would mangle the output.
func (s mcpServer) envValues() ([]string, error) {
	cfg, _, _, err := resolveConfig(s.configFile, s.configOverlay)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, cmd := range cfg.Commands {
		values = slices.AppendSeq(values, maps.Values(cmd.Env))
	}
	for _, env := range cfg.Environments {
		values = slices.AppendSeq(values, maps.Values(env.Env))
	}
	return values, nil
}

// dial connects to the running daemon; unlike start_environment, the other
// tools never spawn one.
func (s mcpServer) dial() (*daemon.ClientController, error) {
	client, err := daemon.DialOnly(s.socketPath)
	if err == nil && client == nil {
		err = errors.New("no daemon running: start an environment first")
	}
	return client, err
}

func timeoutOrDefault(seconds int) time.Duration {
	if seconds <= 0 {
		return mcpDefaultTimeout
	}
	return time.Duration(seconds) * time.Second
}
