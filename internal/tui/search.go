package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// userSearchModel is a modal that finds a person to chat with, to forward a
// message to, or to mention in a group chat: a search box over a bubbles
// list. The list's fuzzy filter narrows known people as you type, and
// server results are merged in as items when they arrive.
type userSearchModel struct {
	st      *styles
	ctx     context.Context
	b       backend.Backend
	input   textinput.Model
	list    list.Model
	base    backend.UserSearchBase
	remote  []backend.UserCandidate
	query   string
	gen     int
	opening bool
	forward *forwardRequest
	mention *backend.ChatEntry // the group chat being written in
	// in is the chat open under the modal, whose messages are searched too.
	in     *backend.ChatEntry
	msgs   []backend.Message
	inMsgs bool // the selection is msgs[msgIdx] rather than the list's
	// onGlobal selects the row that searches every chat for the query.
	onGlobal bool
	msgIdx   int
	msgBusy  bool
	// channels is set when the backend lists channels beside people.
	channels bool
	busy     bool   // loading, searching or opening
	status   string // the last error
	width    int
	height   int
}

// forwardRequest is a message waiting for a recipient.
type forwardRequest struct {
	from  backend.ChatEntry
	msgID string
}

// userItem adapts a candidate to list.DefaultItem: the name is the title and
// the rest of the label (username, source) the description.
type userItem struct{ c backend.UserCandidate }

func (i userItem) Title() string {
	name, _, _ := strings.Cut(textfmt.Plain(i.c.Label), "  ")
	return name
}

func (i userItem) Description() string {
	_, rest, _ := strings.Cut(textfmt.Plain(i.c.Label), "  ")
	return strings.Join(strings.Fields(rest), " ")
}

func (i userItem) FilterValue() string {
	return i.c.Label + " " + i.c.Match
}

// glyph marks the kind of chat a pick opens.
func (i userItem) glyph() string {
	switch i.c.Kind {
	case backend.CandidateChannel:
		return "#"
	case backend.CandidateGroup:
		return "⁂"
	}
	return "@"
}

// kind names a group or a channel; people have none.
func (i userItem) kind() string {
	switch i.c.Kind {
	case backend.CandidateChannel:
		return "channel"
	case backend.CandidateGroup:
		return "group"
	}
	return ""
}

type userSearchLoaded struct {
	base backend.UserSearchBase
	err  error
}
type userSearchDebounce struct{ gen int }
type userSearchResults struct {
	gen        int
	candidates []backend.UserCandidate
	err        error
}
type userSearchMessages struct {
	gen  int
	msgs []backend.Message
	err  error
}

// userSearchGlobal opens the search of every chat for query.
type userSearchGlobal struct{ query string }

// userSearchMessage picks a message of the chat under the modal.
type userSearchMessage struct{ id string }

type userSearchOpened struct {
	entry backend.ChatEntry
	err   error
}
type userForwardDone struct {
	target backend.ChatEntry
	err    error
}

// userSearchClosed ends the modal; a mention picker hands back what was
// typed after the @, so cancelling never loses text.
type userSearchClosed struct{ insert string }

// userMentionPicked is the mention text to insert into the composer.
type userMentionPicked struct{ text string }

// newForwardSearch picks the recipient for req instead of opening a chat.
func newForwardSearch(ctx context.Context, st *styles, b backend.Backend, recent []backend.ChatEntry, req forwardRequest) (userSearchModel, tea.Cmd) {
	m, cmd := newUserSearch(ctx, st, b, recent)
	m.forward = &req
	return m, cmd
}

// newMentionSearch picks a member of ch to mention in it.
func newMentionSearch(ctx context.Context, st *styles, b backend.Backend, mn backend.Mentioner, ch backend.ChatEntry) (userSearchModel, tea.Cmd) {
	m := newSearchModel(ctx, st, b)
	m.mention = &ch
	m.input.Placeholder = "name…"
	load := func() tea.Msg {
		base, err := mn.MentionBase(ctx, ch)
		return userSearchLoaded{base, err}
	}
	return m, tea.Batch(m.input.Focus(), load)
}

