package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// labelPicker is the modal opened from the sidebar's "Labels…" row: every
// label of the account, each ticked when its section is in the sidebar.
// Space shows or hides one there; Enter goes to one, showing it first.
type labelPicker struct {
	st     *styles
	lb     backend.Labeler
	labels []backend.Label
	input  textinput.Model
	sel    int // index into shown()
	notice string
	width  int
	height int
}

// labelPickerTop is how many lines sit above the labels: the filter and a gap.
const labelPickerTop = 2

func newLabelPicker(st *styles, lb backend.Labeler) (*labelPicker, error) {
	labels, err := lb.Labels()
	if err != nil {
		return nil, err
	}
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "type to filter"
	in.CharLimit = 64
	m := &labelPicker{st: st, lb: lb, labels: labels, input: in}
	m.input.Focus()
	return m, nil
}

// shown is the labels matching the filter.
func (m *labelPicker) shown() []backend.Label {
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var out []backend.Label
	for _, l := range m.labels {
		if q == "" || strings.Contains(strings.ToLower(l.Name), q) {
			out = append(out, l)
		}
	}
	return out
}

// toggle shows or hides the label at i of shown() in the sidebar.
func (m *labelPicker) toggle(i int) {
	shown := m.shown()
	if i < 0 || i >= len(shown) {
		return
	}
	m.setShown(shown[i].Name, !shown[i].Shown)
}

func (m *labelPicker) setShown(name string, on bool) bool {
	if err := m.lb.ShowLabel(name, on); err != nil {
		m.notice = textfmt.CleanError(err)
		return false
	}
	for i := range m.labels {
		if m.labels[i].Name == name {
			m.labels[i].Shown = on
		}
	}
	return true
}

// key handles a key press: open names the label to go to, and done closes
// the modal.
func (m *labelPicker) key(msg tea.KeyPressMsg) (open string, done bool, cmd tea.Cmd) {
	m.notice = ""
	switch msg.String() {
	case "esc", "ctrl+c":
		return "", true, nil
	case "enter":
		shown := m.shown()
		if m.sel >= len(shown) {
			return "", false, nil
		}
		if !m.setShown(shown[m.sel].Name, true) {
			return "", false, nil
		}
		return shown[m.sel].Name, true, nil
	case "up":
		m.sel = max(0, m.sel-1)
	case "down":
		m.sel = min(max(0, len(m.shown())-1), m.sel+1)
	case "space", " ":
		m.toggle(m.sel)
	default:
		m.input, cmd = m.input.Update(msg)
		m.sel = min(m.sel, max(0, len(m.shown())-1))
	}
	return "", false, cmd
}

func (m *labelPicker) paste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.sel = 0
	return cmd
}

// resized fits the modal into an area width by height.
func (m *labelPicker) resized(width, height int) {
	m.width = min(56, width-4)
	// Borders, the top lines, the labels, a gap and the status line.
	m.height = max(labelPickerTop+6, min(height-2, labelPickerTop+len(m.labels)+4))
}

// rows is how many labels fit between the filter and the status line.
func (m *labelPicker) rows() int { return max(1, m.height-2-labelPickerTop-2) }

func (m *labelPicker) size() (int, int) { return m.width, m.height }

func (m *labelPicker) View() string {
	inner := m.width - 4
	m.input.SetWidth(max(1, inner-10))
	body := []string{m.st.subtle.Render("Filter    ") + m.input.View(), ""}
	shown := m.shown()
	if len(shown) == 0 {
		text := "  no labels"
		if len(m.labels) == 0 {
			text = "  no labels yet; they are listed when the account connects"
		}
		body = append(body, m.st.dim.Render(textfmt.ClipLine(text, inner)))
	}
	first := window(m.sel, m.rows())
	for i := first; i < min(len(shown), first+m.rows()); i++ {
		l := shown[i]
		box := "[ ] "
		if l.Shown {
			box = "[x] "
		}
		count := ""
		if l.Unread > 0 {
			count = fmt.Sprintf("%d unread", l.Unread)
		}
		room := max(1, inner-2-4-textfmt.DisplayWidth(count)-1)
		name := truncate(textfmt.Plain(l.Name), room)
		gap := strings.Repeat(" ", max(1, inner-2-4-textfmt.DisplayWidth(name)-
			textfmt.DisplayWidth(count)))
		row := box + name
		if i == m.sel {
			row = m.st.accent.Render("> ") + m.st.selectedRow.Render(row)
		} else {
			row = "  " + row
		}
		if count != "" {
			row += gap + m.st.dim.Render(count)
		}
		body = append(body, row)
	}
	for len(body) < m.height-3 {
		body = append(body, "")
	}
	status := m.st.dim.Render(textfmt.ClipLine("space: show/hide in sidebar · enter: go to label",
		inner))
	if m.notice != "" {
		status = styleError.Render(textfmt.ClipLine(m.notice, inner))
	}
	body = append(body, status)
	return renderBox("Labels", body, m.width, m.height, m.st.accentColor)
}

// click answers a left click at x, y of the box, counted from its top-left
// corner: a label row shows or hides it, and outside the box closes it.
func (m *labelPicker) click(x, y int) (done bool) {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return true
	}
	if row := y - 1 - labelPickerTop; row >= 0 && row < m.rows() {
		if i := window(m.sel, m.rows()) + row; i < len(m.shown()) {
			m.sel = i
			m.toggle(i)
		}
	}
	return false
}

func (m *labelPicker) helpKeys() []helpKey {
	return []helpKey{{"↑↓", "select"}, {"space", "show/hide"}, {"enter", "go to"},
		{"esc", "close"}}
}
