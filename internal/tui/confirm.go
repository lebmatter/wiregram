package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/textfmt"
)

// confirmModal asks a yes/no question before an action, such as quitting
// or deleting, drawn like the message actions modal.
type confirmModal struct {
	st       *styles
	title    string
	question string
	action   string // the label of the row that goes ahead
	row      int    // 0 is the action, 1 is Cancel
}

const (
	confirmModalMinWidth = 30
	confirmModalMaxWidth = 50
)

// newConfirmModal starts on the action, or on Cancel when safe is set so a
// stray Enter cannot do what cannot be undone.
func newConfirmModal(st *styles, title, question, action string, safe bool) *confirmModal {
	m := &confirmModal{st: st, title: title, question: question, action: action}
	if safe {
		m.row = 1
	}
	return m
}

func newQuitModal(st *styles) *confirmModal {
	return newConfirmModal(st, "Quit", "Quit wiregram?", "Quit", false)
}

// key moves the selection or picks a row: yes when the action is chosen,
// done when the modal closes either way.
func (m *confirmModal) key(k string) (yes, done bool) {
	switch k {
	case "esc", "n", "N", "ctrl+c":
		return false, true
	case "y", "Y":
		return true, true
	case "up", "k":
		m.row = 0
	case "down", "j":
		m.row = 1
	case "enter":
		return m.row == 0, true
	}
	return false, false
}

func (m *confirmModal) lines() []string {
	w := textfmt.DisplayWidth(m.question) + 4
	w = min(confirmModalMaxWidth, max(confirmModalMinWidth, w))
	return strings.Split(ansi.Wordwrap(m.question, w-4, ""), "\n")
}

func (m *confirmModal) size() (int, int) {
	lines := m.lines()
	w := 0
	for _, l := range lines {
		w = max(w, textfmt.DisplayWidth(l))
	}
	return max(confirmModalMinWidth, w+4), len(lines) + 5 // borders, a blank and two rows
}

func (m *confirmModal) View() string {
	w, h := m.size()
	var body []string
	for _, l := range m.lines() {
		body = append(body, m.st.subtle.Render(l))
	}
	body = append(body, "")
	for i, label := range []string{m.action, "Cancel"} {
		if i == m.row {
			body = append(body, m.st.accent.Render("> ")+m.st.selectedRow.Render(label))
		} else {
			body = append(body, "  "+label)
		}
	}
	return renderBox(m.title, body, w, h, m.st.accentColor)
}

// hit selects the row at row y of the box, counted from its top edge.
func (m *confirmModal) hit(y int) bool {
	row := y - 2 - len(m.lines()) // under the top border, the question and a blank
	if row < 0 || row > 1 {
		return false
	}
	m.row = row
	return true
}

// click answers a left click at x, y of a screen where the modal sits at
// rx, ry: outside it cancels, on a row it picks that row.
func (m *confirmModal) click(x, y, rx, ry int) (yes, done bool) {
	w, h := m.size()
	if x < rx || x >= rx+w || y < ry || y >= ry+h {
		return false, true
	}
	if m.hit(y - ry) {
		return m.key("enter")
	}
	return false, false
}

func (m *confirmModal) helpKeys() []helpKey {
	return []helpKey{
		{"↑↓", "select"},
		{"enter", "choose"},
		{"y", strings.ToLower(m.action)},
		{"esc", "cancel"},
	}
}
