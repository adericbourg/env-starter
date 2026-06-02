// Command env-starter is a text-based meta-launcher that starts a named
// environment's commands in dependency order, waiting for each dependency to be
// healthy before starting its dependents.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/daemon"
	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/tui"
	"github.com/adericbourg/env-starter/internal/update"
)

// logSource is the subset of daemon.ClientController used by tailStartupLogs.
// Defined here so the function can be tested with a stub.
type logSource interface {
	WorkflowCommands(env string) []string
	Logs(command string) []string
}

// cmdColorCodes is the cycling ANSI color palette used to distinguish command
// prefixes in the startup log stream (same spirit as docker compose).
var cmdColorCodes = []string{
	"\033[36m", // cyan
	"\033[33m", // yellow
	"\033[32m", // green
	"\033[35m", // magenta
	"\033[34m", // blue
	"\033[96m", // bright cyan
	"\033[93m", // bright yellow
	"\033[92m", // bright green
	"\033[95m", // bright magenta
	"\033[94m", // bright blue
}

const ansiReset = "\033[0m"

// buildPrefixes returns a map from command name to its colored "[name]" prefix.
func buildPrefixes(cmds []string) map[string]string {
	prefixes := make(map[string]string, len(cmds))
	for i, cmd := range cmds {
		color := cmdColorCodes[i%len(cmdColorCodes)]
		prefixes[cmd] = color + "[" + cmd + "]" + ansiReset
	}
	return prefixes
}

// tailStartupLogs polls log lines for all commands in envName every 200 ms and
// writes new lines to out with a colored [cmdname] prefix. It stops when
// stopCh is closed or ctx is done, performing a final flush in either case.
func tailStartupLogs(ctx context.Context, src logSource, envName string, out io.Writer, stopCh <-chan struct{}) {
	cmds := src.WorkflowCommands(envName)
	if len(cmds) == 0 {
		return
	}

	prefixes := buildPrefixes(cmds)
	seenLines := make(map[string]int, len(cmds))

	flush := func() {
		for _, cmd := range cmds {
			lines := src.Logs(cmd)
			start := seenLines[cmd]
			prefix := prefixes[cmd]
			for _, line := range lines[start:] {
				fmt.Fprintf(out, "%s %s\n", prefix, line)
			}
			seenLines[cmd] = len(lines)
		}
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-stopCh:
			flush()
			return
		case <-ticker.C:
			flush()
		}
	}
}

// version is the build version, overridden at release time via -ldflags
// "-X main.version=...".
var version = "dev"

func main() {
	// Subcommand dispatch must happen before flag.Parse because stdlib flag
	// does not support subcommands natively.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__daemon":
			// Hidden subcommand: build engine + reloadController from flags, then
			// serve on socket. This is the process spawned by EnsureDaemon.
			runDaemon()
			return
		case "update":
			runUpdate()
			return
		case "run":
			runRun(os.Args[2:])
			return
		case "stop":
			runStop(os.Args[2:])
			return
		case "list":
			runList(os.Args[2:])
			return
		case "ps":
			runPs()
			return
		case "shutdown":
			runShutdown()
			return
		case "help", "-h", "--help":
			printHelp(os.Stdout)
			return
		}
	}

	// Check for -h/--help in position 1 already handled above.
	// Check for unknown subcommands: anything that looks like a subcommand
	// (non-flag argument) that wasn't matched above is an error.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		printHelp(os.Stderr)
		os.Exit(2)
	}

	// Default TUI path.
	var (
		showVersion   bool
		configFile    string
		configOverlay string
	)

	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.StringVar(&configFile, "config", "", "use `FILE` as the configuration, replacing the default")
	flag.StringVar(&configOverlay, "config-overlay", "", "load `FILE` as an overlay on top of the base config")
	flag.Parse()

	if showVersion {
		fmt.Printf("env-starter %s\n", version)
		os.Exit(0)
	}

	maybeSuggestUpdate()

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

	client, err := daemon.EnsureDaemon(ctx, socketPath, lockPath, configFile, configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	detached, tuiErr := tui.Run(client)
	if detached {
		fmt.Println("Detached. Your environments keep running in the background — run `env-starter` again to resume this session, or `env-starter shutdown` to stop everything.")
		// On detach: release client resources without shutting down the daemon.
		client.Detach()
		return
	}

	// If the OS signal fired before TUI exited, detach instead of shutting down —
	// one client being killed should not tear down the shared daemon.
	select {
	case <-ctx.Done():
		stop()
		client.Detach()
		return
	default:
	}

	if tuiErr != nil {
		fmt.Fprintf(os.Stderr, "error: tui: %v\n", tuiErr)
		os.Exit(1)
	}
}

