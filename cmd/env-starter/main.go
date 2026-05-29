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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/tui"
	"github.com/adericbourg/env-starter/internal/update"
)

// version is the build version, overridden at release time via -ldflags
// "-X main.version=...".
var version = "dev"

func main() {
	// Subcommand dispatch must happen before flag.Parse because stdlib flag
	// does not support subcommands natively.
	if len(os.Args) > 1 && os.Args[1] == "update" {
		runUpdate()
		return
	}

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

	cfg, err := resolveConfig(configFile, configOverlay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	eng, err := engine.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: initialising engine: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// tuiDone receives the error (or nil) from tui.Run once it returns.
	tuiDone := make(chan error, 1)
	go func() {
		tuiDone <- tui.Run(eng)
	}()

	// Wait for either the TUI to finish or a signal to arrive.
	var tuiErr error
	select {
	case tuiErr = <-tuiDone:
		stop() // release signal resources
	case <-ctx.Done():
		stop()
		// Drain: wait for TUI to exit after the signal interrupts it.
		tuiErr = <-tuiDone
	}

	fmt.Fprintln(os.Stderr, "shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	eng.Shutdown(shutdownCtx)

	if tuiErr != nil {
		fmt.Fprintf(os.Stderr, "error: tui: %v\n", tuiErr)
		os.Exit(1)
	}
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

// resolveConfig loads the effective *config.Config from the flag values.
// baseFile is the --config flag; overlayFile is the --config-overlay flag.
// When neither flag is set, the XDG default path is used as the base.
func resolveConfig(baseFile, overlayFile string) (*config.Config, error) {
	basePath := baseFile
	if basePath == "" {
		basePath = defaultConfigPath()
	}

	base, err := config.Load(basePath)
	if err != nil {
		if baseFile == "" && errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"no configuration file found at %s; "+
					"create ~/.config/env-starter/config.yaml or pass --config",
				basePath,
			)
		}
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
