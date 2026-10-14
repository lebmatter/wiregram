package tui

import (
	"context"
	"fmt"
	"strings"
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
	switchTo *account.Account
}

// tuiModel is one account's main screen: header, chat list, chat, footer.
// At twoPaneWidth columns and wider the list and chat sit side by side.
type tuiModel struct {
	st            *styles
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
	// agentPane is the account's Chat Agent settings, open in the chat's place.
	agentPane agentPaneModel
	agentOpen bool
	// wiz creates the account's agent, drawn as a form over the screen.
	wiz *wizardModel
	// agentsCtx ends with this screen, so its wait on the agent runtime
	// cannot outlive it and swallow the next screen's signal.
	agentsCtx context.Context
	notice    string
	// welcome is up the first time an account opens after being added.
	welcome *welcomeModal
	// alert tells the user something, centred over the screen; any key or
	// click closes it.
	alert *confirmModal
	// backToMentions sends Esc from the open chat back to Mentions, the
	// view it was opened from, rather than Unread.
	backToMentions bool
	focus          tuiFocus
	result         *tuiResult
	kitty          *kittyImages // nil without kitty graphics
	loadErr        error
	width          int
	height         int
}

type tuiDialogsLoaded struct {
	entries       []backend.ChatEntry
	archiveUnread int
	err           error
}

type tuiRetryDialogs struct{}
type agentRuntimeChanged struct{}
type agentApproveDone struct{ err error }

func listenAgents(ctx context.Context, mgr *account.AccountManager) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-mgr.Agents.Changed():
			return agentRuntimeChanged{}
		case <-ctx.Done():
			return nil
		}
	}
}

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
		st:            newStyles(loadTheme(mgr.Store)),
		ctx:           ctx,
		mgr:           mgr,
		active:        active,
		b:             b,
		events:        events,
		histories:     histories,
		entries:       entries,
		archiveUnread: archiveUnread,
		agentsCtx:     ctx,
		width:         w,
		height:        h,
	}
	if kittyGraphics() {
		m.kitty = &kittyImages{}
	}
	m.list = newChatListModel(ctx, m.st, mgr, active, b, entries, false, archiveUnread)
	return m.sized()
}

// Init opens the Unread view, so entering an account starts there.
func (m tuiModel) Init() tea.Cmd {
	openUnread := func() tea.Msg { return chatListUnread{} }
	cmds := []tea.Cmd{listenEvents(m.events), chipsTick(), pruneMediaCache, openUnread}
	if m.mgr != nil && m.mgr.Agents != nil {
		cmds = append(cmds, listenAgents(m.agentsCtx, m.mgr))
	}
	return tea.Batch(cmds...)
}

func (m tuiModel) twoPane() bool { return m.width >= twoPaneWidth }

// paneOpen reports whether the right-hand pane holds a chat, the unread view
// or the agent pane.
func (m tuiModel) paneOpen() bool { return m.chatOpen || m.unreadOpen || m.agentOpen }

// forwarding reports whether the forward picker is up over the chat it came from.
func (m tuiModel) forwarding() bool { return m.searchOpen && m.search.forward != nil }

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
	if m.searchOpen {
		_, areaWidth := m.searchArea()
		m.search = m.search.resized(areaWidth, bodyHeight)
	}
	paneWidth := m.width
	if m.twoPane() {
		paneWidth -= sidebarWidth
	}
	if m.unreadOpen {
		m.unread = m.unread.resized(paneWidth, bodyHeight)
	}
	if m.agentOpen {
		m.agentPane = m.agentPane.resized(paneWidth, bodyHeight)
	}
	m.chat.embedded = m.twoPane()
	m.chat.focused = m.focus == focusChat
	m.list = m.list.scrolled()
	return m
}

func (m tuiModel) done(result tuiResult) (tuiModel, tea.Cmd) {
	m.result = &result
	return m, tea.Sequence(m.kitty.deleteAll(), tea.Quit)
}

func (m tuiModel) closeChat() tuiModel {
	m.searchOpen = false
	m.unreadOpen = false
	m.agentOpen = false
	if !m.chatOpen {
		m.focus = focusList
		return m
	}
	m.histories[m.chat.entry.ID] = *m.chat.histories
	m.chat.cleanup()
	m.chat.dropImages()
	m.chatOpen = false
	m.focus = focusList
	return m
}

