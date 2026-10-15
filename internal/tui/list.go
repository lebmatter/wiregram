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
// accounts above, chats, the action rows (Unread, Mentions, archive, search,
// the label sections and the account's agent), accounts below, then
// "Settings…".
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
	labeler       bool // the backend groups chats under labels
	// loading is set while the backend's first sync has brought no chats
	// yet; the tree shows spin, the root's spinner frame, in their place.
	loading bool
	spin    string
	labels  []labelSection
	// labelOpen names the expanded label sections; the screen owns it, so
	// it outlasts reloads of the list.
	labelOpen     map[string]bool
	confirm       *chatListConfirm
	offset        int
	width, height int
}

// chatListConfirm is a pending question: delete entry.
type chatListConfirm struct {
	entry  backend.ChatEntry
	revoke bool
	modal  *confirmModal
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
type chatListAgent struct{ id int64 } // 0 creates a bot
type chatListInbox struct{ bot inboxBot }
type chatListDeleteDone struct{ err error }
type chatListLabel struct{ name string } // expands or collapses a label section
type chatListLabels struct{}
type chatListOpen struct{ entry backend.ChatEntry }

// labelSection is a label shown in the sidebar, with its chats.
type labelSection struct {
	name    string
	unread  int
	entries []backend.ChatEntry
}

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
	_, m.labeler = b.(backend.Labeler)
	if s, ok := b.(backend.Syncer); ok {
		m.loading = s.Syncing() && len(entries) == 0 && !archived
	}
	m.selected = m.before()
	return m
}

// listAction is a row under the chats and what Enter on it sends.
type listAction struct {
	entry backend.ChatEntry
	mark  string
	msg   tea.Msg
	// section is the heading ruled above the row, which starts a section.
	section string
	// chat is a chat inside a label section, drawn as a chat, indented.
	chat bool
	// label is the label section header's name.
	label string
}

func (m chatListModel) actions() []listAction {
	if m.loading {
		return nil // nothing to act on until the chats arrive
	}
	unread := backend.ChatEntry{Title: "Unread"}
	for _, e := range m.entries {
		unread.Unread += e.Unread
	}
	acts := []listAction{{entry: unread, msg: chatListUnread{}}}
	if m.mentions {
		acts = append(acts, listAction{entry: backend.ChatEntry{Title: "Mentions"},
			msg: chatListMentions{}})
	}
	if m.caps.Archive {
		archive := backend.ChatEntry{Title: "Archived chats", Unread: m.archiveUnread}
		if m.archived {
			archive = backend.ChatEntry{Title: "Back to chats"}
		}
		acts = append(acts, listAction{entry: archive, msg: chatListArchiveToggle{}})
	}
	acts = append(acts, listAction{entry: backend.ChatEntry{Title: "Search…"}, msg: chatListSearch{}})
	if m.labeler && !m.archived {
		acts = append(acts, m.labelActions()...)
	}
	if agentsAllowed(m.mgr, m.active) {
		first := len(acts)
		for _, rec := range m.mgr.Agents.AgentsFor(m.active) {
			acts = append(acts, listAction{
				entry: backend.ChatEntry{Title: rec.Name + " @" + rec.Handle},
				mark:  m.st.agentMark(rec.Enabled), msg: chatListAgent{id: rec.ID},
			})
			if reg, ok := m.mgr.Agents.Registration(rec.ID); ok {
				// The registered username, which on Telegram ends in bot.
				acts = append(acts, listAction{
					entry: backend.ChatEntry{Title: "  Inbox",
						Unread: m.mgr.Agents.InboxWaiting(rec.ID)},
					msg: chatListInbox{inboxBot{id: rec.ID, name: rec.Name, handle: reg.Username}},
				})
			}
		}
		acts = append(acts, listAction{entry: backend.ChatEntry{Title: "New bot…"},
			msg: chatListAgent{}})
		acts[first].section = "Bots"
	}
	return acts
}

// labelActions is the Labels section: a header per label shown in the
// sidebar, its chats under it while expanded, then "Labels…".
func (m chatListModel) labelActions() []listAction {
	var acts []listAction
	for _, l := range m.labels {
		arrow := "▸ "
		if m.labelOpen[l.name] {
			arrow = "▾ "
		}
		acts = append(acts, listAction{entry: backend.ChatEntry{Title: arrow + l.name,
			Unread: l.unread}, msg: chatListLabel{l.name}, label: l.name})
		if !m.labelOpen[l.name] {
			continue
		}
		for _, e := range l.entries {
			acts = append(acts, listAction{entry: e, msg: chatListOpen{e}, chat: true})
		}
	}
	acts = append(acts, listAction{entry: backend.ChatEntry{Title: "Labels…"},
		msg: chatListLabels{}})
	acts[0].section = "Labels"
	return acts
}

