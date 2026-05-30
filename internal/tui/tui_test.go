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
	envs      []engine.EnvInfo
	commands  map[string][]string
	envState  map[string]engine.EnvState
	cmdState  map[string]engine.CmdState
	cmdRetries map[string][2]int // [attempts, max]
	logs      map[string][]string
	events    chan engine.Event
	stopping  []engine.StoppingCommand

	startedEnvs    []string
	stoppedEnvs    []string
	shutdownCalled bool
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

func (f *fakeController) Events() <-chan engine.Event { return f.events }

func (f *fakeController) StoppingCommands() []engine.StoppingCommand { return f.stopping }

func (f *fakeController) Shutdown(_ context.Context) { f.shutdownCalled = true }

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
	if !strings.Contains(view, "quit") {
		t.Error("expected view to contain footer shortcut hint 'quit'")
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)

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
	if !strings.Contains(footer, "Ctrl+C") {
		t.Errorf("expected footer to contain 'Ctrl+C' during confirmation, got %q", footer)
	}
	if !strings.Contains(footer, "again") {
		t.Errorf("expected footer to contain 'again' during confirmation, got %q", footer)
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

func TestRenderShutdown_withStoppingCommands_listsCommandWithEnvsSpinnerAndCountdown(t *testing.T) {
	// Given — two envs, both referencing "mariadb"; it is currently stopping with
	// 12 s elapsed out of a 30 s grace.
	ctrl := newFakeController()
	ctrl.envs = []engine.EnvInfo{
		{Name: "dev", Description: "first"},
		{Name: "dev2", Description: "second"},
	}
	ctrl.commands = map[string][]string{
		"dev":  {"mariadb"},
		"dev2": {"mariadb"},
	}
	ctrl.envState = map[string]engine.EnvState{
		"dev":  engine.EnvStopping,
		"dev2": engine.EnvStopping,
	}
	ctrl.stopping = []engine.StoppingCommand{
		{Command: "mariadb", Elapsed: 12 * time.Second, Grace: 30 * time.Second},
	}

	m := seed(New(ctrl))
	m.quitting = true

	// When
	view := ansi.Strip(m.render())

	// Then — env group, command name, and countdown must all appear.
	if !strings.Contains(view, "[dev, dev2]") {
		t.Errorf("expected env group '[dev, dev2]', got:\n%s", view)
	}
	if !strings.Contains(view, "mariadb") {
		t.Errorf("expected command name 'mariadb', got:\n%s", view)
	}
	if !strings.Contains(view, "12s / 30s") {
		t.Errorf("expected countdown '12s / 30s', got:\n%s", view)
	}
}

func TestRenderShutdown_whenStoppingCommandBelongsToSingleEnv_showsSingleEnvGroup(t *testing.T) {
	// Given — only "dev" references "proxy".
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
	if !strings.Contains(view, "[dev]") {
		t.Errorf("expected env group '[dev]', got:\n%s", view)
	}
	if !strings.Contains(view, "proxy") {
		t.Errorf("expected command name 'proxy', got:\n%s", view)
	}
	if !strings.Contains(view, "9s / 30s") {
		t.Errorf("expected countdown '9s / 30s', got:\n%s", view)
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
