package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/linkscan"
	"github.com/adericbourg/env-starter/internal/openfile"
	"github.com/adericbourg/env-starter/internal/trust"
)

// shutdownCmdColors is the cycling ANSI color palette used to prefix command
// names in the shutdown log panel — the same palette the run subcommand uses,
// so the visual language is consistent across TUI and CLI.
var shutdownCmdColors = []string{
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

// focus tracks which pane currently has keyboard focus.
type focus int

const (
	focusEnvs focus = iota
	focusCmds
	focusLogs
)

// eventMsg wraps an engine.Event so it can travel through Bubble Tea's message bus.
type eventMsg engine.Event

// tickMsg is sent by the periodic refresh ticker.
type tickMsg struct{}

// quitResetMsg is sent once the quit-confirmation window elapses; it clears a
// pending confirmation so a stale first Ctrl+C no longer counts.
type quitResetMsg struct{}

// shutdownDoneMsg is sent once the engine teardown triggered by a confirmed quit
// has finished, allowing the program to stop.
type shutdownDoneMsg struct{}

// noticeResetMsg is sent once the notice display window elapses, clearing the
// transient notice so the normal shortcut bar is shown again.
type noticeResetMsg struct{}

// configScanMsg is sent by the periodic config-file scanner to trigger a check
// for on-disk changes.
type configScanMsg struct{}

// spinnerTickMsg is sent by the dedicated spinner ticker to advance the
// starting/stopping/restarting spinner frame, independently of the slower
// log-refresh tick.
type spinnerTickMsg struct{}

// daemonGoneMsg is sent when the event stream from the daemon closes unexpectedly,
// meaning the daemon was killed or crashed.
type daemonGoneMsg struct{}

// reloadDoneMsg is sent once the background config reload (triggered by pressing
// "c") has completed. err is nil on success.
type reloadDoneMsg struct{ err error }

const tickInterval = 500 * time.Millisecond

// spinnerTickInterval is the spinner's own frame-advance cadence — kept
// separate from tickInterval so a fast, fluid animation doesn't force
// refreshLogView's full log re-wrap to run 3-6x more often.
const spinnerTickInterval = 80 * time.Millisecond

// configScanInterval is how often the config files are stat-ed for changes.
// 2s is sufficient latency for a human-initiated banner; using a dedicated
// ticker (rather than every Nth tickMsg) keeps the two concerns independent.
const configScanInterval = 2 * time.Second

// quitConfirmWindow is how long after a first Ctrl+C a second press still counts
// as confirmation. After it elapses the confirmation is cancelled.
const quitConfirmWindow = 3 * time.Second

// noticeWindow is how long a transient notice (e.g. "opened …") is shown in the
// footer before the normal shortcut bar is restored.
const noticeWindow = 4 * time.Second

// shutdownGrace bounds how long the engine teardown may take before surviving
// processes are force-killed. Mirrors the deadline used on the signal path in main.
const shutdownGrace = 35 * time.Second

// Model is the root Bubble Tea model for the TUI.
type Model struct {
	ctrl Controller

	// version is the build version shown at the bottom right of the default
	// footer. Empty means "no version to show" — set by Run after New.
	version string

	// selection state
	envCursor int
	cmdCursor int
	focused   focus

	// terminal dimensions
	width  int
	height int

	// log viewport
	logView viewport.Model

	// contextLinks holds the URLs extracted from the last contextLinksWindow log
	// lines of every command in the selected environment. It is recomputed by
	// refreshLogView on every render cycle and drives the contextual links overlay.
	contextLinks []linkscan.Link

	// shutdown log panel
	shutdownLogView   viewport.Model
	shutdownLogLines  []string
	shutdownSeenLines map[string]int
	shutdownCmds      []string
	shutdownPrefixes  map[string]string

	// spinner advances on every spinnerTickMsg to animate starting-state indicators
	spinnerFrame int

	// quit flow
	confirmingQuit bool // first Ctrl+C seen; awaiting a confirming second press
	quitting       bool // confirmed: engine teardown in progress, shutdown screen shown
	detaching      bool // Ctrl+D pressed: exit without daemon shutdown

	// transient footer notice (e.g. "opened …" after ^L); cleared by noticeResetMsg
	notice string

	// configDirty is set when the on-disk config differs semantically from the
	// running engine's config. It triggers the sticky reload banner and enables
	// the "c" key. Cleared by a successful Reload.
	configDirty bool

	// reloadErr holds the error message from the last failed reload attempt (only
	// for engine-construction failures; parse errors surface via configParseErr).
	// Appended to the configDirty banner; cleared on a successful reload.
	reloadErr string

	// configParseErr holds a parse error reported by the last config scan. While
	// set, the error banner is shown and the (c) key is blocked. Cleared when the
	// file next loads successfully or a reload succeeds.
	configParseErr string

	// configApproved is false when configParseErr is actually a
	// *trust.NotApprovedError — the file is well-formed but was refused by
	// the trust gate (unapproved, or changed since approval). The footer
	// shows a distinct, actionable banner instead of the generic "cannot be
	// parsed" message, since the file parses fine and just isn't trusted yet.
	configApproved bool

	// openFile opens the given path in the OS default application. Defaults to
	// openfile.Open; swapped out in tests.
	openFile func(string) error

	// envInspector, when non-nil, is the currently open env-inspector overlay.
	// While open it takes over rendering and all key input (see handleKey).
	envInspector *envInspectorState
}

// envInspectorFocus tracks which widget owns keyboard input on the env
// inspector's list screen: the search field or the table.
type envInspectorFocus int

const (
	envInspectorFocusSearch envInspectorFocus = iota
	envInspectorFocusTable
)

// envInspectorScreen tracks which screen the env inspector overlay is
// currently showing.
type envInspectorScreen int

const (
	envInspectorScreenList envInspectorScreen = iota
	envInspectorScreenDetail
)

// envOriginFilter narrows the table to variables won by one source, or
// envOriginFilterAll to show every row.
type envOriginFilter string

const envOriginFilterAll envOriginFilter = "all"

// envInspectorState is the state of the open env-inspector overlay: the
// resolved, provenance-tagged variables for one environment or command, the
// search/origin filters narrowing the table, which row is selected, which
// screen (table list or a row's details) is shown, and whether the details
// screen's value is currently revealed.
//
// Values are masked by default and reveal is scoped to the details screen of
// one row at a time — leaving that screen re-masks — so at most one secret is
// ever shown on screen at once.
type envInspectorState struct {
	title        string // e.g. `environment "dev"` or `command "api"`
	allVars      []engine.ResolvedEnvVar
	search       textinput.Model
	originFilter envOriginFilter
	focus        envInspectorFocus
	screen       envInspectorScreen
	cursor       int                   // index into filteredVars()
	scroll       int                   // index of the first visible table row
	detail       engine.ResolvedEnvVar // snapshot of the row shown on the details screen
	revealed     bool
}

// filteredVars returns allVars narrowed by the origin filter and a
// case-insensitive "key contains" search. Values are never searched.
func (insp *envInspectorState) filteredVars() []engine.ResolvedEnvVar {
	query := strings.ToLower(insp.search.Value())
	out := make([]engine.ResolvedEnvVar, 0, len(insp.allVars))
	for _, v := range insp.allVars {
		if insp.originFilter != envOriginFilterAll && envOriginFilter(v.Winning.Source) != insp.originFilter {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(v.Key), query) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// selectedRow returns the row at cursor within the current filtered set.
func (insp *envInspectorState) selectedRow() (engine.ResolvedEnvVar, bool) {
	vars := insp.filteredVars()
	if insp.cursor < 0 || insp.cursor >= len(vars) {
		return engine.ResolvedEnvVar{}, false
	}
	return vars[insp.cursor], true
}

// New creates a TUI model driven by the given controller.
func New(ctrl Controller) Model {
	return Model{
		ctrl:            ctrl,
		logView:         viewport.New(),
		shutdownLogView: viewport.New(),
		openFile:        openfile.Open,
		configApproved:  true,
	}
}

// Init returns the initial commands: start listening for engine events, arm
// the periodic refresh ticker, arm the config-file scanner, and arm the
// spinner ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.ctrl), tickCmd(), configScanCmd(), spinnerTickCmd())
}

// waitForEvent returns a Cmd that blocks until the next engine event, then
// delivers it as an eventMsg. The caller re-arms this command in Update so
// that every subsequent event is also delivered.
func waitForEvent(ctrl Controller) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ctrl.Events()
		if !ok {
			return daemonGoneMsg{}
		}
		return eventMsg(ev)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(spinnerTickInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// quitResetCmd fires quitResetMsg after the confirmation window, cancelling a
// pending first-Ctrl+C if the user did not follow up.
func quitResetCmd() tea.Cmd {
	return tea.Tick(quitConfirmWindow, func(time.Time) tea.Msg {
		return quitResetMsg{}
	})
}

// noticeResetCmd fires noticeResetMsg after noticeWindow, restoring the normal
// shortcut bar once the transient notice has been shown long enough.
func noticeResetCmd() tea.Cmd {
	return tea.Tick(noticeWindow, func(time.Time) tea.Msg {
		return noticeResetMsg{}
	})
}

func configScanCmd() tea.Cmd {
	return tea.Tick(configScanInterval, func(time.Time) tea.Msg {
		return configScanMsg{}
	})
}

// reloadCmd runs the full config reload (Shutdown → re-load → engine.New →
// swap) in the background and delivers the outcome as a reloadDoneMsg.
func reloadCmd(ctrl Controller) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return reloadDoneMsg{err: ctrl.Reload(ctx)}
	}
}

// shutdownCmd runs the engine teardown and then signals that it has finished.
// It runs inside the Bubble Tea event loop so the "shutting down" screen stays
// visible until teardown completes.
func shutdownCmd(ctrl Controller) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		ctrl.Shutdown(ctx)
		return shutdownDoneMsg{}
	}
}