// runDaemon is the hidden __daemon subcommand. It parses its own --config and
// --config-overlay flags, builds the engine and reloadController, then serves
// on the daemon socket until shutdown.
func runDaemon() {
	fs := flag.NewFlagSet("__daemon", flag.ExitOnError)
	configFile := fs.String("config", "", "use FILE as the configuration")
	configOverlay := fs.String("config-overlay", "", "load FILE as an overlay on top of the base config")
	_ = fs.Parse(os.Args[2:])

	cfg, loadFn, watchPaths, err := resolveConfig(*configFile, *configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	eng, err := engine.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: initialising engine: %v\n", err)
		os.Exit(1)
	}

	if removed, err := eng.PurgeOldLogs(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: purging old logs: %v\n", err)
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "purged %d old log file(s)\n", len(removed))
	}

	ctrl := tui.NewReloadController(eng, cfg, watchPaths, loadFn)

	socketPath, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Serve(ctx, socketPath, ctrl); err != nil {
		fmt.Fprintln(os.Stderr, "daemon error:", err)
		os.Exit(1)
	}
}

// runRun implements the "env-starter run <env>" subcommand. It ensures the
// daemon is running, starts the named environment, then waits until the
// environment has settled (running or failed) and exits accordingly.
//
// Exit codes:
//
//	0 — env is running (EnvRunning)
//	1 — env failed to start (EnvError/EnvDegraded)
//	2 — wrong usage (missing arg, unknown env)
//	3 — timed out waiting for env to settle
func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum time to wait for env to start")
	configFile := fs.String("config", "", "use FILE as the configuration")
	configOverlay := fs.String("config-overlay", "", "load FILE as an overlay on top of the base config")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: env-starter run <env>")
		os.Exit(2)
	}
	envName := fs.Arg(0)

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

	timeoutCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Starting environment %q…\n", envName)

	client, err := daemon.EnsureDaemon(timeoutCtx, socketPath, lockPath, *configFile, *configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Validate env name against configured environments before calling StartEnvironment.
	envs := client.Environments()
	found := false
	for _, e := range envs {
		if e.Name == envName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "error: unknown environment %q\n", envName)
		client.Detach()
		os.Exit(2)
	}

	if err := client.StartEnvironment(envName); err != nil {
		fmt.Fprintf(os.Stderr, "error: start environment %q: %v\n", envName, err)
		os.Exit(1)
	}

	stopTail := make(chan struct{})
	var tailWg sync.WaitGroup
	tailWg.Add(1)
	go func() {
		defer tailWg.Done()
		tailStartupLogs(timeoutCtx, client, envName, os.Stdout, stopTail)
	}()

	running, err := daemon.WaitForEnvSettled(timeoutCtx, client, envName)
	close(stopTail)
	tailWg.Wait()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(1)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "timed out waiting for environment %q to start\n", envName)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "error waiting for environment %q: %v\n", envName, err)
		os.Exit(1)
	}

	if running {
		fmt.Fprintf(os.Stderr, "Environment %q is running.\n", envName)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "Environment %q failed to start.\n", envName)
	os.Exit(1)
}

