package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// chatListModel is the sidebar tree: every account as a header, with the
// active account expanded to its chats and the others collapsed above and
// below it. Selection indexes run over the selectable rows in display order:
// accounts above, chats, the action rows (Unread, Mentions, archive and
// search), accounts below, then "Settings…".
type chatListModel struct {
	st            *styles
	ctx           context.Context
	mgr           *account.AccountManager
	active        *account.Account
	b             backend.Backend
	entries       []backend.ChatEntry
	selected      int
	archived      bool
	archiveUnread int
	caps          backend.Capabilities
	mentions      bool // the backend can list mentions
	confirm       *chatListConfirm
	offset        int
	width, height int
}

// chatListConfirm is a pending y/n question: delete entry.
type chatListConfirm struct {
	entry  backend.ChatEntry
	revoke bool
}

type chatListChoice struct{ index int }
type chatListSwitch struct{ to *account.Account }
type chatListRefresh struct{}
type chatListAccounts struct{}
type chatListSettings struct{}
type chatListSearch struct{}
type chatListUnread struct{}
type chatListMentions struct{}
type chatListArchiveToggle struct{}
type chatListDeleteDone struct{ err error }

func newChatListModel(ctx context.Context, st *styles, mgr *account.AccountManager, active *account.Account, b backend.Backend, entries []backend.ChatEntry, archived bool, archiveUnread int) chatListModel {
	w, h := termSize()
	m := chatListModel{
		st:            st,
		ctx:           ctx,
		mgr:           mgr,
		active:        active,
		b:             b,
		entries:       entries,
		archived:      archived,
		archiveUnread: archiveUnread,
		caps:          b.Capabilities(),
		width:         w,
		height:        h,
	}
	_, m.mentions = b.(backend.MentionFetcher)
	m.selected = m.before()
	return m
}

// listAction is a row under the chats and what Enter on it sends.
type listAction struct {
	entry backend.ChatEntry
	msg   tea.Msg
}

func (m chatListModel) actions() []listAction {
	unread := backend.ChatEntry{Title: "Unread"}
	for _, e := range m.entries {
		unread.Unread += e.Unread
	}
	acts := []listAction{{unread, chatListUnread{}}}
	if m.mentions {
		acts = append(acts, listAction{backend.ChatEntry{Title: "Mentions"}, chatListMentions{}})
	}
	archive := backend.ChatEntry{Title: "Archived chats", Unread: m.archiveUnread}
	if m.archived {
		archive = backend.ChatEntry{Title: "Back to chats"}
	}
	return append(acts, listAction{archive, chatListArchiveToggle{}},
		listAction{backend.ChatEntry{Title: "Search users…"}, chatListSearch{}})
}

// others splits the accounts other than the active one into those listed
// above and below it.
func (m chatListModel) others() (above, below []*account.Account) {
	if m.mgr == nil {
		return nil, nil
	}
	for i, a := range m.mgr.Accounts {
		if a == m.active {
			return m.mgr.Accounts[:i], m.mgr.Accounts[i+1:]
		}
	}
	return nil, m.mgr.Accounts
}

func (m chatListModel) before() int {
	above, _ := m.others()
	return len(above)
}

func (m chatListModel) maxIndex() int {
	above, below := m.others()
	return len(above) + len(m.entries) + len(m.actions()) + len(below)
}

// chatIndex maps the selection to an entry, if a chat is selected.
func (m chatListModel) chatIndex() (int, bool) {
	i := m.selected - m.before()
	return i, i >= 0 && i < len(m.entries)
}

func (m chatListModel) Init() tea.Cmd { return nil }

func (m chatListModel) refreshUnread() chatListModel {
	for i := range m.entries {
		m.entries[i].Unread = m.b.CachedUnread(m.entries[i])
	}
	return m
}

// row maps a selection index to its display row. The active account's
// header sits above its chats, and a separator below them.
func (m chatListModel) row(index int) int {
	before := m.before()
	if index < before {
		return index
	}
	if index < before+len(m.entries) {
		return index + 1
	}
	return index + 2 + max(0, 1-len(m.entries)) // "No chats here" placeholder
}

// itemAt maps a body row on screen to a selection index; headers,
// placeholders and separators are not selectable.
func (m chatListModel) itemAt(y int) (int, bool) {
	r := y + m.offset
	for i := 0; i <= m.maxIndex(); i++ {
		if m.row(i) == r {
			return i, true
		}
	}
	return 0, false
}

func (m chatListModel) scrolled() chatListModel {
	r := m.row(m.selected)
	if r < m.offset {
		m.offset = r
	}
	if r >= m.offset+m.height {
		m.offset = r - m.height + 1
	}
	m.offset = max(0, m.offset)
	return m
}