// Update handles all incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.resizePanes()
		m = m.refreshLogView()
		return m, nil

	case tickMsg:
		m = m.refreshLogView()
		if m.quitting {
			m = m.refreshShutdownLogs()
		}
		return m, tickCmd()

	case spinnerTickMsg:
		m.spinnerFrame++
		return m, spinnerTickCmd()

	case quitResetMsg:
		m.confirmingQuit = false
		return m, nil

	case noticeResetMsg:
		m.notice = ""
		return m, nil

	case configScanMsg:
		dirty, parseErr := m.ctrl.ConfigChanged()
		m.configDirty = dirty
		if parseErr != nil {
			m.configParseErr = parseErr.Error()
			m.configApproved = !errors.As(parseErr, new(*trust.NotApprovedError))
			m.reloadErr = "" // parse error supersedes any prior reload error
		} else {
			m.configParseErr = ""
			m.configApproved = true
		}
		return m, configScanCmd()

	case reloadDoneMsg:
		if msg.err != nil {
			m.reloadErr = msg.err.Error()
			// configDirty stays true so the banner remains and shows the error.
			return m, nil
		}
		m.configDirty = false
		m.reloadErr = ""
		m.configParseErr = ""
		m.configApproved = true
		m = m.clampCursors()
		m = m.refreshLogView()
		// Re-arm event listening against the new engine's channel.
		return m, waitForEvent(m.ctrl)

	case shutdownDoneMsg:
		return m, tea.Quit

	case daemonGoneMsg:
		// The daemon was killed externally. Show a message and exit.
		m.notice = "Daemon stopped — press any key to exit"
		return m, tea.Quit

	case eventMsg:
		// Re-arm so the next engine event is also delivered.
		m = m.refreshLogView()
		return m, waitForEvent(m.ctrl)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While the shutdown screen is shown, all input is ignored.
	if m.quitting {
		return m, nil
	}

	// While the env inspector overlay is open, it owns all key input.
	if m.envInspector != nil {
		return m.handleEnvInspectorKey(msg)
	}

	if msg.String() == "ctrl+d" {
		m.detaching = true
		m.ctrl.Detach()
		return m, tea.Quit
	}

	if msg.String() == "ctrl+c" {
		if m.confirmingQuit {
			// Second Ctrl+C within the window: start engine teardown.
			m.confirmingQuit = false
			m.quitting = true
			m = m.initShutdownLog()
			return m, shutdownCmd(m.ctrl)
		}
		// First Ctrl+C: arm the confirmation window; do NOT touch any environment.
		m.confirmingQuit = true
		return m, quitResetCmd()
	}

	// Any key other than Ctrl+C cancels a pending confirmation.
	m.confirmingQuit = false

	// ^L opens the selected command's log file in the OS default app.
	if msg.String() == "ctrl+l" {
		return m.openSelectedLog()
	}

	switch msg.String() {
	case "tab", "right":
		switch m.focused {
		case focusEnvs:
			m.focused = focusCmds
		case focusCmds:
			m.focused = focusLogs
		default:
			m.focused = focusEnvs
		}
		m = m.refreshLogView()

	case "left":
		switch m.focused {
		case focusCmds:
			m.focused = focusEnvs
		case focusLogs:
			m.focused = focusCmds
		default:
			m.focused = focusEnvs
		}

	case "up", "k":
		m = m.moveCursorUp()

	case "down", "j":
		m = m.moveCursorDown()

	case "s":
		if m.canStartSelectedEnv() {
			_ = m.ctrl.StartEnvironment(m.selectedEnvName())
		}

	case "x":
		if m.canStopSelectedEnv() {
			_ = m.ctrl.StopEnvironment(m.selectedEnvName())
		}

	case "r":
		if m.focused == focusLogs {
			m = m.refreshLogView()
		}

	case "R":
		if m.canRestartSelectedCommand() {
			cmd := m.selectedCommand()
			_ = m.ctrl.RestartCommand(cmd)
			m.notice = fmt.Sprintf("Restarting %s…", cmd)
			return m, noticeResetCmd()
		}

	case "e":
		if !m.canOpenEnvInspector() {
			break
		}
		switch m.focused {
		case focusEnvs:
			return m.openEnvInspector("", m.selectedEnvName())
		case focusCmds, focusLogs:
			return m.openEnvInspector(m.selectedCommand(), "")
		}

	case "c":
		if !m.configDirty || m.configParseErr != "" {
			// "c" is inert when no valid change is pending (or file is unparseable).
			break
		}
		m.notice = "Reloading config…"
		return m, reloadCmd(m.ctrl)
	}

	// Forward scroll keys to viewport when logs are focused.
	if m.focused == focusLogs {
		var cmd tea.Cmd
		m.logView, cmd = m.logView.Update(msg)
		return m, cmd
	}

	return m, nil
}

