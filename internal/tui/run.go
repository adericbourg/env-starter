package tui

import tea "charm.land/bubbletea/v2"

// Run starts the Bubble Tea program and returns whether the exit was a detach
// (Ctrl+D) rather than a shutdown. A detach exit leaves the daemon running.
func Run(ctrl Controller) (detached bool, err error) {
	m := New(ctrl)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if finalModel, ok := final.(Model); ok {
		detached = finalModel.detaching
	}
	return detached, err
}