// runStop implements the "env-starter stop <env>" subcommand. It dials an
// existing daemon, sends a StopEnvironment RPC, then waits for the environment
// to reach EnvStopped before exiting.
func runStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: env-starter stop <env>")
		os.Exit(2)
	}
	envName := fs.Arg(0)

	socketPath, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	client, err := daemon.DialOnly(socketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if client == nil {
		fmt.Println("No daemon running.")
		return
	}

	if err := client.StopEnvironment(envName); err != nil {
		fmt.Fprintf(os.Stderr, "error: stop environment %q: %v\n", envName, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Stopping environment %q…\n", envName)

	// Wait until the env reaches EnvStopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startTime := time.Now()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(1)
		case ev, ok := <-client.Events():
			if !ok {
				// Daemon closed the stream — check final state.
				if client.EnvState(envName) == engine.EnvStopped {
					os.Exit(0)
				}
				fmt.Fprintf(os.Stderr, "event stream closed before %q stopped\n", envName)
				os.Exit(1)
			}
			_ = ev // state is reflected in the mirror; read it directly
			fmt.Fprintf(os.Stderr, "\rstopping %s… %ds", envName, int(time.Since(startTime).Seconds()))
			if client.EnvState(envName) == engine.EnvStopped {
				fmt.Fprintln(os.Stderr)
				os.Exit(0)
			}
		}
	}
}

// runList implements the "env-starter list" subcommand. It loads the config
// locally (no daemon needed) and prints each environment name and description.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	configFile := fs.String("config", "", "use FILE as the configuration")
	configOverlay := fs.String("config-overlay", "", "load FILE as an overlay on top of the base config")
	_ = fs.Parse(args)

	cfg, _, _, err := resolveConfig(*configFile, *configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Determine the column width for alignment.
	maxLen := 0
	for _, env := range cfg.Environments {
		if len(env.Name) > maxLen {
			maxLen = len(env.Name)
		}
	}

	for _, env := range cfg.Environments {
		fmt.Printf("%-*s  %s\n", maxLen, env.Name, env.Description)
	}
}

