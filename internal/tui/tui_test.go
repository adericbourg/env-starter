package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/trust"
)

// ── Fake controller ───────────────────────────────────────────────────────────

type fakeController struct {
	envs         []engine.EnvInfo
	commands     map[string][]string
	envState     map[string]engine.EnvState
	cmdState     map[string]engine.CmdState
	cmdRetries   map[string][2]int // [attempts, max]
	cmdUnmanaged map[string]bool
	logPaths     map[string]string // override for LogPath; missing key falls back to the default
	logs         map[string][]string
	events       chan engine.Event
	stopping     []engine.StoppingCommand
	// resolvedEnv is keyed by "env:<name>" or "cmd:<name>".
	resolvedEnv map[string][]engine.ResolvedEnvVar

	startedEnvs       []string
	stoppedEnvs       []string
	restartedCmds     []string
	restartCommandErr error
	shutdownCalled    bool

	// hot-reload stubs
	configChanged  bool
	configParseErr error
	reloadCalled   bool
	reloadErr      error
}

func newFakeController() *fakeController {
	return &fakeController{
		envs: []engine.EnvInfo{
			{Name: "alpha", Description: "first env"},
			{Name: "beta", Description: "second env"},
		},
		commands: map[string][]string{
			"alpha": {"svc-a", "svc-b"},
			"beta":  {"svc-c"},
		},
		envState: map[string]engine.EnvState{
			"alpha": engine.EnvStopped,
			"beta":  engine.EnvRunning,
		},
		cmdState: map[string]engine.CmdState{
			"svc-a": engine.CmdPending,
			"svc-b": engine.CmdPending,
			"svc-c": engine.CmdHealthy,
		},
		logs: map[string][]string{
			"svc-a": {"log line 1", "log line 2"},
			"svc-c": {"svc-c started"},
		},
		events: make(chan engine.Event, 16),
	}
}

func (f *fakeController) Environments() []engine.EnvInfo { return f.envs }

func (f *fakeController) WorkflowCommands(env string) []string { return f.commands[env] }

func (f *fakeController) EnvState(env string) engine.EnvState {
	if s, ok := f.envState[env]; ok {
		return s
	}
	return engine.EnvStopped
}

func (f *fakeController) CmdState(cmd string) engine.CmdState {
	if s, ok := f.cmdState[cmd]; ok {
		return s
	}
	return engine.CmdPending
}

func (f *fakeController) CmdRetries(cmd string) (attempts, max int) {
	if r, ok := f.cmdRetries[cmd]; ok {
		return r[0], r[1]
	}
	return 0, 0
}

func (f *fakeController) IsUnmanaged(cmd string) bool { return f.cmdUnmanaged[cmd] }

func (f *fakeController) Logs(cmd string) []string { return f.logs[cmd] }

func (f *fakeController) LogPath(cmd string) string {
	if path, ok := f.logPaths[cmd]; ok {
		return path
	}
	return "/tmp/logs/" + cmd + ".log"
}

func (f *fakeController) ResolveEnv(envName, command string) []engine.ResolvedEnvVar {
	if command != "" {
		return f.resolvedEnv["cmd:"+command]
	}
	return f.resolvedEnv["env:"+envName]
}

func (f *fakeController) StartEnvironment(env string) error {
	f.startedEnvs = append(f.startedEnvs, env)
	return nil
}

func (f *fakeController) StopEnvironment(env string) error {
	f.stoppedEnvs = append(f.stoppedEnvs, env)
	return nil
}

func (f *fakeController) RestartCommand(command string) error {
	f.restartedCmds = append(f.restartedCmds, command)
	return f.restartCommandErr
}

func (f *fakeController) Events() <-chan engine.Event { return f.events }

func (f *fakeController) StoppingCommands() []engine.StoppingCommand { return f.stopping }

func (f *fakeController) Shutdown(_ context.Context) { f.shutdownCalled = true }

func (f *fakeController) ConfigChanged() (bool, error) {
	return f.configChanged, f.configParseErr
}

func (f *fakeController) Reload(_ context.Context) error {
	f.reloadCalled = true
	return f.reloadErr
}

func (f *fakeController) Detach() {}

// ── Helpers ───────────────────────────────────────────────────────────────────

// seed gives the model a non-zero terminal size so View() renders real content.
func seed(m Model) Model {
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func keyMsg(key string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
}

func specialKey(t rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: t}
}

func sendKey(m Model, key string) Model {
	updated, _ := m.Update(keyMsg(key))
	return updated.(Model)
}

func sendSpecialKey(m Model, kt rune) Model {
	updated, _ := m.Update(specialKey(kt))
	return updated.(Model)
}

// typeText sends each rune of text as a separate key press, simulating typing
// into a focused text field one character at a time.
func typeText(m Model, text string) Model {
	for _, r := range text {
		m = sendKey(m, string(r))
	}
	return m
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestView_initial_rendersEnvNameAndFooterShortcut(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	view := m.render()

	// Then
	if !strings.Contains(view, "alpha") {
		t.Error("expected view to contain environment name 'alpha'")
	}
	if !strings.Contains(view, "shutdown") {
		t.Error("expected view to contain footer shortcut hint 'shutdown'")
	}
}

func TestUpdate_whenDownArrow_movesEnvSelection(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	if m.envCursor != 0 {
		t.Fatalf("expected initial cursor at 0, got %d", m.envCursor)
	}

	// When
	m = sendSpecialKey(m, tea.KeyDown)

	// Then
	if m.envCursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.envCursor)
	}
}

func TestUpdate_whenUpArrowAtTop_clampsAtZero(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendSpecialKey(m, tea.KeyUp)

	// Then
	if m.envCursor != 0 {
		t.Errorf("expected cursor to remain at 0, got %d", m.envCursor)
	}
}

func TestUpdate_whenDownArrowAtBottom_clampsAtLastEnv(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyDown) // cursor = 1 (last)

	// When
	m = sendSpecialKey(m, tea.KeyDown) // should clamp

	// Then
	if m.envCursor != 1 {
		t.Errorf("expected cursor to remain at 1, got %d", m.envCursor)
	}
}

func TestUpdate_whenTab_switchesFocusToCmds(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	if m.focused != focusEnvs {
		t.Fatal("expected initial focus on envs pane")
	}

	// When
	m = sendSpecialKey(m, tea.KeyTab)

	// Then
	if m.focused != focusCmds {
		t.Errorf("expected focus on cmds pane after tab, got %v", m.focused)
	}
}

func TestUpdate_whenTabThenDown_movesCmdSelection(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds
	if m.cmdCursor != 0 {
		t.Fatal("expected initial cmd cursor at 0")
	}

	// When
	m = sendSpecialKey(m, tea.KeyDown)

	// Then
	if m.cmdCursor != 1 {
		t.Errorf("expected cmd cursor at 1, got %d", m.cmdCursor)
	}
}

func TestUpdate_whenS_callsStartEnvironment(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "s")

	// Then
	if len(ctrl.startedEnvs) != 1 || ctrl.startedEnvs[0] != "alpha" {
		t.Errorf("expected StartEnvironment('alpha'), got %v", ctrl.startedEnvs)
	}
	_ = m
}

func TestAutoStartCmd_ofAutoStartAndStoppedEnv_startsEnvironment(t *testing.T) {
	// Given an auto-start environment that is currently stopped.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{{Name: "alpha", AutoStart: true}}
	ctrl.envState["alpha"] = engine.EnvStopped

	// When
	autoStartCmd(ctrl)()

	// Then
	if len(ctrl.startedEnvs) != 1 || ctrl.startedEnvs[0] != "alpha" {
		t.Errorf("expected StartEnvironment('alpha'), got %v", ctrl.startedEnvs)
	}
}

func TestAutoStartCmd_ofAutoStartAndRunningEnv_isNoOp(t *testing.T) {
	// Given an auto-start environment that is already running, e.g. because the
	// TUI is reconnecting to a daemon that kept it alive.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{{Name: "alpha", AutoStart: true}}
	ctrl.envState["alpha"] = engine.EnvRunning

	// When
	autoStartCmd(ctrl)()

	// Then
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call, got %v", ctrl.startedEnvs)
	}
}

func TestAutoStartCmd_withoutAutoStart_isNoOp(t *testing.T) {
	// Given a stopped environment without auto-start.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{{Name: "alpha"}}
	ctrl.envState["alpha"] = engine.EnvStopped

	// When
	autoStartCmd(ctrl)()

	// Then
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call, got %v", ctrl.startedEnvs)
	}
}

func TestUpdate_whenX_callsStopEnvironment(t *testing.T) {
	// Given a running environment selected.
	ctrl := newFakeController()
	ctrl.envState["alpha"] = engine.EnvRunning
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "x")

	// Then
	if len(ctrl.stoppedEnvs) != 1 || ctrl.stoppedEnvs[0] != "alpha" {
		t.Errorf("expected StopEnvironment('alpha'), got %v", ctrl.stoppedEnvs)
	}
	_ = m
}

func TestUpdate_whenS_andEnvRunning_isNoOp(t *testing.T) {
	// Given the selected environment is already running.
	ctrl := newFakeController()
	ctrl.envState["alpha"] = engine.EnvRunning
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "s")

	// Then
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call, got %v", ctrl.startedEnvs)
	}
	_ = m
}

func TestUpdate_whenS_withCmdsFocused_isNoOp(t *testing.T) {
	// Given a model with the commands pane focused.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	m = sendKey(m, "s")

	// Then
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call, got %v", ctrl.startedEnvs)
	}
	_ = m
}

func TestUpdate_whenS_withLogsFocused_isNoOp(t *testing.T) {
	// Given a model with the logs pane focused.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	m = sendKey(m, "s")

	// Then
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call, got %v", ctrl.startedEnvs)
	}
	_ = m
}

