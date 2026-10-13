package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type tuiFocus int

const (
	focusList tuiFocus = iota
	focusChat
)

type tuiResult struct {
	accounts bool
	settings bool
	agents   bool
	switchTo *account.Account
}

// tuiModel is one account's main screen: header, chat list, chat, footer.
// At twoPaneWidth columns and wider the list and chat sit side by side.
type tuiModel struct {
	ctx           context.Context
	mgr           *account.AccountManager
	active        *account.Account
	b             backend.Backend
	events        *backend.Subscription // every event of this account
	histories     map[backend.ChatID][]string
	entries       []backend.ChatEntry
	archived      bool
	archiveUnread int
	list          chatListModel
	chat          chatViewModel
	chatOpen      bool
	search        userSearchModel
	searchOpen    bool
	unread        unreadModel
	unreadOpen    bool
	focus         tuiFocus
	result        *tuiResult
	loadErr       error
	width         int
	height        int
}

type tuiDialogsLoaded struct {
	entries       []backend.ChatEntry
	archiveUnread int
	err           error
}

type tuiRetryDialogs struct{}
type tuiChipsTick struct{}

// busEvent is one event from the account's subscription; ok is false once
// the subscription has been closed.
type busEvent struct {
	ev backend.Event
	ok bool
}

func listenEvents(sub *backend.Subscription) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-sub.C
		return busEvent{ev, ok}
	}
}

func loadDialogs(ctx context.Context, b backend.Backend, archived bool) tea.Cmd {
	return func() tea.Msg {
		entries, archiveUnread, err := b.Dialogs(ctx, archived)
		return tuiDialogsLoaded{entries, archiveUnread, err}
	}
}

// chipsTick refreshes the account chips; mgr.changed already has a consumer.
func chipsTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tuiChipsTick{} })
}

func newTuiModel(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, events *backend.Subscription, histories map[backend.ChatID][]string, entries []backend.ChatEntry, archiveUnread int) tuiModel {
	w, h := termSize()
	m := tuiModel{
		ctx:           ctx,
		mgr:           mgr,
		active:        active,
		b:             b,
		events:        events,
		histories:     histories,
		entries:       entries,
		archiveUnread: archiveUnread,
		width:         w,
		height:        h,
	}
	m.list = newChatListModel(ctx, mgr, active, b, entries, false, archiveUnread)
	return m.sized()
}

// Init opens the Unread view, so entering an account starts there.
func (m tuiModel) Init() tea.Cmd {
	openUnread := func() tea.Msg { return chatListUnread{} }
	return tea.Batch(listenEvents(m.events), chipsTick(), pruneMediaCache, openUnread)
}

func (m tuiModel) twoPane() bool { return m.width >= twoPaneWidth }

// paneOpen reports whether the right-hand pane holds a chat, the user search
// or the unread view.
func (m tuiModel) paneOpen() bool { return m.chatOpen || m.searchOpen || m.unreadOpen }

// forwarding reports whether the forward picker is up over the chat it came from.
func (m tuiModel) forwarding() bool { return m.chatOpen && m.searchOpen }

func (m tuiModel) bodyHeight() int {
	return max(4, m.height-lipgloss.Height(m.footer()))
}

// sized pushes the current layout down to the list and chat models.
func (m tuiModel) sized() tuiModel {
	bodyHeight := m.bodyHeight()
	if !m.twoPane() && m.paneOpen() {
		m.focus = focusChat
	}
	if m.twoPane() {
		m.list.width, m.list.height = sidebarWidth-4, bodyHeight-2
		m.chat.width, m.chat.height = m.width-sidebarWidth, bodyHeight
	} else {
		m.list.width, m.list.height = m.width-4, bodyHeight-2
		m.chat.width, m.chat.height = m.width, bodyHeight
	}
	if m.searchOpen && m.twoPane() {
		m.search = m.search.resized(m.width-sidebarWidth, bodyHeight)
	} else if m.searchOpen {
		m.search = m.search.resized(m.width, bodyHeight)
	}
	if m.unreadOpen && m.twoPane() {
		m.unread = m.unread.resized(m.width-sidebarWidth, bodyHeight)
	} else if m.unreadOpen {
		m.unread = m.unread.resized(m.width, bodyHeight)
	}
	m.chat.embedded = m.twoPane()
	m.chat.focused = m.focus == focusChat
	m.list = m.list.scrolled()
	return m
}

