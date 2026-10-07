package tui

import (
	"context"
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

// userSearchModel finds a person to chat with, or to forward a message to:
// a search box over a bubbles list. The list's fuzzy filter narrows known
// people as you type, and directory results are merged in as items when
// they arrive.
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
type userSearchClosed struct{}

// newForwardSearch picks the recipient for req instead of opening a chat.
func newForwardSearch(ctx context.Context, b backend.Backend, recent []backend.ChatEntry, req forwardRequest) (userSearchModel, tea.Cmd) {
	m, cmd := newUserSearch(ctx, b, recent)
	m.forward = &req
	return m, cmd
}

func newUserSearch(ctx context.Context, b backend.Backend, recent []backend.ChatEntry) (userSearchModel, tea.Cmd) {
	d := list.NewDefaultDelegate()
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(colorAccent).BorderForeground(colorAccent)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(colorSubtle).BorderForeground(colorAccent)
	d.Styles.FilterMatch = lipgloss.NewStyle().Underline(true).Foreground(colorAccent)

	// The search box drives the filter, so the list's own input stays hidden.
	l := list.New(nil, d, 0, 0)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.DisableQuitKeybindings()
	l.SetStatusBarItemName("person", "people")
	l.Title = "Loading…"
	l.Styles.Title = styleSubtle
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 0, 2)
	l.Styles.Spinner = styleAccent
	l.Styles.StatusBar = styleDim.Padding(0, 0, 1, 2)
	l.Styles.StatusBarActiveFilter = styleSubtle
	l.Styles.StatusBarFilterCount = styleDim
	l.Styles.NoItems = styleSubtle.Padding(0, 0, 0, 2)

	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "Name or username…"

	m := userSearchModel{ctx: ctx, b: b, input: in, list: l}
	load := func() tea.Msg {
		base, err := b.UserSearchBase(ctx, recent)
		return userSearchLoaded{base, err}
	}
	return m, tea.Batch(m.input.Focus(), m.list.StartSpinner(), load)
}

// searchBoxHeight is the bordered input plus a blank line under it.
const searchBoxHeight = 4

func (m userSearchModel) resized(width, height int) userSearchModel {
	m.list.SetSize(max(1, width-4), max(1, height-2-searchBoxHeight))
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
	return m, nil
}

// queryChanged drops stale directory results and schedules a new search.
func (m userSearchModel) queryChanged() (userSearchModel, tea.Cmd) {
	m.gen++
	m.remote = nil
	m, cmd := m.setItems()
	if utf8.RuneCountInString(m.query) < 2 {
		m.list.StopSpinner()
		return m, cmd
	}
	gen := m.gen
	return m, tea.Batch(cmd, m.list.StartSpinner(), tea.Tick(300*time.Millisecond,
		func(time.Time) tea.Msg { return userSearchDebounce{gen} }))
}

func (m userSearchModel) Update(msg tea.Msg) (userSearchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case userSearchLoaded:
		m.list.StopSpinner()
		m.base = msg.base
		m, cmd := m.setItems()
		if msg.err != nil {
			cmd = tea.Batch(cmd, m.list.NewStatusMessage(styleError.Render(textfmt.CleanError(msg.err))))
		}
		return m, cmd

	case userSearchDebounce:
		if msg.gen != m.gen {
			return m, nil
		}
		ctx, b, gen, query := m.ctx, m.b, m.gen, m.query
		return m, func() tea.Msg {
			found, err := b.UserSearchRemote(ctx, query)
			return userSearchResults{gen, found, err}
		}

	case userSearchResults:
		if msg.gen != m.gen {
			return m, nil
		}
		m.list.StopSpinner()
		if msg.err != nil {
			return m, m.list.NewStatusMessage(styleError.Render(textfmt.CleanError(msg.err)))
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
	// Spinner ticks, filter results and status-message timeouts go to the
	// list; pastes go to the search box.
	var cmd, edit tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m, edit = m.edited(msg)
	return m, tea.Batch(cmd, edit)
}

// finished ends an open or forward; on failure the picker stays up with the error.
func (m userSearchModel) finished(err error) (userSearchModel, tea.Cmd) {
	m.opening = false
	m.list.StopSpinner()
	if err != nil {
		return m, m.list.NewStatusMessage(styleError.Render(textfmt.CleanError(err)))
	}
	return m, nil
}

func (m userSearchModel) handleKey(msg tea.KeyPressMsg) (userSearchModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		return m, func() tea.Msg { return userSearchClosed{} }
	case "esc":
		if m.input.Value() == "" {
			return m, func() tea.Msg { return userSearchClosed{} }
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
		m.list.PrevPage()
		return m, nil
	case "pgdown":
		m.list.NextPage()
		return m, nil
	case "enter":
		item, ok := m.list.SelectedItem().(userItem)
		if !ok || m.opening {
			return m, nil
		}
		m.opening = true
		ctx, b, fwd := m.ctx, m.b, m.forward
		return m, tea.Batch(m.list.StartSpinner(), func() tea.Msg {
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

func (m userSearchModel) view(width, height int, focused bool) string {
	body := composerBox("", nil, "@ ", m.input, width-4, boxBorder(focused))
	body = append(body, "")
	body = append(body, strings.Split(m.list.View(), "\n")...)
	title := "Search users"
	if m.forward != nil {
		title = "Forward to"
	}
	return renderBox(title, body, width, height, boxBorder(focused))
}

func (m userSearchModel) helpKeys() []helpKey {
	if m.forward != nil {
		return forwardSearchHelp
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
