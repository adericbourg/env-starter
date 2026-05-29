package tui

import (
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

	startedEnvs []string
	stoppedEnvs []string
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

func TestUpdate_whenQ_returnsQuitCmd(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	_, cmd := m.Update(keyMsg("q"))

	// Then
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from 'q'")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestUpdate_whenCtrlC_returnsQuitCmd(t *testing.T) {
	// Given
	ctrl := newFakeController()
	m := seed(New(ctrl))

	// When
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	// Then
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from ctrl+c")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
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