// envInspectorChromeLines approximates the number of non-row lines rendered
// around the table on the list screen (title, facets, search box with its
// border, table header/separator, footer) so the visible row window can be
// sized to the terminal height.
const envInspectorChromeLines = 12

// envInspectorVisibleRows returns how many table rows fit in the current
// terminal height, floored so a very short terminal still shows something.
func (m Model) envInspectorVisibleRows() int {
	rows := m.height - envInspectorChromeLines
	if rows < 3 {
		rows = 3
	}
	return rows
}

// envInspectorDetailMinWidth and envInspectorDetailMaxWidth clamp the details
// screen to a comfortably-sized fixed panel — wide enough for most values,
// but not stretched edge-to-edge on a wide terminal.
const (
	envInspectorDetailMinWidth = 40
	envInspectorDetailMaxWidth = 90
)

// envInspectorDetailWidth returns the details screen's fixed content width.
// Rendering at a fixed width (rather than letting the block size itself to
// the value) keeps the block's horizontal position constant when Space
// toggles between the short masked placeholder and a much longer real
// value — a long value wraps onto extra lines instead of widening the block
// and shifting it sideways.
func (m Model) envInspectorDetailWidth() int {
	w := m.width - 16
	if w > envInspectorDetailMaxWidth {
		w = envInspectorDetailMaxWidth
	}
	if w < envInspectorDetailMinWidth {
		w = envInspectorDetailMinWidth
	}
	return w
}

// clampEnvInspectorList keeps cursor within the current filtered set and
// scroll within a window that keeps cursor visible.
func (m Model) clampEnvInspectorList(insp *envInspectorState) {
	if n := len(insp.filteredVars()); insp.cursor >= n {
		insp.cursor = n - 1
	}
	if insp.cursor < 0 {
		insp.cursor = 0
	}
	visible := m.envInspectorVisibleRows()
	if insp.cursor < insp.scroll {
		insp.scroll = insp.cursor
	}
	if insp.cursor >= insp.scroll+visible {
		insp.scroll = insp.cursor - visible + 1
	}
	if insp.scroll < 0 {
		insp.scroll = 0
	}
}

// openEnvInspector resolves the env variables for envName (an environment) or
// command (a command) — exactly one should be non-empty — and opens the
// inspector overlay on them, selecting the first table row.
func (m Model) openEnvInspector(command, envName string) (tea.Model, tea.Cmd) {
	title := fmt.Sprintf("environment %q", envName)
	if command != "" {
		title = fmt.Sprintf("command %q", command)
	}

	searchWidth := m.width - 8
	if searchWidth < 10 {
		searchWidth = 10
	}
	search := textinput.New()
	search.Placeholder = "search by key…"
	search.SetWidth(searchWidth)

	m.envInspector = &envInspectorState{
		title:        title,
		allVars:      m.ctrl.ResolveEnv(envName, command),
		search:       search,
		originFilter: envOriginFilterAll,
		focus:        envInspectorFocusTable,
	}
	return m, nil
}

// handleEnvInspectorKey handles all key input while the env inspector overlay
// is open. F5–F8 change the origin filter regardless of screen or focus;
// everything else is dispatched to the current screen's handler.
func (m Model) handleEnvInspectorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "f5":
		return m.setEnvInspectorOriginFilter(envOriginFilterAll)
	case "f6":
		return m.setEnvInspectorOriginFilter(envOriginFilter(engine.EnvSourceOS))
	case "f7":
		return m.setEnvInspectorOriginFilter(envOriginFilter(engine.EnvSourceEnvironment))
	case "f8":
		return m.setEnvInspectorOriginFilter(envOriginFilter(engine.EnvSourceCommand))
	}

	if m.envInspector.screen == envInspectorScreenDetail {
		return m.handleEnvInspectorDetailKey(msg)
	}
	return m.handleEnvInspectorListKey(msg)
}

// setEnvInspectorOriginFilter applies an origin filter, resets the selection
// to the top of the newly-filtered set, and re-clamps scrolling.
func (m Model) setEnvInspectorOriginFilter(f envOriginFilter) (tea.Model, tea.Cmd) {
	insp := *m.envInspector
	insp.originFilter = f
	insp.cursor = 0
	insp.scroll = 0
	m.envInspector = &insp
	return m, nil
}

// handleEnvInspectorListKey handles key input on the table/search list
// screen. Esc always closes the overlay; q/e only close it while the table
// (not the search field) has focus, so those letters remain typeable in a
// search query.
func (m Model) handleEnvInspectorListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		m.envInspector = nil
		return m, nil
	}

	insp := *m.envInspector

	if insp.focus == envInspectorFocusSearch {
		switch msg.String() {
		case "down":
			insp.focus = envInspectorFocusTable
			insp.search.Blur()
			insp.cursor = 0
			m.clampEnvInspectorList(&insp)
			m.envInspector = &insp
			return m, nil
		case "enter":
			// No row is "current" while typing; enter has no effect here.
			return m, nil
		}
		var cmd tea.Cmd
		insp.search, cmd = insp.search.Update(msg)
		insp.cursor = 0
		m.clampEnvInspectorList(&insp)
		m.envInspector = &insp
		return m, cmd
	}

	switch msg.String() {
	case "q", "e":
		m.envInspector = nil
		return m, nil
	case "up":
		if insp.cursor == 0 {
			insp.focus = envInspectorFocusSearch
			insp.search.Focus()
		} else {
			insp.cursor--
		}
	case "down":
		if insp.cursor < len(insp.filteredVars())-1 {
			insp.cursor++
		}
	case "enter":
		if v, ok := insp.selectedRow(); ok {
			insp.screen = envInspectorScreenDetail
			insp.detail = v
			insp.revealed = false
		}
	}
	m.clampEnvInspectorList(&insp)
	m.envInspector = &insp
	return m, nil
}

