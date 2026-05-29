package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/adericbourg/env-starter/internal/engine"
)

// ── Fake controller ───────────────────────────────────────────────────────────

type fakeController struct {
	envs     []engine.EnvInfo
	commands map[string][]string
	envState map[string]engine.EnvState
	cmdState map[string]engine.CmdState
	logs     map[string][]string
	events   chan engine.Event

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

func (f *fakeController) Logs(cmd string) []string { return f.logs[cmd] }

func (f *fakeController) StartEnvironment(env string) error {
	f.startedEnvs = append(f.startedEnvs, env)
	return nil
}

func (f *fakeController) StopEnvironment(env string) error {
	f.stoppedEnvs = append(f.stoppedEnvs, env)
	return nil
}

func (f *fakeController) Events() <-chan engine.Event { return f.events }

func (f *fakeController) Shutdown(_ context.Context) { f.shutdownCalled = true }

// ── Helpers ───────────────────────────────────────────────────────────────────

// seed gives the model a non-zero terminal size so View() renders real content.
func seed(m Model) Model {
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func keyMsg(key string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func specialKey(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func sendKey(m Model, key string) Model {
	updated, _ := m.Update(keyMsg(key))
	return updated.(Model)
}

func sendSpecialKey(m Model, kt tea.KeyType) Model {
	updated, _ := m.Update(specialKey(kt))
	return updated.(Model)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestView_initial_rendersEnvNameAndFooterShortcut(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	view := m.View()

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
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
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
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
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
	view := m.View()

	// Then
	if !strings.Contains(view, "shutting down") {
		t.Errorf("expected view to contain 'shutting down' while quitting, got %q", view)
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
	view := m.View()
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
	view := m.View()

	// Then
	if !strings.Contains(view, "log line 1") {
		t.Error("expected view to contain log line 'log line 1'")
	}
}
