// Command env-starter is a text-based meta-launcher that starts a named
// environment's commands in dependency order, waiting for each dependency to be
// healthy before starting its dependents.
package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/adericbourg/env-starter/internal/completion"
	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/daemon"
	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/linkscan"
	"github.com/adericbourg/env-starter/internal/termsafe"
	"github.com/adericbourg/env-starter/internal/textdiff"
	"github.com/adericbourg/env-starter/internal/trust"
	"github.com/adericbourg/env-starter/internal/tui"
	"github.com/adericbourg/env-starter/internal/update"
)

//go:embed completions
var completionFS embed.FS

// logSource is the subset of daemon.ClientController used by tailStartupLogs.
// Defined here so the function can be tested with a stub.
type logSource interface {
	WorkflowCommands(env string) []string
	Logs(command string) []string
}

// stateSource is the subset of daemon.ClientController used by ps and
// command list. Defined here so the gathering logic can be tested with a stub.
type stateSource interface {
	Environments() []engine.EnvInfo
	EnvState(env string) engine.EnvState
	WorkflowCommands(env string) []string
	CmdState(command string) engine.CmdState
}

// cmdStatus and envStatus are the --json output shapes of ps and command list.
type cmdStatus struct {
	Name  string          `json:"name"`
	State engine.CmdState `json:"state"`
}

type envStatus struct {
	Name     string          `json:"name"`
	State    engine.EnvState `json:"state"`
	Commands []cmdStatus     `json:"commands"`
}

// activeEnvironments returns every non-stopped environment with the state of
// each of its workflow commands.
// Slices are never nil so that --json prints [] rather than null.
func activeEnvironments(src stateSource) []envStatus {
	envs := []envStatus{}
	for _, env := range src.Environments() {
		state := src.EnvState(env.Name)
		if state == engine.EnvStopped {
			continue
		}
		cmds := []cmdStatus{}
		for _, cmd := range src.WorkflowCommands(env.Name) {
			cmds = append(cmds, cmdStatus{Name: cmd, State: src.CmdState(cmd)})
		}
		envs = append(envs, envStatus{Name: env.Name, State: state, Commands: cmds})
	}
	return envs
}

