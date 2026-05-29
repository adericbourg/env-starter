package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/adericbourg/env-starter/internal/engine"
)

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

const tickInterval = 500 * time.Millisecond

// quitConfirmWindow is how long after a first Ctrl+C a second press still counts
// as confirmation. After it elapses the confirmation is cancelled.
const quitConfirmWindow = 3 * time.Second

// shutdownGrace bounds how long the engine teardown may take before surviving
// processes are force-killed. Mirrors the deadline used on the signal path in main.
const shutdownGrace = 35 * time.Second

// Model is the root Bubble Tea model for the TUI.
type Model struct {
	ctrl Controller

	// selection state
	envCursor int
	cmdCursor int
	focused   focus

	// terminal dimensions
	width  int
	height int

	// log viewport
	logView viewport.Model

	// spinner advances on every tickMsg to animate starting-state indicators
	spinnerFrame int

	// quit flow
	confirmingQuit bool // first Ctrl+C seen; awaiting a confirming second press
	quitting       bool // confirmed: engine teardown in progress, shutdown screen shown
}

// New creates a TUI model driven by the given controller.
func New(ctrl Controller) Model {
	return Model{
		ctrl:    ctrl,
		logView: viewport.New(0, 0),
	}
}

// Init returns the initial commands: start listening for engine events and arm
// the periodic refresh ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.ctrl), tickCmd())
}

// waitForEvent returns a Cmd that blocks until the next engine event, then
// delivers it as an eventMsg. The caller re-arms this command in Update so
// that every subsequent event is also delivered.
func waitForEvent(ctrl Controller) tea.Cmd {
	return func() tea.Msg {
		ev := <-ctrl.Events()
		return eventMsg(ev)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// quitResetCmd fires quitResetMsg after the confirmation window, cancelling a
// pending first-Ctrl+C if the user did not follow up.
func quitResetCmd() tea.Cmd {
	return tea.Tick(quitConfirmWindow, func(time.Time) tea.Msg {
		return quitResetMsg{}
	})
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
		return m, nil

	case tickMsg:
		m.spinnerFrame++
		m = m.refreshLogView()
		return m, tickCmd()

	case quitResetMsg:
		m.confirmingQuit = false
		return m, nil

	case shutdownDoneMsg:
		return m, tea.Quit

	case eventMsg:
		// Re-arm so the next engine event is also delivered.
		m = m.refreshLogView()
		return m, waitForEvent(m.ctrl)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the shutdown screen is shown, all input is ignored.
	if m.quitting {
		return m, nil
	}

	if msg.String() == "ctrl+c" {
		if m.confirmingQuit {
			// Second Ctrl+C within the window: start engine teardown.
			m.confirmingQuit = false
			m.quitting = true
			return m, shutdownCmd(m.ctrl)
		}
		// First Ctrl+C: arm the confirmation window; do NOT touch any environment.
		m.confirmingQuit = true
		return m, quitResetCmd()
	}

	// Any key other than Ctrl+C cancels a pending confirmation.
	m.confirmingQuit = false

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

	case "l":
		m.focused = focusLogs
		m.refreshLogView()

	case "up", "k":
		m = m.moveCursorUp()

	case "down", "j":
		m = m.moveCursorDown()

	case "s":
		envs := m.ctrl.Environments()
		if m.envCursor < len(envs) {
			_ = m.ctrl.StartEnvironment(envs[m.envCursor].Name)
		}

	case "x":
		envs := m.ctrl.Environments()
		if m.envCursor < len(envs) {
			_ = m.ctrl.StopEnvironment(envs[m.envCursor].Name)
		}

	case "r":
		m = m.refreshLogView()
	}

	// Forward scroll keys to viewport when logs are focused.
	if m.focused == focusLogs {
		var cmd tea.Cmd
		m.logView, cmd = m.logView.Update(msg)
		return m, cmd
	}

	return m, nil
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

// refreshLogView populates the viewport with the latest log lines for the
// currently selected command.
func (m Model) refreshLogView() Model {
	cmd := m.selectedCommand()
	if cmd == "" {
		m.logView.SetContent("")
		return m
	}
	lines := m.ctrl.Logs(cmd)
	m.logView.SetContent(strings.Join(lines, "\n"))
	m.logView.GotoBottom()
	return m
}

// resizePanes recalculates the viewport size to fit the terminal.
func (m Model) resizePanes() Model {
	// Layout: top half split into env+cmd panes, bottom half is logs.
	topHeight := m.height / 2
	logHeight := m.height - topHeight - footerHeight - 2 // 2 for log pane border
	if logHeight < 1 {
		logHeight = 1
	}
	logWidth := m.width - 2 // 2 for border
	if logWidth < 1 {
		logWidth = 1
	}
	m.logView.Width = logWidth
	m.logView.Height = logHeight
	return m
}

// footerHeight is the number of terminal rows occupied by the footer bar.
const footerHeight = 1

// View renders the full TUI.
func (m Model) View() string {
	if m.width == 0 {
		return "initialising…\n"
	}

	if m.quitting {
		return m.renderShutdown()
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

	// quitConfirmStyle renders the "Press Ctrl+C again to quit" hint in amber so
	// it stands out clearly from the normal shortcut bar.
	quitConfirmStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("214"))

	// shutdownStyle is used for the full-screen "env shutting down…" message.
	shutdownStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("214"))
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
		line := fmt.Sprintf("%s %s", indicator, cmd)
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
	title := "logs"
	if cmd := m.selectedCommand(); cmd != "" {
		title = fmt.Sprintf("logs: %s", cmd)
	}
	_ = title
	content := m.logView.View()
	style := paneStyle(m.focused == focusLogs).
		Width(m.width - 2)
	return style.Render(content)
}

func (m Model) renderFooter() string {
	if m.confirmingQuit {
		return quitConfirmStyle.Render("Press Ctrl+C again to quit")
	}
	shortcuts := "↑/↓ move  tab/←/→ focus  s start  x stop  l logs  r refresh  ^C quit"
	return footerStyle.Render(shortcuts)
}

// renderShutdown shows a full-screen "env shutting down…" notice while the
// engine tears down running commands. It replaces the normal TUI so the user
// has clear feedback that the app is intentionally closing.
func (m Model) renderShutdown() string {
	primary := shutdownStyle.Render("env shutting down…")
	secondary := ""
	if n := m.activeCommandCount(); n > 0 {
		noun := "commands"
		if n == 1 {
			noun = "command"
		}
		secondary = fmt.Sprintf("stopping %d %s…", n, noun)
	}

	content := primary
	if secondary != "" {
		content = lipgloss.JoinVertical(lipgloss.Center, primary, secondary)
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// activeCommandCount returns the number of commands that are currently running
// or starting (and therefore need to be stopped during shutdown).
func (m Model) activeCommandCount() int {
	count := 0
	for _, env := range m.ctrl.Environments() {
		for _, cmd := range m.ctrl.WorkflowCommands(env.Name) {
			switch m.ctrl.CmdState(cmd) {
			case engine.CmdHealthy, engine.CmdStarting, engine.CmdStopping:
				count++
			}
		}
	}
	return count
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
	case engine.CmdDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("✓")
	case engine.CmdError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗")
	case engine.CmdStopped:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("○")
	default: // CmdPending
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("·")
	}
}
