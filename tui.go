package main

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

type tuiMode int

const (
	tuiModeList tuiMode = iota
	tuiModeChat
)

type tuiResult struct {
	index   int
	refresh bool
	err     error
}

type tuiModel struct {
	ctx           context.Context
	b             backend
	histories     map[chatID][]string
	entries       []chatEntry
	archived      bool
	archiveUnread int
	mode          tuiMode
	list          chatListModel
	chat          chatViewModel
	result        *tuiResult
	loadErr       error
	width         int
	height        int
}

type tuiDialogsLoaded struct {
	entries       []chatEntry
	archiveUnread int
	err           error
}

type tuiRetryDialogs struct{}

func loadDialogs(ctx context.Context, b backend, archived bool) tea.Cmd {
	return func() tea.Msg {
		entries, archiveUnread, err := b.Dialogs(ctx, archived)
		return tuiDialogsLoaded{entries, archiveUnread, err}
	}
}

func newTuiModel(ctx context.Context, b backend, histories map[chatID][]string, entries []chatEntry, archived bool, archiveUnread int) tuiModel {
	w, h := termSize()
	m := tuiModel{
		ctx:           ctx,
		b:             b,
		histories:     histories,
		entries:       entries,
		archived:      archived,
		archiveUnread: archiveUnread,
		width:         w,
		height:        h,
	}
	m.list = newChatListModel(ctx, b, entries, archived, archiveUnread)
	m.list.width, m.list.height = w, h
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return m.list.Init()
}

func (m tuiModel) done(index int, refresh bool) (tea.Model, tea.Cmd) {
	m.result = &tuiResult{index: index, refresh: refresh}
	return m, tea.Quit
}

func (m tuiModel) openChat(index int) (tea.Model, tea.Cmd) {
	entry := m.entries[index]
	history := m.histories[entry.ID]
	if history == nil {
		history = make([]string, 0)
	}
	m.histories[entry.ID] = history
	h := m.histories[entry.ID]
	m.chat = newChatViewModel(m.ctx, m.b, entry, &h)
	m.chat.width, m.chat.height = m.width, m.height
	m.mode = tuiModeChat
	return m, m.chat.Init()
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == tuiModeChat {
			m.chat.width, m.chat.height = msg.Width, msg.Height
		} else {
			m.list.width, m.list.height = msg.Width, msg.Height
		}
		return m, nil
	}

	if m.mode == tuiModeChat {
		return m.updateChat(msg)
	}
	return m.updateList(msg)
}

func (m tuiModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.loadErr != nil {
			m.loadErr = nil
			return m, loadDialogs(m.ctx, m.b, m.archived)
		}

	case tuiDialogsLoaded:
		if msg.err != nil {
			if !networkError(msg.err) {
				m.loadErr = msg.err
				return m, nil
			}
			m.loadErr = msg.err
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
				return tuiRetryDialogs{}
			})
		}
		m.entries = msg.entries
		m.archiveUnread = msg.archiveUnread
		m.list = newChatListModel(m.ctx, m.b, msg.entries, m.archived, msg.archiveUnread)
		m.list.width, m.list.height = m.width, m.height
		return m, m.list.Init()

	case tuiRetryDialogs:
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListChoice:
		if msg.index >= 0 && msg.index < len(m.entries) {
			return m.openChat(msg.index)
		}

	case chatListRefresh:
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListArchiveToggle:
		m.archived = !m.archived
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListSearch:
		return m.done(len(m.entries)+1, false)

	case chatListAccounts:
		return m.done(len(m.entries)+2, false)

	case chatListQuit:
		return m.done(-1, false)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m tuiModel) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatReturnToList:
		m.histories[m.chat.entry.ID] = *m.chat.histories
		m.chat.cleanup()
		m.mode = tuiModeList
		return m, loadDialogs(m.ctx, m.b, m.archived)
	case chatForwardReq:
		m.histories[m.chat.entry.ID] = *m.chat.histories
		m.chat.cleanup()
		m.mode = tuiModeList
		m.result = &tuiResult{err: msg.req}
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.chat, cmd = m.chat.Update(msg)
	return m, cmd
}

func (m tuiModel) View() tea.View {
	var content string
	switch {
	case m.loadErr != nil:
		content = fmt.Sprintf("Error: %s\nPress any key to retry.", cleanError(m.loadErr))
	case m.mode == tuiModeChat:
		content = m.chat.View()
	default:
		return m.list.View()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func tuiSelectChat(ctx context.Context, b backend, histories map[chatID][]string, entries []chatEntry, archived bool, archiveUnread int) (int, bool, error) {
	m := newTuiModel(ctx, b, histories, entries, archived, archiveUnread)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return -1, false, fmt.Errorf("run TUI: %w", err)
	}
	final := result.(tuiModel)
	if final.result == nil {
		return -1, false, nil
	}
	return final.result.index, final.result.refresh, final.result.err
}