// activeCommands returns every command referenced by a non-stopped
// environment, deduplicated (a command can be shared by several environments).
func activeCommands(src stateSource) []cmdStatus {
	seen := make(map[string]bool)
	cmds := []cmdStatus{}
	for _, env := range activeEnvironments(src) {
		for _, cmd := range env.Commands {
			if !seen[cmd.Name] {
				seen[cmd.Name] = true
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

// commandLogSource is the subset of daemon.ClientController used by logs.
type commandLogSource interface {
	logSource
	Environments() []engine.EnvInfo
}

// commandLogs returns the last tail log lines of command, or an error when no
// configured environment references it.
func commandLogs(src commandLogSource, command string, tail int) ([]string, error) {
	for _, env := range src.Environments() {
		if slices.Contains(src.WorkflowCommands(env.Name), command) {
			lines := src.Logs(command)
			return lines[max(0, len(lines)-tail):], nil
		}
	}
	return nil, fmt.Errorf("unknown command %q", command)
}

// writeJSON writes v as one line of JSON.
func writeJSON(w io.Writer, v any) {
	_ = json.NewEncoder(w).Encode(v)
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

// cliLinksWindow is the number of recently-printed prefixed log lines scanned
// for URLs to surface in the CLI contextual-links overlay.
const cliLinksWindow = 5

// tailStartupLogs polls log lines for all commands in envName every 200 ms and
// writes new lines to out with a colored [cmdname] prefix. When interactive is
// true (stdout is a terminal), a contextual-links overlay is kept pinned just
// above the cursor, redrawing as new URLs appear or age out of the window.
// When interactive is false the output is plain text — no ANSI cursor control —
// so piped or redirected output is never corrupted.
// It stops when stopCh is closed or ctx is done, performing a final flush in
// either case (the overlay is cleared on exit).
func tailStartupLogs(ctx context.Context, src logSource, envName string, out io.Writer, interactive bool, stopCh <-chan struct{}) {
	cmds := src.WorkflowCommands(envName)
	if len(cmds) == 0 {
		return
	}

	prefixes := buildPrefixes(cmds)
	seenLines := make(map[string]int, len(cmds))

	// last5 is a ring of the cliLinksWindow most-recently printed tagged lines,
	// used to compute the contextual-links overlay in interactive mode.
	last5 := make([]linkscan.TaggedLine, 0, cliLinksWindow)
	prevOverlayHeight := 0

	// appendLast5 appends a tagged line to the sliding window, evicting the
	// oldest entry once the window is full.
	appendLast5 := func(cmd, text string) {
		if len(last5) >= cliLinksWindow {
			last5 = last5[1:]
		}
		last5 = append(last5, linkscan.TaggedLine{Command: cmd, Text: text})
	}

	// eraseOverlay writes the ANSI sequence to move the cursor up by
	// prevOverlayHeight rows and clear from there to the end of the screen,
	// effectively erasing the previously drawn overlay.
	eraseOverlay := func() {
		if prevOverlayHeight > 0 {
			_, _ = fmt.Fprintf(out, "\x1b[%dA\x1b[J", prevOverlayHeight)
			prevOverlayHeight = 0
		}
	}

	// drawOverlay renders the contextual-links block below the current cursor
	// position and updates prevOverlayHeight with the number of rows written.
	// Terminal width is sampled each call so the block adapts to window resizes.
	drawOverlay := func() {
		links := linkscan.Collect(last5)
		termWidth := 80
		if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
			termWidth = w
		}
		overlay := renderCLIOverlay(links, termWidth)
		if overlay == "" {
			return
		}
		_, _ = fmt.Fprint(out, overlay)
		prevOverlayHeight = strings.Count(overlay, "\n")
	}

	flush := func(final bool) {
		if interactive {
			eraseOverlay()
		}
		for _, cmd := range cmds {
			lines := src.Logs(cmd)
			start := seenLines[cmd]
			prefix := prefixes[cmd]
			for _, line := range lines[start:] {
				_, _ = fmt.Fprintf(out, "%s %s\n", prefix, line)
				if interactive {
					appendLast5(cmd, line)
				}
			}
			seenLines[cmd] = len(lines)
		}
		if interactive && !final {
			drawOverlay()
		}
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			flush(true)
			return
		case <-stopCh:
			flush(true)
			return
		case <-ticker.C:
			flush(false)
		}
	}
}

// Rounded-border characters matching Lipgloss's RoundedBorder, so the CLI
// overlay looks consistent with the TUI panels.
const (
	borderTopLeft     = "╭"
	borderTopRight    = "╮"
	borderBottomLeft  = "╰"
	borderBottomRight = "╯"
	borderHoriz       = "─"
	borderVert        = "│"
	// borderColor is the same dim-grey used for borderNormal in the TUI (color 240).
	borderColor = "\033[90m"
)

// titleInBorder embeds text into a top border line, e.g. "╭─── Title ───╮".
func titleInBorder(title string, totalWidth int) string {
	const pad = " " // one space on each side of the title
	label := pad + title + pad
	remaining := totalWidth - 2 - len(label) // 2 for corners
	if remaining < 0 {
		remaining = 0
	}
	left := remaining / 2
	right := remaining - left
	return borderTopLeft + strings.Repeat(borderHoriz, left) + label + strings.Repeat(borderHoriz, right) + borderTopRight
}

// renderCLIOverlay builds the contextual-links block for the CLI interactive
// mode using a rounded border to match the TUI panel style. Returns an empty
// string when there are no links. Each URL is clickable via OSC 8.
func renderCLIOverlay(links []linkscan.Link, termWidth int) string {
	if len(links) == 0 {
		return ""
	}
	if termWidth < 10 {
		termWidth = 10
	}

	// Content width = total width minus the two vertical border characters.
	contentWidth := termWidth - 2

	// Find the longest "[cmd]" label so rows can be right-aligned.
	maxLabelLen := 0
	for _, link := range links {
		if l := len("[" + link.Command + "]"); l > maxLabelLen {
			maxLabelLen = l
		}
	}

	var b strings.Builder

	// Top border with title embedded.
	b.WriteString(borderColor + titleInBorder("Contextual links", termWidth) + ansiReset + "\n")

	for i, link := range links {
		label := "[" + link.Command + "]"
		pad := strings.Repeat(" ", maxLabelLen-len(label))
		color := cmdColorCodes[i%len(cmdColorCodes)]
		coloredLabel := color + pad + label + ansiReset

		urlWidth := contentWidth - maxLabelLen - 2 // 2 for the "  " separator
		if urlWidth < 1 {
			urlWidth = 1
		}
		displayURL := link.URL
		if len(displayURL) > urlWidth {
			displayURL = displayURL[:urlWidth-1] + "…"
		}
		// Right-pad to fill content width so the right border aligns.
		rightPad := strings.Repeat(" ", urlWidth-len(displayURL))
		// OSC 8 clickable hyperlink: full URL as target, display text as label.
		clickable := "\x1b]8;;" + link.URL + "\x07" + displayURL + "\x1b]8;;\x07"

		b.WriteString(borderColor + borderVert + ansiReset)
		fmt.Fprintf(&b, "%s  %s%s", coloredLabel, clickable, rightPad)
		b.WriteString(borderColor + borderVert + ansiReset + "\n")
	}

	// Bottom border.
	b.WriteString(borderColor + borderBottomLeft + strings.Repeat(borderHoriz, contentWidth) + borderBottomRight + ansiReset + "\n")

	return b.String()
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
		case "__complete":
			// Hidden subcommand invoked by shell completion scripts on every TAB.
			// Prints candidates + a directive line; never starts the daemon.
			runComplete(os.Args[2:])
			return
		case "completion":
			runCompletion(os.Args[2:])
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
		case "allow":
			runAllow(os.Args[2:])
			return
		case "ps":
			runPs(os.Args[2:])
			return
		case "command":
			runCommand(os.Args[2:])
			return
		case "logs":
			runLogs(os.Args[2:])
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

	checkConfigTrust(configFile, configOverlay)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := daemon.EnsureDaemon(ctx, socketPath, lockPath, configFile, configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	detached, tuiErr := tui.Run(client, version)
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

	checkConfigTrust(*configFile, *configOverlay)

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

	// interactive is true when stdout is a terminal, enabling the sticky
	// contextual-links overlay. When output is piped or redirected we stay plain.
	interactive := term.IsTerminal(os.Stdout.Fd())

	stopTail := make(chan struct{})
	var tailWg sync.WaitGroup
	tailWg.Add(1)
	go func() {
		defer tailWg.Done()
		tailStartupLogs(timeoutCtx, client, envName, os.Stdout, interactive, stopTail)
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
	asJSON := fs.Bool("json", false, "print as JSON")
	_ = fs.Parse(args)

	cfg, _, _, err := resolveConfig(*configFile, *configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if *asJSON {
		type envEntry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		entries := []envEntry{}
		for _, env := range cfg.Environments {
			entries = append(entries, envEntry{Name: env.Name, Description: env.Description})
		}
		writeJSON(os.Stdout, entries)
		return
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

// checkConfigTrust runs the same trust gate resolveConfig's loadFn enforces,
// but before spawning or dialing the daemon. Without this, an unapproved or
// changed config still fails safely (the spawned __daemon process's loadFn
// refuses it and exits), but the client only learns that indirectly, after
// EnsureDaemon's opaque 10-second poll times out. Any other error (e.g. the
// config file does not exist) is left to surface through that normal path,
// which already reports it clearly.
func checkConfigTrust(configFile, configOverlay string) {
	err := trust.Check(configPaths(configFile, configOverlay))
	var notApproved *trust.NotApprovedError
	if errors.As(err, &notApproved) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// allowResult reports the outcome of doAllow's core logic, letting runAllow
// decide the process exit code without doAllow itself calling os.Exit (which
// would make it untestable).
type allowResult struct {
	// approved is true when the config's current hashes end up recorded in
	// the trust store — either because they already were, or because this
	// call just wrote them.
	approved bool
	// declined is true when the operator was prompted and answered no.
	declined bool
}

// doAllow implements the "env-starter allow" subcommand's logic: it previews
// every run/setup/teardown/readiness.shell command the resolved config would
// execute, then — unless printOnly is set — prompts on stdin (skipped when
// yes is set) before recording approval in the trust store that
// resolveConfig's loadFn checks on every load and hot-reload (see
// internal/trust and SECURITY.md, "Config trust").
func doAllow(w io.Writer, stdin io.Reader, configFile, configOverlay string, yes, printOnly, full bool) (allowResult, error) {
	paths := configPaths(configFile, configOverlay)

	cfg, err := loadMerged(paths[0], configOverlay)
	if err != nil {
		return allowResult{}, err
	}

	statuses, err := trust.Status(paths)
	if err != nil {
		return allowResult{}, err
	}

	diffs, isDiffComplete, err := changeDiffs(statuses)
	if err != nil {
		return allowResult{}, err
	}
	showListing := full || !isDiffComplete
	printAllowPreview(w, cfg, statuses, diffs, showListing)

	if printOnly {
		return allowResult{}, nil
	}

	allApproved := true
	for _, st := range statuses {
		if !st.Approved {
			allApproved = false
			break
		}
	}
	if allApproved {
		_, _ = fmt.Fprintln(w, "\nAlready approved — nothing to do.")
		return allowResult{approved: true}, nil
	}

	if !yes {
		reader := bufio.NewReader(stdin)
		for {
			if showListing {
				_, _ = fmt.Fprint(w, "\nApprove these commands? [y/N] ")
			} else {
				_, _ = fmt.Fprint(w, "\nApprove these changes? [y/N/f(ull listing)] ")
			}
			line, _ := reader.ReadString('\n')
			answer := strings.ToLower(strings.TrimSpace(line))
			if !showListing && (answer == "f" || answer == "full") {
				printCommandListing(w, cfg)
				continue
			}
			if answer != "y" && answer != "yes" {
				_, _ = fmt.Fprintln(w, "Not approved.")
				return allowResult{declined: true}, nil
			}
			break
		}
	}

	if err := trust.Approve(paths); err != nil {
		return allowResult{}, err
	}
	_, _ = fmt.Fprintln(w, "Approved.")
	return allowResult{approved: true}, nil
}

// runAllow is the CLI entry point for the "allow" subcommand: it parses
// flags, wires real stdin/stdout into doAllow, and translates the result
// into a process exit code.
func runAllow(args []string) {
	fs := flag.NewFlagSet("allow", flag.ExitOnError)
	configFile := fs.String("config", "", "use FILE as the configuration")
	configOverlay := fs.String("config-overlay", "", "load FILE as an overlay on top of the base config")
	yes := fs.Bool("yes", false, "approve without prompting")
	printOnly := fs.Bool("print", false, "show the preview and exit without approving")
	full := fs.Bool("full", false, "list every command, even when a diff since the last approval is available")
	_ = fs.Parse(args)

	result, err := doAllow(os.Stdout, os.Stdin, *configFile, *configOverlay, *yes, *printOnly, *full)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if result.declined {
		os.Exit(1)
	}
}

// changeDiffs returns a unified diff, against its last approved content, of
// each path that changed since it was approved. isComplete is true when
// every path still to approve has such a diff — only then can the diffs
// replace the full command listing in the review.
func changeDiffs(statuses []trust.PathStatus) (diffs []string, isComplete bool, err error) {
	pending := 0
	for _, st := range statuses {
		if st.Approved {
			continue
		}
		pending++
		approved, ok, err := trust.ApprovedContent(st)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		current, err := os.ReadFile(st.Path)
		if err != nil {
			return nil, false, err
		}
		// An empty diff (e.g. only the trailing newline changed) would show
		// nothing to review, so it does not count.
		if d := textdiff.Unified(st.Path+" (approved)", st.Path, approved, current); d != "" {
			diffs = append(diffs, d)
		}
	}
	return diffs, pending > 0 && len(diffs) == pending, nil
}

// printAllowPreview writes a human-reviewable listing of each watched config
// file's approval status, the diff of each file changed since its approval,
// then — when showListing is set — every shell command its commands would
// execute, so the operator can make an informed approve/deny decision.
func printAllowPreview(w io.Writer, cfg *config.Config, statuses []trust.PathStatus, diffs []string, showListing bool) {
	_, _ = fmt.Fprintln(w, "Config files:")
	for _, st := range statuses {
		status := "NEW"
		switch {
		case st.Approved:
			status = "approved"
		case st.ApprovedHash != "":
			status = "CHANGED since approval"
		}
		_, _ = fmt.Fprintf(w, "  [%s] %s\n      sha256:%s\n", status, st.Path, st.CurrentHash)
	}

	if len(diffs) > 0 {
		_, _ = fmt.Fprintln(w, "\nChanges since last approval:")
		for _, d := range diffs {
			_, _ = fmt.Fprintln(w)
			// Escape sequences (styling included) are stripped so a tampered
			// file cannot hide or disguise lines from the reviewer.
			for _, line := range strings.Split(strings.TrimSuffix(d, "\n"), "\n") {
				_, _ = fmt.Fprintln(w, "  "+ansi.Strip(termsafe.Line(line)))
			}
		}
	}

	if showListing {
		printCommandListing(w, cfg)
	} else {
		_, _ = fmt.Fprintln(w, "\n(run with --full, or answer 'f' below, to list every command)")
	}
}

// printCommandListing writes every shell command cfg's commands would
// execute, along with each command's source.
func printCommandListing(w io.Writer, cfg *config.Config) {
	_, _ = fmt.Fprintln(w, "\nCommands that will be executed as shell scripts:")
	for _, cmd := range cfg.Commands {
		_, _ = fmt.Fprintf(w, "\n  %s:\n", cmd.Name)
		if src := formatSource(cmd.Source); src != "" {
			_, _ = fmt.Fprintf(w, "    source:    %s\n", src)
		}
		for _, step := range cmd.Setup {
			_, _ = fmt.Fprintf(w, "    setup:     %s\n", indentContinuation(step))
		}
		if cmd.Run != "" {
			_, _ = fmt.Fprintf(w, "    run:       %s\n", indentContinuation(cmd.Run))
		}
		if cmd.Teardown != "" {
			_, _ = fmt.Fprintf(w, "    teardown:  %s\n", indentContinuation(cmd.Teardown))
		}
		if cmd.Readiness != nil && cmd.Readiness.Shell != "" {
			_, _ = fmt.Fprintf(w, "    readiness: %s\n", indentContinuation(cmd.Readiness.Shell))
		}
	}
}

// formatSource renders a command's source as a browsable URL or filesystem
// path, so the operator can go inspect the actual code behind a run/teardown
// shell string during `allow` review. Returns "" for a zero-value Source
// (validation rejects configs with none of github/url/local set, so this
// only happens for a Command built directly in Go, e.g. in tests).
func formatSource(src config.Source) string {
	switch {
	case src.GitHub != nil:
		branch := src.GitHub.Branch
		if branch == "" {
			branch = "main" // matches source.GitHub.effectiveBranch's default
		}
		url := fmt.Sprintf("https://github.com/%s/tree/%s", src.GitHub.Repo, branch)
		if src.Subdir != "" {
			url += "/" + src.Subdir
		}
		return url
	case src.URLSource != nil:
		return src.URLSource.URL
	case src.Local != "":
		if src.Subdir != "" {
			return filepath.Join(src.Local, src.Subdir)
		}
		return src.Local
	default:
		return ""
	}
}

// indentContinuation indents every line after the first in a multi-line
// shell command (e.g. a YAML "|" block scalar with several statements) so it
// stays visually nested under its "setup:"/"run:"/"teardown:" label instead
// of bleeding out to the left margin — the preview exists to be read.
func indentContinuation(s string) string {
	return strings.ReplaceAll(s, "\n", "\n               ")
}

// runPs implements the "env-starter ps" subcommand. It dials an existing daemon
// and prints the non-stopped environments with their state and command states.
func runPs(args []string) {
	fs := flag.NewFlagSet("ps", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	_ = fs.Parse(args)

	client := dialExistingDaemon()
	if client == nil {
		if *asJSON {
			writeJSON(os.Stdout, []envStatus{})
			return
		}
		fmt.Println("No daemon running.")
		return
	}

	envs := activeEnvironments(client)
	if *asJSON {
		writeJSON(os.Stdout, envs)
		return
	}
	for _, env := range envs {
		cmdParts := make([]string, 0, len(env.Commands))
		for _, cmd := range env.Commands {
			cmdParts = append(cmdParts, fmt.Sprintf("%s:%s", cmd.Name, cmd.State))
		}
		if len(cmdParts) > 0 {
			fmt.Printf("%-20s %-10s (%s)\n", env.Name, string(env.State), strings.Join(cmdParts, "  "))
		} else {
			fmt.Printf("%-20s %s\n", env.Name, string(env.State))
		}
	}

	if len(envs) == 0 {
		fmt.Println("No environments running.")
	}
}

// dialExistingDaemon connects to a running daemon without spawning one. It
// returns nil when no daemon is running and exits on any other error.
func dialExistingDaemon() *daemon.ClientController {
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
	return client
}

// runLogs implements "env-starter logs <command>". It dials an existing daemon
// and prints the latest lines of the command's in-memory log buffer.
func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	tail := fs.Int("tail", 200, "number of lines to print")
	_ = fs.Parse(args)

	if fs.NArg() != 1 || *tail < 1 {
		fmt.Fprintln(os.Stderr, "Usage: env-starter logs [--tail N] <command>  (N >= 1)")
		os.Exit(2)
	}

	client := dialExistingDaemon()
	if client == nil {
		fmt.Fprintln(os.Stderr, "error: no daemon running")
		os.Exit(1)
	}

	lines, err := commandLogs(client, fs.Arg(0), *tail)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

// runCommand implements the "env-starter command <verb>" subcommand group,
// dispatching to the list/restart sub-verbs.
func runCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: env-starter command <list|restart> [args]")
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		runCommandList(args[1:])
	case "restart":
		runCommandRestart(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command verb: %q\n\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: env-starter command <list|restart> [args]")
		os.Exit(2)
	}
}

// runCommandList implements "env-starter command list". It dials an existing
// daemon and prints every command referenced by a non-stopped environment,
// deduplicated (a command can be shared by several environments), with its
// current state.
func runCommandList(args []string) {
	fs := flag.NewFlagSet("command list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	_ = fs.Parse(args)

	client := dialExistingDaemon()
	if client == nil {
		if *asJSON {
			writeJSON(os.Stdout, []cmdStatus{})
			return
		}
		fmt.Println("No daemon running.")
		return
	}

	cmds := activeCommands(client)
	if *asJSON {
		writeJSON(os.Stdout, cmds)
		return
	}
	if len(cmds) == 0 {
		fmt.Println("No commands running.")
		return
	}

	maxLen := 0
	for _, cmd := range cmds {
		if len(cmd.Name) > maxLen {
			maxLen = len(cmd.Name)
		}
	}
	for _, cmd := range cmds {
		fmt.Printf("%-*s  %s\n", maxLen, cmd.Name, string(cmd.State))
	}
}

// runCommandRestart implements "env-starter command restart <name>". It
// restarts a single command in place and waits until it settles, exiting 0
// when it comes back healthy/done, 1 on error/timeout/interruption, or 3 if
// --timeout elapses first.
func runCommandRestart(args []string) {
	fs := flag.NewFlagSet("command restart", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum time to wait for the command to become healthy")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: env-starter command restart <name>")
		os.Exit(2)
	}
	name := fs.Arg(0)

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

	if err := client.RestartCommand(name); err != nil {
		fmt.Fprintf(os.Stderr, "error: restart command %q: %v\n", name, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Restarting command %q…\n", name)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	timeoutCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	healthy, err := daemon.WaitForCmdSettled(timeoutCtx, client, name)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(1)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "timed out waiting for command %q to become healthy\n", name)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "error waiting for command %q: %v\n", name, err)
		os.Exit(1)
	}

	if healthy {
		fmt.Fprintf(os.Stderr, "Command %q is healthy.\n", name)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "Command %q failed to restart (state: %s).\n", name, client.CmdState(name))
	os.Exit(1)
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
	_, _ = fmt.Fprintf(w, `Usage: env-starter [command] [flags]

Commands:
  (default)  Open the TUI to manage environments
  run        Start an environment and wait for it to be ready
  stop       Stop a running environment
  list       List configured environments
  ps         Show running environments
  command    Manage individual commands (list, restart)
  logs       Print the latest log lines of a command
  allow      Review and approve a config's run/setup/teardown commands
  shutdown   Stop all environments and shut down the daemon
  update     Update env-starter to the latest version
  completion Generate shell completion scripts (bash, zsh, fish)
  help       Show this help message

Flags (default TUI and run):
  --config FILE         Use FILE as the configuration
  --config-overlay FILE Apply FILE as overlay on top of base config

run flags:
  --timeout DURATION    Maximum time to wait for env to start (default: %[1]s)

command restart flags:
  --timeout DURATION    Maximum time to wait for the command to become healthy (default: %[1]s)

list, ps, command list flags:
  --json                 Print as JSON

logs flags:
  --tail N               Number of lines to print (default: 200)

allow flags:
  --yes                  Approve without prompting
  --print                Show the preview and exit without approving
`, 5*time.Minute)
}

// runCompletion implements "env-starter completion <shell>". It writes the
// shell-specific completion script to stdout, suitable for sourcing or installing.
func runCompletion(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: env-starter completion <bash|zsh|fish>")
		os.Exit(2)
	}
	shell := args[0]
	data, err := completionFS.ReadFile("completions/env-starter." + shell)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unknown shell %q: supported shells are bash, zsh, fish\n", shell)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(data)
}

// runComplete implements the hidden "__complete" subcommand invoked by shell
// scripts on every TAB press. It resolves config offline (no daemon), computes
// completion candidates, and prints them followed by a directive line.
// Errors during config loading are silently ignored to degrade gracefully.
func runComplete(words []string) {
	// Scan words for --config / --config-overlay values so the config the user
	// is working with is used as the source for environment names.
	configFile := ""
	configOverlay := ""
	for i, w := range words {
		if w == "--config" && i+1 < len(words) {
			configFile = words[i+1]
		}
		if w == "--config-overlay" && i+1 < len(words) {
			configOverlay = words[i+1]
		}
	}

	var envNames []completion.NameDesc
	var cmdNames []completion.NameDesc
	if cfg, _, _, err := resolveConfig(configFile, configOverlay); err == nil {
		envNames = make([]completion.NameDesc, len(cfg.Environments))
		for i, env := range cfg.Environments {
			envNames[i] = completion.NameDesc{Name: env.Name, Description: env.Description}
		}
		cmdNames = make([]completion.NameDesc, len(cfg.Commands))
		for i, cmd := range cfg.Commands {
			cmdNames[i] = completion.NameDesc{Name: cmd.Name}
		}
	}

	result := completion.Complete(words, envNames, cmdNames...)
	for _, c := range result.Candidates {
		fmt.Println(c)
	}
	fmt.Printf(":%d\n", result.Directive)
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

// configPaths resolves the absolute set of config file paths for the given
// --config/--config-overlay flag values (defaulting the base path when
// empty). Shared by resolveConfig (for hot-reload watching) and the `allow`
// subcommand / trust pre-flight checks so they always agree on exactly which
// files are in scope.
func configPaths(baseFile, overlayFile string) []string {
	basePath := baseFile
	if basePath == "" {
		basePath = defaultConfigPath()
	}
	paths := []string{basePath}
	if overlayFile != "" {
		paths = append(paths, overlayFile)
	}
	return paths
}

// loadMerged loads the base config at basePath and, if overlayFile is set,
// merges it on top. This is the raw load/merge step, with no trust check —
// shared by resolveConfig's gated loadFn and the `allow` subcommand's
// preview, which intentionally bypasses the gate since its whole purpose is
// to let the operator review a config before approving it.
func loadMerged(basePath, overlayFile string) (*config.Config, error) {
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

// resolveConfig loads the configuration from disk and returns:
//   - the initially parsed *config.Config
//   - a loadFn closure that re-runs the same load+merge logic for hot-reload
//   - watchPaths: the set of files to stat for change detection
//   - any error encountered during the initial load
func resolveConfig(baseFile, overlayFile string) (*config.Config, func() (*config.Config, error), []string, error) {
	watchPaths := configPaths(baseFile, overlayFile)
	basePath := watchPaths[0]

	// loadFn re-runs the full resolution (base + optional overlay) using the
	// resolved paths captured here. It is safe to call repeatedly.
	//
	// The trust check runs first and gates every call, including hot-reload:
	// this is the single chokepoint every *config.Config passed to
	// engine.New flows through (see internal/trust and SECURITY.md), so a
	// tampered or unapproved file is refused here before it is ever parsed
	// or executed, on both initial start and hot-reload.
	loadFn := func() (*config.Config, error) {
		if err := trust.Check(watchPaths); err != nil {
			return nil, err
		}
		return loadMerged(basePath, overlayFile)
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

	// Surface non-fatal advisories once, on the initial load only (not on
	// hot-reload via loadFn), so the user sees footguns without blocking startup.
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
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
