package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type chatListModel struct {
	ctx           context.Context
	b             backend.Backend
	entries       []backend.ChatEntry
	selected      int
	archived      bool
	archiveUnread int
	caps          backend.Capabilities
	confirm       *chatDeleteConfirm
	offset        int
	width, height int
}

type chatDeleteConfirm struct {
	entry  backend.ChatEntry
	revoke bool
}

type chatListChoice struct{ index int }
type chatListRefresh struct{}
type chatListAccounts struct{}
type chatListSearch struct{}
type chatListArchiveToggle struct{}
type chatListQuit struct{}
type chatListDeleteDone struct{ err error }
type backendChanged struct{}

func newChatListModel(ctx context.Context, b backend.Backend, entries []backend.ChatEntry, archived bool, archiveUnread int) chatListModel {
	w, h := termSize()
	return chatListModel{
		ctx:           ctx,
		b:             b,
		entries:       entries,
		archived:      archived,
		archiveUnread: archiveUnread,
		caps:          b.Capabilities(),
		width:         w,
		height:        h,
	}
}

func (m chatListModel) maxIndex() int { return len(m.entries) + 2 }

func (m chatListModel) Init() tea.Cmd { return nil }

func listenBackend(b backend.Backend) tea.Cmd {
	return func() tea.Msg {
		<-b.Changed()
		return backendChanged{}
	}
}

func (m chatListModel) refreshUnread() chatListModel {
	for i := range m.entries {
		m.entries[i].Unread = m.b.CachedUnread(m.entries[i])
	}
	return m
}

// row maps the selection to a display row; a separator sits between the
// chats and the archive/search/accounts actions.
func (m chatListModel) row(index int) int {
	if index < len(m.entries) {
		return index
	}
	if len(m.entries) == 0 {
		return index + 2 // "No chats here" placeholder row
	}
	return index + 1
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
		case "ctrl+d":
			return m, func() tea.Msg { return chatListAccounts{} }
		case "q":
			return m, func() tea.Msg { return chatListQuit{} }
		case "r":
			return m, func() tea.Msg { return chatListRefresh{} }
		case "/":
			return m, func() tea.Msg { return chatListSearch{} }
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
			if m.selected >= len(m.entries) || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[m.selected]
			if !entry.IsDM {
				return m, nil
			}
			m.confirm = &chatDeleteConfirm{entry: entry, revoke: false}
		case "ctrl+alt+d":
			if m.selected >= len(m.entries) || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[m.selected]
			if !entry.IsDM || entry.IsSelf {
				return m, nil
			}
			m.confirm = &chatDeleteConfirm{entry: entry, revoke: true}
		case "enter":
			idx := m.selected
			switch {
			case idx < len(m.entries):
				return m, func() tea.Msg { return chatListChoice{idx} }
			case idx == len(m.entries):
				return m, func() tea.Msg { return chatListArchiveToggle{} }
			case idx == len(m.entries)+1:
				return m, func() tea.Msg { return chatListSearch{} }
			case idx == len(m.entries)+2:
				return m, func() tea.Msg { return chatListAccounts{} }
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

// body renders the list rows for a width×height area.
func (m chatListModel) body(width, height int, focused bool) []string {
	var rows []string
	for i, entry := range m.entries {
		rows = append(rows, chatListLine(entry, i == m.selected, focused, width))
	}
	if len(m.entries) == 0 {
		rows = append(rows, styleSubtle.Render("  No chats here"))
	}
	rows = append(rows, styleDim.Render(strings.Repeat("─", width)))

	archive := backend.ChatEntry{Title: "Archived chats", Unread: m.archiveUnread}
	if m.archived {
		archive = backend.ChatEntry{Title: "Back to chats"}
	}
	actions := []backend.ChatEntry{archive, {Title: "Search users…"}, {Title: "Accounts…"}}
	for i, a := range actions {
		index := len(m.entries) + i
		line := chatListLine(a, index == m.selected, focused, width)
		if index != m.selected {
			line = styleSubtle.Render(line)
		}
		rows = append(rows, line)
	}
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

var chatListHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "open"},
	{"/", "search"},
	{"a", "accounts"},
	{"r", "refresh"},
	{"q", "quit"},
}