func (m tuiModel) done(result tuiResult) (tuiModel, tea.Cmd) {
	m.result = &result
	return m, tea.Quit
}

func (m tuiModel) closeChat() tuiModel {
	m.searchOpen = false
	m.unreadOpen = false
	if !m.chatOpen {
		m.focus = focusList
		return m
	}
	m.histories[m.chat.entry.ID] = *m.chat.histories
	m.chat.cleanup()
	m.chatOpen = false
	m.focus = focusList
	return m
}

func (m tuiModel) openChat(entry backend.ChatEntry) (tuiModel, tea.Cmd) {
	// Closing a chat marks it read, so the list's counts are stale after it.
	var reload tea.Cmd
	if m.chatOpen {
		reload = loadDialogs(m.ctx, m.b, m.archived)
	}
	m = m.closeChat()
	h := m.histories[entry.ID]
	if h == nil {
		h = make([]string, 0)
	}
	m.histories[entry.ID] = h
	m.chat = newChatViewModel(m.ctx, m.b, entry, &h)
	m.chatOpen = true
	m.focus = focusChat
	m = m.sized()
	return m, tea.Batch(m.chat.Init(), reload)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next.sized(), cmd
}

func (m tuiModel) update(msg tea.Msg) (tuiModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiChipsTick:
		return m, chipsTick()

	case tea.MouseClickMsg:
		return m.handleClick(msg.Mouse())

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)

	case busEvent:
		// The root owns the account's only UI subscription and routes events
		// for the open chat, so the list and chat never race for them.
		if !msg.ok {
			return m, nil
		}
		var cmd tea.Cmd
		if m.chatOpen && m.chat.wants(msg.ev) {
			m.chat, cmd = m.chat.Update(chatEvent{msg.ev})
		}
		m.list = m.list.refreshUnread()
		if msg.ev.Kind == backend.EventUnread && msg.ev.Chat == (backend.ChatID{}) {
			cmd = tea.Batch(cmd, loadDialogs(m.ctx, m.b, m.archived))
		}
		return m, tea.Batch(cmd, listenEvents(m.events))

	case chatHistoryLoaded:
		if !m.chatOpen {
			return m, nil
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd

	case chatReturnToList:
		m = m.closeChat()
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatToUnread:
		// Esc from a chat lands on Unread; a second Esc reaches the list.
		// The list's counts predate reading this chat, so leave it out.
		read := m.chat.entry.ID
		m = m.closeChat()
		var entries []backend.ChatEntry
		for _, e := range m.entries {
			if e.ID != read {
				entries = append(entries, e)
			}
		}
		var cmd tea.Cmd
		m.unread, cmd = newUnread(m.ctx, m.b, entries)
		m.unreadOpen = true
		m.focus = focusChat
		return m.sized(), tea.Batch(cmd, loadDialogs(m.ctx, m.b, m.archived))

	case chatForwardReq:
		var cmd tea.Cmd
		m.search, cmd = newForwardSearch(m.ctx, m.b, m.entries, msg.req)
		m.searchOpen = true
		m.focus = focusChat
		return m, cmd

	case userForwardDone:
		if msg.err != nil || !m.forwarding() {
			break
		}
		m.searchOpen = false
		m.chat.notice = "Message forwarded to " + textfmt.Plain(msg.target.Title)
		return m, nil

	case userSearchOpened:
		if msg.err != nil {
			break
		}
		return m.openChat(msg.entry)

	case unreadLoaded:
		if m.unreadOpen {
			m.unread, _ = m.unread.Update(msg)
		}
		return m, nil

	case unreadPicked:
		return m.openChat(msg.entry)

	case unreadClosed:
		m = m.closeChat()
		return m, nil

	case userSearchClosed:
		if m.forwarding() {
			m.searchOpen = false
			return m, nil
		}
		m = m.closeChat()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		// A paste goes wherever typing would.
		if !m.paneOpen() || m.focus != focusChat || (m.chatOpen && m.chat.locked) {
			return m, nil
		}
		var cmd tea.Cmd
		if m.searchOpen {
			m.search, cmd = m.search.Update(msg)
		} else if m.chatOpen {
			m.chat, cmd = m.chat.resetLock().updateInput(msg)
		}
		return m, cmd
	}

	if m.isListMsg(msg) {
		return m.updateList(msg)
	}
	// While forwarding, the chat underneath still has work in flight; each
	// model ignores messages that are not its own.
	var searchCmd, chatCmd tea.Cmd
	if m.searchOpen {
		m.search, searchCmd = m.search.Update(msg)
	}
	var inputCmd tea.Cmd
	if m.chatOpen {
		m.chat, chatCmd = m.chat.Update(msg)
		// Ctrl+V comes back from the input as a message of its own.
		if !m.searchOpen && m.focus == focusChat {
			m.chat, inputCmd = m.chat.updateInput(msg)
		}
	}
	return m, tea.Batch(searchCmd, chatCmd, inputCmd)
}