func TestUpdate_whenX_withCmdsFocused_isNoOp(t *testing.T) {
	// Given a model with the commands pane focused.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	m = sendKey(m, "x")

	// Then
	if len(ctrl.stoppedEnvs) != 0 {
		t.Errorf("expected no StopEnvironment call, got %v", ctrl.stoppedEnvs)
	}
	_ = m
}

func TestUpdate_whenX_withLogsFocused_isNoOp(t *testing.T) {
	// Given a model with the logs pane focused.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	m = sendKey(m, "x")

	// Then
	if len(ctrl.stoppedEnvs) != 0 {
		t.Errorf("expected no StopEnvironment call, got %v", ctrl.stoppedEnvs)
	}
	_ = m
}

func TestUpdate_whenX_andEnvStopped_isNoOp(t *testing.T) {
	// Given the selected environment is already stopped (the default fixture state).
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "x")

	// Then
	if len(ctrl.stoppedEnvs) != 0 {
		t.Errorf("expected no StopEnvironment call, got %v", ctrl.stoppedEnvs)
	}
	_ = m
}

func TestUpdate_whenLowerR_withLogsFocused_refreshesLogView(t *testing.T) {
	// Given a model with the logs pane focused and svc-a's logs already rendered.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs
	ctrl.logs["svc-a"] = append(ctrl.logs["svc-a"], "log line 3")

	// When
	m = sendKey(m, "r")

	// Then — the newly appended log line must now appear in the pane.
	pane := ansi.Strip(m.renderLogsPane())
	if !strings.Contains(pane, "log line 3") {
		t.Errorf("expected refreshed log pane to contain 'log line 3', got:\n%s", pane)
	}
}

func TestUpdate_whenLowerR_withoutLogsFocused_isNoOp(t *testing.T) {
	// Given a model with the envs pane focused (the default) and svc-a's logs already rendered.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	ctrl.logs["svc-a"] = append(ctrl.logs["svc-a"], "log line 3")

	// When
	m = sendKey(m, "r")

	// Then — the log pane must not pick up the new line while envs is focused.
	pane := ansi.Strip(m.renderLogsPane())
	if strings.Contains(pane, "log line 3") {
		t.Errorf("expected log pane to stay stale outside logs pane, got:\n%s", pane)
	}
}

func TestUpdate_whenLowerL_doesNotChangeFocus(t *testing.T) {
	// Given — default focus is the envs pane (the jump-to-logs shortcut was removed).
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "l")

	// Then
	if m.focused != focusEnvs {
		t.Errorf("expected focus to remain on envs pane, got %v", m.focused)
	}
}

func TestUpdate_whenR_withCmdsFocused_callsRestartCommand(t *testing.T) {
	// Given a model with the commands pane focused and the first (running) command selected.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdHealthy
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds; cmdCursor 0 -> "svc-a" (env "alpha")

	// When
	m = sendKey(m, "R")

	// Then
	if len(ctrl.restartedCmds) != 1 || ctrl.restartedCmds[0] != "svc-a" {
		t.Errorf("expected RestartCommand('svc-a'), got %v", ctrl.restartedCmds)
	}
	_ = m
}

func TestUpdate_whenR_withoutCmdsFocused_isNoOp(t *testing.T) {
	// Given a model with the envs pane focused (the default).
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "R")

	// Then
	if len(ctrl.restartedCmds) != 0 {
		t.Errorf("expected no RestartCommand call, got %v", ctrl.restartedCmds)
	}
	_ = m
}

func TestUpdate_whenR_andCmdStopped_isNoOp(t *testing.T) {
	// Given a model with the commands pane focused and the selected command stopped.
	ctrl := newFakeController()
	ctrl.cmdState = map[string]engine.CmdState{"svc-a": engine.CmdStopped}
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds; cmdCursor 0 -> "svc-a" (env "alpha")

	// When
	m = sendKey(m, "R")

	// Then
	if len(ctrl.restartedCmds) != 0 {
		t.Errorf("expected no RestartCommand call, got %v", ctrl.restartedCmds)
	}
	if m.notice != "" {
		t.Errorf("expected no notice, got %q", m.notice)
	}
}

func TestUpdate_whenR_andCmdPending_isNoOp(t *testing.T) {
	// Given a model with the commands pane focused and the selected command never started
	// (CmdPending, the default fixture state).
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds; cmdCursor 0 -> "svc-a" (env "alpha")

	// When
	m = sendKey(m, "R")

	// Then
	if len(ctrl.restartedCmds) != 0 {
		t.Errorf("expected no RestartCommand call, got %v", ctrl.restartedCmds)
	}
	if m.notice != "" {
		t.Errorf("expected no notice, got %q", m.notice)
	}
}

func TestUpdate_whenQ_doesNotQuit(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(Model)

	// Then: q must not quit — no QuitMsg and quitting flag stays false.
	if m.quitting {
		t.Error("expected quitting to remain false after pressing 'q'")
	}
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Error("expected no tea.QuitMsg from 'q'")
		}
	}
}

func TestUpdate_whenFirstCtrlC_armsConfirmationWithoutQuitting(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then: confirmation window armed, quitting flag still false.
	if !m.confirmingQuit {
		t.Error("expected confirmingQuit to be true after first Ctrl+C")
	}
	if m.quitting {
		t.Error("expected quitting to remain false after first Ctrl+C")
	}
	// The returned command must NOT immediately produce a QuitMsg.
	if cmd == nil {
		t.Fatal("expected a non-nil cmd (reset tick) from first Ctrl+C")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Error("expected no tea.QuitMsg from first Ctrl+C")
	}
}

func TestUpdate_whenFirstCtrlC_doesNotShutDownEnvs(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	_, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	// Then: shutdown must NOT have been called — environments keep running.
	if ctrl.shutdownCalled {
		t.Error("expected Shutdown not to be called after first Ctrl+C")
	}
}

func TestUpdate_whenSecondCtrlC_startsShutdown(t *testing.T) {
	// Given: model already has confirmingQuit set (simulates first Ctrl+C)
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.confirmingQuit = true

	// When
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then: quitting is set; the returned cmd calls Shutdown and yields shutdownDoneMsg.
	if !m.quitting {
		t.Error("expected quitting to be true after second Ctrl+C")
	}
	if m.confirmingQuit {
		t.Error("expected confirmingQuit to be cleared after second Ctrl+C")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from second Ctrl+C")
	}
	msg := cmd()
	if _, ok := msg.(shutdownDoneMsg); !ok {
		t.Errorf("expected shutdownDoneMsg, got %T", msg)
	}
	if !ctrl.shutdownCalled {
		t.Error("expected Shutdown to be called on confirmed quit")
	}
}

func TestUpdate_whenQuitResetMsg_clearsConfirmation(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.confirmingQuit = true

	// When
	updated, _ := m.Update(quitResetMsg{})
	m = updated.(Model)

	// Then
	if m.confirmingQuit {
		t.Error("expected confirmingQuit to be cleared by quitResetMsg")
	}
}

func TestUpdate_whenShutdownDoneMsg_returnsQuitCmd(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	_, cmd := m.Update(shutdownDoneMsg{})

	// Then
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from shutdownDoneMsg")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestUpdate_whenOtherKeyDuringConfirmation_cancelsConfirmation(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.confirmingQuit = true

	// When: pressing an unrelated key cancels the pending confirmation.
	updated, _ := m.Update(keyMsg("r"))
	m = updated.(Model)

	// Then
	if m.confirmingQuit {
		t.Error("expected confirmingQuit to be cancelled by an unrelated key")
	}
}

func TestRenderFooter_whenConfirmingQuit_showsPressAgainMessage(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.confirmingQuit = true

	// When
	footer := m.renderFooter()

	// Then
	if !strings.Contains(footer, "^C") {
		t.Errorf("expected footer to contain '^C' during confirmation, got %q", footer)
	}
	if !strings.Contains(footer, "again") {
		t.Errorf("expected footer to contain 'again' during confirmation, got %q", footer)
	}
	if !strings.Contains(footer, "shutdown daemon") {
		t.Errorf("expected footer to contain 'shutdown daemon' during confirmation, got %q", footer)
	}
}

func TestView_whenQuitting_showsShuttingDownMessage(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.quitting = true

	// When
	view := m.render()

	// Then
	if !strings.Contains(view, "shutting down") {
		t.Errorf("expected view to contain 'shutting down' while quitting, got %q", view)
	}
}

func TestRenderShutdown_withStoppingCommands_showsEnvHeaderWithCommandChildrenAndCountdown(t *testing.T) {
	// Given — "dev" has two stopping commands; "dev2" shares "mariadb" with "dev".
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{
		{Name: "dev", Description: "first"},
		{Name: "dev2", Description: "second"},
	}
	ctrl.commands = map[string][]string{
		"dev":  {"mariadb", "redis"},
		"dev2": {"mariadb"},
	}
	ctrl.envState = map[string]engine.EnvState{
		"dev":  engine.EnvStopping,
		"dev2": engine.EnvStopping,
	}
	ctrl.stopping = []engine.StoppingCommand{
		{Command: "mariadb", Elapsed: 12 * time.Second, Grace: 30 * time.Second},
		{Command: "redis", Elapsed: 12 * time.Second, Grace: 30 * time.Second},
	}

	m := seed(New(ctrl))
	m.quitting = true

	// When
	view := ansi.Strip(m.render())

	// Then — each env appears as a header, commands appear indented under it.
	if !strings.Contains(view, "dev") {
		t.Errorf("expected env name 'dev', got:\n%s", view)
	}
	if !strings.Contains(view, "Stopping mariadb") {
		t.Errorf("expected 'Stopping mariadb', got:\n%s", view)
	}
	if !strings.Contains(view, "Stopping redis") {
		t.Errorf("expected 'Stopping redis', got:\n%s", view)
	}
	if !strings.Contains(view, "12s / 30s") {
		t.Errorf("expected countdown '12s / 30s', got:\n%s", view)
	}
}