// labelIndex is the selection index of label's section header, if shown.
func (m chatListModel) labelIndex(label string) (int, bool) {
	for i, a := range m.actions() {
		if label != "" && a.label == label {
			return m.before() + len(m.entries) + i, true
		}
	}
	return 0, false
}

// agentsAllowed reports whether the account can have bots (Chat Agents): they
// answer DMs, and email publishes none.
func agentsAllowed(mgr *account.AccountManager, a *account.Account) bool {
	return mgr != nil && mgr.Agents != nil && a != nil && a.BackendType != "email"
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
	for _, l := range m.labels {
		for i := range l.entries {
			l.entries[i].Unread = m.b.CachedUnread(l.entries[i])
		}
	}
	return m
}

// row maps a selection index to its display row. The active account's
// header sits above its chats, a separator below them, and each section's
// heading (Labels, Bots) above its first row.
func (m chatListModel) row(index int) int {
	before := m.before()
	if index < before {
		return index
	}
	if index < before+len(m.entries) {
		return index + 1
	}
	r := index + 2 + max(0, 1-len(m.entries)) // "No chats here" or loading placeholder
	for i, a := range m.actions() {
		if a.section != "" && before+len(m.entries)+i <= index {
			r++
		}
	}
	return r
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
			yes, done := req.modal.key(msg.String())
			if !done {
				return m, nil
			}
			m.confirm = nil
			if yes {
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
			m.confirm = m.askDelete(entry, false)
		case "ctrl+alt+d":
			i, ok := m.chatIndex()
			if !ok || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[i]
			if !entry.IsDM || entry.IsSelf {
				return m, nil
			}
			m.confirm = m.askDelete(entry, true)
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
	mark := ""
	if m.mgr.Agents != nil {
		if bots := m.mgr.Agents.AgentsFor(a); len(bots) > 0 {
			mark = " " + m.st.agentMark(anyEnabled(bots))
		}
	}
	row := truncate(text, width-textfmt.DisplayWidth(mark))
	row += strings.Repeat(" ", max(0, width-textfmt.DisplayWidth(mark)-textfmt.DisplayWidth(row)))
	switch {
	case selected && focused:
		return m.st.selectedRow.Render(row) + mark
	case selected:
		return styleTitle.Render(row) + mark
	case expanded:
		return styleTitle.Foreground(m.st.accentColor).Render(row) + mark
	}
	return m.st.subtle.Render(row) + mark
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
	var pending map[backend.ChatID]bool
	answers := func(backend.ChatEntry) bool { return false }
	if m.mgr != nil && m.mgr.Agents != nil && m.active != nil {
		key := m.mgr.AccountKey(m.active)
		pending = m.mgr.Agents.PendingChats(key)
		answers = func(e backend.ChatEntry) bool { return m.mgr.Agents.Answers(key, e) }
	}
	for _, entry := range m.entries {
		agent := pending[entry.ID] || answers(entry)
		rows = append(rows, "    "+m.st.chatListLine(entry, agent, "", index == m.selected,
			focused, max(1, width-4)))
		index++
	}
	switch {
	case m.loading:
		rows = append(rows, "      "+m.spin+m.st.subtle.Render(" Loading chats…"))
	case len(m.entries) == 0:
		rows = append(rows, m.st.subtle.Render("      No chats here"))
	}
	rows = append(rows, "    "+m.st.dim.Render(strings.Repeat("─", max(1, width-4))))

	for _, a := range m.actions() {
		if a.section != "" {
			rows = append(rows, "    "+m.st.dim.Render(sectionRule("── "+a.section+" ",
				max(1, width-4))))
		}
		indent := "    "
		if a.chat {
			indent = "      "
		}
		line := m.st.chatListLine(a.entry, false, a.mark, index == m.selected, focused,
			max(1, width-len(indent)))
		if index != m.selected && !a.chat && a.label == "" {
			line = m.st.subtle.Render(line)
		}
		rows = append(rows, indent+line)
		index++
	}
	for _, a := range below {
		rows = append(rows, m.accountRow(a, false, index == m.selected, focused, width))
		index++
	}
	manage := m.st.chatListLine(backend.ChatEntry{Title: "Settings…"}, false, "", index == m.selected,
		focused, width)
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

func (m chatListModel) askDelete(entry backend.ChatEntry, revoke bool) *chatListConfirm {
	return &chatListConfirm{entry: entry, revoke: revoke, modal: newConfirmModal(m.st,
		"Delete chat", deleteChatQuestion(entry, revoke), "Delete", true)}
}

func (m chatListModel) helpKeys() []helpKey {
	keys := []helpKey{{"↑↓", "select"}, {"enter", "open"}, {"/", "search"}, {"u", "unread"}}
	if m.mentions {
		keys = append(keys, helpKey{"m", "mentions"})
	}
	keys = append(keys, helpKey{"a", "manage accounts"}, helpKey{"r", "refresh"})
	if m.caps.DeleteChat {
		keys = append(keys, helpKey{"M-d", "delete chat"})
	}
	return keys
}