// overList reports whether column x of the body shows the chat list.
func (m tuiModel) overList(x int) bool {
	if m.twoPane() {
		return x < sidebarWidth
	}
	return !m.paneOpen()
}

// handleClick makes a left click on a row act like selecting it and pressing
// Enter; a left click on a message opens its attachment, a right click its
// actions.
func (m tuiModel) handleClick(mouse tea.Mouse) (tuiModel, tea.Cmd) {
	right := mouse.Button == tea.MouseRight
	if (mouse.Button != tea.MouseLeft && !right) || mouse.Y >= m.bodyHeight() {
		return m, nil
	}
	if m.chatOpen && m.chat.locked {
		return m.handleKey(keyPress(tea.KeyEnter)) // any key unlocks
	}
	var cmd tea.Cmd
	if m.menuShown() {
		x, y, w, h := m.menuRect()
		inside := mouse.X >= x && mouse.X < x+w && mouse.Y >= y && mouse.Y < y+h
		if right && inside {
			return m, nil
		}
		m.chat, cmd = m.chat.menuClick(mouse.X-x, mouse.Y-y, inside)
		return m, cmd
	}
	if right && (m.overList(mouse.X) || m.unreadOpen || m.searchOpen) {
		return m, nil
	}
	if m.overList(mouse.X) {
		if m.loadErr != nil || m.list.confirm != nil {
			return m, nil
		}
		m.focus = focusList
		i, ok := m.list.itemAt(mouse.Y - 1)
		if !ok || mouse.Y < 1 || mouse.Y > m.list.height {
			return m, nil
		}
		m.list.selected = i
		m.list, cmd = m.list.Update(keyPress(tea.KeyEnter))
		return m, cmd
	}
	if !m.paneOpen() {
		return m, nil
	}
	m.focus = focusChat
	x := mouse.X
	if m.twoPane() {
		x -= sidebarWidth
	}
	switch {
	case m.unreadOpen:
		if i, ok := m.unread.cardAt(mouse.Y); ok {
			return m.openChat(m.unread.cards[i].entry)
		}
	case m.searchOpen:
		if m.forwarding() {
			m.chat = m.chat.resetLock()
		}
		m.search, cmd = m.search.click(mouse.Y)
	default:
		m.chat, cmd = m.chat.Update(chatClick{x, mouse.Y, right})
	}
	return m, cmd
}

// handleWheel scrolls whichever list is under the pointer, as its up and
// down keys would, without moving the focus.
func (m tuiModel) handleWheel(msg tea.MouseWheelMsg) (tuiModel, tea.Cmd) {
	k, ok := wheelKey(msg)
	mouse := msg.Mouse()
	if !ok || mouse.Y >= m.bodyHeight() || (m.chatOpen && m.chat.locked) {
		return m, nil
	}
	var cmd tea.Cmd
	switch {
	case m.menuShown():
		m.chat, cmd = m.chat.Update(k)
	case m.overList(mouse.X):
		if m.loadErr == nil && m.list.confirm == nil {
			m.list, cmd = m.list.Update(k)
		}
	case m.unreadOpen:
		m.unread, cmd = m.unread.Update(k)
	case m.searchOpen:
		m.search, cmd = m.search.Update(k)
	case m.chatOpen:
		switch m.chat.subMode {
		case chatSubNone, chatSubThread, chatSubMenu, chatSubSearch, chatSubUpload, chatSubReader:
			// Other submodes are prompts that any key would answer.
			m.chat, cmd = m.chat.Update(k)
		}
	}
	return m, cmd
}

func (m tuiModel) isListMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tuiDialogsLoaded, tuiRetryDialogs, chatListChoice, chatListRefresh,
		chatListArchiveToggle, chatListSearch, chatListUnread, chatListAccounts, chatListSettings, chatListQuit, chatListSwitch,
		chatListDeleteDone:
		return true
	}
	return false
}

