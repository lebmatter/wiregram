package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// dmPicker is the modal, opened from a bot's Chats row, that chooses which
// private chats it answers: a radio list of all of them or selected users,
// then, on backends with backend.SelfChatter, a tick for the user's chat with
// themselves, which is apart from both and has at most one bot too.
// Selected users lists the people recently chatted with, filtered as you type,
// and on backends with backend.DMFinder the people found on the server too,
// so a DM that has not started yet can be picked. No DM has two bots, so chats
// another bot answers are shown with its name and cannot be ticked, all is
// unavailable while another bot answers any, and only the user's own chat can
// change while another bot answers all.
type dmPicker struct {
	st     *styles
	ctx    context.Context
	b      backend.Backend
	finder backend.DMFinder // nil when only existing chats can be picked
	bot    string
	all    bool
	selfOK bool              // the backend has a chat with oneself
	self   bool              // the bot answers it
	selfBy string            // the other bot answering it, or ""
	dms    []agent.AgentChat // the bot's own picks first, then recent DMs
	picked map[string]bool
	owners map[string]string // DM ID → the other bot answering it
	allBy  string            // the other bot answering every DM, or ""
	input  textinput.Model
	query  string
	remote []backend.UserCandidate
	gen    int
	busy   bool // searching or resolving a pick
	sel    int  // m.top() rows, then index into items()
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

// dmRadioRows is the two radio rows; dmPickerRows the people shown at most.
const (
	dmRadioRows  = 2
	dmPickerRows = 14
)

// top is how many rows the arrows reach above the people: the radio rows and
// the user's own chat.
func (m *dmPicker) top() int {
	if m.selfOK {
		return dmRadioRows + 1
	}
	return dmRadioRows
}

// head is the lines above the people: the top rows, the boxed filter and the
// heading rule.
func (m *dmPicker) head() int { return m.top() + 4 }

func newDMPicker(ctx context.Context, st *styles, b backend.Backend, bot string, all, self bool,
	dms []agent.AgentChat, picked map[string]bool, owners map[string]string,
	allBy, selfBy string) *dmPicker {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "filter chats…"
	in.CharLimit = 64
	finder, _ := b.(backend.DMFinder)
	if finder != nil {
		in.Placeholder = "name or username…"
	}
	_, selfOK := b.(backend.SelfChatter)
	m := &dmPicker{st: st, ctx: ctx, b: b, finder: finder, bot: bot, all: all, selfOK: selfOK,
		self: self, selfBy: selfBy, dms: dms, picked: picked, owners: owners, allBy: allBy,
		input: in}
	if !all {
		m.sel = 1
	}
	switch {
	case allBy != "" && selfOK:
		m.sel = dmRadioRows // only the user's own chat can change
	case allBy != "":
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

// chosen is what the modal saves: whether the bot answers all, else its
// ticks, and whether it answers the user's own chat.
func (m *dmPicker) chosen() (all, self bool, chats []agent.AgentChat) {
	if m.all {
		return true, m.self, nil
	}
	for _, c := range m.dms {
		if m.picked[c.ID] {
			chats = append(chats, c)
		}
	}
	return false, m.self, chats
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
	switch {
	case i < dmRadioRows && m.allBy != "":
		m.notice = "Bot " + m.allBy + " answers all private chats"
		return nil
	case i < dmRadioRows:
		m.setAll(i == 0)
		return nil
	case i < m.top():
		if m.selfBy != "" {
			m.notice = "Bot " + m.selfBy + " answers your own chat"
			return nil
		}
		m.self = !m.self
		return nil
	}
	items := m.items()
	i -= m.top()
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
	if m.all || m.allBy != "" {
		return m.top()
	}
	return m.top() + len(m.items())
}

// key handles a key press: save when the choice should be stored, done when
// the modal closes.
func (m *dmPicker) key(msg tea.KeyPressMsg) (save, done bool, cmd tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc", "ctrl+c":
		return false, true, nil
	case "enter":
		return m.allBy == "" || m.selfOK, true, nil
	}
	if m.allBy != "" && !m.selfOK {
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
		if m.all || m.allBy != "" {
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
	m.sel = min(m.rowCount()-1, m.top())
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
		if m.sel < m.top() {
			m.sel = m.top()
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
	switch {
	case m.allBy != "" && !m.selfOK:
		m.height = 6 // borders, the two lines of View and the keys
		return
	case m.allBy != "":
		m.height = m.top() + 7 // borders, the top rows, a gap, two lines and the keys
		return
	}
	// Borders, the top lines, the people and the keys.
	m.height = max(m.head()+6, min(height-2, m.head()+dmPickerRows+4))
}

// rows is how many people fit between the heading and the keys.
func (m *dmPicker) rows() int { return max(1, m.height-2-m.head()-2) }

func (m *dmPicker) size() (int, int) { return m.width, m.height }

func (m *dmPicker) View() string {
	inner := m.width - 4
	title := "chats " + textfmt.Plain(m.bot) + " answers"
	if m.allBy != "" && !m.selfOK {
		body := []string{
			m.st.subtle.Render(textfmt.ClipLine(m.notice, inner)),
			m.st.dim.Render(textfmt.ClipLine("A chat has one bot; change "+m.allBy+
				"'s Chats first.", inner)),
		}
		body = append(body, m.st.modalKeys([]helpKey{{"esc", "close"}}, "", inner)...)
		return m.st.modalBox(title, body, m.width, m.height)
	}
	radio := func(i int, on bool, label string) string {
		glyph, style := m.st.dim.Render("○"), m.st.subtle
		switch {
		case m.allBy != "":
			style = m.st.dim
		case on:
			glyph, style = m.st.accent.Render("●"), lipgloss.NewStyle()
		}
		if i == m.sel {
			style = m.st.brand
		}
		return m.st.modalRow("", glyph+" "+style.Render(label), i == m.sel, inner)
	}
	body := []string{
		radio(0, m.all, "All private chats"),
		radio(1, !m.all, "Selected users"),
	}
	if m.selfOK {
		body = append(body, m.selfRow(inner))
	}
	switch {
	case m.allBy != "":
		body = append(body, "", m.st.subtle.Render(textfmt.ClipLine(
			"   Bot "+m.allBy+" answers all private chats;", inner)),
			m.st.dim.Render(textfmt.ClipLine("   change "+m.allBy+"'s Chats to pick others.",
				inner)))
	case m.all:
		body = append(body, "", m.st.subtle.Render(textfmt.ClipLine(
			"   Every private chat of this account, now and later.", inner)))
	default:
		m.input.SetWidth(max(1, inner-6))
		body = append(body, m.st.modalField("/", m.input.View(), inner)...)
		body = append(body, m.heading(inner))
		body = append(body, m.people(inner)...)
	}
	body = fillLines(body, m.height-4)
	keys := []helpKey{{"space", "tick"}, {"enter", "save"}, {"esc", "cancel"}}
	count := ""
	if !m.all && m.allBy == "" {
		_, _, chats := m.chosen()
		count = m.st.subtle.Render(fmt.Sprintf("%d selected", len(chats)))
	}
	strip := m.st.modalKeys(keys, count, inner)
	if m.notice != "" {
		strip[0] = styleError.Render(textfmt.ClipLine(m.notice, inner))
	}
	body = append(body, strip...)
	return m.st.modalBox(title, body, m.width, m.height)
}

// heading names the people listed under the filter, over a rule.
func (m *dmPicker) heading(width int) string {
	heading := "recently chatted with"
	n := len(m.items())
	if m.query != "" {
		heading = fmt.Sprintf("%d %s", n, plural(n, "match", "matches"))
	} else if n > 0 {
		heading += fmt.Sprintf(" · %d", n)
	}
	if m.busy {
		heading += " · searching…"
	}
	return m.st.modalRule(heading, width)
}

// check is a tick box: ticked in the accent, held by another bot dim.
func (m *dmPicker) check(on, taken bool) string {
	switch {
	case taken:
		return m.st.dim.Render("[-]")
	case on:
		return m.st.accent.Render("[x]")
	}
	return m.st.dim.Render("[ ]")
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
	sel := m.sel - m.top()
	first := window(max(0, sel), m.rows())
	for i := first; i < min(len(items), first+m.rows()); i++ {
		it := items[i]
		owner, name, desc, on := "", "", "", false
		if it.cand != nil {
			name, desc = userItem{*it.cand}.Title(), userItem{*it.cand}.Description()
		} else {
			name, owner = textfmt.Plain(it.chat.Title), m.owners[it.chat.ID]
			on = m.picked[it.chat.ID]
		}
		tag := ""
		if owner != "" {
			tag = agentIcon + " " + owner
		}
		room := max(1, inner-modalMark-4-textfmt.DisplayWidth(tag)-2)
		name = truncate(name, room)
		if desc != "" {
			desc = truncate("  "+desc, max(0, room-textfmt.DisplayWidth(name)))
		}
		style := lipgloss.NewStyle()
		switch {
		case i == sel:
			style = m.st.brand
		case owner != "":
			style = m.st.dim
		}
		row := m.check(on, owner != "") + " " + style.Render(name) + m.st.dim.Render(desc)
		if tag != "" {
			row = modalRight(row, m.st.agent.Render(tag), inner-modalMark)
		}
		out = append(out, m.st.modalRow("", row, i == sel, inner))
	}
	return out
}

// click answers a left click at x, y of the box, counted from its top-left
// corner, and reports whether it closes the modal: outside the box cancels.
func (m *dmPicker) click(x, y int) (done bool, cmd tea.Cmd) {
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return true, nil
	}
	if m.allBy != "" && !m.selfOK {
		return false, nil
	}
	m.notice = ""
	switch row := y - 1; {
	case row < m.top():
		m.sel = row
		cmd = m.pick(row)
	case row >= m.head() && !m.all && m.allBy == "" && row-m.head() < m.rows():
		i := window(max(0, m.sel-m.top()), m.rows()) + row - m.head()
		if i < len(m.items()) {
			m.sel = m.top() + i
			cmd = m.pick(m.sel)
		}
	}
	return false, cmd
}

func (m *dmPicker) helpKeys() []helpKey {
	if m.allBy != "" && !m.selfOK {
		return []helpKey{{"esc", "close"}}
	}
	return []helpKey{{"↑↓", "select"}, {"space", "pick/tick"}, {"enter", "save"},
		{"esc", "cancel"}}
}

// selfRow is the tick for the user's chat with themselves.
func (m *dmPicker) selfRow(inner int) string {
	name := "You"
	switch {
	case m.sel == dmRadioRows:
		name = m.st.brand.Render(name)
	case m.selfBy != "":
		name = m.st.dim.Render(name)
	}
	tag := m.st.dim.Render("notes to yourself")
	if m.selfBy != "" {
		tag = m.st.agent.Render(agentIcon + " " + truncate(m.selfBy, 24))
	}
	row := modalRight(m.check(m.self, m.selfBy != "")+" "+name, tag, inner-modalMark)
	return m.st.modalRow("", row, m.sel == dmRadioRows, inner)
}