func (m tuiModel) openChat(entry backend.ChatEntry) (tuiModel, tea.Cmd) {
	return m.openChatAt(entry, nil)
}

// openCard opens the chat of a card in the Unread or Mentions view; a
// mention opens with its newest message selected.
func (m tuiModel) openCard(c unreadCard) (tuiModel, tea.Cmd) {
	mentions := m.unread.mentions
	var target *backend.Message
	if mentions && len(c.msgs) > 0 {
		target = &c.msgs[len(c.msgs)-1]
	}
	m, cmd := m.openChatAt(c.entry, target)
	m.backToMentions = mentions
	return m, cmd
}

// openChatAt opens entry with the message target selected, if loaded, and
// its thread open on backends that keep threads on the server.
func (m tuiModel) openChatAt(entry backend.ChatEntry, target *backend.Message) (tuiModel, tea.Cmd) {
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
	m.chat = newChatViewModel(m.ctx, m.st, m.b, entry, &h, m.kitty)
	m.chat.target = target
	m.backToMentions = false
	m.chatOpen = true
	m = m.withAgentSent()
	m.focus = focusChat
	m = m.sized()
	return m, tea.Batch(m.chat.Init(), reload)
}

// Update handles msg, then fetches any image it brought into view and sends
// the terminal the kitty images the chat now draws.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next = next.withSuggestion().sized()
	if next.chatOpen && imagesMoved(msg) {
		cmd = tea.Batch(cmd, next.chat.loadVisibleImages(), next.chat.placeImages())
	}
	return next, tea.Batch(cmd, next.kitty.flush())
}