func (m tuiModel) handleKey(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	var cmd tea.Cmd
	if m.chatOpen && m.chat.locked {
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}
	paneFocused := m.paneOpen() && m.focus == focusChat
	chatFocused := m.chatOpen && !m.searchOpen && m.focus == focusChat
	idle := !paneFocused || (chatFocused && m.chat.subMode == chatSubNone && m.chat.input.Value() == "")

	k := msg.String()
	if k == "alt+a" && idle && m.mgr != nil && m.mgr.Agents != nil {
		m = m.closeChat()
		return m.done(tuiResult{agents: true})
	}
	switch {
	case k == "tab" && m.twoPane() && m.paneOpen() && (!chatFocused || m.chat.subMode == chatSubNone):
		if paneFocused {
			m.focus = focusList
		} else {
			m.focus = focusChat
		}
		return m, nil
	case k == "esc" && !paneFocused && m.paneOpen() && m.list.confirm == nil:
		m = m.closeChat()
		return m, loadDialogs(m.ctx, m.b, m.archived)
	}
	if paneFocused && m.unreadOpen {
		m.unread, cmd = m.unread.Update(msg)
		return m, cmd
	}
	if paneFocused && m.searchOpen {
		if m.forwarding() {
			// Typing in the picker counts as activity for the chat's lock.
			m.chat = m.chat.resetLock()
		}
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	if chatFocused {
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}
	return m.updateList(msg)
}

func (m tuiModel) updateList(msg tea.Msg) (tuiModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.loadErr != nil {
			m.loadErr = nil
			return m, loadDialogs(m.ctx, m.b, m.archived)
		}

	case tuiDialogsLoaded:
		if msg.err != nil {
			m.loadErr = msg.err
			if !backend.NetworkError(msg.err) {
				return m, nil
			}
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
				return tuiRetryDialogs{}
			})
		}
		m.loadErr = nil
		selected, offset := m.list.selected, m.list.offset
		m.entries = msg.entries
		m.archiveUnread = msg.archiveUnread
		m.list = newChatListModel(m.ctx, m.mgr, m.active, m.b, msg.entries, m.archived, msg.archiveUnread)
		m.list.selected = min(selected, m.list.maxIndex())
		m.list.offset = offset
		return m, nil

	case tuiRetryDialogs:
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListChoice:
		if msg.index >= 0 && msg.index < len(m.entries) {
			return m.openChat(m.entries[msg.index])
		}

	case chatListRefresh:
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListArchiveToggle:
		m = m.closeChat()
		m.archived = !m.archived
		m.list.selected, m.list.offset = m.list.before(), 0
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListSearch:
		m = m.closeChat()
		var cmd tea.Cmd
		m.search, cmd = newUserSearch(m.ctx, m.b, m.entries)
		m.searchOpen = true
		m.focus = focusChat
		return m, cmd

	case chatListUnread:
		m = m.closeChat()
		var cmd tea.Cmd
		m.unread, cmd = newUnread(m.ctx, m.b, m.entries)
		m.unreadOpen = true
		m.focus = focusChat
		return m.sized(), cmd

	case chatListAccounts:
		m = m.closeChat()
		return m.done(tuiResult{accounts: true})

	case chatListSettings:
		m = m.closeChat()
		return m.done(tuiResult{settings: true})

	case chatListSwitch:
		m = m.closeChat()
		return m.done(tuiResult{switchTo: msg.to})

	case chatListQuit:
		m = m.closeChat()
		return m.done(tuiResult{})
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m tuiModel) footer() string {
	if m.chatOpen && !m.searchOpen && (m.focus == focusChat || m.chat.opening != nil) {
		if line, ok := m.chat.footerLine(); ok {
			return truncate(line, m.width)
		}
	}
	var keys []helpKey
	notice := m.list.notice()
	switch {
	case m.searchOpen && m.focus == focusChat:
		keys = m.search.helpKeys()
		if m.twoPane() {
			keys = append(keys, helpKey{"tab", "chats"})
		}
	case m.unreadOpen && m.focus == focusChat:
		keys = m.unread.helpKeys()
		if m.twoPane() {
			keys = append(keys, helpKey{"tab", "chats"})
		}
	case m.chatOpen && m.focus == focusChat:
		keys = m.chat.helpKeys()
		notice = m.chat.notice
	case m.list.confirm != nil:
		keys = chatConfirmHelp
	default:
		keys = append(keys, m.list.helpKeys()...)
		if m.searchOpen {
			keys = append(keys, helpKey{"tab", "search"}, helpKey{"esc", "close search"})
		}
		if m.unreadOpen {
			keys = append(keys, helpKey{"tab", "unread"}, helpKey{"esc", "close unread"})
		}
		if m.chatOpen && !m.searchOpen {
			keys = append(keys, helpKey{"tab", "chat"}, helpKey{"esc", "close chat"})
			if notice == "" {
				notice = m.chat.notice
			}
		}
		if m.mgr != nil && m.mgr.Agents != nil {
			keys = append(keys, helpKey{"M-a", "agents"})
		}
	}
	return renderFooter(keys, notice, m.width)
}

