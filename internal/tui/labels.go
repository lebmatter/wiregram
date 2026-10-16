package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// labelPicker is the modal opened from the sidebar's "Labels…" row: every
// label of the account, each ticked when it is in the sidebar. Space shows
// or hides one there; Enter goes to one, showing it first. Opened to move
// mail (move), it lists the folders mail can go to and Enter picks one.
type labelPicker struct {
	st     *styles
	lb     backend.Labeler
	move   bool
	labels []backend.Label
	input  textinput.Model
	sel    int // index into shown()
	notice string
	dirty  bool // a label was shown or hidden since the sidebar last reloaded
	width  int
	height int
}

// labelPickerTop is how many lines sit above the labels: the boxed filter and
// the heading.
const labelPickerTop = 4

func newLabelPicker(st *styles, lb backend.Labeler) (*labelPicker, error) {
	labels, err := lb.Labels()
	if err != nil {
		return nil, err
	}
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "filter labels…"
	in.CharLimit = 64
	m := &labelPicker{st: st, lb: lb, labels: labels, input: in}
	m.input.Focus()
	return m, nil
}

// newMovePicker lists where mail in the folder from can move: the folders
// named first, then every label.
func newMovePicker(st *styles, lb backend.Labeler, first []string, from string) (*labelPicker,
	error) {
	m, err := newLabelPicker(st, lb)
	if err != nil {
		return nil, err
	}
	var labels []backend.Label
	for _, name := range first {
		labels = append(labels, backend.Label{Name: name})
	}
	m.labels = append(labels, m.labels...)
	m.labels = slices.DeleteFunc(m.labels, func(l backend.Label) bool { return l.Name == from })
	m.move = true
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
	m.dirty = true
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
		if m.move {
			return shown[m.sel].Name, true, nil
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
		if !m.move {
			m.toggle(m.sel)
		}
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
	m.input.SetWidth(max(1, inner-4))
	shown := m.shown()
	title, heading := "labels", "labels"
	if m.move {
		title, heading = "move to", "folders"
	}
	if len(shown) > 0 {
		heading += fmt.Sprintf(" · %d", len(shown))
	}
	rule := m.st.dim.Render(textfmt.ClipLine(heading, inner))
	if m.notice != "" {
		rule = styleError.Render(textfmt.ClipLine(m.notice, inner))
	}
	body := append(m.st.modalField("", m.input.View(), inner), rule)
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
		mark := ""
		switch {
		case m.move:
		case l.Shown:
			mark = m.st.accent.Render("[x]")
		default:
			mark = m.st.dim.Render("[ ]")
		}
		count := ""
		if l.Unread > 0 {
			count = m.st.dim.Render(fmt.Sprintf("%d unread", l.Unread))
		}
		room := max(1, inner-modalMark-4-textfmt.DisplayWidth(count)-1)
		name := truncate(textfmt.Plain(l.Name), room)
		if i == m.sel {
			name = m.st.brand.Render(name)
		}
		if mark != "" {
			name = mark + " " + name
		}
		body = append(body, m.st.modalRow("", modalRight(name, count, inner-modalMark),
			i == m.sel, inner))
	}
	body = fillLines(body, m.height-4)
	keys := []helpKey{{"space", "show/hide"}, {"esc", "close"}}
	if m.move {
		keys = []helpKey{{"↑↓", "select"}, {"enter", "move here"}, {"esc", "cancel"}}
	}
	body = append(body, m.st.modalKeys(keys, "", inner)...)
	return m.st.modalBox(title, body, m.width, m.height)
}

// click answers a left click at x, y of the box, counted from its top-left
// corner: a label row shows or hides it, or when moving, picks it; outside
// the box closes it.
func (m *labelPicker) click(x, y int) (pick string, done bool) {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return "", true
	}
	if row := y - 1 - labelPickerTop; row >= 0 && row < m.rows() {
		if i := window(m.sel, m.rows()) + row; i < len(m.shown()) {
			m.sel = i
			if m.move {
				return m.shown()[i].Name, true
			}
			m.toggle(i)
		}
	}
	return "", false
}

func (m *labelPicker) helpKeys() []helpKey {
	if m.move {
		return []helpKey{{"↑↓", "select"}, {"enter", "move here"}, {"esc", "cancel"}}
	}
	return []helpKey{{"↑↓", "select"}, {"space", "show/hide"}, {"esc", "close"}}
}