func (m tuiModel) update(msg tea.Msg) (tuiModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tuiChipsTick:
		return m, chipsTick()

	case agentRuntimeChanged:
		return m.withAgentSent(), listenAgents(m.agentsCtx, m.mgr)

	case chatSuggestion:
		return m.suggestionAct(msg.act)

	case agentApproveDone:
		if m.chatOpen {
			m.chat.notice = "Agent's reply sent"
			if msg.err != nil {
				m.chat.notice = textfmt.CleanError(msg.err)
			}
		}
		return m, nil

	case agentPaneClosed:
		m = m.closeChat()
		return m, nil

	case wizardFinished:
		return m.agentCreated(msg.answers)

	case wizardCancelled:
		m.wiz = nil
		return m, nil

	case tea.MouseClickMsg:
		if m.welcome != nil || m.alert != nil {
			m.welcome, m.alert = nil, nil
			return m, nil
		}
		return m.handleClick(msg.Mouse())

	case tea.MouseWheelMsg:
		if m.welcome != nil || m.alert != nil {
			return m, nil
		}
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
			if msg.ev.Message != nil && msg.ev.Message.Outgoing {
				m = m.withAgentSent() // the agent may have sent it
			}
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
		if m.backToMentions {
			m.unread, cmd = newMentions(m.ctx, m.st, m.b)
		} else {
			m.unread, cmd = newUnread(m.ctx, m.st, m.b, entries)
		}
		m.unreadOpen = true
		m.focus = focusChat
		return m.sized(), tea.Batch(cmd, loadDialogs(m.ctx, m.b, m.archived))

	case chatForwardReq:
		var cmd tea.Cmd
		m.search, cmd = newForwardSearch(m.ctx, m.st, m.b, m.entries, msg.req)
		m.searchOpen = true
		return m, cmd

	case chatMentionReq:
		mn, ok := m.b.(backend.Mentioner)
		if !ok || !m.chatOpen {
			break
		}
		var cmd tea.Cmd
		m.search, cmd = newMentionSearch(m.ctx, m.st, m.b, mn, m.chat.entry)
		m.searchOpen = true
		return m, cmd

	case userMentionPicked:
		m.searchOpen = false
		if m.chatOpen {
			m.chat = m.chat.insertMention(msg.text)
		}
		return m, nil

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
		return m.openCard(msg.card)

	case unreadClosed:
		m = m.closeChat()
		return m, nil

	case userSearchClosed:
		m.searchOpen = false
		if m.chatOpen && msg.insert != "" {
			m.chat = m.chat.insertMention(msg.insert)
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.welcome != nil || m.alert != nil {
			m.welcome, m.alert = nil, nil
			return m, nil
		}
		m.notice = ""
		return m.handleKey(msg)

	case tea.PasteMsg:
		// A paste goes wherever typing would.
		var cmd tea.Cmd
		if m.chatOpen && m.chat.locked {
			return m, nil
		}
		if m.wiz != nil {
			wiz, cmd := m.wiz.Update(msg)
			m.wiz = &wiz
			return m, cmd
		}
		if m.searchOpen {
			m.search, cmd = m.search.Update(msg)
			return m, cmd
		}
		if !m.paneOpen() || m.focus != focusChat {
			return m, nil
		}
		if m.agentOpen {
			m.agentPane, cmd = m.agentPane.Update(msg)
			return m, cmd
		}
		if m.chatOpen {
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
// chatX turns screen column x into a column of the chat pane.
func (m tuiModel) chatX(x int) int {
	if m.twoPane() {
		return x - sidebarWidth
	}
	return x
}

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
	if m.wiz != nil {
		if right {
			return m, nil
		}
		wiz, cmd := m.wiz.modalClick(mouse.X, mouse.Y, 0, m.width, m.bodyHeight())
		m.wiz = &wiz
		return m, cmd
	}
	if c, x, y, ok := m.confirmShown(); ok {
		if right {
			return m, nil
		}
		// The owner of the confirmation takes the answer as a key.
		switch yes, done := c.click(mouse.X, mouse.Y, x, y); {
		case yes:
			return m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
		case done:
			return m.handleKey(keyPress(tea.KeyEscape))
		}
		return m, nil
	}
	var cmd tea.Cmd
	if m.searchOpen {
		x, y, w, h := m.searchRect()
		inside := mouse.X >= x && mouse.X < x+w && mouse.Y >= y && mouse.Y < y+h
		switch {
		case !inside:
			return m, m.search.cancel()
		case !right:
			if m.chatOpen {
				m.chat = m.chat.resetLock()
			}
			m.search, cmd = m.search.click(mouse.Y - y)
		}
		return m, cmd
	}
	if m.menuShown() {
		x, y, w, h := m.menuRect()
		inside := mouse.X >= x && mouse.X < x+w && mouse.Y >= y && mouse.Y < y+h
		if right && inside {
			return m, nil
		}
		m.chat, cmd = m.chat.menuClick(mouse.X-x, mouse.Y-y, inside)
		return m, cmd
	}
	if right && (m.overList(mouse.X) || m.unreadOpen || m.agentOpen) {
		return m, nil
	}
	if m.overList(mouse.X) {
		if m.loadErr != nil {
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
	x := m.chatX(mouse.X)
	switch {
	case m.unreadOpen:
		if i, ok := m.unread.cardAt(mouse.Y); ok {
			return m.openCard(m.unread.cards[i])
		}
	case m.agentOpen:
		if i, ok := m.agentPane.rowAt(mouse.Y); ok && !m.agentPane.editing {
			m.agentPane.sel = i
			m.agentPane, cmd = m.agentPane.Update(keyPress(tea.KeyEnter))
		}
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
	if _, _, _, ok := m.confirmShown(); ok || m.wiz != nil {
		return m.handleKey(k)
	}
	var cmd tea.Cmd
	switch {
	case m.searchOpen:
		m.search, cmd = m.search.Update(k)
	case m.menuShown():
		m.chat, cmd = m.chat.Update(k)
	case m.overList(mouse.X):
		if m.loadErr == nil {
			m.list, cmd = m.list.Update(k)
		}
	case m.unreadOpen:
		m.unread, cmd = m.unread.Update(k)
	case m.agentOpen:
		if !m.agentPane.editing {
			m.agentPane, cmd = m.agentPane.Update(k)
		}
	case m.chatOpen && m.chat.overThread(m.chatX(mouse.X)):
		lines := 3
		if k.Code == tea.KeyDown {
			lines = -lines
		}
		m.chat = m.chat.scrollThread(lines)
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
		chatListArchiveToggle, chatListSearch, chatListUnread, chatListMentions, chatListAccounts, chatListSettings, chatListSwitch,
		chatListDeleteDone, chatListAgent:
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
	if m.wiz != nil {
		wiz, cmd := m.wiz.Update(msg)
		m.wiz = &wiz
		return m, cmd
	}
	if m.searchOpen {
		if m.chatOpen {
			// Typing in the modal counts as activity for the chat's lock.
			m.chat = m.chat.resetLock()
		}
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	paneFocused := m.paneOpen() && m.focus == focusChat
	chatFocused := m.chatOpen && m.focus == focusChat
	editing := m.agentOpen && (m.agentPane.editing || m.agentPane.confirm != nil)

	k := msg.String()
	switch {
	case k == "tab" && m.twoPane() && m.paneOpen() && !editing &&
		(!chatFocused || m.chat.subMode == chatSubNone):
		if paneFocused {
			m.focus = focusList
		} else {
			m.focus = focusChat
		}
		return m, nil
	case k == "left" && m.twoPane() && paneFocused && m.unreadOpen:
		m.focus = focusList
		return m, nil
	case k == "right" && m.twoPane() && !paneFocused && m.unreadOpen && m.list.confirm == nil:
		m.focus = focusChat
		return m, nil
	case k == "esc" && !paneFocused && m.paneOpen() && m.list.confirm == nil:
		m = m.closeChat()
		return m, loadDialogs(m.ctx, m.b, m.archived)
	}
	if paneFocused && m.unreadOpen {
		m.unread, cmd = m.unread.Update(msg)
		return m, cmd
	}
	if paneFocused && m.agentOpen {
		m.agentPane, cmd = m.agentPane.Update(msg)
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
		m.list = newChatListModel(m.ctx, m.st, m.mgr, m.active, m.b, msg.entries, m.archived, msg.archiveUnread)
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
		var cmd tea.Cmd
		m.search, cmd = newUserSearch(m.ctx, m.st, m.b, m.entries)
		m.searchOpen = true
		return m, cmd

	case chatListUnread:
		m = m.closeChat()
		var cmd tea.Cmd
		m.unread, cmd = newUnread(m.ctx, m.st, m.b, m.entries)
		m.unreadOpen = true
		m.focus = focusChat
		return m.sized(), cmd

	case chatListMentions:
		m = m.closeChat()
		var cmd tea.Cmd
		m.unread, cmd = newMentions(m.ctx, m.st, m.b)
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

	case chatListAgent:
		return m.agentRow()
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m tuiModel) footer() string {
	if m.welcome != nil {
		return m.st.footer(welcomeModalHelp, "", m.width)
	}
	if m.alert != nil {
		return m.st.footer(m.alert.helpKeys(), "", m.width)
	}
	if m.chatOpen && !m.searchOpen && (m.focus == focusChat || m.chat.opening != nil) {
		if line, ok := m.chat.footerLine(); ok {
			return truncate(line, m.width)
		}
	}
	var keys []helpKey
	notice := m.notice
	switch {
	case m.wiz != nil:
		keys = m.wiz.helpKeys()
	case m.searchOpen:
		keys = m.search.helpKeys()
	case m.unreadOpen && m.focus == focusChat:
		keys = m.unread.helpKeys()
		if m.twoPane() {
			keys = append(keys, helpKey{"tab", "chats"})
		}
	case m.agentOpen && m.focus == focusChat:
		keys = m.agentPane.helpKeys()
		if m.twoPane() && m.agentPane.confirm == nil && !m.agentPane.editing {
			keys = append(keys, helpKey{"tab", "chats"})
		}
		notice = m.agentPane.notice
	case m.chatOpen && m.focus == focusChat:
		keys = m.chat.helpKeys()
		notice = m.chat.notice
	case m.list.confirm != nil:
		keys = m.list.confirm.modal.helpKeys()
	default:
		keys = append(keys, m.list.helpKeys()...)
		if m.unreadOpen {
			name := strings.ToLower(m.unread.name())
			keys = append(keys, helpKey{"tab", name}, helpKey{"esc", "close " + name})
		}
		if m.agentOpen {
			keys = append(keys, helpKey{"tab", "agent"}, helpKey{"esc", "close agent"})
		}
		if m.chatOpen {
			keys = append(keys, helpKey{"tab", "chat"}, helpKey{"esc", "close chat"})
			if notice == "" {
				notice = m.chat.notice
			}
		}
		if !m.paneOpen() {
			keys = append(keys, helpKey{"esc", "home"})
		}
	}
	return m.st.footer(keys, notice, m.width)
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
			m.st.subtle.Render("Press any key to retry.")
		body = renderBox(m.list.title(), centerLines(m.width-4, bodyHeight-2, msg),
			m.width, bodyHeight, colorRed)
	case m.twoPane():
		list := renderBox(m.list.title(), m.list.body(sidebarWidth-4, bodyHeight-2, listFocused),
			sidebarWidth, bodyHeight, m.st.border(listFocused))
		right := m.emptyPane(m.width-sidebarWidth, bodyHeight)
		switch {
		case m.unreadOpen:
			right = m.unread.view(!listFocused)
		case m.agentOpen:
			right = m.agentPane.view(!listFocused)
		case m.chatOpen:
			right = m.chat.View()
		}
		body = joinColumns(list, right)
	case m.unreadOpen:
		body = m.unread.view(true)
	case m.agentOpen:
		body = m.agentPane.view(true)
	case m.chatOpen:
		body = m.chat.View()
	default:
		body = renderBox(m.list.title(), m.list.body(m.width-4, bodyHeight-2, true),
			m.width, bodyHeight, m.st.accentColor)
	}
	if m.menuShown() {
		x, y, _, _ := m.menuRect()
		body = m.st.fadeUnder(body, m.chat.menu.View(m.chat.width), x, y)
	}
	if m.searchOpen {
		x, y, _, _ := m.searchRect()
		body = m.st.fadeUnder(body, m.search.View(), x, y)
	}
	if c, x, y, ok := m.confirmShown(); ok {
		body = m.st.fadeUnder(body, c.View(), x, y)
	}
	if m.wiz != nil {
		box, _ := m.wiz.modal(m.width, bodyHeight)
		body = m.st.overlay(body, box, m.width, bodyHeight)
	}
	if m.alert != nil {
		w, h := m.alert.size()
		x, y := modalRect(m.width, bodyHeight, w, h)
		body = m.st.fadeUnder(body, m.alert.View(), x, y)
	}
	if m.welcome != nil {
		w, h := m.welcome.size()
		x, y := modalRect(m.width, bodyHeight, w, h)
		body = m.st.fadeUnder(body, m.welcome.View(), x, y)
	}
	return body + "\n" + m.footer()
}

// confirmShown is the delete confirmation that is up, if any, with its
// top-left corner: centred over the chat for one asked in the chat, else
// over the screen.
func (m tuiModel) confirmShown() (c *confirmModal, x, y int, ok bool) {
	left, width := 0, m.width
	switch {
	case m.list.confirm != nil:
		c = m.list.confirm.modal
	case m.chatOpen && m.chat.confirm != nil:
		c = m.chat.confirm
		if m.twoPane() {
			left = sidebarWidth
		}
		width = m.chat.width
	case m.agentOpen && m.agentPane.confirm != nil:
		c = m.agentPane.confirm
		if m.twoPane() {
			left = sidebarWidth
		}
		width = m.agentPane.width
	default:
		return nil, 0, 0, false
	}
	w, h := c.size()
	x, y = modalRect(width, m.bodyHeight(), w, h)
	return c, left + x, y, true
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

// searchArea is the part of the screen the search modal centres in: the chat
// for a forward or a mention, else the whole body.
func (m tuiModel) searchArea() (left, width int) {
	if m.chatOpen && (m.search.forward != nil || m.search.mention != nil) {
		if m.twoPane() {
			left = sidebarWidth
		}
		return left, m.chat.width
	}
	return 0, m.width
}

// searchRect is where the search modal sits.
func (m tuiModel) searchRect() (x, y, w, h int) {
	left, width := m.searchArea()
	w, h = m.search.width, m.search.height
	return left + (width-w)/2, max(0, (m.bodyHeight()-h)/2), w, h
}

func (m tuiModel) emptyPane(width, height int) string {
	hint := lipgloss.JoinVertical(lipgloss.Center,
		m.st.logo(width-4),
		"",
		m.st.subtle.Render("Pick a chat and press enter"),
		m.st.dim.Render(textfmt.Plain(m.b.AccountLabel())),
	)
	return renderBox("", centerLines(width-4, height-2, hint), width, height, m.st.dimColor)
}

// tuiSelectChat runs one account's main screen until the user quits (nil),
// asks for the account picker, or switches account.
func tuiSelectChat(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, histories map[backend.ChatID][]string, entries []backend.ChatEntry, archiveUnread int, welcome bool) error {
	events := b.Events().Subscribe(backend.Filter{})
	defer events.Close()
	agentsCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()
	active.ClearUnseen()
	defer active.ClearUnseen()
	m := newTuiModel(ctx, mgr, active, b, events, histories, entries, archiveUnread)
	m.agentsCtx = agentsCtx
	if welcome {
		w := newWelcomeModal(m.st, b, m.twoPane())
		m.welcome = &w
	}
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
	}
	return nil
}

// withSuggestion shows the open chat's waiting agent reply, if any.
func (m tuiModel) withSuggestion() tuiModel {
	if !m.chatOpen || m.mgr == nil || m.mgr.Agents == nil {
		return m
	}
	_, sug, _ := m.mgr.Agents.PendingFor(m.mgr.AccountKey(m.active), m.chat.entry.ID)
	m.chat.suggestion = sug.Text
	return m
}

// withAgentSent rereads which of the open chat's messages an agent sent.
func (m tuiModel) withAgentSent() tuiModel {
	if !m.chatOpen || m.mgr == nil || m.mgr.Agents == nil {
		return m
	}
	sent, err := m.mgr.Agents.Db.SentBy(m.mgr.AccountKey(m.active), m.chat.entry.ID.Id)
	if err != nil {
		return m
	}
	m.chat.agentSent = sent
	m.chat = m.chat.markAgents()
	return m
}

// suggestionAct approves, edits or discards the open chat's agent reply.
// Edit moves the text into the composer, where it is sent as the user's own.
func (m tuiModel) suggestionAct(act string) (tuiModel, tea.Cmd) {
	if !m.chatOpen || m.mgr == nil || m.mgr.Agents == nil {
		return m, nil
	}
	rt := m.mgr.Agents
	key, sug, ok := rt.PendingFor(m.mgr.AccountKey(m.active), m.chat.entry.ID)
	if !ok {
		return m, nil
	}
	switch act {
	case "approve":
		m.chat.notice = "Sending the agent's reply…"
		return m, func() tea.Msg { return agentApproveDone{rt.Approve(key)} }
	case "edit":
		rt.Discard(key)
		m.chat.input.SetValue(sug.Text)
		m.chat.input.CursorEnd()
		m.focus = focusChat
	case "discard":
		rt.Discard(key)
	}
	return m, nil
}

// agentRow opens the account's agent pane, or creates the agent first.
func (m tuiModel) agentRow() (tuiModel, tea.Cmd) {
	if !agentsAllowed(m.mgr, m.active) {
		return m, nil
	}
	if _, ok := m.mgr.Agents.AgentFor(m.active); ok {
		return m.openAgentPane()
	}
	providers, err := m.mgr.Agents.Db.ListProviders()
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	if len(providers) == 0 {
		m.alert = newAlertModal(m.st, "No AI provider",
			"Add an AI provider in Settings > AI providers first, then create the agent here.")
		return m, nil
	}
	wiz := newWizard(m.st, "New agent for "+accountLabel(m.active),
		agentSteps(providers, m.mgr.Dir), nil)
	m.wiz = &wiz
	return m, wiz.Init()
}

func (m tuiModel) openAgentPane() (tuiModel, tea.Cmd) {
	pane, err := newAgentPane(m.st, m.mgr, m.active, m.entries)
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m = m.closeChat()
	m.agentPane, m.agentOpen, m.focus = pane, true, focusChat
	return m.sized(), nil
}

// agentCreated saves the agent the wizard described and opens its pane.
func (m tuiModel) agentCreated(a wizardAnswers) (tuiModel, tea.Cmd) {
	switch a["confirm"] {
	case "restart":
		wiz := m.wiz.restart()
		m.wiz = &wiz
		return m, wiz.Init()
	case "cancel":
		m.wiz = nil
		return m, nil
	}
	m.wiz = nil
	rec, err := createAgent(m.mgr.Agents.Db, m.mgr.AccountKey(m.active), a)
	m.mgr.Agents.Reload()
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m, cmd := m.openAgentPane()
	m.agentPane.notice = "Agent " + textfmt.Plain(rec.Name) + " created"
	return m, cmd
}
