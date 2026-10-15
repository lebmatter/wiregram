package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// dmPicker is the modal, opened from a bot's Chats row, that chooses which
// private chats it answers: a radio list of all of them or selected users.
// Selected users lists the people recently chatted with, filtered as you type,
// and on backends with backend.DMFinder the people found on the server too,
// so a DM that has not started yet can be picked. No DM has two bots, so chats
// another bot answers are shown with its name and cannot be ticked, all is
// unavailable while another bot answers any, and nothing can change while
// another bot answers all.
type dmPicker struct {
	st     *styles
	ctx    context.Context
	b      backend.Backend
	finder backend.DMFinder // nil when only existing chats can be picked
	bot    string
	all    bool
	dms    []agent.AgentChat // the bot's own picks first, then recent DMs
	picked map[string]bool
	owners map[string]string // DM ID → the other bot answering it
	allBy  string            // the other bot answering every DM, or ""
	input  textinput.Model
	query  string
	remote []backend.UserCandidate
	gen    int
	busy   bool // searching or resolving a pick
	sel    int  // dmRadioRows radio rows, then index into items()
	notice string
	width  int
	height int
}

// dmItem is a row under the filter: a known DM, or a person found on the
// server whose DM ID is looked up when ticked.
type dmItem struct {
	chat agent.AgentChat
	cand *backend.UserCandidate
}

type dmSearchDebounce struct{ gen int }
type dmSearchResults struct {
	gen        int
	candidates []backend.UserCandidate
	err        error
}
type dmResolved struct {
	chat agent.AgentChat
	err  error
}

// The picker's lines above its people: the two radio rows, the filter and
// the heading.
const (
	dmRadioRows  = 2
	dmPickerTop  = dmRadioRows + 2
	dmPickerRows = 14
)

func newDMPicker(ctx context.Context, st *styles, b backend.Backend, bot string, all bool,
	dms []agent.AgentChat, picked map[string]bool, owners map[string]string,
	allBy string) *dmPicker {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "type to filter"
	in.CharLimit = 64
	finder, _ := b.(backend.DMFinder)
	if finder != nil {
		in.Placeholder = "name or username"
	}
	m := &dmPicker{st: st, ctx: ctx, b: b, finder: finder, bot: bot, all: all, dms: dms,
		picked: picked, owners: owners, allBy: allBy, input: in}
	if !all {
		m.sel = 1
	}
	if allBy != "" {
		m.notice = "Bot " + allBy + " answers all private chats"
	}
	m.input.Focus()
	return m
}

