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
)

// ── Fake controller ───────────────────────────────────────────────────────────

type fakeController struct {
	envs       []engine.EnvInfo
	commands   map[string][]string
	envState   map[string]engine.EnvState
	cmdState   map[string]engine.CmdState
	cmdRetries map[string][2]int // [attempts, max]
	logs       map[string][]string
	events     chan engine.Event
	stopping   []engine.StoppingCommand

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

func (f *fakeController) Logs(cmd string) []string { return f.logs[cmd] }

func (f *fakeController) LogPath(cmd string) string { return "/tmp/logs/" + cmd + ".log" }

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

func TestUpdate_whenX_callsStopEnvironment(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	m = sendKey(m, "x")

	// Then
	if len(ctrl.stoppedEnvs) != 1 || ctrl.stoppedEnvs[0] != "alpha" {
		t.Errorf("expected StopEnvironment('alpha'), got %v", ctrl.stoppedEnvs)
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

func TestUpdate_whenR_withCmdsFocused_callsRestartCommand(t *testing.T) {
	// Given a model with the commands pane focused and the first command selected.
	ctrl := newFakeController()
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

func TestUpdate_whenTickMsg_advancesSpinnerFrame(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))
	initial := m.spinnerFrame

	// When
	updated, _ := m.Update(tickMsg{})
	m = updated.(Model)

	// Then
	if m.spinnerFrame != initial+1 {
		t.Errorf("expected spinnerFrame %d, got %d", initial+1, m.spinnerFrame)
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

	// Then — dirty, reload error, and parse error all cleared; event listener re-armed.
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
		t.Error("expected a non-nil cmd (re-arm waitForEvent) after successful reload")
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