// handleEnvInspectorDetailKey handles key input on the details screen: space
// toggles revealing the value (and everything it overrides); esc/q/e return
// to the list screen with search/filter/selection intact.
func (m Model) handleEnvInspectorDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	insp := *m.envInspector
	switch msg.String() {
	case "esc", "q", "e":
		insp.screen = envInspectorScreenList
		insp.revealed = false
		m.clampEnvInspectorList(&insp)
	case "space":
		insp.revealed = !insp.revealed
	}
	m.envInspector = &insp
	return m, nil
}

// openSelectedLog opens the currently selected command's log file in the OS
// default application. It is a no-op when the logs panel is not focused or no
// command is selected. A transient notice is shown in the footer to confirm the
// outcome.
func (m Model) openSelectedLog() (tea.Model, tea.Cmd) {
	if !m.canOpenSelectedLog() {
		return m, nil
	}
	cmd := m.selectedCommand()
	path := m.ctrl.LogPath(cmd)
	if err := m.openFile(path); err != nil {
		m.notice = fmt.Sprintf("could not open log: %s", err)
	} else {
		m.notice = fmt.Sprintf("opened %s", path)
	}
	return m, noticeResetCmd()
}

func (m Model) moveCursorUp() Model {
	switch m.focused {
	case focusEnvs:
		if m.envCursor > 0 {
			m.envCursor--
			m.cmdCursor = 0
		}
		m = m.refreshLogView()
	case focusCmds:
		if m.cmdCursor > 0 {
			m.cmdCursor--
		}
		m = m.refreshLogView()
	case focusLogs:
		m.logView.ScrollUp(1)
	}
	return m
}

func (m Model) moveCursorDown() Model {
	switch m.focused {
	case focusEnvs:
		envs := m.ctrl.Environments()
		if m.envCursor < len(envs)-1 {
			m.envCursor++
			m.cmdCursor = 0
		}
		m = m.refreshLogView()
	case focusCmds:
		cmds := m.selectedEnvCommands()
		if m.cmdCursor < len(cmds)-1 {
			m.cmdCursor++
		}
		m = m.refreshLogView()
	case focusLogs:
		m.logView.ScrollDown(1)
	}
	return m
}

// selectedEnvName returns the name of the currently selected environment, or
// an empty string when the list is empty.
func (m Model) selectedEnvName() string {
	envs := m.ctrl.Environments()
	if len(envs) == 0 || m.envCursor >= len(envs) {
		return ""
	}
	return envs[m.envCursor].Name
}

// selectedEnvCommands returns the workflow commands for the selected env.
func (m Model) selectedEnvCommands() []string {
	name := m.selectedEnvName()
	if name == "" {
		return nil
	}
	return m.ctrl.WorkflowCommands(name)
}

// selectedCommand returns the name of the currently selected command.
func (m Model) selectedCommand() string {
	cmds := m.selectedEnvCommands()
	if len(cmds) == 0 || m.cmdCursor >= len(cmds) {
		return ""
	}
	return cmds[m.cmdCursor]
}

// canStartSelectedEnv reports whether "s" would have an effect on the
// currently selected environment.
func (m Model) canStartSelectedEnv() bool {
	if m.focused != focusEnvs {
		return false
	}
	name := m.selectedEnvName()
	if name == "" {
		return false
	}
	switch m.ctrl.EnvState(name) {
	case engine.EnvRunning, engine.EnvStarting:
		return false
	default:
		return true
	}
}

// canStopSelectedEnv reports whether "x" would have an effect on the
// currently selected environment.
func (m Model) canStopSelectedEnv() bool {
	if m.focused != focusEnvs {
		return false
	}
	name := m.selectedEnvName()
	if name == "" {
		return false
	}
	switch m.ctrl.EnvState(name) {
	case engine.EnvStopped, engine.EnvStopping:
		return false
	default:
		return true
	}
}

// canRestartSelectedCommand reports whether "R" would have an effect on the
// currently selected command. Mirrors Engine.RestartCommand's holder check:
// CmdPending (never started) and CmdStopped (holders released) are the states
// where the command has no holders and a restart is rejected.
func (m Model) canRestartSelectedCommand() bool {
	if m.focused != focusCmds {
		return false
	}
	cmd := m.selectedCommand()
	if cmd == "" {
		return false
	}
	switch m.ctrl.CmdState(cmd) {
	case engine.CmdPending, engine.CmdStopped:
		return false
	default:
		return true
	}
}

// canOpenSelectedLog reports whether "^L" would have an effect on the
// currently selected command's log file.
func (m Model) canOpenSelectedLog() bool {
	if m.focused != focusLogs {
		return false
	}
	cmd := m.selectedCommand()
	if cmd == "" {
		return false
	}
	return m.ctrl.LogPath(cmd) != ""
}

// canOpenEnvInspector reports whether "e" would have an effect given the
// currently focused pane and selection.
func (m Model) canOpenEnvInspector() bool {
	switch m.focused {
	case focusEnvs:
		return m.selectedEnvName() != ""
	case focusCmds, focusLogs:
		return m.selectedCommand() != ""
	default:
		return false
	}
}

// clampCursors ensures envCursor and cmdCursor remain within the bounds of the
// current environment and command lists. This is called after a reload, where
// the new config may have fewer entries than the old one.
func (m Model) clampCursors() Model {
	envs := m.ctrl.Environments()
	if m.envCursor >= len(envs) {
		m.envCursor = max(len(envs)-1, 0)
	}
	cmds := m.selectedEnvCommands()
	if m.cmdCursor >= len(cmds) {
		m.cmdCursor = max(len(cmds)-1, 0)
	}
	return m
}

// logsTitle builds the header shown above the logs viewport, identifying which
// environment and command the logs belong to.
func (m Model) logsTitle() string {
	env := m.selectedEnvName()
	cmd := m.selectedCommand()
	switch {
	case env != "" && cmd != "":
		return fmt.Sprintf("%s > %s", env, cmd)
	case env != "":
		return env
	default:
		return "logs"
	}
}

// contextLinksWindow is the number of trailing log lines (per command) scanned
// for URLs to surface in the contextual links overlay.
const contextLinksWindow = 5

// logAreaHeight returns the number of rows available inside the logs pane for
// the viewport plus any contextual-links overlay combined. It accounts for the
// pane border, title row, and path footer but not the overlay itself.
func (m Model) logAreaHeight() int {
	topHeight := m.height / 2
	h := m.height - topHeight - footerHeight - 2 - 1 - logPathHeight
	if h < 1 {
		return 1
	}
	return h
}