func TestRenderShutdown_whenStoppingCommandBelongsToSingleEnv_showsOnlyThatEnv(t *testing.T) {
	// Given — only "dev" references "proxy"; "dev2" is already stopped.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{
		{Name: "dev", Description: "first"},
		{Name: "dev2", Description: "second"},
	}
	ctrl.commands = map[string][]string{
		"dev":  {"proxy"},
		"dev2": {},
	}
	ctrl.envState = map[string]engine.EnvState{
		"dev":  engine.EnvStopping,
		"dev2": engine.EnvStopped,
	}
	ctrl.stopping = []engine.StoppingCommand{
		{Command: "proxy", Elapsed: 9 * time.Second, Grace: 30 * time.Second},
	}

	m := seed(New(ctrl))
	m.quitting = true

	// When
	view := ansi.Strip(m.render())

	// Then
	if !strings.Contains(view, "dev") {
		t.Errorf("expected env name 'dev', got:\n%s", view)
	}
	if strings.Contains(view, "dev2") {
		t.Errorf("expected 'dev2' to be absent (already stopped), got:\n%s", view)
	}
	if !strings.Contains(view, "Stopping proxy") {
		t.Errorf("expected 'Stopping proxy', got:\n%s", view)
	}
	if !strings.Contains(view, "9s / 30s") {
		t.Errorf("expected countdown '9s / 30s', got:\n%s", view)
	}
}

func TestRenderShutdown_withTwoDistinctEnvs_showsEachEnvBlock(t *testing.T) {
	// Given — two envs with distinct commands, both stopping simultaneously.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{
		{Name: "env1", Description: "first"},
		{Name: "env2", Description: "second"},
	}
	ctrl.commands = map[string][]string{
		"env1": {"cmd-a", "cmd-b"},
		"env2": {"cmd-c"},
	}
	ctrl.envState = map[string]engine.EnvState{
		"env1": engine.EnvStopping,
		"env2": engine.EnvStopping,
	}
	ctrl.stopping = []engine.StoppingCommand{
		{Command: "cmd-a", Elapsed: 5 * time.Second, Grace: 30 * time.Second},
		{Command: "cmd-b", Elapsed: 5 * time.Second, Grace: 30 * time.Second},
		{Command: "cmd-c", Elapsed: 5 * time.Second, Grace: 30 * time.Second},
	}

	m := seed(New(ctrl))
	m.quitting = true

	// When
	view := ansi.Strip(m.render())

	// Then — both env blocks appear with their respective commands.
	for _, want := range []string{"env1", "env2", "Stopping cmd-a", "Stopping cmd-b", "Stopping cmd-c", "5s / 30s"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in shutdown view, got:\n%s", want, view)
		}
	}
}

func TestUpdate_whenEventMsg_rearmsEventWaitCmd(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// Inject an event into the fake events channel before dispatching eventMsg
	// so the re-armed waitForEvent cmd can drain it without blocking.
	ev := engine.Event{
		Kind:     "command",
		Command:  "svc-a",
		CmdState: engine.CmdHealthy,
	}
	ctrl.events <- ev

	// When
	_, cmd := m.Update(eventMsg(ev))

	// Then
	if cmd == nil {
		t.Fatal("expected a non-nil cmd after eventMsg (re-arm)")
	}
	// Calling the returned cmd should drain the channel and yield another eventMsg.
	result := cmd()
	if _, ok := result.(eventMsg); !ok {
		t.Errorf("expected eventMsg from re-armed cmd, got %T", result)
	}
}

func TestUpdate_whenEventMsg_healthyCmdAppearsInView(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	// Switch focus and select svc-a so its logs are shown
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// Update fake state to healthy
	ctrl.cmdState["svc-a"] = engine.CmdHealthy
	ctrl.events <- engine.Event{Kind: "command", Command: "svc-a", CmdState: engine.CmdHealthy}

	ev := engine.Event{Kind: "command", Command: "svc-a", CmdState: engine.CmdHealthy}

	// When
	updated, _ := m.Update(eventMsg(ev))
	m = updated.(Model)

	// Then
	view := m.render()
	if !strings.Contains(view, "svc-a") {
		t.Error("expected view to contain command name 'svc-a'")
	}
}

func TestUpdate_whenSpinnerTickMsg_advancesSpinnerFrame(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	initial := m.spinnerFrame

	// When
	updated, _ := m.Update(spinnerTickMsg{})
	m = updated.(Model)

	// Then
	if m.spinnerFrame != initial+1 {
		t.Errorf("expected spinnerFrame %d, got %d", initial+1, m.spinnerFrame)
	}
}

func TestUpdate_whenTickMsg_doesNotAdvanceSpinnerFrame(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	initial := m.spinnerFrame

	// When
	updated, _ := m.Update(tickMsg{})
	m = updated.(Model)

	// Then
	if m.spinnerFrame != initial {
		t.Errorf("expected spinnerFrame to stay %d, got %d", initial, m.spinnerFrame)
	}
}

func TestCmdStateIndicator_whenStarting_producesDistinctOutputAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := cmdStateIndicator(engine.CmdStarting, 0)
	frame1 := cmdStateIndicator(engine.CmdStarting, 1)

	// Then
	if frame0 == frame1 {
		t.Errorf("expected distinct indicators at frame 0 and 1, both got %q", frame0)
	}
}

func TestCmdStateIndicator_whenHealthy_isUnchangedAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := cmdStateIndicator(engine.CmdHealthy, 0)
	frame1 := cmdStateIndicator(engine.CmdHealthy, 1)

	// Then
	if frame0 != frame1 {
		t.Errorf("expected identical indicators across frames, got %q and %q", frame0, frame1)
	}
}

func TestEnvStateIndicator_whenStarting_producesDistinctOutputAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := envStateIndicator(engine.EnvStarting, 0)
	frame1 := envStateIndicator(engine.EnvStarting, 1)

	// Then
	if frame0 == frame1 {
		t.Errorf("expected distinct indicators at frame 0 and 1, both got %q", frame0)
	}
}

func TestEnvStateIndicator_whenRunning_isUnchangedAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := envStateIndicator(engine.EnvRunning, 0)
	frame1 := envStateIndicator(engine.EnvRunning, 1)

	// Then
	if frame0 != frame1 {
		t.Errorf("expected identical indicators across frames, got %q and %q", frame0, frame1)
	}
}

func TestCmdStateIndicator_whenStopping_producesDistinctOutputAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := cmdStateIndicator(engine.CmdStopping, 0)
	frame1 := cmdStateIndicator(engine.CmdStopping, 1)

	// Then
	if frame0 == frame1 {
		t.Errorf("expected distinct indicators at frame 0 and 1, both got %q", frame0)
	}
}

func TestEnvStateIndicator_whenStopping_producesDistinctOutputAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := envStateIndicator(engine.EnvStopping, 0)
	frame1 := envStateIndicator(engine.EnvStopping, 1)

	// Then
	if frame0 == frame1 {
		t.Errorf("expected distinct indicators at frame 0 and 1, both got %q", frame0)
	}
}

func TestView_whenCommandSelected_showsLogLines(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	// Navigate to cmds pane and select svc-a (already at index 0)
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	view := m.render()

	// Then
	if !strings.Contains(view, "log line 1") {
		t.Error("expected view to contain log line 'log line 1'")
	}
}

// ── wrapLogLine tests ─────────────────────────────────────────────────────────

func TestWrapLogLine_whenLineFitsWidth_returnsUnchanged(t *testing.T) {
	// Given
	line := "short line"

	// When
	result := wrapLogLine(line, 40)

	// Then
	if result != line {
		t.Errorf("expected line unchanged, got %q", result)
	}
}

func TestWrapLogLine_whenLineExceedsWidth_wrapsWithIndentedContinuations(t *testing.T) {
	// Given
	line := "This is a very long line that will certainly exceed the narrow width we set for this test"
	width := 30

	// When
	result := wrapLogLine(line, width)

	// Then
	rows := strings.Split(result, "\n")
	if len(rows) < 2 {
		t.Fatalf("expected multiple rows after wrapping, got %d: %q", len(rows), result)
	}
	// First row must not be indented.
	if strings.HasPrefix(rows[0], "  ") {
		t.Errorf("first row must not start with indent, got %q", rows[0])
	}
	// Every continuation row must start with the hanging indent.
	for i, row := range rows[1:] {
		if !strings.HasPrefix(row, "  ") {
			t.Errorf("continuation row %d missing indent, got %q", i+1, row)
		}
	}
	// No row may exceed the requested width (display cells).
	for i, row := range rows {
		w := ansi.StringWidth(row)
		if w > width {
			t.Errorf("row %d has display width %d > %d: %q", i, w, width, row)
		}
	}
}

func TestWrapLogLine_whenWidthTooSmall_returnsUnchanged(t *testing.T) {
	// Given — width <= logWrapIndent means there is no usable space to wrap into
	line := "some log line"

	// When
	result := wrapLogLine(line, logWrapIndent)

	// Then
	if result != line {
		t.Errorf("expected line unchanged for width=%d, got %q", logWrapIndent, result)
	}
}

func TestWrapLogLine_withAnsiEscapeCodes_preservesEscapes(t *testing.T) {
	// Given — a line with an ANSI colour code that wraps
	line := "\x1b[32mThis green line is intentionally long enough to force wrapping at a narrow width\x1b[0m"
	width := 30

	// When
	result := wrapLogLine(line, width)

	// Then — the result must still contain ANSI escape sequences
	if !strings.Contains(result, "\x1b[") {
		t.Error("expected ANSI escape sequences to survive wrapping, but none found")
	}
	// And must have wrapped into multiple rows
	if !strings.Contains(result, "\n") {
		t.Error("expected multiple rows after wrapping, but got a single row")
	}
}

// ── openSelectedLog / ^L tests ────────────────────────────────────────────────

