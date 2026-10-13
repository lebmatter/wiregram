package tui

import "charm.land/bubbletea/v2"

// quitModal asks before wiregram exits, drawn like the message actions modal.
type quitModal struct {
	st  *styles
	row int // 0 is Quit, 1 is Cancel
}

var quitModalRows = []string{"Quit", "Cancel"}

const quitModalWidth = 30

// key moves the selection or picks a row: quit when Quit is chosen, done when
// the modal closes either way.
func (m quitModal) key(msg tea.KeyPressMsg) (q quitModal, quit, done bool) {
	switch msg.String() {
	case "esc", "n":
		return m, false, true
	case "y":
		return m, true, true
	case "up", "k":
		m.row = 0
	case "down", "j":
		m.row = 1
	case "enter":
		return m, m.row == 0, true
	}
	return m, false, false
}

func (m quitModal) size() (int, int) {
	return quitModalWidth, 4 + len(quitModalRows) // borders, the question and a blank
}

func (m quitModal) View() string {
	w, h := m.size()
	body := []string{m.st.subtle.Render("Quit wiregram?"), ""}
	for i, label := range quitModalRows {
		if i == m.row {
			body = append(body, m.st.accent.Render("> ")+m.st.selectedRow.Render(label))
		} else {
			body = append(body, "  "+label)
		}
	}
	return renderBox("Quit", body, w, h, m.st.accentColor)
}

// hit selects the row at row y of the box, counted from its top edge.
func (m quitModal) hit(y int) (quitModal, bool) {
	row := y - 3 // under the top border, the question and the blank
	if row < 0 || row >= len(quitModalRows) {
		return m, false
	}
	m.row = row
	return m, true
}

var quitModalHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "choose"},
	{"y", "quit"},
	{"esc", "cancel"},
}
