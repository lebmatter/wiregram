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

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// userSearchModel is a modal that finds a person to chat with, to forward a
// message to, or to mention in a group chat: a search box over a bubbles
// list. The list's fuzzy filter narrows known people as you type, and
// server results are merged in as items when they arrive.
type userSearchModel struct {
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
	busy    bool               // loading, searching or opening
	status  string             // the last error
	width   int
	height  int
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

func (i userItem) FilterValue() string { return i.c.Label + " " + i.c.Match }

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
func newForwardSearch(ctx context.Context, b backend.Backend, recent []backend.ChatEntry, req forwardRequest) (userSearchModel, tea.Cmd) {
	m, cmd := newUserSearch(ctx, b, recent)
	m.forward = &req
	return m, cmd
}

// newMentionSearch picks a member of ch to mention in it.
func newMentionSearch(ctx context.Context, b backend.Backend, mn backend.Mentioner, ch backend.ChatEntry) (userSearchModel, tea.Cmd) {
	m := newSearchModel(ctx, b)
	m.mention = &ch
	m.input.Placeholder = "Name…"
	load := func() tea.Msg {
		base, err := mn.MentionBase(ctx, ch)
		return userSearchLoaded{base, err}
	}
	return m, tea.Batch(m.input.Focus(), load)
}

func newUserSearch(ctx context.Context, b backend.Backend, recent []backend.ChatEntry) (userSearchModel, tea.Cmd) {
	m := newSearchModel(ctx, b)
	load := func() tea.Msg {
		base, err := b.UserSearchBase(ctx, recent)
		return userSearchLoaded{base, err}
	}
	return m, tea.Batch(m.input.Focus(), load)
}

func newSearchModel(ctx context.Context, b backend.Backend) userSearchModel {
	// The list only filters and keeps the selection; View draws the rows.
	l := list.New(nil, rowDelegate{}, 0, 0)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.DisableQuitKeybindings()

	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "Name or username…"

	return userSearchModel{ctx: ctx, b: b, input: in, list: l, busy: true}
}

// rowDelegate gives the list one-line items without drawing them.
type rowDelegate struct{}

func (rowDelegate) Height() int                                  { return 1 }
func (rowDelegate) Spacing() int                                 { return 0 }
func (rowDelegate) Update(tea.Msg, *list.Model) tea.Cmd          { return nil }
func (rowDelegate) Render(io.Writer, list.Model, int, list.Item) {}

// searchMaxRows is the most people the modal lists at once.
const searchMaxRows = 14

// searchHeader is the box's top border, the query line and the heading.
const searchHeader = 3

// resized fits the modal into a width×height area.
func (m userSearchModel) resized(width, height int) userSearchModel {
	m.width = min(56, width-4)
	m.height = max(searchHeader+2, min(searchHeader+searchMaxRows+1, height-2))
	// One page holds every item, so Index counts from the first.
	m.list.SetSize(max(1, m.width-4), 1<<20)
	return m
}

// rows is how many people fit under the heading.
func (m userSearchModel) rows() int { return max(1, m.height-searchHeader-1) }

// firstRow is the index of the top row shown: the list pages by rows.
func (m userSearchModel) firstRow() int {
	idx := m.list.Index()
	return idx - idx%m.rows()
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
	return m, nil
}

// queryChanged drops stale directory results and schedules a new search.
func (m userSearchModel) queryChanged() (userSearchModel, tea.Cmd) {
	m.gen++
	m.remote = nil
	m, cmd := m.setItems()
	minQuery := 2
	if m.mention != nil {
		minQuery = 1 // a group's members are few; one letter narrows them
	}
	if utf8.RuneCountInString(m.query) < minQuery {
		m.busy = m.opening
		return m, cmd
	}
	gen := m.gen
	m.busy, m.status = true, ""
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
		return m, func() tea.Msg {
			if ch != nil {
				mn, _ := b.(backend.Mentioner)
				found, err := mn.MentionSearch(ctx, *ch, query)
				return userSearchResults{gen, found, err}
			}
			found, err := b.UserSearchRemote(ctx, query)
			return userSearchResults{gen, found, err}
		}

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
		m.list.CursorUp()
		return m, nil
	case "down", "ctrl+n":
		m.list.CursorDown()
		return m, nil
	case "pgup":
		m.list.Select(max(0, m.list.Index()-m.rows()))
		return m, nil
	case "pgdown":
		m.list.Select(min(len(m.list.VisibleItems())-1, m.list.Index()+m.rows()))
		return m, nil
	case "enter":
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

// click picks the person at row y of the modal, counted from its top edge.
func (m userSearchModel) click(y int) (userSearchModel, tea.Cmd) {
	row := y - searchHeader
	i := m.firstRow() + row
	if row < 0 || row >= m.rows() || i >= len(m.list.VisibleItems()) {
		return m, nil
	}
	m.list.Select(i)
	return m.handleKey(keyPress(tea.KeyEnter))
}

func (m userSearchModel) View() string {
	inner := m.width - 4
	m.input.SetWidth(max(1, inner-3))
	body := []string{styleAccent.Render("@ ") + m.input.View(), m.heading()}
	items := m.list.VisibleItems()
	first := m.firstRow()
	for i := first; i < min(len(items), first+m.rows()); i++ {
		item := items[i].(userItem)
		name, desc := item.Title(), item.Description()
		if desc != "" {
			desc = " " + styleDim.Render(desc)
		}
		if i == m.list.Index() {
			body = append(body, styleAccent.Render("> ")+styleSelectedRow.Render(name)+desc)
		} else {
			body = append(body, "  "+name+desc)
		}
	}
	if len(items) == 0 && !m.busy {
		body = append(body, styleDim.Render("  no one found"))
	}
	title := "Search users"
	switch {
	case m.forward != nil:
		title = "Forward to"
	case m.mention != nil:
		title = "Mention"
	}
	return renderBox(title, body, m.width, m.height, colorAccent)
}

// heading names the list and counts it, or shows the last error.
func (m userSearchModel) heading() string {
	if m.status != "" {
		return styleError.Render(m.status)
	}
	heading := m.base.Heading
	if m.query != "" {
		heading = "Results"
	}
	line := styleSubtle.Render(heading)
	if n := len(m.list.VisibleItems()); n > 0 {
		line += styleDim.Render(fmt.Sprintf(" · %d", n))
	}
	if m.busy {
		line += styleDim.Render(" · searching…")
	}
	return line
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
	{"⏎", "open chat"},
	{"esc", "clear / back"},
}

var forwardSearchHelp = []helpKey{
	{"type", "filter"},
	{"↑↓", "select"},
	{"pgup/pgdn", "page"},
	{"⏎", "forward"},
	{"esc", "clear / cancel"},
}

var mentionSearchHelp = []helpKey{
	{"type", "filter"},
	{"↑↓", "select"},
	{"⏎", "mention"},
	{"esc", "type @ instead"},
}