// newChatSearch is the search modal over chat ch: chats and people, then
// ch's messages.
func newChatSearch(ctx context.Context, st *styles, b backend.Backend, recent []backend.ChatEntry, ch backend.ChatEntry) (userSearchModel, tea.Cmd) {
	m, cmd := newUserSearch(ctx, st, b, recent)
	m.in = &ch
	m.input.Placeholder = "name, channel or message…"
	return m, cmd
}

func newUserSearch(ctx context.Context, st *styles, b backend.Backend, recent []backend.ChatEntry) (userSearchModel, tea.Cmd) {
	m := newSearchModel(ctx, st, b)
	if b.Capabilities().ChannelSearch {
		m.channels = true
		m.input.Placeholder = "name, username or channel…"
	}
	load := func() tea.Msg {
		base, err := b.UserSearchBase(ctx, recent)
		return userSearchLoaded{base, err}
	}
	return m, tea.Batch(m.input.Focus(), load)
}

func newSearchModel(ctx context.Context, st *styles, b backend.Backend) userSearchModel {
	// The list only filters and keeps the selection; View draws the rows.
	l := list.New(nil, rowDelegate{}, 0, 0)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.DisableQuitKeybindings()
	l.Filter = substringFilter

	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "name or username…"

	return userSearchModel{st: st, ctx: ctx, b: b, input: in, list: l, busy: true}
}

// substringFilter keeps the targets holding term, ignoring case, in their
// order; the list's fuzzy default lets "short" match "shobhit, ritesh".
func substringFilter(term string, targets []string) []list.Rank {
	term = strings.ToLower(term)
	var ranks []list.Rank
	for i, t := range targets {
		if strings.Contains(strings.ToLower(t), term) {
			ranks = append(ranks, list.Rank{Index: i})
		}
	}
	return ranks
}

// rowDelegate gives the list one-line items without drawing them.
type rowDelegate struct{}

func (rowDelegate) Height() int                                  { return 1 }
func (rowDelegate) Spacing() int                                 { return 0 }
func (rowDelegate) Update(tea.Msg, *list.Model) tea.Cmd          { return nil }
func (rowDelegate) Render(io.Writer, list.Model, int, list.Item) {}

// searchMaxRows is the most people the modal lists at once; searching a
// chat too, it has room for both sections and wider message lines.
const (
	searchMaxRows     = 14
	searchChatRows    = 26
	searchChatWidth   = 100
	searchPickerWidth = 72
)

// searchHeader is the box's top border, the boxed query and the heading rule.
const searchHeader = 5

// searchFooter is the key rule, the key line and the box's bottom border.
const searchFooter = 3

// resized fits the modal into a width×height area.
func (m userSearchModel) resized(width, height int) userSearchModel {
	w, rows := searchPickerWidth, searchMaxRows
	if m.in != nil {
		w, rows = searchChatWidth, searchChatRows
	}
	m.width = min(w, width-4)
	m.height = max(searchHeader+searchFooter+1,
		min(searchHeader+rows+searchFooter, height-2))
	// One page holds every item, so Index counts from the first.
	m.list.SetSize(max(1, m.width-4), 1<<20)
	return m
}

// rows is how many rows fit under the heading.
func (m userSearchModel) rows() int { return max(1, m.height-searchHeader-searchFooter) }

// searchRow is a row under the heading: a list item, a message, the row
// that searches every chat, the messages' heading rule, or a dim note when
// item and msg are both -1.
type searchRow struct {
	item, msg int
	global    bool
	rule      bool
	note      string
}

// global reports whether the modal offers to search every chat: a search,
// not a picker, with something typed, on a backend that can.
func (m userSearchModel) global() bool {
	_, ok := m.b.(backend.GlobalSearcher)
	return ok && m.forward == nil && m.mention == nil && m.query != ""
}