// runPs implements the "env-starter ps" subcommand. It dials an existing daemon
// and prints the non-stopped environments with their state and command states.
func runPs() {
	socketPath, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	client, err := daemon.DialOnly(socketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if client == nil {
		fmt.Println("No daemon running.")
		return
	}

	envs := client.Environments()
	printed := 0
	for _, env := range envs {
		state := client.EnvState(env.Name)
		if state == engine.EnvStopped {
			continue
		}
		cmds := client.WorkflowCommands(env.Name)
		cmdParts := make([]string, 0, len(cmds))
		for _, cmd := range cmds {
			cmdParts = append(cmdParts, fmt.Sprintf("%s:%s", cmd, client.CmdState(cmd)))
		}
		if len(cmdParts) > 0 {
			fmt.Printf("%-20s %-10s (%s)\n", env.Name, string(state), strings.Join(cmdParts, "  "))
		} else {
			fmt.Printf("%-20s %s\n", env.Name, string(state))
		}
		printed++
	}

	if printed == 0 {
		fmt.Println("No environments running.")
	}
}

// runShutdown implements the "env-starter shutdown" subcommand. It dials an
// existing daemon, sends a shutdown RPC, then waits until the event stream
// closes (daemon fully gone).
func runShutdown() {
	socketPath, err := daemon.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	client, err := daemon.DialOnly(socketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if client == nil {
		fmt.Println("No daemon running.")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "Shutting down…")
	client.Shutdown(ctx)
}

// printHelp prints usage information for all subcommands to w.
func printHelp(w *os.File) {
	fmt.Fprintf(w, `Usage: env-starter [command] [flags]

Commands:
  (default)  Open the TUI to manage environments
  run        Start an environment and wait for it to be ready
  stop       Stop a running environment
  list       List configured environments
  ps         Show running environments
  shutdown   Stop all environments and shut down the daemon
  update     Update env-starter to the latest version
  help       Show this help message

Flags (default TUI and run):
  --config FILE         Use FILE as the configuration
  --config-overlay FILE Apply FILE as overlay on top of base config

run flags:
  --timeout DURATION    Maximum time to wait for env to start (default: %s)
`, 5*time.Minute)
}

// runUpdate implements the "env-starter update" subcommand. It checks whether
// a newer release is available and, if so, downloads and installs it, then exits.
func runUpdate() {
	if version == "dev" {
		fmt.Println("running a dev build, updates disabled")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := update.New()
	rel, err := client.Latest(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: checking for updates: %v\n", err)
		os.Exit(1)
	}

	if !update.IsNewer(version, rel.TagName) {
		fmt.Printf("env-starter %s is already up to date\n", version)
		return
	}

	fmt.Printf("updating %s → %s…\n", version, rel.TagName)
	if err := client.Apply(ctx, rel); err != nil {
		fmt.Fprintf(os.Stderr, "error: applying update: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("updated to %s\n", rel.TagName)
}

// maybeSuggestUpdate checks for a newer release at startup and, when one
// exists, prompts the user to apply it. The check is bounded by a short
// timeout so it never meaningfully delays startup; any error is silently
// ignored so network issues never block normal use.
func maybeSuggestUpdate() {
	if version == "dev" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := update.New()
	rel, err := client.Latest(ctx)
	if err != nil {
		return // silent: network issues should not block startup
	}
	if !update.IsNewer(version, rel.TagName) {
		return
	}

	fmt.Printf("env-starter %s is available (current: %s). Update now? [y/N]: ", rel.TagName, version)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return
	}
	answer := strings.TrimSpace(strings.ToLower(sc.Text()))
	if answer != "y" && answer != "yes" {
		return
	}

	fmt.Println("Applying update…")
	applyCtx, applyCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer applyCancel()

	if err := client.Apply(applyCtx, rel); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return
	}

	fmt.Printf("Updated to %s. Restarting…\n", rel.TagName)
	if err := update.ReExec(); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-exec after update: %v\n", err)
		os.Exit(1)
	}
}

// resolveConfig loads the configuration from disk and returns:
//   - the initially parsed *config.Config
//   - a loadFn closure that re-runs the same load+merge logic for hot-reload
//   - watchPaths: the set of files to stat for change detection
//   - any error encountered during the initial load
func resolveConfig(baseFile, overlayFile string) (*config.Config, func() (*config.Config, error), []string, error) {
	basePath := baseFile
	if basePath == "" {
		basePath = defaultConfigPath()
	}

	// loadFn re-runs the full resolution (base + optional overlay) using the
	// resolved paths captured here. It is safe to call repeatedly.
	loadFn := func() (*config.Config, error) {
		base, err := config.Load(basePath)
		if err != nil {
			return nil, fmt.Errorf("loading config %q: %w", basePath, err)
		}
		if overlayFile == "" {
			return base, nil
		}
		overlay, err := config.Load(overlayFile)
		if err != nil {
			return nil, fmt.Errorf("loading overlay config %q: %w", overlayFile, err)
		}
		return config.Merge(base, overlay), nil
	}

	cfg, err := loadFn()
	if err != nil {
		if baseFile == "" && errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, fmt.Errorf(
				"no configuration file found at %s; "+
					"create ~/.config/env-starter/config.yaml or pass --config",
				basePath,
			)
		}
		return nil, nil, nil, err
	}

	watchPaths := []string{basePath}
	if overlayFile != "" {
		watchPaths = append(watchPaths, overlayFile)
	}

	return cfg, loadFn, watchPaths, nil
}

// defaultConfigPath returns $XDG_CONFIG_HOME/env-starter/config.yaml if
// XDG_CONFIG_HOME is set; otherwise ~/.config/env-starter/config.yaml.
func defaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "env-starter", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback when home dir cannot be determined.
		return filepath.Join(".config", "env-starter", "config.yaml")
	}
	return filepath.Join(home, ".config", "env-starter", "config.yaml")
}