// openerSpy records which path was passed to it and returns a configurable error.
type openerSpy struct {
	calledWith string
	err        error
}

func (s *openerSpy) open(path string) error {
	s.calledWith = path
	return s.err
}

// modelWithSpy builds a seeded model wired to the given opener spy.
func modelWithSpy(spy *openerSpy) Model {
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.openFile = spy.open
	return m
}

func TestUpdate_whenCtrlLOnLogsPane_opensSelectedLog(t *testing.T) {
	// Given — logs panel focused on svc-a
	spy := &openerSpy{}
	m := modelWithSpy(spy)
	m.focused = focusLogs // svc-a is selected (envCursor=0, cmdCursor=0)

	// When
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	_ = updated

	// Then — opener must have been called with svc-a's log path
	want := "/tmp/logs/svc-a.log"
	if spy.calledWith != want {
		t.Errorf("expected opener called with %q, got %q", want, spy.calledWith)
	}
}

func TestUpdate_whenCtrlLNotOnLogsPane_doesNothing(t *testing.T) {
	// Given — envs panel focused
	spy := &openerSpy{}
	m := modelWithSpy(spy)
	// default focus is focusEnvs

	// When
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	_ = updated

	// Then — opener must NOT have been called
	if spy.calledWith != "" {
		t.Errorf("expected opener not called, but got calledWith=%q", spy.calledWith)
	}
}

func TestUpdate_whenCtrlLAndNoLogPath_doesNothing(t *testing.T) {
	// Given — logs pane focused on svc-a, but its log path is unknown (e.g. daemon
	// hasn't reported one yet).
	ctrl := newFakeController()
	ctrl.logPaths = map[string]string{"svc-a": ""}
	spy := &openerSpy{}
	m := seed(New(ctrl))
	m.openFile = spy.open
	m.focused = focusLogs

	// When
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then — opener must NOT have been called, and no notice is set.
	if spy.calledWith != "" {
		t.Errorf("expected opener not called, but got calledWith=%q", spy.calledWith)
	}
	if m.notice != "" {
		t.Errorf("expected no notice, got %q", m.notice)
	}
}

func TestUpdate_whenOpenFails_setsErrorNotice(t *testing.T) {
	// Given — logs pane focused, opener returns an error
	spy := &openerSpy{err: fmt.Errorf("no such file")}
	m := modelWithSpy(spy)
	m.focused = focusLogs

	// When
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then — notice must contain the error text
	if !strings.Contains(m.notice, "no such file") {
		t.Errorf("expected notice to contain error, got %q", m.notice)
	}
}

func TestUpdate_whenOpenSucceeds_setsSuccessNotice(t *testing.T) {
	// Given — logs pane focused, opener succeeds
	spy := &openerSpy{}
	m := modelWithSpy(spy)
	m.focused = focusLogs

	// When
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then — notice must confirm which path was opened
	if !strings.Contains(m.notice, "/tmp/logs/svc-a.log") {
		t.Errorf("expected notice to contain log path, got %q", m.notice)
	}
}

func TestUpdate_whenNoticeResetMsg_clearsNotice(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.notice = "some notice"

	// When
	updated, _ := m.Update(noticeResetMsg{})
	m = updated.(Model)

	// Then
	if m.notice != "" {
		t.Errorf("expected notice to be cleared, got %q", m.notice)
	}
}

func TestRenderLogsPane_whenCommandSelected_showsLogPath(t *testing.T) {
	// Given — command selected so logs pane has a path to show
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds, svc-a selected

	// When
	pane := m.renderLogsPane()

	// Then — the log path must appear in the rendered pane
	if !strings.Contains(pane, "/tmp/logs/svc-a.log") {
		t.Errorf("expected pane to contain log path, got:\n%s", pane)
	}
}

func TestRenderFooter_whenNotice_showsNoticeInsteadOfShortcuts(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.notice = "opened /tmp/logs/svc-a.log"

	// When
	footer := m.renderFooter()

	// Then — notice is shown, not the normal shortcuts
	if !strings.Contains(footer, "opened /tmp/logs/svc-a.log") {
		t.Errorf("expected footer to show notice, got %q", footer)
	}
	if strings.Contains(footer, "↑/↓") {
		t.Errorf("expected shortcuts to be suppressed while notice is shown, got %q", footer)
	}
}

func TestRenderFooter_envsFocused_showsStartStop(t *testing.T) {
	// Given — default focus is the envs pane.
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if !strings.Contains(footer, "s start") || !strings.Contains(footer, "x stop") {
		t.Errorf("expected footer to contain 's start' and 'x stop', got %q", footer)
	}
}

// shortcutAvailability looks up a shortcutEntries() entry by its exact label.
func shortcutAvailability(t *testing.T, entries []shortcut, label string) bool {
	t.Helper()
	for _, e := range entries {
		if e.label == label {
			return e.available
		}
	}
	t.Fatalf("no shortcut entry with label %q, got %+v", label, entries)
	return false
}

func TestShortcutEntries_whenEnvRunning_marksStartUnavailable(t *testing.T) {
	// Given the selected environment is already running.
	ctrl := newFakeController()
	ctrl.envState["alpha"] = engine.EnvRunning
	m := seed(New(ctrl))

	// When
	entries := m.shortcutEntries()

	// Then
	if shortcutAvailability(t, entries, "s start") {
		t.Error("expected 's start' to be unavailable for a running env")
	}
	if !shortcutAvailability(t, entries, "x stop") {
		t.Error("expected 'x stop' to be available for a running env")
	}
}

func TestShortcutEntries_whenEnvStopped_marksStopUnavailable(t *testing.T) {
	// Given the selected environment is stopped (the default fixture state).
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	entries := m.shortcutEntries()

	// Then
	if !shortcutAvailability(t, entries, "s start") {
		t.Error("expected 's start' to be available for a stopped env")
	}
	if shortcutAvailability(t, entries, "x stop") {
		t.Error("expected 'x stop' to be unavailable for a stopped env")
	}
}

func TestShortcutEntries_whenCmdPending_marksRestartUnavailable(t *testing.T) {
	// Given the selected command was never started (the default fixture state).
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	entries := m.shortcutEntries()

	// Then
	if shortcutAvailability(t, entries, "R restart") {
		t.Error("expected 'R restart' to be unavailable for a pending command")
	}
}

func TestShortcutEntries_whenCmdHealthy_marksRestartAvailable(t *testing.T) {
	// Given the selected command is running.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdHealthy
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	entries := m.shortcutEntries()

	// Then
	if !shortcutAvailability(t, entries, "R restart") {
		t.Error("expected 'R restart' to be available for a healthy command")
	}
}

func TestShortcutEntries_whenNoLogPath_marksOpenLogUnavailable(t *testing.T) {
	// Given the selected command's log path is unknown.
	ctrl := newFakeController()
	ctrl.logPaths = map[string]string{"svc-a": ""}
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	entries := m.shortcutEntries()

	// Then
	if shortcutAvailability(t, entries, "^L open") {
		t.Error("expected '^L open' to be unavailable with no log path")
	}
}

func TestRenderFooter_cmdsFocused_hidesStartStop(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "s start") || strings.Contains(footer, "x stop") {
		t.Errorf("expected footer to hide 's start'/'x stop' outside envs pane, got %q", footer)
	}
}

func TestRenderFooter_logsFocused_hidesStartStop(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "s start") || strings.Contains(footer, "x stop") {
		t.Errorf("expected footer to hide 's start'/'x stop' outside envs pane, got %q", footer)
	}
}

func TestRenderFooter_cmdsFocused_showsRestart(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if !strings.Contains(footer, "R restart") {
		t.Errorf("expected footer to contain 'R restart', got %q", footer)
	}
}

func TestRenderFooter_envsFocused_hidesRestart(t *testing.T) {
	// Given — default focus is the envs pane.
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "R restart") {
		t.Errorf("expected footer to hide 'R restart' outside cmds pane, got %q", footer)
	}
}

func TestRenderFooter_logsFocused_hidesRestart(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "R restart") {
		t.Errorf("expected footer to hide 'R restart' outside cmds pane, got %q", footer)
	}
}

func TestRenderFooter_logsFocused_showsOpenHint(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if !strings.Contains(footer, "^L open") {
		t.Errorf("expected footer to contain '^L open', got %q", footer)
	}
}

func TestRenderFooter_envsFocused_hidesOpenHint(t *testing.T) {
	// Given — default focus is the envs pane.
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "^L open") {
		t.Errorf("expected footer to hide '^L open' outside logs pane, got %q", footer)
	}
}

func TestRenderFooter_cmdsFocused_hidesOpenHint(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "^L open") {
		t.Errorf("expected footer to hide '^L open' outside logs pane, got %q", footer)
	}
}

func TestView_rendersLogsTitleForSelectedEnvAndCommand(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	view := ansi.Strip(m.render())

	// Then — default selection is alpha / svc-a
	if !strings.Contains(view, "alpha > svc-a") {
		t.Errorf("expected view to contain logs title %q, got:\n%s", "alpha > svc-a", view)
	}
}

func TestView_logsTitleTracksEnvCursorWhileEnvPanelFocused(t *testing.T) {
	// Given — focus starts on the env pane (focusEnvs is the initial state)
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When — move env cursor down to beta (which has command svc-c)
	m = sendSpecialKey(m, tea.KeyDown)
	view := ansi.Strip(m.render())

	// Then — title must reflect the new env + its first command
	if !strings.Contains(view, "beta > svc-c") {
		t.Errorf("expected view to contain logs title %q after moving env cursor, got:\n%s", "beta > svc-c", view)
	}
	if strings.Contains(view, "alpha > svc-a") {
		t.Errorf("expected stale title %q to be gone after moving env cursor, but found it in:\n%s", "alpha > svc-a", view)
	}
}