// items is the known DMs matching the filter, then the people found on the
// server, leaving out those already listed by name.
func (m *dmPicker) items() []dmItem {
	q := strings.ToLower(m.query)
	var out []dmItem
	listed := make(map[string]bool)
	for _, c := range m.dms {
		if q == "" || strings.Contains(strings.ToLower(c.Title), q) {
			out = append(out, dmItem{chat: c})
			listed[strings.ToLower(c.Title)] = true
		}
	}
	for i := range m.remote {
		c := &m.remote[i]
		if !listed[strings.ToLower(userItem{*c}.Title())] {
			out = append(out, dmItem{cand: c})
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

// setAll picks all private chats or selected users.
func (m *dmPicker) setAll(all bool) {
	if all && !m.all {
		for _, owner := range m.owners {
			m.notice = "Bot " + owner + " answers some private chats here"
			return
		}
	}
	m.all = all
	m.notice = ""
}

// pick acts on row i of the radio rows and items: a radio row is chosen, a
// person ticked or unticked.
func (m *dmPicker) pick(i int) tea.Cmd {
	if i < dmRadioRows {
		m.setAll(i == 0)
		return nil
	}
	items := m.items()
	i -= dmRadioRows
	if m.all || i >= len(items) {
		return nil
	}
	if c := items[i].cand; c != nil {
		if m.busy {
			return nil
		}
		m.busy, m.notice = true, ""
		ctx, b, cand := m.ctx, m.b, *c
		return func() tea.Msg {
			entry, err := b.OpenUserCandidate(ctx, cand)
			return dmResolved{agent.AgentChat{ID: entry.ID.Id,
				Title: textfmt.Plain(entry.Title)}, err}
		}
	}
	m.tick(items[i].chat)
	return nil
}

func (m *dmPicker) tick(c agent.AgentChat) {
	if owner := m.owners[c.ID]; owner != "" {
		m.notice = "Bot " + owner + " answers " + c.Title
		return
	}
	m.picked[c.ID] = !m.picked[c.ID]
	m.notice = ""
}

// rowCount is how many rows the arrows move through.
func (m *dmPicker) rowCount() int {
	if m.all {
		return dmRadioRows
	}
	return dmRadioRows + len(m.items())
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
	case "up":
		m.sel = max(0, m.sel-1)
	case "down":
		m.sel = min(m.rowCount()-1, m.sel+1)
	case "space", " ":
		cmd = m.pick(m.sel)
	default:
		if m.all {
			return false, false, nil
		}
		m.input, cmd = m.input.Update(msg)
		return false, false, tea.Batch(cmd, m.edited())
	}
	return false, false, cmd
}

func (m *dmPicker) paste(msg tea.PasteMsg) tea.Cmd {
	if m.all || m.allBy != "" {
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return tea.Batch(cmd, m.edited())
}

// edited follows a change to the filter: the selection moves to the first
// match and the server is searched again after a pause.
func (m *dmPicker) edited() tea.Cmd {
	q := strings.TrimSpace(m.input.Value())
	if q == m.query {
		return nil
	}
	m.query, m.remote = q, nil
	m.gen++
	m.sel = min(m.rowCount()-1, dmRadioRows)
	if m.finder == nil || utf8.RuneCountInString(q) < 2 {
		m.busy = false
		return nil
	}
	m.busy = true
	gen := m.gen
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return dmSearchDebounce{gen} })
}

// update takes the picker's own messages: searches and looked-up picks.
func (m *dmPicker) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case dmSearchDebounce:
		if msg.gen != m.gen {
			return nil
		}
		ctx, finder, gen, query := m.ctx, m.finder, m.gen, m.query
		return func() tea.Msg {
			found, err := finder.FindPeople(ctx, query)
			return dmSearchResults{gen, found, err}
		}
	case dmSearchResults:
		if msg.gen != m.gen {
			return nil
		}
		m.busy = false
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
			return nil
		}
		m.remote = msg.candidates
		if m.sel < dmRadioRows {
			m.sel = dmRadioRows
		}
		m.sel = min(m.sel, m.rowCount()-1)
	case dmResolved:
		m.busy = false
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
			return nil
		}
		for _, c := range m.dms {
			if c.ID == msg.chat.ID {
				if !m.picked[c.ID] {
					m.tick(c)
				}
				return nil
			}
		}
		if owner := m.owners[msg.chat.ID]; owner != "" {
			m.notice = "Bot " + owner + " answers " + msg.chat.Title
			return nil
		}
		// The person joins the known DMs, so the filter keeps showing them.
		m.dms = append(m.dms, msg.chat)
		m.picked[msg.chat.ID] = true
	}
	return nil
}

// resized fits the modal into an area width by height.
func (m *dmPicker) resized(width, height int) {
	m.width = min(56, width-4)
	if m.allBy != "" {
		m.height = 4 // the two lines of View
		return
	}
	// Borders, the top lines, the people, a gap and the status line.
	m.height = max(dmPickerTop+6, min(height-2, dmPickerTop+dmPickerRows+4))
}

