package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/agent"
	"wiregram/internal/textfmt"
)

// dmPicker is the modal, opened from a bot's Chats row, that chooses which
// private chats it answers: all of them, or the ticked ones. No DM has two
// bots, so chats another bot answers are shown with its name and cannot be
// ticked, all is unavailable while another bot answers any, and nothing can
// change while another bot answers all.
type dmPicker struct {
	st     *styles
	bot    string
	all    bool
	dms    []agent.AgentChat // the bot's own picks first
	picked map[string]bool
	owners map[string]string // DM ID → the other bot answering it
	allBy  string            // the other bot answering every DM, or ""
	input  textinput.Model
	sel    int // index into shown()
	notice string
	width  int
	height int
}

// dmPickerTop is how many lines sit above the chats: Answers, the filter and a gap.
const dmPickerTop = 3

func newDMPicker(st *styles, bot string, all bool, dms []agent.AgentChat,
	picked map[string]bool, owners map[string]string, allBy string) *dmPicker {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "type to filter"
	in.CharLimit = 64
	m := &dmPicker{st: st, bot: bot, all: all, dms: dms, picked: picked, owners: owners,
		allBy: allBy, input: in}
	if allBy != "" {
		m.notice = "Bot " + allBy + " answers all private chats"
	}
	m.input.Focus()
	return m
}

// shown is the chats matching the filter.
func (m *dmPicker) shown() []agent.AgentChat {
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var out []agent.AgentChat
	for _, c := range m.dms {
		if q == "" || strings.Contains(strings.ToLower(c.Title), q) {
			out = append(out, c)
		}
	}
	return out
}

// chosen is what the modal saves: whether the bot answers all, else its ticks.
func (m *dmPicker) chosen() (bool, []agent.AgentChat) {
	if m.all {
		return true, nil
	}
	var chats []agent.AgentChat
	for _, c := range m.dms {
		if m.picked[c.ID] {
			chats = append(chats, c)
		}
	}
	return false, chats
}

// toggleAll switches between all private chats and the ticked ones.
func (m *dmPicker) toggleAll() {
	if !m.all {
		for _, owner := range m.owners {
			m.notice = "Bot " + owner + " answers some private chats here"
			return
		}
	}
	m.all = !m.all
	m.notice = ""
}

func (m *dmPicker) toggle(i int) {
	shown := m.shown()
	if m.all || i < 0 || i >= len(shown) {
		return
	}
	c := shown[i]
	if owner := m.owners[c.ID]; owner != "" {
		m.notice = "Bot " + owner + " answers " + c.Title
		return
	}
	m.picked[c.ID] = !m.picked[c.ID]
	m.notice = ""
}

// key handles a key press: save when the choice should be stored, done when
// the modal closes.
func (m *dmPicker) key(msg tea.KeyPressMsg) (save, done bool, cmd tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc", "ctrl+c":
		return false, true, nil
	case "enter":
		return m.allBy == "", true, nil
	}
	if m.allBy != "" {
		return false, false, nil
	}
	m.notice = ""
	switch k {
	case "tab":
		m.toggleAll()
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
	return false, false, cmd
}

func (m *dmPicker) paste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.sel = 0
	return cmd
}

// resized fits the modal into an area width by height.
func (m *dmPicker) resized(width, height int) {
	m.width = min(56, width-4)
	if m.allBy != "" {
		m.height = 4 // the two lines of View
		return
	}
	// Borders, the top lines, the chats, a gap and the status line.
	m.height = max(dmPickerTop+6, min(height-2, dmPickerTop+len(m.dms)+4))
}

// rows is how many chats fit between the filter and the status line.
func (m *dmPicker) rows() int { return max(1, m.height-2-dmPickerTop-2) }

func (m *dmPicker) size() (int, int) { return m.width, m.height }

func (m *dmPicker) View() string {
	inner := m.width - 4
	title := "Chats " + textfmt.Plain(m.bot) + " answers"
	if m.allBy != "" {
		return renderBox(title, []string{
			m.st.subtle.Render(textfmt.ClipLine(m.notice, inner)),
			m.st.dim.Render(textfmt.ClipLine("A chat has one bot; change "+m.allBy+
				"'s Chats first.", inner)),
		}, m.width, m.height, m.st.accentColor)
	}
	radio := func(on bool, label string) string {
		if on {
			return m.st.accent.Render("● ") + label
		}
		return m.st.subtle.Render("○ " + label)
	}
	m.input.SetWidth(max(1, inner-10))
	body := []string{
		m.st.subtle.Render("Answers   ") + radio(m.all, "all private chats") + "  " +
			radio(!m.all, "selected"),
		m.st.subtle.Render("Filter    ") + m.input.View(),
		"",
	}
	shown := m.shown()
	switch {
	case m.all:
		body = append(body, m.st.subtle.Render(textfmt.ClipLine(
			"Every private chat of this account, now and later.", inner)))
	case len(shown) == 0:
		body = append(body, m.st.dim.Render("  no private chats"))
	}
	first := window(m.sel, m.rows())
	for i := first; !m.all && i < min(len(shown), first+m.rows()); i++ {
		c := shown[i]
		box, owner := "[ ] ", m.owners[c.ID]
		switch {
		case owner != "":
			box = "[-] "
		case m.picked[c.ID]:
			box = "[x] "
		}
		tag := ""
		if owner != "" {
			tag = agentIcon + " " + owner
		}
		room := max(1, inner-2-4-textfmt.DisplayWidth(tag)-1)
		title := truncate(textfmt.Plain(c.Title), room)
		gap := strings.Repeat(" ", max(1, inner-2-4-textfmt.DisplayWidth(title)-
			textfmt.DisplayWidth(tag)))
		row := box + title
		switch {
		case i == m.sel:
			row = m.st.accent.Render("> ") + m.st.selectedRow.Render(row)
		case owner != "":
			row = "  " + m.st.dim.Render(row)
		default:
			row = "  " + row
		}
		if tag != "" {
			row += gap + m.st.agent.Render(tag)
		}
		body = append(body, row)
	}
	for len(body) < m.height-3 {
		body = append(body, "")
	}
	var status string
	switch {
	case m.notice != "":
		status = styleError.Render(textfmt.ClipLine(m.notice, inner))
	case m.all:
		status = m.st.dim.Render(textfmt.ClipLine("tab: pick chats instead", inner))
	default:
		_, chats := m.chosen()
		count := fmt.Sprintf("%d selected", len(chats))
		status = m.st.subtle.Render(count) + m.st.dim.Render(textfmt.ClipLine(
			" · tab: all private chats", max(0, inner-textfmt.DisplayWidth(count))))
	}
	body = append(body, status)
	return renderBox(title, body, m.width, m.height, m.st.accentColor)
}

// click answers a left click at x, y of the box, counted from its top-left
// corner, and reports whether it closes the modal: outside the box cancels.
func (m *dmPicker) click(x, y int) (done bool) {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return true
	}
	if m.allBy != "" {
		return false
	}
	switch row := y - 1; {
	case row == 0:
		m.toggleAll()
	case row >= dmPickerTop && !m.all:
		i := window(m.sel, m.rows()) + row - dmPickerTop
		if i < len(m.shown()) && row-dmPickerTop < m.rows() {
			m.sel = i
			m.toggle(i)
		}
	}
	return false
}

func (m *dmPicker) helpKeys() []helpKey {
	if m.allBy != "" {
		return []helpKey{{"esc", "close"}}
	}
	return []helpKey{{"↑↓", "select"}, {"space", "tick"}, {"tab", "all/selected"},
		{"enter", "save"}, {"esc", "cancel"}}
}