func TestCmdStateIndicator_whenTimedOut_containsHourglassGlyph(t *testing.T) {
	// Given / When
	indicator := cmdStateIndicator(engine.CmdTimeout, 0)

	// Then: the rendered indicator must contain the hourglass glyph and be
	// identical across frames (it is not animated).
	if !strings.Contains(indicator, "⧖") {
		t.Errorf("expected indicator for CmdTimeout to contain '⧖', got: %q", indicator)
	}

	frame1 := cmdStateIndicator(engine.CmdTimeout, 1)
	if indicator != frame1 {
		t.Errorf("expected CmdTimeout indicator to be frame-stable, got %q (frame 0) vs %q (frame 1)", indicator, frame1)
	}
}

func TestCmdStateIndicator_whenRestarting_producesDistinctOutputAcrossFrames(t *testing.T) {
	// Given / When
	frame0 := cmdStateIndicator(engine.CmdRestarting, 0)
	frame1 := cmdStateIndicator(engine.CmdRestarting, 1)

	// Then: the indicator is animated (spinner), so it must differ across frames.
	if frame0 == frame1 {
		t.Errorf("expected CmdRestarting indicator to vary across frames, got %q for both", frame0)
	}
}

func TestRenderCmdPane_whenRestarting_showsRetryCounter(t *testing.T) {
	// Given a command in CmdRestarting with retries=1, max=3.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdRestarting
	ctrl.cmdRetries = map[string][2]int{
		"svc-a": {1, 3},
	}
	m := seed(New(ctrl))

	// When the command pane is rendered.
	pane := m.renderCmdPane(40, 10)

	// Then the retry counter appears next to the command name.
	if !strings.Contains(pane, "svc-a") {
		t.Fatalf("expected pane to contain command name 'svc-a', got: %q", pane)
	}
	if !strings.Contains(ansi.Strip(pane), "(retry 2/3)") {
		t.Errorf("expected pane to contain '(retry 2/3)', got: %q", ansi.Strip(pane))
	}
}

func TestRenderCmdPane_whenFailedAfterRetries_showsFailedCount(t *testing.T) {
	// Given a command in CmdError that exhausted 3 retries.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdError
	ctrl.cmdRetries = map[string][2]int{
		"svc-a": {3, 3},
	}
	m := seed(New(ctrl))

	// When the command pane is rendered.
	pane := m.renderCmdPane(40, 10)

	// Then the failure annotation appears next to the command name.
	if !strings.Contains(ansi.Strip(pane), "svc-a (failed after 3 retries)") {
		t.Errorf("expected pane to contain 'svc-a (failed after 3 retries)', got: %q", ansi.Strip(pane))
	}
}

func TestRenderCmdPane_whenErrorWithNoRetries_showsNoSuffix(t *testing.T) {
	// Given a command in CmdError that never auto-restarted (attempts=0).
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdError
	// cmdRetries not set → (0, 0)
	m := seed(New(ctrl))

	// When the command pane is rendered.
	pane := m.renderCmdPane(40, 10)

	// Then no retry annotation is appended.
	stripped := ansi.Strip(pane)
	if strings.Contains(stripped, "(failed") || strings.Contains(stripped, "(retry") {
		t.Errorf("expected no retry suffix for error with 0 retries, got: %q", stripped)
	}
}

func TestRenderCmdPane_whenUnmanaged_showsUnmanagedLabel(t *testing.T) {
	// Given a healthy command adopted as unmanaged.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdHealthy
	ctrl.cmdUnmanaged = map[string]bool{"svc-a": true}
	m := seed(New(ctrl))

	// When the command pane is rendered.
	pane := m.renderCmdPane(40, 10)

	// Then the unmanaged annotation appears next to the command name.
	if !strings.Contains(ansi.Strip(pane), "svc-a (unmanaged)") {
		t.Errorf("expected pane to contain 'svc-a (unmanaged)', got: %q", ansi.Strip(pane))
	}
}

func TestRenderCmdPane_whenNotUnmanaged_showsNoUnmanagedLabel(t *testing.T) {
	// Given a healthy command that env-starter launched itself.
	ctrl := newFakeController()
	ctrl.cmdState["svc-a"] = engine.CmdHealthy
	// cmdUnmanaged not set → false
	m := seed(New(ctrl))

	// When the command pane is rendered.
	pane := m.renderCmdPane(40, 10)

	// Then no unmanaged annotation is appended.
	if strings.Contains(ansi.Strip(pane), "(unmanaged)") {
		t.Errorf("expected no unmanaged suffix for a managed command, got: %q", ansi.Strip(pane))
	}
}

// ── Config hot-reload model tests ─────────────────────────────────────────────

func TestRenderFooter_normalState_hidesConfigReloadHint(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "(c)") {
		t.Errorf("expected '(c)' not to appear in the normal footer, got %q", footer)
	}
}

func TestRenderFooter_logsFocused_showsRefreshLogsLabel(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.focused = focusLogs

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if !strings.Contains(footer, "r refresh logs") {
		t.Errorf("expected footer to contain 'r refresh logs', got %q", footer)
	}
}

func TestRenderFooter_envsFocused_hidesRefreshLogsLabel(t *testing.T) {
	// Given — default focus is the envs pane.
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "r refresh logs") {
		t.Errorf("expected footer to hide 'r refresh logs' outside logs pane, got %q", footer)
	}
}

func TestRenderFooter_cmdsFocused_hidesRefreshLogsLabel(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then
	if strings.Contains(footer, "r refresh logs") {
		t.Errorf("expected footer to hide 'r refresh logs' outside logs pane, got %q", footer)
	}
}

func TestRenderFooter_whenConfigDirty_showsBanner(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — static shortcuts must be suppressed; banner must show.
	if !strings.Contains(footer, "Press (c) to reload config") {
		t.Errorf("expected footer to contain 'Press (c) to reload config', got %q", footer)
	}
	if strings.Contains(footer, "r refresh logs") {
		t.Errorf("expected static shortcuts to be suppressed while banner is shown, got %q", footer)
	}
}

func TestRenderFooter_whenConfigParseErr_showsParseErrorBanner(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configParseErr = "yaml: line 3: did not find expected key"

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — parse error banner shown; reload offer suppressed.
	if !strings.Contains(footer, "cannot be parsed") {
		t.Errorf("expected footer to contain 'cannot be parsed', got %q", footer)
	}
	if strings.Contains(footer, "Press (c)") {
		t.Errorf("expected reload offer to be suppressed when parse error is shown, got %q", footer)
	}
	if strings.Contains(footer, "r refresh logs") {
		t.Errorf("expected static shortcuts to be suppressed, got %q", footer)
	}
}

func TestRenderFooter_whenConfigParseErrTakesPriorityOverDirty(t *testing.T) {
	// Given — both set simultaneously (shouldn't normally happen, but guard it).
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configParseErr = "parse failure"
	m.configDirty = true

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — parse error wins.
	if !strings.Contains(footer, "cannot be parsed") {
		t.Errorf("expected parse error banner, got %q", footer)
	}
	if strings.Contains(footer, "Press (c)") {
		t.Errorf("expected reload offer to be suppressed, got %q", footer)
	}
}

func TestRenderFooter_whenConfigNotApproved_showsNotApprovedBanner(t *testing.T) {
	// Given — a trust error, not a YAML parse error: the file is well-formed
	// but was refused by the approval gate.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configParseErr = `config "/path/config.yaml" has changed since it was approved; run ` + "`env-starter allow`" + ` to review and approve it`
	m.configApproved = false

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — the distinct "not approved" banner is shown, not the generic
	// "cannot be parsed" one, and the reload offer stays suppressed.
	if !strings.Contains(footer, "not approved") {
		t.Errorf("expected footer to contain 'not approved', got %q", footer)
	}
	if strings.Contains(footer, "cannot be parsed") {
		t.Errorf("expected the generic parse-error banner to be suppressed, got %q", footer)
	}
	if strings.Contains(footer, "Press (c)") {
		t.Errorf("expected reload offer to be suppressed, got %q", footer)
	}
}

func TestUpdate_configScanMsg_whenNotApprovedError_clearsConfigApproved(t *testing.T) {
	// Given — the controller's ConfigChanged reports the exact error type
	// resolveConfig's loadFn returns for an unapproved/changed config (see
	// internal/trust.NotApprovedError).
	ctrl := newFakeController()
	ctrl.configParseErr = &trust.NotApprovedError{Path: "/path/config.yaml", Reason: trust.ReasonUnknown}
	m := seed(New(ctrl))

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then — the model detects the trust error via errors.As, not just the
	// error string, so renderFooter can show the distinct banner.
	if m.configApproved {
		t.Error("expected configApproved to be false for a *trust.NotApprovedError")
	}
	if !strings.Contains(m.configParseErr, "env-starter allow") {
		t.Errorf("expected configParseErr to carry the actionable message, got %q", m.configParseErr)
	}
}

func TestUpdate_configScanMsg_whenPlainParseError_leavesConfigApprovedTrue(t *testing.T) {
	// Given — an ordinary YAML parse error, not a trust error.
	ctrl := newFakeController()
	ctrl.configParseErr = fmt.Errorf("yaml: line 3: did not find expected key")
	m := seed(New(ctrl))
	m.configApproved = false // stale from a previous scan; must be cleared

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then
	if !m.configApproved {
		t.Error("expected configApproved to be true for a plain parse error")
	}
}

func TestRenderFooter_whenConfigDirtyWithReloadErr_showsErrorInBanner(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true
	m.reloadErr = "syntax error on line 5"

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — both the banner and the error must be present.
	if !strings.Contains(footer, "Press (c) to reload config") {
		t.Errorf("expected footer to contain reload prompt, got %q", footer)
	}
	if !strings.Contains(footer, "syntax error on line 5") {
		t.Errorf("expected footer to contain reload error, got %q", footer)
	}
}