// shown is the rows under the heading. Over a chat the people and the
// chat's messages page apart, each behind its own heading, so neither hides
// the other: the messages get the rows they need and the people at least three.
func (m userSearchModel) shown() []searchRow {
	var top []searchRow
	avail := m.rows()
	if m.global() {
		top = []searchRow{{item: -1, msg: -1, global: true}}
		avail--
	}
	var people []searchRow
	for i := range m.list.VisibleItems() {
		people = append(people, searchRow{item: i, msg: -1})
	}
	if len(people) == 0 && !m.busy {
		note := "no one found"
		if m.channels {
			note = "no matches"
		}
		people = append(people, searchRow{item: -1, msg: -1, note: note})
	}
	sel := -1
	if !m.inMsgs && !m.onGlobal {
		sel = m.list.Index()
	}
	if m.in == nil {
		return append(top, page(people, sel, avail)...)
	}
	var msgs []searchRow
	for i := range m.msgs {
		msgs = append(msgs, searchRow{item: -1, msg: i})
	}
	switch {
	case len(m.msgs) > 0 || m.msgBusy:
	case m.query == "":
		msgs = append(msgs, searchRow{item: -1, msg: -1, note: "type to search messages"})
	default:
		msgs = append(msgs, searchRow{item: -1, msg: -1, note: "no matching messages"})
	}
	rest := avail - 1 // the messages' heading
	n := min(len(people), max(min(3, rest-1), rest-max(1, len(msgs))))
	out := append(top, page(people, sel, n)...)
	out = append(out, searchRow{item: -1, msg: -1, rule: true})
	sel = -1
	if m.inMsgs {
		sel = m.msgIdx
	}
	return append(out, page(msgs, sel, m.rows()-len(out))...)
}

// page is the n rows of rows holding the one at sel, paging by n.
func page(rows []searchRow, sel, n int) []searchRow {
	if n <= 0 {
		return nil
	}
	first := max(0, sel) - max(0, sel)%n
	return rows[first:min(len(rows), first+n)]
}

// settle keeps the selection on something that is there.
func (m userSearchModel) settle() userSearchModel {
	n := len(m.list.VisibleItems())
	m.onGlobal = m.onGlobal && m.global()
	switch {
	case m.onGlobal:
		m.inMsgs = false
	case len(m.msgs) == 0:
		m.inMsgs = false
	case !m.inMsgs && n == 0:
		m.inMsgs, m.msgIdx = true, 0
	}
	m.msgIdx = max(0, min(m.msgIdx, len(m.msgs)-1))
	return m
}