func (m tuiModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	// The mouse clicks and scrolls; this takes over native selection
	// (Shift+drag in most terminals).
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m tuiModel) render() string {
	if m.chatOpen && m.chat.locked {
		return m.chat.lockView(m.width, m.height)
	}
	bodyHeight := m.bodyHeight()
	listFocused := m.focus == focusList
	var body string
	switch {
	case m.loadErr != nil && !m.paneOpen():
		msg := styleError.Render("Error: "+textfmt.CleanError(m.loadErr)) + "\n" +
			styleSubtle.Render("Press any key to retry.")
		body = renderBox(m.list.title(), centerLines(m.width-4, bodyHeight-2, msg),
			m.width, bodyHeight, colorRed)
	case m.twoPane():
		list := renderBox(m.list.title(), m.list.body(sidebarWidth-4, bodyHeight-2, listFocused),
			sidebarWidth, bodyHeight, boxBorder(listFocused))
		right := m.emptyPane(m.width-sidebarWidth, bodyHeight)
		switch {
		case m.searchOpen:
			right = m.search.view(m.width-sidebarWidth, bodyHeight, !listFocused)
		case m.unreadOpen:
			right = m.unread.view(!listFocused)
		case m.chatOpen:
			right = m.chat.View()
		}
		body = joinColumns(list, right)
	case m.searchOpen:
		body = m.search.view(m.width, bodyHeight, true)
	case m.unreadOpen:
		body = m.unread.view(true)
	case m.chatOpen:
		body = m.chat.View()
	default:
		body = renderBox(m.list.title(), m.list.body(m.width-4, bodyHeight-2, true),
			m.width, bodyHeight, colorAccent)
	}
	if m.menuShown() {
		x, y, _, _ := m.menuRect()
		body = fadeUnder(body, m.chat.menu.View(m.chat.width), x, y)
	}
	return body + "\n" + m.footer()
}

// menuShown reports whether the open chat's message actions modal is up.
func (m tuiModel) menuShown() bool {
	return m.chatOpen && !m.searchOpen && !m.unreadOpen && m.chat.menuOpen()
}

// menuRect is where the message actions modal sits: centred over the chat.
func (m tuiModel) menuRect() (x, y, w, h int) {
	left := 0
	if m.twoPane() {
		left = sidebarWidth
	}
	w, h = m.chat.menu.size(m.chat.width)
	return left + (m.chat.width-w)/2, max(0, (m.bodyHeight()-h)/2), w, h
}

func (m tuiModel) emptyPane(width, height int) string {
	hint := lipgloss.JoinVertical(lipgloss.Center,
		renderLogo(width-4),
		"",
		styleSubtle.Render("Pick a chat and press ⏎"),
		styleDim.Render(textfmt.Plain(m.b.AccountLabel())),
	)
	return renderBox("", centerLines(width-4, height-2, hint), width, height, colorDim)
}

// tuiSelectChat runs one account's main screen until the user quits (nil),
// asks for the account picker, or switches account.
func tuiSelectChat(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, histories map[backend.ChatID][]string, entries []backend.ChatEntry, archiveUnread int) error {
	events := b.Events().Subscribe(backend.Filter{})
	defer events.Close()
	active.ClearUnseen()
	defer active.ClearUnseen()
	m := newTuiModel(ctx, mgr, active, b, events, histories, entries, archiveUnread)
	result, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	final := result.(tuiModel)
	switch {
	case final.result == nil:
		return nil
	case final.result.switchTo != nil:
		return accountSwitch{to: final.result.switchTo}
	case final.result.accounts:
		return accountsRequested{}
	case final.result.settings:
		return settingsRequested{}
	case final.result.agents:
		return agentsRequested{}
	}
	return nil
}
