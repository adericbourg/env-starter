package tui

import tea "github.com/charmbracelet/bubbletea"

// Run starts the Bubble Tea program. It is the only place a real tea.Program
// is created; all other code is pure model logic testable without a TTY.
func Run(ctrl Controller) error {
	m := New(ctrl)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