// linksBlockHeight returns the number of rows the contextual-links overlay will
// occupy: 0 when there are no links, or len(links)+3 (border top, title row,
// one row per link, border bottom) otherwise.
func linksBlockHeight(links []linkscan.Link) int {
	if len(links) == 0 {
		return 0
	}
	return len(links) + 3
}

// collectContextLinks scans the last contextLinksWindow log lines of each
// command in the selected environment and returns the deduplicated set of URLs,
// each tagged with the command that first emitted them.
func (m Model) collectContextLinks() []linkscan.Link {
	cmds := m.selectedEnvCommands()
	var tagged []linkscan.TaggedLine
	for _, cmd := range cmds {
		lines := m.ctrl.Logs(cmd)
		start := len(lines) - contextLinksWindow
		if start < 0 {
			start = 0
		}
		for _, line := range lines[start:] {
			tagged = append(tagged, linkscan.TaggedLine{Command: cmd, Text: line})
		}
	}
	return linkscan.Collect(tagged)
}

// refreshLogView populates the viewport with the latest log lines for the
// currently selected command. Each line is pre-wrapped to the viewport width so
// the viewport never truncates them.
func (m Model) refreshLogView() Model {
	// Recompute the contextual-links overlay for all commands in the env, then
	// shrink the viewport by the overlay height so the pane total stays constant.
	m.contextLinks = m.collectContextLinks()
	viewportHeight := m.logAreaHeight() - linksBlockHeight(m.contextLinks)
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	m.logView.SetHeight(viewportHeight)

	cmd := m.selectedCommand()
	if cmd == "" {
		m.logView.SetContent("")
		return m
	}
	lines := m.ctrl.Logs(cmd)
	width := m.logView.Width()
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, wrapLogLine(line, width))
	}
	m.logView.SetContent(strings.Join(wrapped, "\n"))
	m.logView.GotoBottom()
	return m
}

// logWrapIndent is the number of spaces prepended to continuation rows of a
// wrapped log line, so wrapped segments are visually distinct from new lines.
const logWrapIndent = 2

// wrapLogLine wraps a single log line to the given width, applying a hanging
// indent (logWrapIndent spaces) to every continuation row. ANSI escape codes
// and wide characters are preserved. Returns the line unchanged when there is
// not enough width to wrap.
func wrapLogLine(line string, width int) string {
	if width <= logWrapIndent {
		return line
	}
	wrapped := ansi.Wrap(line, width-logWrapIndent, "")
	rows := strings.Split(wrapped, "\n")
	for i := 1; i < len(rows); i++ {
		rows[i] = strings.Repeat(" ", logWrapIndent) + rows[i]
	}
	return strings.Join(rows, "\n")
}

// resizePanes recalculates the viewport size to fit the terminal.
func (m Model) resizePanes() Model {
	// Layout: top half split into env+cmd panes, bottom half is logs.
	// logAreaHeight() provides the viewport+overlay budget; subtract the
	// current overlay height to get the viewport's share. refreshLogView
	// recomputes this after every event, so these initial values converge
	// immediately once the first tick fires.
	logHeight := m.logAreaHeight() - linksBlockHeight(m.contextLinks)
	if logHeight < 1 {
		logHeight = 1
	}
	logWidth := m.width - 2 // 2 for border
	if logWidth < 1 {
		logWidth = 1
	}
	m.logView.SetWidth(logWidth)
	m.logView.SetHeight(logHeight)

	// Shutdown log panel: bottom half of the shutdown screen, minus border.
	shutdownLogPanelHeight := m.height / 2
	shutdownLogWidth := m.width - 2
	if shutdownLogWidth < 1 {
		shutdownLogWidth = 1
	}
	shutdownLogHeight := shutdownLogPanelHeight - 2
	if shutdownLogHeight < 1 {
		shutdownLogHeight = 1
	}
	m.shutdownLogView.SetWidth(shutdownLogWidth)
	m.shutdownLogView.SetHeight(shutdownLogHeight)
	return m
}

// footerHeight is the number of terminal rows occupied by the footer bar.
const footerHeight = 1

// logPathHeight is the number of rows reserved inside the logs pane for the
// on-disk path line shown below the viewport.
const logPathHeight = 1

// View satisfies tea.Model and declares the full TUI as a tea.View. Alt-screen
// mode is declared here (the v2 way) instead of via tea.WithAltScreen() in
// NewProgram. Rendering itself is delegated to render().
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render returns the full TUI as a plain string; it is the rendering
// implementation called by View() and directly by tests (same package).
func (m Model) render() string {
	if m.width == 0 {
		return "initialising…\n"
	}

	if m.quitting {
		return m.renderShutdown()
	}

	if m.envInspector != nil {
		return m.renderEnvInspector()
	}

	top := m.renderTopRow()
	logs := m.renderLogsPane()
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, top, logs, footer)
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	borderNormal = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	borderFocused = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("69"))

	selectedLine = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	// shortcutStyle renders a legend entry whose shortcut can act on the current
	// selection.
	shortcutStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	// shortcutDisabledStyle renders a legend entry whose shortcut would be a
	// no-op given the current selection — dimmer than shortcutStyle so it reads
	// as unavailable rather than broken.
	shortcutDisabledStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))

	// quitConfirmStyle renders the "Press Ctrl+C again to quit" hint in amber so
	// it stands out clearly from the normal shortcut bar.
	quitConfirmStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("214"))

	// configDirtyStyle renders the config-change banner in the same amber as the
	// quit confirmation so it is visually prominent.
	configDirtyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("214"))

	// shutdownStyle is used for the full-screen "env shutting down…" message.
	shutdownStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("214"))

	// logsTitleStyle renders the "Env > Command" header inside the logs pane.
	logsTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	// envFacetKeyStyle dims an origin-facet button's function-key hint (e.g.
	// "F6") so it reads as a shortcut rather than part of the label.
	envFacetKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("241"))

	// envFacetSelectedStyle highlights the currently active origin facet.
	// Color 39 is distinct from both the table's selected-row color (212)
	// and the warning/danger color (214/9) used elsewhere.
	envFacetSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39"))
)

// ── Render helpers ────────────────────────────────────────────────────────────

func (m Model) renderTopRow() string {
	topHeight := m.height / 2
	envWidth := m.width / 3
	cmdWidth := m.width - envWidth

	envPane := m.renderEnvPane(envWidth-2, topHeight-2)
	cmdPane := m.renderCmdPane(cmdWidth-2, topHeight-2)

	envBox := paneStyle(m.focused == focusEnvs).
		Width(envWidth - 2).
		Height(topHeight - 2).
		Render(envPane)

	cmdBox := paneStyle(m.focused == focusCmds).
		Width(cmdWidth - 2).
		Height(topHeight - 2).
		Render(cmdPane)

	return lipgloss.JoinHorizontal(lipgloss.Top, envBox, cmdBox)
}