func TestRenderFooter_whenVersionSet_showsVersionAtRight(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.version = "1.2.3"

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — version is appended after the shortcuts legend, right-aligned.
	if !strings.HasSuffix(footer, "v1.2.3") {
		t.Errorf("expected footer to end with version 'v1.2.3', got %q", footer)
	}
	if !strings.Contains(footer, "shutdown") {
		t.Errorf("expected footer to still contain shortcuts legend, got %q", footer)
	}
}

func TestRenderFooter_whenShortcutUnavailable_rendersItDimmed(t *testing.T) {
	// Given two models that differ only in whether "s start" can act on the
	// selected env.
	stopped := newFakeController()
	m1 := seed(New(stopped)) // "alpha" is stopped by default: s is available

	running := newFakeController()
	running.envState["alpha"] = engine.EnvRunning
	m2 := seed(New(running)) // s is unavailable

	// When — render without stripping ANSI, since the styling itself is what's
	// under test.
	footer1 := m1.renderFooter()
	footer2 := m2.renderFooter()

	// Then — the same label is styled differently depending on availability.
	rendered1 := shortcutStyle.Render("s start")
	rendered2 := shortcutDisabledStyle.Render("s start")
	if !strings.Contains(footer1, rendered1) {
		t.Errorf("expected available 's start' to render as %q, got footer %q", rendered1, footer1)
	}
	if !strings.Contains(footer2, rendered2) {
		t.Errorf("expected unavailable 's start' to render as %q, got footer %q", rendered2, footer2)
	}
	if strings.Contains(footer2, rendered1) {
		t.Errorf("expected unavailable 's start' not to use the available style, got footer %q", footer2)
	}
}

func TestRenderFooter_whenVersionEmpty_omitsVersion(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — no version set means the footer is exactly the shortcuts legend.
	if want := ansi.Strip(m.shortcutsLegend()); footer != want {
		t.Errorf("expected footer to be unchanged when no version is set, got %q, want %q", footer, want)
	}
}

func TestRenderFooter_whenNarrowWidth_omitsVersionRatherThanOverflow(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.version = "1.2.3-does-not-fit-in-a-narrow-terminal"
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 20, Height: 40})
	m = updated.(Model)

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — falls back to the plain legend instead of overflowing the width.
	if want := ansi.Strip(m.shortcutsLegend()); footer != want {
		t.Errorf("expected version to be omitted when it doesn't fit, got %q, want %q", footer, want)
	}
}

func TestUpdate_whenConfigScanMsg_andChanged_setsDirty(t *testing.T) {
	// Given
	ctrl := newFakeController()
	ctrl.configChanged = true
	m := seed(New(ctrl))
	if m.configDirty {
		t.Fatal("expected configDirty to start false")
	}

	// When
	updated, cmd := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then — dirty flag set; scan re-armed.
	if !m.configDirty {
		t.Error("expected configDirty to be set after configScanMsg with change detected")
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd (re-arm configScanCmd) from configScanMsg")
	}
}

func TestUpdate_whenConfigScanMsg_andNotChanged_staysClean(t *testing.T) {
	// Given
	ctrl := newFakeController()
	ctrl.configChanged = false
	m := seed(New(ctrl))

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then
	if m.configDirty {
		t.Error("expected configDirty to remain false when no change detected")
	}
}

func TestUpdate_whenConfigScanMsg_andAlreadyDirty_remainsDirty(t *testing.T) {
	// Given — controller reports dirty=true (unchanged file, still pending).
	ctrl := newFakeController()
	ctrl.configChanged = true
	m := seed(New(ctrl))
	m.configDirty = true

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then — dirty must remain.
	if !m.configDirty {
		t.Error("expected configDirty to remain true when controller still reports dirty")
	}
}

func TestUpdate_whenConfigScanMsg_andParseError_setsParseErrAndClearsDirty(t *testing.T) {
	// Given — controller reports a parse error (file changed but is invalid).
	ctrl := newFakeController()
	ctrl.configChanged = false
	ctrl.configParseErr = fmt.Errorf("yaml: line 3: did not find expected key")
	m := seed(New(ctrl))
	m.configDirty = true // had a prior valid change
	m.reloadErr = "old error"

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then — parse error surfaced; dirty and reload error cleared.
	if m.configDirty {
		t.Error("expected configDirty to be cleared when parse error is detected")
	}
	if !strings.Contains(m.configParseErr, "line 3") {
		t.Errorf("expected configParseErr to contain the error, got %q", m.configParseErr)
	}
	if m.reloadErr != "" {
		t.Errorf("expected reloadErr to be cleared by parse error, got %q", m.reloadErr)
	}
}

func TestUpdate_whenConfigScanMsg_andParseErrorCleared_clearsParseErr(t *testing.T) {
	// Given — controller now reports clean (file fixed, but same as running).
	ctrl := newFakeController()
	ctrl.configChanged = false
	ctrl.configParseErr = nil
	m := seed(New(ctrl))
	m.configParseErr = "stale yaml error"

	// When
	updated, _ := m.Update(configScanMsg{})
	m = updated.(Model)

	// Then — stale parse error cleared.
	if m.configParseErr != "" {
		t.Errorf("expected configParseErr to be cleared, got %q", m.configParseErr)
	}
}

func TestKeyC_whenParseErr_isNoop(t *testing.T) {
	// Given — dirty is true but file currently has a parse error.
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true
	m.configParseErr = "yaml: unexpected key"

	// When
	_, cmd := m.Update(keyMsg("c"))

	// Then — Reload must not be invoked.
	if ctrl.reloadCalled {
		t.Error("expected Reload not to be called when configParseErr is set")
	}
	_ = cmd
}

func TestKeyC_whenNotDirty_isNoop(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = false

	// When
	updated, cmd := m.Update(keyMsg("c"))
	m = updated.(Model)

	// Then — Reload must not be invoked; no cmd returned.
	if ctrl.reloadCalled {
		t.Error("expected Reload not to be called when configDirty is false")
	}
	_ = m
	_ = cmd
}

func TestKeyC_whenDirty_invokesReloadAndReturnsCmd(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true

	// When
	_, cmd := m.Update(keyMsg("c"))

	// Then — a background cmd must be returned (it will call Reload).
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from pressing 'c' when dirty")
	}
	// Execute the cmd to simulate the background goroutine completing.
	result := cmd()
	if _, ok := result.(reloadDoneMsg); !ok {
		t.Errorf("expected reloadDoneMsg from the reload cmd, got %T", result)
	}
	if !ctrl.reloadCalled {
		t.Error("expected Reload to have been called by the cmd")
	}
}

func TestUpdate_whenReloadDoneMsg_success_clearsDirtyAndParseErr(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true
	m.reloadErr = "old error"
	m.configParseErr = "old parse error"

	// When
	updated, cmd := m.Update(reloadDoneMsg{err: nil})
	m = updated.(Model)

	// Then — dirty, reload error, and parse error all cleared. The Cmd
	// returned is noticeResetCmd, not a re-armed waitForEvent: the engine is
	// mutated in place (see ApplyConfig), never swapped, so the waitForEvent
	// loop armed at startup is already reading the right channel.
	if m.configDirty {
		t.Error("expected configDirty to be cleared after successful reload")
	}
	if m.reloadErr != "" {
		t.Errorf("expected reloadErr to be cleared, got %q", m.reloadErr)
	}
	if m.configParseErr != "" {
		t.Errorf("expected configParseErr to be cleared, got %q", m.configParseErr)
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd (noticeResetCmd) after successful reload")
	}
	if m.notice == "" {
		t.Error("expected a notice describing the applied reload")
	}
}

func TestUpdate_whenReloadDoneMsg_error_keepsDirtyAndSetsErr(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.configDirty = true

	// When
	updated, _ := m.Update(reloadDoneMsg{err: fmt.Errorf("parse error")})
	m = updated.(Model)

	// Then — dirty remains; error text captured for the banner.
	if !m.configDirty {
		t.Error("expected configDirty to remain true after failed reload")
	}
	if !strings.Contains(m.reloadErr, "parse error") {
		t.Errorf("expected reloadErr to contain 'parse error', got %q", m.reloadErr)
	}
}

func TestClampCursors_whenCursorsOutOfRange_clampsToLast(t *testing.T) {
	// Given — controller with 1 env and 1 command; cursors pointing past end.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{{Name: "solo"}}
	ctrl.commands = map[string][]string{"solo": {"cmd-a"}}
	ctrl.envState = map[string]engine.EnvState{"solo": engine.EnvStopped}
	ctrl.cmdState = map[string]engine.CmdState{"cmd-a": engine.CmdPending}

	m := seed(New(ctrl))
	m.envCursor = 5
	m.cmdCursor = 5

	// When
	m = m.clampCursors()

	// Then — both clamp to 0 (last valid index in a 1-element list).
	if m.envCursor != 0 {
		t.Errorf("expected envCursor clamped to 0, got %d", m.envCursor)
	}
	if m.cmdCursor != 0 {
		t.Errorf("expected cmdCursor clamped to 0, got %d", m.cmdCursor)
	}
}

func TestModel_ctrlD_setsDetachingAndQuits(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Then — detaching flag set, quit command emitted.
	if !m.detaching {
		t.Error("expected detaching to be true after Ctrl+D")
	}
	if m.quitting {
		t.Error("expected quitting to remain false after Ctrl+D (detach does not shut down daemon)")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil cmd (tea.Quit) from Ctrl+D")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg from Ctrl+D, got %T", msg)
	}
}

func TestModel_ctrlC_confirmText_showsShutdownDaemon(t *testing.T) {
	// Given — model with confirmingQuit already set (first Ctrl+C pressed).
	ctrl := newFakeController()
	m := seed(New(ctrl))
	m.confirmingQuit = true

	// When
	footer := ansi.Strip(m.renderFooter())

	// Then — updated wording shown, not the old "Press Ctrl+C again to quit".
	if !strings.Contains(footer, "^C again to shutdown daemon") {
		t.Errorf("expected footer to contain '^C again to shutdown daemon', got %q", footer)
	}
	if strings.Contains(footer, "quit") {
		t.Errorf("expected footer not to contain old 'quit' wording, got %q", footer)
	}
}