// rows is how many people fit between the heading and the status line.
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
	marker := func(i int) string {
		if i == m.sel {
			return m.st.accent.Render("> ")
		}
		return "  "
	}
	radio := func(i int, on bool, label string) string {
		if on {
			return marker(i) + m.st.accent.Render("● ") + label
		}
		return marker(i) + m.st.subtle.Render("○ "+label)
	}
	body := []string{
		radio(0, m.all, "All private chats"),
		radio(1, !m.all, "Selected users"),
	}
	if m.all {
		body = append(body, "", m.st.subtle.Render(textfmt.ClipLine(
			"  Every private chat of this account, now and later.", inner)))
	} else {
		m.input.SetWidth(max(1, inner-10))
		body = append(body, m.st.subtle.Render("  Filter  ")+m.input.View(), m.heading())
		body = append(body, m.people(inner)...)
	}
	for len(body) < m.height-3 {
		body = append(body, "")
	}
	var status string
	switch {
	case m.notice != "":
		status = styleError.Render(textfmt.ClipLine(m.notice, inner))
	case m.all:
		status = m.st.dim.Render(textfmt.ClipLine("space: pick · enter: save", inner))
	default:
		_, chats := m.chosen()
		count := fmt.Sprintf("%d selected", len(chats))
		status = m.st.subtle.Render(count) + m.st.dim.Render(textfmt.ClipLine(
			" · space: tick · enter: save", max(0, inner-textfmt.DisplayWidth(count))))
	}
	body = append(body, status)
	return renderBox(title, body, m.width, m.height, m.st.accentColor)
}

// heading names the people listed under the filter.
func (m *dmPicker) heading() string {
	heading := "  Recently chatted with"
	if m.query != "" {
		heading = "  Results"
	}
	line := m.st.subtle.Render(heading)
	if n := len(m.items()); n > 0 {
		line += m.st.dim.Render(fmt.Sprintf(" · %d", n))
	}
	if m.busy {
		line += m.st.dim.Render(" · searching…")
	}
	return line
}

func (m *dmPicker) people(inner int) []string {
	items := m.items()
	if len(items) == 0 && !m.busy {
		empty := "  no private chats"
		if m.query != "" {
			empty = "  no one found"
		}
		return []string{m.st.dim.Render(empty)}
	}
	var out []string
	sel := m.sel - dmRadioRows
	first := window(max(0, sel), m.rows())
	for i := first; i < min(len(items), first+m.rows()); i++ {
		it := items[i]
		box, owner, name, desc := "[ ] ", "", "", ""
		if it.cand != nil {
			name, desc = userItem{*it.cand}.Title(), userItem{*it.cand}.Description()
		} else {
			name, owner = textfmt.Plain(it.chat.Title), m.owners[it.chat.ID]
			switch {
			case owner != "":
				box = "[-] "
			case m.picked[it.chat.ID]:
				box = "[x] "
			}
		}
		tag := ""
		if owner != "" {
			tag = agentIcon + " " + owner
		}
		room := max(1, inner-2-4-textfmt.DisplayWidth(tag)-1)
		name = truncate(name, room)
		if desc != "" {
			desc = truncate(" "+desc, max(0, room-textfmt.DisplayWidth(name)))
		}
		gap := strings.Repeat(" ", max(1, inner-2-4-textfmt.DisplayWidth(name)-
			textfmt.DisplayWidth(desc)-textfmt.DisplayWidth(tag)))
		row := box + name
		switch {
		case i == sel:
			row = m.st.accent.Render("> ") + m.st.selectedRow.Render(row)
		case owner != "":
			row = "  " + m.st.dim.Render(row)
		default:
			row = "  " + row
		}
		row += m.st.dim.Render(desc)
		if tag != "" {
			row += gap + m.st.agent.Render(tag)
		}
		out = append(out, row)
	}
	return out
}

// click answers a left click at x, y of the box, counted from its top-left
// corner, and reports whether it closes the modal: outside the box cancels.
func (m *dmPicker) click(x, y int) (done bool, cmd tea.Cmd) {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return true, nil
	}
	if m.allBy != "" {
		return false, nil
	}
	m.notice = ""
	switch row := y - 1; {
	case row < dmRadioRows:
		m.sel = row
		m.setAll(row == 0)
	case row >= dmPickerTop && !m.all && row-dmPickerTop < m.rows():
		i := window(max(0, m.sel-dmRadioRows), m.rows()) + row - dmPickerTop
		if i < len(m.items()) {
			m.sel = dmRadioRows + i
			cmd = m.pick(m.sel)
		}
	}
	return false, cmd
}

func (m *dmPicker) helpKeys() []helpKey {
	if m.allBy != "" {
		return []helpKey{{"esc", "close"}}
	}
	return []helpKey{{"↑↓", "select"}, {"space", "pick/tick"}, {"enter", "save"},
		{"esc", "cancel"}}
}