func paneStyle(focused bool) lipgloss.Style {
	if focused {
		return borderFocused
	}
	return borderNormal
}

func (m Model) renderEnvPane(width, height int) string {
	_ = width
	_ = height
	envs := m.ctrl.Environments()
	var b strings.Builder
	for i, env := range envs {
		state := m.ctrl.EnvState(env.Name)
		indicator := envStateIndicator(state, m.spinnerFrame)
		line := fmt.Sprintf("%s %s", indicator, env.Name)
		if i == m.envCursor && m.focused == focusEnvs {
			line = selectedLine.Render("> " + line)
		} else if i == m.envCursor {
			line = "> " + line
		} else {
			line = "  " + line
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

func (m Model) renderCmdPane(width, height int) string {
	_ = width
	_ = height
	cmds := m.selectedEnvCommands()
	var b strings.Builder
	for i, cmd := range cmds {
		state := m.ctrl.CmdState(cmd)
		indicator := cmdStateIndicator(state, m.spinnerFrame)
		retryAttempts, retryMax := m.ctrl.CmdRetries(cmd)
		label := cmd + cmdRetrySuffix(state, retryAttempts, retryMax) + cmdUnmanagedSuffix(m.ctrl.IsUnmanaged(cmd))
		line := fmt.Sprintf("%s %s", indicator, label)
		if i == m.cmdCursor && m.focused == focusCmds {
			line = selectedLine.Render("> " + line)
		} else if i == m.cmdCursor {
			line = "> " + line
		} else {
			line = "  " + line
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

func (m Model) renderLogsPane() string {
	title := logsTitleStyle.Render(m.logsTitle())
	pathLine := m.renderLogPath()
	parts := []string{title, m.logView.View()}
	if block := m.renderContextLinks(); block != "" {
		parts = append(parts, block)
	}
	parts = append(parts, pathLine)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	style := paneStyle(m.focused == focusLogs).
		Width(m.width - 2)
	return style.Render(content)
}

// renderContextLinks renders the contextual-links block shown at the bottom of
// the logs pane (above the path line) using the same bordered panel style as
// every other pane. Returns an empty string when there are no links to display.
func (m Model) renderContextLinks() string {
	if len(m.contextLinks) == 0 {
		return ""
	}

	// Content width inside this block's own border: subtract the log pane
	// border (2) and this block's border (2).
	contentWidth := m.width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	title := logsTitleStyle.Render("Contextual links")

	// Find the widest "[cmd]" label so all rows can be right-aligned consistently.
	maxLabelLen := 0
	for _, link := range m.contextLinks {
		if l := len("[" + link.Command + "]"); l > maxLabelLen {
			maxLabelLen = l
		}
	}

	// Build one row per link: right-aligned padded label + two spaces + clickable URL.
	rows := make([]string, 0, len(m.contextLinks))
	for i, link := range m.contextLinks {
		label := "[" + link.Command + "]"
		pad := strings.Repeat(" ", maxLabelLen-len(label))

		color := shutdownCmdColors[i%len(shutdownCmdColors)]
		coloredLabel := color + pad + label + ansiReset

		urlWidth := contentWidth - maxLabelLen - 2
		if urlWidth < 1 {
			urlWidth = 1
		}
		displayURL := ansi.Truncate(link.URL, urlWidth, "…")
		// Wrap in an OSC 8 hyperlink: visible text is truncated for display but
		// the full URL remains the clickable target.
		clickable := ansi.SetHyperlink(link.URL) + displayURL + ansi.ResetHyperlink()

		rows = append(rows, coloredLabel+"  "+clickable)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
	return borderNormal.Width(contentWidth).Render(content)
}

// renderLogPath renders the one-row on-disk path hint shown at the bottom of
// the logs pane. Returns an empty string when no command is selected.
func (m Model) renderLogPath() string {
	cmd := m.selectedCommand()
	if cmd == "" {
		return ""
	}
	path := m.ctrl.LogPath(cmd)
	label := path + "  (^L to open)"
	innerWidth := m.width - 2 // 2 for pane border
	label = ansi.Truncate(label, innerWidth, "…")
	return footerStyle.Render(label)
}

func (m Model) renderFooter() string {
	if m.confirmingQuit {
		return quitConfirmStyle.Render("Press ^C again to shutdown daemon")
	}
	if !m.configApproved {
		return configDirtyStyle.Render("Updated configuration is not approved — run `env-starter allow` to review and apply it")
	}
	if m.configParseErr != "" {
		return configDirtyStyle.Render("Updated configuration cannot be parsed — fix errors to reload")
	}
	if m.configDirty {
		msg := "Configuration file has been updated. Press (c) to reload config"
		if m.reloadErr != "" {
			msg += "  (reload failed: " + m.reloadErr + ")"
		}
		return configDirtyStyle.Render(msg)
	}
	if m.notice != "" {
		return footerStyle.Render(m.notice)
	}
	// Not wrapped in footerStyle: shortcutsLegend already styles each entry
	// individually, and re-wrapping the already-styled string would nest ANSI
	// sequences instead of coloring it uniformly.
	return m.footerLine()
}

// footerLine returns the shortcuts legend, right-padded with the build
// version when one is set and it fits within the terminal width.
func (m Model) footerLine() string {
	legend := m.shortcutsLegend()
	if m.version == "" {
		return legend
	}
	versionLabel := footerStyle.Render("v" + m.version)
	pad := m.width - lipgloss.Width(legend) - lipgloss.Width(versionLabel)
	if pad < 1 {
		return legend
	}
	return legend + strings.Repeat(" ", pad) + versionLabel
}

// shortcut is one entry in the footer's shortcut legend.
type shortcut struct {
	label     string // e.g. "R restart"
	available bool   // whether the key would currently have an effect
}

// shortcutEntries builds the footer's shortcut hints for the currently
// focused pane — shortcuts whose behavior is scoped to one pane are only
// listed while that pane is focused. Each entry also carries whether it would
// currently be a no-op, so shortcutsLegend can dim it accordingly.
func (m Model) shortcutEntries() []shortcut {
	entries := []shortcut{
		{label: "↑/↓ move", available: true},
		{label: "tab/←/→ focus", available: true},
	}
	if m.focused == focusEnvs {
		entries = append(entries,
			shortcut{label: "s start", available: m.canStartSelectedEnv()},
			shortcut{label: "x stop", available: m.canStopSelectedEnv()},
		)
	}
	if m.focused == focusCmds {
		entries = append(entries, shortcut{label: "R restart", available: m.canRestartSelectedCommand()})
	}
	if m.focused == focusLogs {
		entries = append(entries,
			shortcut{label: "r refresh logs", available: true},
			shortcut{label: "^L open", available: m.canOpenSelectedLog()},
		)
	}
	entries = append(entries, shortcut{label: "e env inspector", available: m.canOpenEnvInspector()})
	entries = append(entries,
		shortcut{label: "^D detach", available: true},
		shortcut{label: "^C shutdown", available: true},
	)
	return entries
}

// shortcutsLegend renders shortcutEntries, styling each entry by availability.
func (m Model) shortcutsLegend() string {
	entries := m.shortcutEntries()
	parts := make([]string, len(entries))
	for i, e := range entries {
		style := shortcutStyle
		if !e.available {
			style = shortcutDisabledStyle
		}
		parts[i] = style.Render(e.label)
	}
	return strings.Join(parts, "  ")
}

// initShutdownLog collects the ordered, deduplicated set of commands across all
// environments and builds a colored "[name]" prefix for each one — the same
// format the run subcommand uses. Called once when quitting begins.
func (m Model) initShutdownLog() Model {
	seen := make(map[string]bool)
	var cmds []string
	for _, env := range m.ctrl.Environments() {
		for _, cmd := range m.ctrl.WorkflowCommands(env.Name) {
			if !seen[cmd] {
				seen[cmd] = true
				cmds = append(cmds, cmd)
			}
		}
	}

	prefixes := make(map[string]string, len(cmds))
	for i, cmd := range cmds {
		color := shutdownCmdColors[i%len(shutdownCmdColors)]
		prefixes[cmd] = color + "[" + cmd + "]" + ansiReset
	}

	m.shutdownCmds = cmds
	m.shutdownPrefixes = prefixes
	m.shutdownSeenLines = make(map[string]int, len(cmds))
	return m
}

// refreshShutdownLogs polls each command's ring buffer for new lines and
// appends them (with their colored prefix) to shutdownLogLines. It then
// rebuilds the viewport content and scrolls to the bottom.
func (m Model) refreshShutdownLogs() Model {
	if len(m.shutdownCmds) == 0 {
		return m
	}
	for _, cmd := range m.shutdownCmds {
		lines := m.ctrl.Logs(cmd)
		start := m.shutdownSeenLines[cmd]
		prefix := m.shutdownPrefixes[cmd]
		for _, line := range lines[start:] {
			m.shutdownLogLines = append(m.shutdownLogLines, prefix+" "+line)
		}
		m.shutdownSeenLines[cmd] = len(lines)
	}

	width := m.shutdownLogView.Width()
	wrapped := make([]string, 0, len(m.shutdownLogLines))
	for _, line := range m.shutdownLogLines {
		wrapped = append(wrapped, wrapLogLine(line, width))
	}
	m.shutdownLogView.SetContent(strings.Join(wrapped, "\n"))
	m.shutdownLogView.GotoBottom()
	return m
}

// renderShutdownLogPanel renders the accumulated shutdown log lines in a
// bordered viewport occupying the bottom half of the shutdown screen.
func (m Model) renderShutdownLogPanel() string {
	return borderNormal.
		Width(m.width - 2).
		Render(m.shutdownLogView.View())
}

// renderShutdown shows a full-screen "Env shutting down" notice while the
// engine tears down running commands. It lists each stopping command grouped
// by the environments that reference it, with a braille spinner and an
// elapsed/grace countdown toward the SIGKILL deadline.
func (m Model) renderShutdown() string {
	title := shutdownStyle.Render("Env shutting down")

	// Index stopping commands by name for O(1) lookup.
	stopping := m.ctrl.StoppingCommands()
	cmdMap := make(map[string]engine.StoppingCommand, len(stopping))
	for _, sc := range stopping {
		cmdMap[sc.Command] = sc
	}

	// Build one block per env that still has stopping commands.
	type envBlock struct {
		name    string
		elapsed int
		grace   int
		cmds    []string
	}
	var blocks []envBlock
	for _, env := range m.ctrl.Environments() {
		if m.ctrl.EnvState(env.Name) == engine.EnvStopped {
			continue
		}
		var stoppingCmds []string
		maxElapsed, grace := 0, 30
		for _, cmd := range m.ctrl.WorkflowCommands(env.Name) {
			sc, ok := cmdMap[cmd]
			if !ok {
				continue
			}
			stoppingCmds = append(stoppingCmds, cmd)
			if e := int(sc.Elapsed.Seconds()); e > maxElapsed {
				maxElapsed = e
			}
			grace = int(sc.Grace.Seconds())
		}
		if len(stoppingCmds) == 0 {
			continue
		}
		if maxElapsed > grace {
			maxElapsed = grace
		}
		blocks = append(blocks, envBlock{name: env.Name, elapsed: maxElapsed, grace: grace, cmds: stoppingCmds})
	}

	logPanelHeight := m.height / 2
	statusHeight := m.height - logPanelHeight

	var statusContent string
	if len(blocks) == 0 {
		statusContent = title
	} else {
		const innerWidth = 40
		var rows []string
		rows = append(rows, title, "")
		for _, b := range blocks {
			left := fmt.Sprintf("%s %s", spinnerChar(m.spinnerFrame), b.name)
			right := fmt.Sprintf("%ds / %ds", b.elapsed, b.grace)
			gap := innerWidth - len(left) - len(right)
			if gap < 1 {
				gap = 1
			}
			rows = append(rows, left+strings.Repeat(" ", gap)+right)
			for _, cmd := range b.cmds {
				rows = append(rows, "  Stopping "+cmd)
			}
		}
		statusContent = lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	status := lipgloss.Place(m.width, statusHeight, lipgloss.Center, lipgloss.Center, statusContent)
	return lipgloss.JoinVertical(lipgloss.Left, status, m.renderShutdownLogPanel())
}

// envMaskedValue is shown in place of every env value by default. It is a
// fixed placeholder rather than one sized to the real value, so a value's
// length is not leaked either.
const envMaskedValue = "••••••••"

// renderEnvInspector dispatches to whichever screen the inspector overlay is
// currently showing.
func (m Model) renderEnvInspector() string {
	if m.envInspector.screen == envInspectorScreenDetail {
		return m.renderEnvInspectorDetail()
	}
	return m.renderEnvInspectorList()
}

// envOriginFacet is one selectable origin-filter button shown above the
// search field.
type envOriginFacet struct {
	key    string
	label  string
	filter envOriginFilter
}

var envOriginFacets = []envOriginFacet{
	{"F5", "All", envOriginFilterAll},
	{"F6", "OS/user", envOriginFilter(engine.EnvSourceOS)},
	{"F7", "environment", envOriginFilter(engine.EnvSourceEnvironment)},
	{"F8", "command", envOriginFilter(engine.EnvSourceCommand)},
}

// renderEnvOriginFacets renders the "F5 All  F6 OS/user  ..." filter row,
// highlighting whichever facet is active.
func renderEnvOriginFacets(active envOriginFilter) string {
	buttons := make([]string, 0, len(envOriginFacets))
	for _, f := range envOriginFacets {
		if f.filter == active {
			buttons = append(buttons, envFacetSelectedStyle.Render(f.key+" "+f.label))
		} else {
			buttons = append(buttons, envFacetKeyStyle.Render(f.key)+" "+f.label)
		}
	}
	return strings.Join(buttons, "   ")
}

// padCell right-pads s with spaces to width (no-op if s is already as long).
func padCell(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// envInspectorOriginColumn renders a row's Origin column: the winning
// source's label alone, or annotated with the nearest layer it overrides.
func envInspectorOriginColumn(v engine.ResolvedEnvVar) string {
	label := envSourceLabel(v.Winning.Source)
	if len(v.Shadowed) == 0 {
		return label
	}
	return fmt.Sprintf("%s (overrides %s)", label, envSourceLabel(v.Shadowed[0].Source))
}

// renderEnvInspectorList renders the origin facets, search field, and the
// Key/Origin table (values are never shown here — see the details screen).
func (m Model) renderEnvInspectorList() string {
	insp := m.envInspector
	vars := insp.filteredVars()

	searchBox := paneStyle(insp.focus == envInspectorFocusSearch).
		Width(m.width - 4).
		Render(insp.search.View())

	rows := []string{
		logsTitleStyle.Render("Environment variables — " + insp.title),
		"",
		renderEnvOriginFacets(insp.originFilter),
		"",
		searchBox,
		"",
	}

	keyWidth := len("Key")
	originWidth := len("Origin")
	for _, v := range vars {
		if l := len(v.Key); l > keyWidth {
			keyWidth = l
		}
		if l := len(envInspectorOriginColumn(v)); l > originWidth {
			originWidth = l
		}
	}
	rows = append(rows,
		fmt.Sprintf("| %s | %s |", padCell("Key", keyWidth), padCell("Origin", originWidth)),
		fmt.Sprintf("|%s|%s|", strings.Repeat("-", keyWidth+2), strings.Repeat("-", originWidth+2)),
	)

	if len(vars) == 0 {
		rows = append(rows, "  (no matching variables)")
	} else {
		visible := m.envInspectorVisibleRows()
		start := insp.scroll
		end := start + visible
		if end > len(vars) {
			end = len(vars)
		}
		for i := start; i < end; i++ {
			v := vars[i]
			line := fmt.Sprintf("| %s | %s |", padCell(v.Key, keyWidth), padCell(envInspectorOriginColumn(v), originWidth))
			if i == insp.cursor && insp.focus == envInspectorFocusTable {
				line = selectedLine.Render(line)
			}
			rows = append(rows, line)
		}
	}

	rows = append(rows, "", footerStyle.Render("↑/↓ move   enter details   F5-F8 filter origin   esc close"))

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Top, content)
}

// renderEnvInspectorDetail renders the details screen for the row snapshot
// captured when the row was selected: its value, origin, and every
// lower-priority value it overrides — all masked unless revealed.
func (m Model) renderEnvInspectorDetail() string {
	insp := m.envInspector
	v := insp.detail

	value := envMaskedValue
	if insp.revealed {
		value = v.Winning.Value
	}

	rows := []string{
		logsTitleStyle.Render(v.Key),
		"",
		fmt.Sprintf("Value      %s", value),
		fmt.Sprintf("Origin     %s", envSourceLabel(v.Winning.Source)),
	}

	if len(v.Shadowed) > 0 {
		rows = append(rows, "", "Overrides:")
		for _, s := range v.Shadowed {
			shadowValue := envMaskedValue
			if insp.revealed {
				shadowValue = s.Value
			}
			rows = append(rows, fmt.Sprintf("  %-12s (was %s)", envSourceLabel(s.Source), shadowValue))
		}
	}

	rows = append(rows, "")
	if insp.revealed {
		rows = append(rows, quitConfirmStyle.Render("⚠ value shown — visible to anyone viewing/recording your screen"))
	}
	rows = append(rows, footerStyle.Render("space reveal   esc back"))

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	content = lipgloss.NewStyle().Width(m.envInspectorDetailWidth()).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Top, content)
}

// envSourceLabel renders an engine.EnvSource for display. OS-sourced
// variables display as "user" since that's how the OS environment reaches
// the app in practice — the same source is offered as the "OS/user" facet.
func envSourceLabel(s engine.EnvSource) string {
	switch s {
	case engine.EnvSourceCommand:
		return "command"
	case engine.EnvSourceEnvironment:
		return "environment"
	default:
		return "user"
	}
}

// ── State indicators ──────────────────────────────────────────────────────────

var spinnerChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerChar(frame int) string {
	return spinnerChars[frame%len(spinnerChars)]
}

func envStateIndicator(s engine.EnvState, frame int) string {
	switch s {
	case engine.EnvRunning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("●")
	case engine.EnvStarting:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(spinnerChar(frame))
	case engine.EnvStopping:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render(spinnerChar(frame))
	case engine.EnvDegraded:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("◐")
	case engine.EnvError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗")
	default: // EnvStopped
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("○")
	}
}

func cmdStateIndicator(s engine.CmdState, frame int) string {
	switch s {
	case engine.CmdHealthy:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("●")
	case engine.CmdStarting:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(spinnerChar(frame))
	case engine.CmdStopping:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render(spinnerChar(frame))
	case engine.CmdRestarting:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(spinnerChar(frame))
	case engine.CmdDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("✓")
	case engine.CmdError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗")
	case engine.CmdTimeout:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("⧖")
	case engine.CmdStopped:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("○")
	default: // CmdPending
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("·")
	}
}

// cmdRetrySuffix returns a short annotation to append after the command name.
// During a restart cycle it shows "(retry N/max)"; after permanent failure it
// shows "(failed after N retries)". Returns an empty string in all other cases.
func cmdRetrySuffix(state engine.CmdState, attempts, max int) string {
	switch state {
	case engine.CmdRestarting:
		return fmt.Sprintf(" (retry %d/%d)", attempts+1, max)
	case engine.CmdError:
		if attempts > 0 {
			return fmt.Sprintf(" (failed after %d retries)", attempts)
		}
	}
	return ""
}

// cmdUnmanagedSuffix returns a short dimmed annotation appended after the
// command name when it is healthy only because an external process already
// satisfied its readiness probe before env-starter spawned anything. Returns
// an empty string otherwise.
func cmdUnmanagedSuffix(unmanaged bool) string {
	if !unmanaged {
		return ""
	}
	return " " + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("(unmanaged)")
}