func mergeCandidates(groups ...[]backend.UserCandidate) []backend.UserCandidate {
	seen := make(map[string]bool)
	var out []backend.UserCandidate
	for _, group := range groups {
		for _, c := range group {
			if !seen[c.Key] {
				seen[c.Key] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// setItems shows suggestions first, then everyone else known locally, then
// directory results for the current query, and re-applies the filter.
func (m userSearchModel) setItems() (userSearchModel, tea.Cmd) {
	candidates := mergeCandidates(m.base.Base, m.base.Local, m.remote)
	items := make([]list.Item, len(candidates))
	for i, c := range candidates {
		items[i] = userItem{c}
	}
	// Reset first so SetItems filters synchronously below rather than
	// returning a filter command that could land after a newer query.
	m.list.ResetFilter()
	m.list.SetItems(items)
	m.list.Title = m.base.Heading
	if m.query != "" {
		m.list.Title = "Results"
		m.list.SetFilterText(m.query)
	}
	return m.settle(), nil
}

// queryChanged drops stale directory results and schedules a new search.
func (m userSearchModel) queryChanged() (userSearchModel, tea.Cmd) {
	m.gen++
	m.remote, m.msgs, m.msgBusy = nil, nil, m.in != nil && m.query != ""
	m.onGlobal = m.global() // Enter after typing searches every chat
	m, cmd := m.setItems()
	if utf8.RuneCountInString(m.query) < m.minQuery() {
		m.busy = m.opening
		if !m.msgBusy {
			return m, cmd
		}
	} else {
		m.busy = true
	}
	gen := m.gen
	m.status = ""
	return m, tea.Batch(cmd, tea.Tick(300*time.Millisecond,
		func(time.Time) tea.Msg { return userSearchDebounce{gen} }))
}

func (m userSearchModel) Update(msg tea.Msg) (userSearchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case userSearchLoaded:
		m.busy = false
		m.base = msg.base
		if msg.err != nil {
			m.status = textfmt.CleanError(msg.err)
		}
		return m.setItems()

	case userSearchDebounce:
		if msg.gen != m.gen {
			return m, nil
		}
		ctx, b, gen, query, ch := m.ctx, m.b, m.gen, m.query, m.mention
		var cmds []tea.Cmd
		if utf8.RuneCountInString(query) >= m.minQuery() {
			cmds = append(cmds, func() tea.Msg {
				if ch != nil {
					mn, _ := b.(backend.Mentioner)
					found, err := mn.MentionSearch(ctx, *ch, query)
					return userSearchResults{gen, found, err}
				}
				found, err := b.UserSearchRemote(ctx, query)
				return userSearchResults{gen, found, err}
			})
		}
		if in := m.in; in != nil {
			cmds = append(cmds, func() tea.Msg {
				msgs, err := b.SearchMessages(ctx, *in, query)
				return userSearchMessages{gen, msgs, err}
			})
		}
		return m, tea.Batch(cmds...)

	case userSearchMessages:
		if msg.gen != m.gen {
			return m, nil
		}
		m.msgBusy = false
		if msg.err != nil {
			m.status = textfmt.CleanError(msg.err)
			return m, nil
		}
		m.msgs = msg.msgs
		return m.settle(), nil

	case userSearchResults:
		if msg.gen != m.gen {
			return m, nil
		}
		m.busy = false
		if msg.err != nil {
			m.status = textfmt.CleanError(msg.err)
			return m, nil
		}
		m.remote = msg.candidates
		return m.setItems()

	case userSearchOpened:
		return m.finished(msg.err)

	case userForwardDone:
		return m.finished(msg.err)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	// Pastes go to the search box.
	return m.edited(msg)
}

// minQuery is the shortest query worth a directory search.
func (m userSearchModel) minQuery() int {
	if m.mention != nil {
		return 1 // a group's members are few; one letter narrows them
	}
	return 2
}

// finished ends an open or forward; on failure the picker stays up with the error.
func (m userSearchModel) finished(err error) (userSearchModel, tea.Cmd) {
	m.opening, m.busy = false, false
	if err != nil {
		m.status = textfmt.CleanError(err)
	}
	return m, nil
}

// cancel closes the modal; a mention picker gives back the @ and the query.
func (m userSearchModel) cancel() tea.Cmd {
	closed := userSearchClosed{}
	if m.mention != nil {
		closed.insert = "@" + m.input.Value()
	}
	return func() tea.Msg { return closed }
}

func (m userSearchModel) handleKey(msg tea.KeyPressMsg) (userSearchModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, m.cancel()
	case "backspace":
		if m.mention != nil && m.input.Value() == "" {
			// Deletes the @ that opened the picker.
			return m, func() tea.Msg { return userSearchClosed{} }
		}
	case "esc":
		if m.input.Value() == "" || m.mention != nil {
			return m, m.cancel()
		}
		m.input.SetValue("")
		m.query = ""
		return m.queryChanged()
	case "up", "ctrl+p":
		return m.move(-1), nil
	case "down", "ctrl+n":
		return m.move(1), nil
	case "pgup":
		return m.move(-m.rows()), nil
	case "pgdown":
		return m.move(m.rows()), nil
	case "enter":
		if m.onGlobal {
			query := m.query
			return m, func() tea.Msg { return userSearchGlobal{query} }
		}
		if m.inMsgs {
			id := m.msgs[m.msgIdx].ID
			return m, func() tea.Msg { return userSearchMessage{id} }
		}
		item, ok := m.list.SelectedItem().(userItem)
		if !ok || m.opening {
			return m, nil
		}
		if m.mention != nil {
			if item.c.Mention == "" {
				return m, nil
			}
			return m, func() tea.Msg { return userMentionPicked{item.c.Mention} }
		}
		m.opening, m.busy, m.status = true, true, ""
		ctx, b, fwd := m.ctx, m.b, m.forward
		return m, tea.Batch(func() tea.Msg {
			entry, err := b.OpenUserCandidate(ctx, item.c)
			if fwd == nil {
				return userSearchOpened{entry, err}
			}
			if err == nil {
				err = b.Forward(ctx, fwd.from, entry, fwd.msgID)
			}
			return userForwardDone{entry, err}
		})
	}
	// Everything else edits the search box.
	return m.edited(msg)
}

// move steps the selection by n through the search of every chat, the
// list, then the messages.
func (m userSearchModel) move(n int) userSearchModel {
	g := 0
	if m.global() {
		g = 1
	}
	items := len(m.list.VisibleItems())
	if g+items+len(m.msgs) == 0 {
		return m
	}
	at := g + m.list.Index()
	switch {
	case m.onGlobal:
		at = 0
	case m.inMsgs:
		at = g + items + m.msgIdx
	}
	at = max(0, min(g+items+len(m.msgs)-1, at+n))
	m.onGlobal = at < g
	m.inMsgs = at >= g+items
	switch {
	case m.inMsgs:
		m.msgIdx = at - g - items
	case !m.onGlobal:
		m.list.Select(at - g)
	}
	return m
}

// edited passes msg to the search box and searches again if the query changed.
func (m userSearchModel) edited(msg tea.Msg) (userSearchModel, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if q := strings.TrimSpace(m.input.Value()); q != m.query {
		m.query = q
		var search tea.Cmd
		m, search = m.queryChanged()
		cmd = tea.Batch(cmd, search)
	}
	return m, cmd
}

// click picks the row at row y of the modal, counted from its top edge.
func (m userSearchModel) click(y int) (userSearchModel, tea.Cmd) {
	row := y - searchHeader
	shown := m.shown()
	if row < 0 || row >= len(shown) {
		return m, nil
	}
	switch r := shown[row]; {
	case r.global:
		m.onGlobal, m.inMsgs = true, false
	case r.item >= 0:
		m.onGlobal, m.inMsgs = false, false
		m.list.Select(r.item)
	case r.msg >= 0:
		m.onGlobal, m.inMsgs, m.msgIdx = false, true, r.msg
	default:
		return m, nil
	}
	return m.handleKey(keyPress(tea.KeyEnter))
}

func (m userSearchModel) View() string {
	inner := m.width - 4
	m.input.SetWidth(max(1, inner-6))
	prompt := ""
	if m.mention != nil {
		prompt = "@" // stands for the @ typed in the composer
	}
	body := append(m.st.modalField(prompt, m.input.View(), inner), m.heading(inner))
	items := m.list.VisibleItems()
	for _, r := range m.shown() {
		switch {
		case r.global:
			body = append(body, m.globalRow(inner))
		case r.item >= 0:
			body = append(body, m.row(items[r.item].(userItem),
				!m.inMsgs && !m.onGlobal && r.item == m.list.Index(), inner))
		case r.msg >= 0:
			text := backend.GenericMessageLine(m.msgs[r.msg], *m.in, nil)
			body = append(body, m.st.modalRow("",
				m.lit(textfmt.ClipLine(text, inner-modalMark-1), lipgloss.NewStyle()),
				m.inMsgs && r.msg == m.msgIdx, inner))
		case r.rule:
			body = append(body, m.msgHeading(inner))
		default:
			body = append(body, m.st.dim.Render("  "+r.note))
		}
	}
	body = fillLines(body, searchHeader-1+m.rows())
	title, action, esc := "search", "open", "close"
	switch {
	case m.forward != nil:
		title, action = "forward to", "forward"
	case m.mention != nil:
		title, action, esc = "mention", "mention", "type @"
	}
	switch {
	case m.onGlobal:
		action = "search all chats"
	case m.inMsgs:
		action = "go to"
	}
	keys := []helpKey{{"↑↓", "select"}, {"enter", action}, {"esc", esc}}
	body = append(body, m.st.modalKeys(keys, "", inner)...)
	return m.st.modalBox(title, body, m.width, m.height)
}

// globalRow offers the search of every chat for the query.
func (m userSearchModel) globalRow(width int) string {
	text := "search all chats for “" + m.query + "”"
	line := modalRight(m.lit(text, lipgloss.NewStyle()), m.st.dim.Render("enter"),
		width-modalMark)
	return m.st.modalRow(m.st.accent.Render("/"), line, m.onGlobal, width)
}

// row draws one result: the kind glyph, the name, the rest of the label dim,
// and the tag at the right edge; the query is lit.
func (m userSearchModel) row(item userItem, selected bool, width int) string {
	name := lipgloss.NewStyle()
	if selected {
		name = name.Bold(true)
	}
	tag := item.c.Tag
	if tag != "" {
		tag = m.st.subtle.Render("[" + tag + "]")
	}
	title, desc := item.Title(), item.Description()
	if desc != "" {
		desc = "  " + desc
	}
	if k := item.kind(); k != "" {
		desc = "  " + k + desc
	}
	text := truncate(title+desc, max(1, width-modalMark-2-textfmt.DisplayWidth(tag)))
	if strings.HasPrefix(text, title) {
		desc = text[len(title):]
	} else {
		title, desc = text, ""
	}
	line := modalRight(m.lit(title, name)+m.lit(desc, m.st.dim), tag, width-modalMark)
	return m.st.modalRow(m.st.dim.Render(item.glyph()), line, selected, width)
}

// lit renders text in style with the first match of the query in bold accent.
func (m userSearchModel) lit(text string, style lipgloss.Style) string {
	i := -1
	if m.query != "" {
		i = strings.Index(strings.ToLower(text), strings.ToLower(m.query))
	}
	if i < 0 || len(strings.ToLower(text)) != len(text) {
		return style.Render(text)
	}
	end := i + len(m.query)
	return style.Render(text[:i]) + m.st.brand.Render(text[i:end]) + style.Render(text[end:])
}

// heading names the list and counts it, or shows the last error, over a rule.
func (m userSearchModel) heading(width int) string {
	if m.status != "" {
		return styleError.Render(m.status)
	}
	n := len(m.list.VisibleItems())
	heading := strings.ToLower(m.base.Heading)
	if heading != "" && n > 0 {
		heading += fmt.Sprintf(" · %d", n)
	}
	if m.query != "" {
		heading = fmt.Sprintf("%d %s", n, plural(n, "match", "matches"))
	}
	if m.busy {
		heading = strings.TrimSpace(heading + " · searching…")
	}
	return m.st.modalRule(heading, width)
}

// msgHeading is the rule over the messages of the chat under the modal.
func (m userSearchModel) msgHeading(width int) string {
	heading := "messages in " + chatName(*m.in)
	switch {
	case m.msgBusy:
		heading += " · searching…"
	case len(m.msgs) > 0:
		heading += fmt.Sprintf(" · %d", len(m.msgs))
	}
	return m.st.modalRule(heading, width)
}

func (m userSearchModel) helpKeys() []helpKey {
	switch {
	case m.forward != nil:
		return forwardSearchHelp
	case m.mention != nil:
		return mentionSearchHelp
	}
	return userSearchHelp
}

var userSearchHelp = []helpKey{
	{"type", "filter"},
	{"↑↓", "select"},
	{"pgup/pgdn", "page"},
	{"enter", "open"},
	{"esc", "clear / back"},
}

var forwardSearchHelp = []helpKey{
	{"type", "filter"},
	{"↑↓", "select"},
	{"pgup/pgdn", "page"},
	{"enter", "forward"},
	{"esc", "clear / cancel"},
}

var mentionSearchHelp = []helpKey{
	{"type", "filter"},
	{"↑↓", "select"},
	{"enter", "mention"},
	{"esc", "type @ instead"},
}