// ── Shutdown log panel tests ──────────────────────────────────────────────────

func TestInitShutdownLog_buildsColorPrefixesForAllCommands(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = m.initShutdownLog()

	// Then — every command in the fake controller gets a non-empty ANSI prefix
	wantCmds := []string{"svc-a", "svc-b", "svc-c"}
	for _, cmd := range wantCmds {
		prefix, ok := m.shutdownPrefixes[cmd]
		if !ok {
			t.Errorf("initShutdownLog: no prefix for command %q", cmd)
			continue
		}
		if !strings.Contains(prefix, "["+cmd+"]") {
			t.Errorf("initShutdownLog: prefix for %q missing command name, got %q", cmd, prefix)
		}
		if !strings.Contains(prefix, "\033[") {
			t.Errorf("initShutdownLog: prefix for %q contains no ANSI escape, got %q", cmd, prefix)
		}
	}
}

func TestInitShutdownLog_deduplicatesSharedCommands(t *testing.T) {
	// Given — two envs sharing a command
	ctrl := newFakeController()
	ctrl.commands = map[string][]string{
		"alpha": {"shared", "only-alpha"},
		"beta":  {"shared", "only-beta"},
	}
	m := seed(New(ctrl))

	// When
	m = m.initShutdownLog()

	// Then — "shared" appears exactly once
	count := 0
	for _, cmd := range m.shutdownCmds {
		if cmd == "shared" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("initShutdownLog: expected 'shared' once in shutdownCmds, got %d times", count)
	}
}

func TestRefreshShutdownLogs_whenNewLines_appendsPrefixedLines(t *testing.T) {
	// Given
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {"line one", "line two"},
	}
	m := seed(New(ctrl))
	m = m.initShutdownLog()

	// When
	m = m.refreshShutdownLogs()

	// Then — both lines appear with the [svc-a] prefix
	joined := strings.Join(m.shutdownLogLines, "\n")
	if !strings.Contains(joined, "[svc-a]") {
		t.Errorf("refreshShutdownLogs: expected [svc-a] prefix, got: %q", joined)
	}
	if !strings.Contains(joined, "line one") {
		t.Errorf("refreshShutdownLogs: expected 'line one', got: %q", joined)
	}
	if !strings.Contains(joined, "line two") {
		t.Errorf("refreshShutdownLogs: expected 'line two', got: %q", joined)
	}
}

func TestRefreshShutdownLogs_whenCalledTwice_doesNotDuplicateLines(t *testing.T) {
	// Given
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {"only once"},
	}
	m := seed(New(ctrl))
	m = m.initShutdownLog()

	// When — refresh twice with the same log state
	m = m.refreshShutdownLogs()
	m = m.refreshShutdownLogs()

	// Then — "only once" appears exactly once
	count := 0
	for _, line := range m.shutdownLogLines {
		if strings.Contains(line, "only once") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("refreshShutdownLogs: expected 'only once' exactly once, got %d times", count)
	}
}

func TestRenderShutdown_whenQuitting_containsLogPanel(t *testing.T) {
	// Given — a command with log output, model in quitting state
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {"shutdown output line"},
	}
	m := seed(New(ctrl))
	m.quitting = true
	m = m.initShutdownLog()
	m = m.refreshShutdownLogs()

	// When
	view := ansi.Strip(m.render())

	// Then — log content appears somewhere in the shutdown view
	if !strings.Contains(view, "shutdown output line") {
		t.Errorf("renderShutdown: expected log line in shutdown view, got:\n%s", view)
	}
}

// ── Contextual links overlay ──────────────────────────────────────────────────

func TestRender_whenLastLinesContainUrls_showsContextLinksBlock(t *testing.T) {
	// Given — the selected env (alpha, first cursor pos) has commands svc-a and
	// svc-b; add a URL in the last 5 lines of svc-a and a different one in svc-b.
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {"starting...", "Login at https://sso.example.com/auth"},
		"svc-b": {"ready at http://localhost:8080"},
	}
	m := seed(New(ctrl))

	// When
	view := ansi.Strip(m.render())

	// Then
	if !strings.Contains(view, "Contextual links") {
		t.Error("expected 'Contextual links' overlay in render output")
	}
	if !strings.Contains(view, "https://sso.example.com/auth") {
		t.Error("expected sso URL in contextual links")
	}
	if !strings.Contains(view, "http://localhost:8080") {
		t.Error("expected localhost URL in contextual links")
	}
	if !strings.Contains(view, "[svc-a]") {
		t.Error("expected '[svc-a]' label in contextual links")
	}
	if !strings.Contains(view, "[svc-b]") {
		t.Error("expected '[svc-b]' label in contextual links")
	}
}

func TestRender_whenUrlAgesOutOfLast5Lines_dropsItFromOverlay(t *testing.T) {
	// Given — URL is in position 0 (6 lines ago), followed by 5 plain lines that
	// push it outside the contextLinksWindow. The URL may still appear in the
	// viewport's scrolling log, but must not appear in the overlay.
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {
			"Login at https://old.example.com",               // line 0 — outside window
			"line 1", "line 2", "line 3", "line 4", "line 5", // 5 newer plain lines
		},
	}
	m := seed(New(ctrl))

	// When
	view := ansi.Strip(m.render())

	// Then — the URL aged out and the overlay header should be absent entirely
	if strings.Contains(view, "Contextual links") {
		t.Error("expected no 'Contextual links' overlay when URL is outside the window")
	}
}

func TestRender_whenNoUrls_omitsContextLinksBlock(t *testing.T) {
	// Given — default fake controller has plain log lines with no URLs
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	view := ansi.Strip(m.render())

	// Then
	if strings.Contains(view, "Contextual links") {
		t.Error("expected no 'Contextual links' overlay when logs contain no URLs")
	}
}

func TestRender_withContextLinks_doesNotExceedTerminalHeight(t *testing.T) {
	// Given — multiple URLs so the overlay adds several rows; terminal is 40 rows.
	ctrl := newFakeController()
	ctrl.logs = map[string][]string{
		"svc-a": {
			"https://link1.example.com",
			"https://link2.example.com",
			"https://link3.example.com",
		},
	}
	const termHeight = 40
	m, _ := New(ctrl).Update(tea.WindowSizeMsg{Width: 120, Height: termHeight})

	// When
	view := m.(Model).render()

	// Then — count rendered lines (the raw string, not stripped, so ANSI codes
	// don't affect the split; what matters is the newline count).
	lines := strings.Split(view, "\n")
	if len(lines) > termHeight {
		t.Errorf("render produced %d lines, exceeds terminal height %d", len(lines), termHeight)
	}
}

// ── Env inspector ───────────────────────────────────────────────────────────

func TestUpdate_whenE_withEnvsFocused_opensEnvInspectorForEnvironment(t *testing.T) {
	// Given
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{Key: "FOO", Winning: engine.EnvLayer{Value: "bar", Source: engine.EnvSourceEnvironment}}},
	}
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "e")

	// Then
	if m.envInspector == nil {
		t.Fatal("expected env inspector to be open")
	}
	if !strings.Contains(m.envInspector.title, "alpha") {
		t.Errorf("expected title to mention environment %q, got %q", "alpha", m.envInspector.title)
	}
	if len(m.envInspector.allVars) != 1 || m.envInspector.allVars[0].Key != "FOO" {
		t.Errorf("expected resolved vars for alpha, got %+v", m.envInspector.allVars)
	}
	if m.envInspector.focus != envInspectorFocusTable {
		t.Error("expected the table to have focus by default")
	}
	if m.envInspector.originFilter != envOriginFilterAll {
		t.Error("expected the origin filter to default to \"all\"")
	}
}

func TestUpdate_whenE_withCmdsFocused_opensEnvInspectorForCommand(t *testing.T) {
	// Given a model with the commands pane focused and "svc-a" selected.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"cmd:svc-a": {{Key: "FOO", Winning: engine.EnvLayer{Value: "bar", Source: engine.EnvSourceCommand}}},
	}
	m := seed(New(ctrl))
	m = sendSpecialKey(m, tea.KeyTab) // focus cmds; cmdCursor 0 -> "svc-a"

	// When
	m = sendKey(m, "e")

	// Then
	if m.envInspector == nil {
		t.Fatal("expected env inspector to be open")
	}
	if !strings.Contains(m.envInspector.title, "svc-a") {
		t.Errorf("expected title to mention command %q, got %q", "svc-a", m.envInspector.title)
	}
}

func TestRenderEnvInspectorList_showsKeyAndOriginButNeverTheValue(t *testing.T) {
	// Given an open inspector with a known secret value.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{Key: "SUPER_SECRET_KEY", Winning: engine.EnvLayer{Value: "top-secret-value", Source: engine.EnvSourceEnvironment}}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When
	view := m.render()

	// Then: the key and its origin are shown, but the value never appears —
	// the list screen doesn't render values at all (see the details screen).
	if !strings.Contains(view, "SUPER_SECRET_KEY") {
		t.Error("expected the table to contain the key name")
	}
	if !strings.Contains(view, "environment") {
		t.Error("expected the table to contain the origin label")
	}
	if strings.Contains(view, "top-secret-value") {
		t.Error("expected the list screen to never show the value")
	}
	if strings.Contains(view, envMaskedValue) {
		t.Error("expected the list screen to not even show the masked placeholder — there's no value column")
	}
}

