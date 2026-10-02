package main

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type chatListModel struct {
	ctx           context.Context
	b             backend
	entries       []chatEntry
	selected      int
	archived      bool
	archiveUnread int
	accountName   string
	caps          capabilities
	confirm       *chatDeleteConfirm
	width, height int
}

type chatDeleteConfirm struct {
	entry  chatEntry
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

func newChatListModel(ctx context.Context, b backend, entries []chatEntry, archived bool, archiveUnread int) chatListModel {
	w, h := termSize()
	return chatListModel{
		ctx:           ctx,
		b:             b,
		entries:       entries,
		archived:      archived,
		archiveUnread: archiveUnread,
		accountName:   b.AccountLabel(),
		caps:          b.Capabilities(),
		width:         w,
		height:        h,
	}
}

func (m chatListModel) maxIndex() int { return len(m.entries) + 2 }

func (m chatListModel) Init() tea.Cmd {
	return listenBackend(m.b)
}

func listenBackend(b backend) tea.Cmd {
	return func() tea.Msg {
		<-b.Changed()
		return backendChanged{}
	}
}

func (m chatListModel) Update(msg tea.Msg) (chatListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case backendChanged:
		m.b.Drain()
		for i := range m.entries {
			m.entries[i].Unread = m.b.CachedUnread(m.entries[i])
		}
		return m, listenBackend(m.b)

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
	return m, nil
}

func (m chatListModel) View() tea.View {
	var b strings.Builder
	w := m.width

	b.WriteString(styleTitle.Width(w).Render(m.accountName))
	b.WriteByte('\n')
	if m.archived {
		b.WriteString("Archived Chats\n")
	}
	for i, entry := range m.entries {
		b.WriteString(chatListLine(entry, i, i == m.selected, w))
		b.WriteByte('\n')
	}
	if len(m.entries) == 0 {
		b.WriteString("No chats here. Press r to refresh or q to quit.\n")
	}

	archiveLabel := "Archived Chats"
	if m.archived {
		archiveLabel = "Back to Chats"
	} else if m.archiveUnread > 0 {
		archiveLabel += styleUnread.Render(fmt.Sprintf(" - %d*", m.archiveUnread))
	}
	marker := "  "
	if m.selected == len(m.entries) {
		marker = "> "
	}
	b.WriteString(fmt.Sprintf("%s    %s\n", marker, archiveLabel))

	marker = "  "
	if m.selected == len(m.entries)+1 {
		marker = "> "
	}
	b.WriteString(fmt.Sprintf("%s    Search users…\n", marker))

	marker = "  "
	if m.selected == len(m.entries)+2 {
		marker = "> "
	}
	b.WriteString(fmt.Sprintf("%s    Accounts…\n", marker))

	if m.confirm != nil {
		scope := "only for you"
		if m.confirm.revoke {
			scope = "for both sides"
		}
		b.WriteString(fmt.Sprintf("\nDelete chat with %s %s? y to confirm; any other key cancels.",
			plain(m.confirm.entry.Title), scope))
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar(chatListHelp, w))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

var chatListHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "open"},
	{"/", "search"},
	{"a", "accounts"},
	{"r", "refresh"},
	{"q", "quit"},
}