func (m chatListModel) Update(msg tea.Msg) (chatListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.confirm != nil {
			req := m.confirm
			m.confirm = nil
			if msg.String() == "y" || msg.String() == "Y" {
				entry := req.entry
				revoke := req.revoke
				ctx := m.ctx
				b := m.b
				return m, func() tea.Msg {
					err := b.DeleteChat(ctx, entry, revoke)
					return chatListDeleteDone{err}
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return chatListAccounts{} }
		case "r":
			return m, func() tea.Msg { return chatListRefresh{} }
		case "/":
			return m, func() tea.Msg { return chatListSearch{} }
		case "u":
			return m, func() tea.Msg { return chatListUnread{} }
		case "m":
			if m.mentions {
				return m, func() tea.Msg { return chatListMentions{} }
			}
		case "a":
			return m, func() tea.Msg { return chatListAccounts{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < m.maxIndex() {
				m.selected++
			}
		case "alt+d", "alt+D":
			i, ok := m.chatIndex()
			if !ok || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[i]
			if !entry.IsDM {
				return m, nil
			}
			m.confirm = &chatListConfirm{entry: entry, revoke: false}
		case "ctrl+alt+d":
			i, ok := m.chatIndex()
			if !ok || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[i]
			if !entry.IsDM || entry.IsSelf {
				return m, nil
			}
			m.confirm = &chatListConfirm{entry: entry, revoke: true}
		case "enter":
			above, below := m.others()
			idx := m.selected
			switch {
			case idx < len(above):
				to := above[idx]
				return m, func() tea.Msg { return chatListSwitch{to} }
			}
			idx -= len(above)
			acts := m.actions()
			switch {
			case idx < len(m.entries):
				return m, func() tea.Msg { return chatListChoice{idx} }
			case idx-len(m.entries) < len(acts):
				act := acts[idx-len(m.entries)].msg
				return m, func() tea.Msg { return act }
			case idx-len(m.entries)-len(acts) < len(below):
				to := below[idx-len(m.entries)-len(acts)]
				return m, func() tea.Msg { return chatListSwitch{to} }
			default:
				return m, func() tea.Msg { return chatListSettings{} }
			}
		}

	case chatListDeleteDone:
		if msg.err != nil {
			return m, nil
		}
		return m, func() tea.Msg { return chatListRefresh{} }
	}
	return m.scrolled(), nil
}

func (m chatListModel) title() string {
	if m.archived {
		return "Archived"
	}
	return "Chats"
}

// accountRow is an account header in the tree: ▾ expanded, ▸ collapsed.
func (m chatListModel) accountRow(a *account.Account, expanded, selected, focused bool, width int) string {
	arrow := "▸ "
	if expanded {
		arrow = "▾ "
	}
	tag := accountShort(m.mgr, m.mgr.AccountKey(a))
	label := accountLabel(a)
	marker := "  "
	if selected {
		marker = "> "
	}
	state := a.Snapshot()
	text := marker + arrow + m.st.accountGlyph(state) + " " + tag + " " + label
	if !expanded && state.Unseen > 0 {
		text += fmt.Sprintf(" (%d)", state.Unseen)
	}
	row := truncate(text, width)
	row += strings.Repeat(" ", max(0, width-textfmt.DisplayWidth(row)))
	switch {
	case selected && focused:
		return m.st.selectedRow.Render(row)
	case selected:
		return styleTitle.Render(row)
	case expanded:
		return styleTitle.Foreground(m.st.accentColor).Render(row)
	}
	return m.st.subtle.Render(row)
}

// body renders the tree rows for a width×height area.
func (m chatListModel) body(width, height int, focused bool) []string {
	above, below := m.others()
	var rows []string
	index := 0
	for _, a := range above {
		rows = append(rows, m.accountRow(a, false, index == m.selected, focused, width))
		index++
	}
	if m.active != nil {
		rows = append(rows, m.accountRow(m.active, true, false, focused, width))
	}
	for _, entry := range m.entries {
		rows = append(rows, "    "+m.st.chatListLine(entry, index == m.selected, focused, max(1, width-4)))
		index++
	}
	if len(m.entries) == 0 {
		rows = append(rows, m.st.subtle.Render("      No chats here"))
	}
	rows = append(rows, "    "+m.st.dim.Render(strings.Repeat("─", max(1, width-4))))

	for _, a := range m.actions() {
		line := m.st.chatListLine(a.entry, index == m.selected, focused, max(1, width-4))
		if index != m.selected {
			line = m.st.subtle.Render(line)
		}
		rows = append(rows, "    "+line)
		index++
	}
	for _, a := range below {
		rows = append(rows, m.accountRow(a, false, index == m.selected, focused, width))
		index++
	}
	manage := m.st.chatListLine(backend.ChatEntry{Title: "Settings…"}, index == m.selected, focused, width)
	if index != m.selected {
		manage = m.st.dim.Render(manage)
	}
	rows = append(rows, manage)
	if m.offset < len(rows) {
		rows = rows[m.offset:]
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return rows
}

func (m chatListModel) notice() string {
	if m.confirm == nil {
		return ""
	}
	scope := "only for you"
	if m.confirm.revoke {
		scope = "for both sides"
	}
	return fmt.Sprintf("Delete chat with %s %s? y to confirm; any other key cancels.",
		textfmt.Plain(m.confirm.entry.Title), scope)
}

func (m chatListModel) helpKeys() []helpKey {
	keys := []helpKey{{"↑↓", "select"}, {"⏎", "open"}, {"/", "search"}, {"u", "unread"}}
	if m.mentions {
		keys = append(keys, helpKey{"m", "mentions"})
	}
	keys = append(keys, helpKey{"a", "manage accounts"}, helpKey{"r", "refresh"})
	if m.caps.DeleteChat {
		keys = append(keys, helpKey{"M-d", "delete chat"})
	}
	return keys
}