func TestRenderEnvInspectorList_showsOverridesAnnotationForShadowedVar(t *testing.T) {
	// Given a var whose command-sourced value overrides an OS-sourced one.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{
			Key:      "BAR",
			Winning:  engine.EnvLayer{Value: "cmd-value", Source: engine.EnvSourceCommand},
			Shadowed: []engine.EnvLayer{{Value: "user-value", Source: engine.EnvSourceOS}},
		}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When
	view := m.render()

	// Then
	if !strings.Contains(view, "command (overrides user)") {
		t.Errorf("expected the Origin column to annotate the override, got view:\n%s", view)
	}
}

func TestUpdate_envInspectorList_searchFiltersByKeySubstring(t *testing.T) {
	// Given six vars, only some of which contain "foo".
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {
			{Key: "FOO_BAR", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "BAR_FOO", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "AFOOA", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "FOO", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "FAR", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "BAR", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
		},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When moving up from the first table row reaches the search field, and
	// "foo" is typed into it.
	m = sendSpecialKey(m, tea.KeyUp)
	if m.envInspector.focus != envInspectorFocusSearch {
		t.Fatal("expected up from the first row to focus the search field")
	}
	m = typeText(m, "foo")

	// Then: only keys containing "foo" (case-insensitively) remain.
	got := m.envInspector.filteredVars()
	wantKeys := []string{"FOO_BAR", "BAR_FOO", "AFOOA", "FOO"}
	if len(got) != len(wantKeys) {
		t.Fatalf("expected %d filtered vars, got %d: %+v", len(wantKeys), len(got), got)
	}
	for i, want := range wantKeys {
		if got[i].Key != want {
			t.Errorf("filtered[%d] = %q, want %q", i, got[i].Key, want)
		}
	}

	// And: moving back down returns focus to the table's first row.
	m = sendSpecialKey(m, tea.KeyDown)
	if m.envInspector.focus != envInspectorFocusTable || m.envInspector.cursor != 0 {
		t.Error("expected down from the search field to focus the table's first row")
	}
}

func TestUpdate_envInspectorList_searchFocused_lettersQAndEDoNotClose(t *testing.T) {
	// Given the search field is focused.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{"env:alpha": {{Key: "QUEUE"}}}
	m := seed(New(ctrl))
	m = sendKey(m, "e")
	m = sendSpecialKey(m, tea.KeyUp)

	// When typing letters that are close shortcuts everywhere else.
	m = typeText(m, "qe")

	// Then: the overlay stays open and the letters landed in the search box.
	if m.envInspector == nil {
		t.Fatal("expected the inspector to remain open while typing in the search field")
	}
	if m.envInspector.search.Value() != "qe" {
		t.Errorf("expected the search field to contain %q, got %q", "qe", m.envInspector.search.Value())
	}
}

func TestUpdate_envInspectorList_originFacetKeysFilterAndAreTracked(t *testing.T) {
	// Given vars from all three sources.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {
			{Key: "HOME", Winning: engine.EnvLayer{Source: engine.EnvSourceOS}},
			{Key: "APP_ENV", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}},
			{Key: "APP_CMD", Winning: engine.EnvLayer{Source: engine.EnvSourceCommand}},
		},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When F7 (environment) is pressed.
	m = sendSpecialKey(m, tea.KeyF7)

	// Then: only the environment-sourced var remains, and the filter is tracked.
	if m.envInspector.originFilter != envOriginFilter(engine.EnvSourceEnvironment) {
		t.Errorf("expected origin filter to be %q, got %q", engine.EnvSourceEnvironment, m.envInspector.originFilter)
	}
	got := m.envInspector.filteredVars()
	if len(got) != 1 || got[0].Key != "APP_ENV" {
		t.Errorf("expected only APP_ENV after filtering by environment, got %+v", got)
	}

	// When F5 (All) is pressed.
	m = sendSpecialKey(m, tea.KeyF5)

	// Then: every var is visible again.
	if len(m.envInspector.filteredVars()) != 3 {
		t.Errorf("expected all 3 vars after selecting the \"All\" facet, got %d", len(m.envInspector.filteredVars()))
	}
}

func TestUpdate_envInspectorList_enterOpensDetailScreen(t *testing.T) {
	// Given an open inspector on its list screen.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{Key: "FOO_BAR_KEY", Winning: engine.EnvLayer{Value: "bar", Source: engine.EnvSourceCommand}}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When
	m = sendSpecialKey(m, tea.KeyEnter)

	// Then
	if m.envInspector.screen != envInspectorScreenDetail {
		t.Fatal("expected enter to open the details screen")
	}
	if m.envInspector.detail.Key != "FOO_BAR_KEY" {
		t.Errorf("expected the details screen to snapshot the selected row, got %+v", m.envInspector.detail)
	}
}

func TestRenderEnvInspectorDetail_masksByDefaultAndSpaceRevealsValueAndOverrides(t *testing.T) {
	// Given a var with one shadowed layer, on the details screen.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{
			Key:      "FOO_BAR_KEY",
			Winning:  engine.EnvLayer{Value: "top-secret-value", Source: engine.EnvSourceCommand},
			Shadowed: []engine.EnvLayer{{Value: "old-secret-value", Source: engine.EnvSourceOS}},
		}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")
	m = sendSpecialKey(m, tea.KeyEnter)

	// When rendered before revealing.
	view := m.render()

	// Then: masked, no plaintext values leaked.
	if strings.Contains(view, "top-secret-value") {
		t.Error("expected the value to be masked before reveal")
	}
	if strings.Contains(view, "old-secret-value") {
		t.Error("expected the overridden value to be masked before reveal")
	}
	if !strings.Contains(view, envMaskedValue) {
		t.Error("expected the masked placeholder to be shown")
	}

	// When space toggles reveal.
	m = sendSpecialKey(m, tea.KeySpace)
	view = m.render()

	// Then: both the value and its override are shown in plaintext.
	if !strings.Contains(view, "top-secret-value") {
		t.Error("expected the value to be revealed")
	}
	if !strings.Contains(view, "old-secret-value") {
		t.Error("expected the overridden value to be revealed")
	}
	if !strings.Contains(view, "user") {
		t.Error("expected the overridden layer's origin (OS -> \"user\") to be shown")
	}
}

func TestRenderEnvInspectorDetail_revealDoesNotShiftBlockHorizontally(t *testing.T) {
	// Given a details screen for a value long enough to have previously
	// widened — and re-centered — the block once revealed.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{
			Key:     "DOCKER_HOST",
			Winning: engine.EnvLayer{Value: "unix:///Users/alban.dericbourg/.colima/default/docker.sock", Source: engine.EnvSourceOS},
		}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")
	m = sendSpecialKey(m, tea.KeyEnter)
	maskedIndent := valueLineIndent(t, ansi.Strip(m.render()))

	// When revealed.
	m = sendSpecialKey(m, tea.KeySpace)
	revealedIndent := valueLineIndent(t, ansi.Strip(m.render()))

	// Then: the block's horizontal position (indent before the "Value" line)
	// is unchanged — only its content wraps, never the block itself.
	if maskedIndent != revealedIndent {
		t.Errorf("details block shifted horizontally: masked indent %d, revealed indent %d", maskedIndent, revealedIndent)
	}
}

// valueLineIndent returns the column at which the "Value" line's text starts
// in a stripped (ANSI-free) view.
func valueLineIndent(t *testing.T, view string) int {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Value") {
			return strings.Index(line, "Value")
		}
	}
	t.Fatal("expected a line containing \"Value\"")
	return -1
}

func TestUpdate_envInspectorDetail_escReturnsToListPreservingFilters(t *testing.T) {
	// Given the details screen open with a search query and origin filter set.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{
		"env:alpha": {{Key: "FOO", Winning: engine.EnvLayer{Source: engine.EnvSourceEnvironment}}},
	}
	m := seed(New(ctrl))
	m = sendKey(m, "e")
	m = sendSpecialKey(m, tea.KeyUp)
	m = typeText(m, "fo")
	m = sendSpecialKey(m, tea.KeyDown)
	m = sendSpecialKey(m, tea.KeyF7) // environment
	m = sendSpecialKey(m, tea.KeyEnter)
	if m.envInspector.screen != envInspectorScreenDetail {
		t.Fatal("expected the details screen to be open")
	}

	// When
	m = sendSpecialKey(m, tea.KeyEscape)

	// Then
	if m.envInspector.screen != envInspectorScreenList {
		t.Error("expected esc to return to the list screen")
	}
	if m.envInspector.search.Value() != "fo" {
		t.Errorf("expected the search query to be preserved, got %q", m.envInspector.search.Value())
	}
	if m.envInspector.originFilter != envOriginFilter(engine.EnvSourceEnvironment) {
		t.Errorf("expected the origin filter to be preserved, got %q", m.envInspector.originFilter)
	}
}

func TestUpdate_envInspector_escFromListCloses(t *testing.T) {
	// Given an open inspector.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{"env:alpha": {{Key: "FOO"}}}
	m := seed(New(ctrl))
	m = sendKey(m, "e")
	if m.envInspector == nil {
		t.Fatal("expected env inspector to be open")
	}

	// When
	m = sendSpecialKey(m, tea.KeyEscape)

	// Then
	if m.envInspector != nil {
		t.Error("expected env inspector to be closed")
	}
}

func TestUpdate_envInspectorOpen_blocksOtherKeys(t *testing.T) {
	// Given an open inspector with the table focused.
	ctrl := newFakeController()
	ctrl.resolvedEnv = map[string][]engine.ResolvedEnvVar{"env:alpha": {{Key: "FOO"}}}
	m := seed(New(ctrl))
	m = sendKey(m, "e")

	// When a key unrelated to the inspector is pressed (e.g. "s" for start).
	m = sendKey(m, "s")

	// Then: the inspector stays open and the unrelated action did not fire.
	if m.envInspector == nil {
		t.Error("expected env inspector to remain open")
	}
	if len(ctrl.startedEnvs) != 0 {
		t.Errorf("expected no StartEnvironment call while inspector is open, got %v", ctrl.startedEnvs)
	}
}
