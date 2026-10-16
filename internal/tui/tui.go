package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
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
	// spin animates the list's loading row; spinning while its ticks run.
	spin       spinner.Model
	spinning   bool
	chat       chatViewModel
	chatOpen   bool
	search     userSearchModel
	searchOpen bool
	unread     unreadModel
	unreadOpen bool
	// agentPane is one of the account's bots (Chat Agents), open in the chat's place.
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
	// profile shows a clicked sender's profile over the chat; any key or
	// click closes it.
	profile *profileModal
	// backToMentions sends Esc from the open chat back to Mentions, the
	// view it was opened from, rather than Unread; backToInbox to the bot's
	// inbox, for a chat opened there.
	backToMentions bool
	backToInbox    *inboxBot
	focus          tuiFocus
	result         *tuiResult
	kitty          *kittyImages // nil without kitty graphics
	loadErr        error
	retryPending   bool // a tuiRetryDialogs is scheduled; one is enough
	width          int
	height         int
	// botEvents is the open bot chat's subscription to the bot's connection.
	botEvents *backend.Subscription
	// labels is the Labels modal, over the screen, or the move picker.
	labels *labelPicker
	// goToLabel is the label to open once the list reloads.
	goToLabel string
	// mail is the open email folder, in the chat's place.
	mail     mailListModel
	mailOpen bool
	// mailDraft is the draft kept while no folder is open.
	mailDraft *mailComposer
	// moving is the mail the move picker is choosing a folder for.
	moving []string
}

type tuiDialogsLoaded struct {
	archived      bool // which list it was loaded for
	entries       []backend.ChatEntry
	archiveUnread int
	err           error
}

type tuiRetryDialogs struct{}
type agentRuntimeChanged struct{}
type agentApproveDone struct{ err error }

// selfChatOpened answers the sidebar's You row.
type selfChatOpened struct {
	entry backend.ChatEntry
	err   error
}

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

// botBusEvent is one event from the open bot chat's subscription, sub.
type botBusEvent struct {
	ev  backend.Event
	ok  bool
	sub *backend.Subscription
}

func listenBotEvents(sub *backend.Subscription) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-sub.C
		return botBusEvent{ev, ok, sub}
	}
}

func loadDialogs(ctx context.Context, b backend.Backend, archived bool) tea.Cmd {
	return func() tea.Msg {
		entries, archiveUnread, err := b.Dialogs(ctx, archived)
		return tuiDialogsLoaded{archived, entries, archiveUnread, err}
	}
}

// chipsTick refreshes the account chips; mgr.changed already has a consumer.
func chipsTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tuiChipsTick{} })
}

func newTuiModel(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, events *backend.Subscription, histories map[backend.ChatID][]string, entries []backend.ChatEntry, archiveUnread int) tuiModel {
	w, h := termSize()
	m := tuiModel{
		st:            loadStyles(mgr.Store),
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
	m.spin = spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(m.st.accent))
	m.list = newChatListModel(ctx, m.st, mgr, active, b, entries, false, archiveUnread)
	m.spinning = m.list.loading
	return m.sized()
}

// Init opens the Unread view, or on email the inbox, so entering an account
// starts there.
func (m tuiModel) Init() tea.Cmd {
	start := func() tea.Msg { return chatListUnread{} }
	if _, ok := m.b.(backend.Mailbox); ok {
		start = func() tea.Msg { return chatListChoice{0} }
	}
	cmds := []tea.Cmd{listenEvents(m.events), chipsTick(), pruneMediaCache, start}
	if m.mgr != nil && m.mgr.Agents != nil {
		cmds = append(cmds, listenAgents(m.agentsCtx, m.mgr))
	}
	if m.spinning {
		cmds = append(cmds, m.spin.Tick)
	}
	return tea.Batch(cmds...)
}

func (m tuiModel) twoPane() bool { return m.width >= twoPaneWidth }

// paneOpen reports whether the right-hand pane holds a chat, the unread view,
// the agent pane or an email folder.
func (m tuiModel) paneOpen() bool {
	return m.chatOpen || m.unreadOpen || m.agentOpen || m.mailOpen
}

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
	if m.mailOpen {
		m.mail.width, m.mail.height = paneWidth, bodyHeight
		m.mail = m.mail.scrolled()
	}
	if m.labels != nil {
		m.labels.resized(m.width, bodyHeight)
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
	if m.botEvents != nil {
		m.botEvents.Close()
		m.botEvents = nil
	}
	m.searchOpen = false
	m.unreadOpen = false
	m.agentOpen = false
	if m.mailOpen {
		m.mailDraft, m.mailOpen = m.mail.composer, false
	}
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
	if bot := m.unread.bot; bot != nil {
		return m.openBotChat(*bot, m.unread.b, c.entry)
	}
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
	m.backToMentions, m.backToInbox = false, nil
	m.chatOpen = true
	m = m.withAgentSent()
	m.focus = focusChat
	m = m.sized()
	return m, tea.Batch(m.chat.Init(), reload)
}

// openFolder shows an email folder in the chat's place, with any kept draft.
func (m tuiModel) openFolder(mb backend.Mailbox, entry backend.ChatEntry) (tuiModel, tea.Cmd) {
	m = m.closeChat()
	var cmd tea.Cmd
	m.mail, cmd = newMailList(m.ctx, m.st, m.b, mb, entry, m.mailDraft)
	m.mailDraft = nil
	m.mailOpen = true
	m.focus = focusChat
	m = m.sized()
	if !m.spinning && m.mail.syncing() {
		m.spinning = true
		cmd = tea.Batch(cmd, m.spin.Tick)
	}
	return m, cmd
}

// openInbox shows the registered bot's private chats, over its connection.
func (m tuiModel) openInbox(bot inboxBot) (tuiModel, tea.Cmd) {
	b := m.mgr.Agents.BotBackend(bot.id)
	if b == nil {
		m.alert = newAlertModal(m.st, "Bot not connected",
			"@"+bot.handle+" is not connected: turn the bot on, with its account online.")
		return m, nil
	}
	m = m.closeChat()
	var cmd tea.Cmd
	m.unread, cmd = newInbox(m.ctx, m.st, b, bot,
		m.mgr.Agents.PendingChats(m.mgr.AccountKey(m.active), bot.id))
	m.unreadOpen = true
	m.focus = focusChat
	return m.sized(), cmd
}

// openBotChat opens one of the bot's private chats on b, its connection, with
// its own subscription there; whatever is sent in it is sent as the bot.
func (m tuiModel) openBotChat(bot inboxBot, b backend.Backend, entry backend.ChatEntry) (
	tuiModel, tea.Cmd) {
	m = m.closeChat()
	h := m.histories[entry.ID]
	if h == nil {
		h = make([]string, 0)
	}
	m.histories[entry.ID] = h
	m.chat = newChatViewModel(m.ctx, m.st, b, entry, &h, m.kitty)
	m.chat.bot = &bot
	m.chat.input.Placeholder = "Message " + textfmt.Plain(entry.Title) + " as @" + bot.handle + "…"
	m.backToMentions, m.backToInbox = false, &bot
	m.botEvents = b.Events().Subscribe(backend.Filter{})
	m.chatOpen = true
	m = m.withAgentSent()
	m.focus = focusChat
	m = m.sized()
	return m, tea.Batch(m.chat.Init(), listenBotEvents(m.botEvents))
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
	if tick, ok := msg.(spinner.TickMsg); ok && tick.ID == m.spin.ID() {
		m.spinning = m.list.loading || (m.mailOpen && m.mail.syncing())
		if !m.spinning {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.BlurMsg:
		// Leaving the terminal's tab or window locks the chat at once, unless
		// focus went to a viewer for an attachment the user just clicked.
		if m.chatOpen && m.chat.opening == nil && time.Since(m.chat.openedAt) > 5*time.Second {
			m.chat.locked = true
		}
		return m, nil

	case tuiChipsTick:
		return m, chipsTick()

	case agentRuntimeChanged:
		var reload tea.Cmd
		if m.unreadOpen && m.unread.bot != nil {
			// A reply may have started or stopped waiting in the open inbox.
			m.unread.waiting = m.mgr.Agents.PendingChats(m.mgr.AccountKey(m.active),
				m.unread.bot.id)
			m.unread, reload = m.unread.load()
		}
		if m.agentOpen {
			m.agentPane = m.agentPane.reloadLatest()
		}
		return m.withAgentSent(), tea.Batch(reload, listenAgents(m.agentsCtx, m.mgr))

	case chatSuggestion:
		return m.suggestionAct(msg.act)

	case agentApproveDone:
		if m.chatOpen {
			m.chat.notice = "Bot's reply sent"
			if msg.err != nil {
				m.chat.notice = textfmt.CleanError(msg.err)
			}
		}
		return m, nil

	case agentPaneClosed:
		m = m.closeChat()
		return m, nil

	case agentBotAlert:
		m.alert = newAlertModal(m.st, msg.title, msg.text)
		return m, nil

	case profileOpen:
		p, ok := msg.b.(backend.Profiler)
		if !ok {
			return m, nil
		}
		m.profile = newProfileModal(m.st, msg.msg)
		return m, loadProfile(msg.ctx, p, msg.msg.SenderID)

	case profileLoaded:
		if m.profile != nil {
			m.profile.loaded(msg)
		}
		return m, nil

	case agentBotDone:
		if !m.agentOpen || m.agentPane.rec.ID != msg.agentID {
			// The pane it was for has closed; a failure still needs saying.
			if msg.err != nil {
				a := msg.alert()
				m.alert = newAlertModal(m.st, a.title, a.text)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.agentPane, cmd = m.agentPane.Update(msg)
		return m, cmd

	case dmSearchDebounce, dmSearchResults, dmResolved:
		if !m.agentOpen {
			return m, nil
		}
		var cmd tea.Cmd
		m.agentPane, cmd = m.agentPane.Update(msg)
		return m, cmd

	case wizardFinished:
		return m.agentCreated(msg.answers)

	case wizardCancelled:
		m.wiz = nil
		return m, nil

	case tea.MouseClickMsg:
		if m.welcome != nil || m.alert != nil || m.profile != nil {
			m.welcome, m.alert, m.profile = nil, nil, nil
			return m, nil
		}
		return m.handleClick(msg.Mouse())

	case tea.MouseWheelMsg:
		if m.welcome != nil || m.alert != nil || m.profile != nil {
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
		if m.mailOpen {
			var reload tea.Cmd
			m.mail, reload = m.mail.reload()
			cmd = tea.Batch(cmd, reload)
		}
		m.list = m.list.refreshUnread()
		if msg.ev.Kind == backend.EventUnread && msg.ev.Chat == (backend.ChatID{}) {
			cmd = tea.Batch(cmd, loadDialogs(m.ctx, m.b, m.archived))
		}
		return m, tea.Batch(cmd, listenEvents(m.events))

	case botBusEvent:
		if !msg.ok || msg.sub != m.botEvents {
			return m, nil // the bot chat closed since
		}
		var cmd tea.Cmd
		if m.chatOpen && m.chat.wants(msg.ev) {
			m.chat, cmd = m.chat.Update(chatEvent{msg.ev})
			if msg.ev.Message != nil && msg.ev.Message.Outgoing {
				m = m.withAgentSent()
			}
		}
		return m, tea.Batch(cmd, listenBotEvents(m.botEvents))

	case chatListInbox:
		return m.openInbox(msg.bot)

	case mailDone:
		if !m.mailOpen {
			// The folder closed while the mail was sending.
			if d := m.mailDraft; msg.sent && d != nil {
				d.sending = false
				if msg.err != nil {
					d.notice = textfmt.CleanError(msg.err)
					m.notice = "mail not sent: " + d.notice + "; c in a folder reopens the draft"
				} else {
					m.mailDraft, m.notice = nil, "sent"
				}
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.mail, cmd = m.mail.update(msg)
		return m, cmd

	case mailLoaded:
		if !m.mailOpen {
			return m, nil
		}
		var cmd tea.Cmd
		m.mail, cmd = m.mail.update(msg)
		return m, cmd

	case mediaOpened, chatSaveResult:
		if m.mailOpen {
			var cmd tea.Cmd
			m.mail, cmd = m.mail.update(msg)
			return m, cmd
		}

	case mailClosed:
		m = m.closeChat()
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case mailMoveReq:
		lb, ok := m.b.(backend.Labeler)
		if !ok {
			return m, nil
		}
		first := []string{"INBOX"}
		if archive := m.mail.mb.ArchiveFolder(); archive != "" {
			first = append(first, archive)
		}
		picker, err := newMovePicker(m.st, lb, first, m.mail.folder.ID.Id)
		if err != nil {
			m.mail.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.labels, m.moving = picker, msg.ids
		return m.sized(), nil

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
		if bot := m.backToInbox; bot != nil {
			return m.openInbox(*bot)
		}
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

	case unreadThread:
		return m.openChatAt(msg.card.entry, &msg.msg)

	case unreadMarked:
		if msg.err != nil {
			m.notice = "could not mark read: " + textfmt.CleanError(msg.err)
		}
		return m, loadDialogs(m.ctx, m.b, m.archived)

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
		if m.welcome != nil || m.alert != nil || m.profile != nil {
			m.welcome, m.alert, m.profile = nil, nil, nil
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
		if m.labels != nil {
			return m, m.labels.paste(msg)
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
			m.chat, cmd = m.chat.updateInput(msg)
		}
		if m.mailOpen {
			m.mail, cmd = m.mail.paste(msg)
		}
		return m, cmd
	}

	if m.isListMsg(msg) {
		return m.updateList(msg)
	}
	// While forwarding, the chat underneath still has work in flight; each
	// model ignores messages that are not its own.
	var searchCmd, chatCmd, mailCmd tea.Cmd
	if m.searchOpen {
		m.search, searchCmd = m.search.Update(msg)
	}
	if m.mailOpen {
		m.mail, mailCmd = m.mail.update(msg)
	}
	var inputCmd tea.Cmd
	if m.chatOpen {
		m.chat, chatCmd = m.chat.Update(msg)
		// Ctrl+V comes back from the input as a message of its own.
		if !m.searchOpen && m.focus == focusChat {
			m.chat, inputCmd = m.chat.updateInput(msg)
		}
	}
	return m, tea.Batch(searchCmd, chatCmd, inputCmd, mailCmd)
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
	if m.composing() {
		return m, nil
	}
	if m.wiz != nil {
		if right {
			return m, nil
		}
		wiz, cmd := m.wiz.modalClick(mouse.X, mouse.Y, 0, m.width, m.bodyHeight())
		m.wiz = &wiz
		return m, cmd
	}
	if m.labels != nil {
		if right {
			return m, nil
		}
		x, y := m.labelsAt()
		if pick, done := m.labels.click(mouse.X-x, mouse.Y-y); done {
			return m.closeLabels(pick)
		}
		return m, nil
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
	if right && (m.overList(mouse.X) || m.unreadOpen || m.agentOpen || m.mailOpen) {
		return m, nil
	}
	if m.overList(mouse.X) {
		if m.loadErr != nil {
			return m, nil
		}
		m.focus = focusList
		i, ok := m.list.itemAt(mouse.X-2, mouse.Y-1)
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
	case m.mailOpen:
		m.mail, cmd = m.mail.click(x, mouse.Y)
	case m.unreadOpen:
		if i, ok := m.unread.cardAt(mouse.Y); ok {
			return m.openCard(m.unread.cards[i])
		}
	case m.agentOpen && m.agentPane.picker != nil:
		x, y := m.pickerAt()
		var done bool
		done, cmd = m.agentPane.picker.click(mouse.X-x, mouse.Y-y)
		if done {
			m.agentPane.picker = nil
		}
	case m.agentOpen && mouse.Y == 1 && !m.agentPane.editing:
		// The status chip pauses or resumes the bot.
		m.agentPane, cmd = m.agentPane.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
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
	if m.composing() {
		return m, nil
	}
	if _, _, _, ok := m.confirmShown(); ok || m.wiz != nil || m.labels != nil {
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
	case m.mailOpen:
		m.mail, cmd = m.mail.wheel(m.chatX(mouse.X), k)
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
		case chatSubNone, chatSubThread, chatSubMenu, chatSubSearch, chatSubUpload:
			// Other submodes are prompts that any key would answer.
			m.chat, cmd = m.chat.Update(k)
		}
	}
	return m, cmd
}

func (m tuiModel) isListMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tuiDialogsLoaded, tuiRetryDialogs, chatListChoice, chatListRefresh,
		chatListArchiveToggle, chatListSearch, chatListUnread, chatListMentions, chatListSelf, selfChatOpened,
		chatListAccounts, chatListSettings, chatListSwitch,
		chatListDeleteDone, chatListAgent, chatListInbox, chatListLabels, chatListCompose:
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
	if m.chatOpen && (msg.String() == "alt+l" || msg.String() == "alt+L") {
		m.chat.locked = true
		return m, nil
	}
	if m.wiz != nil {
		wiz, cmd := m.wiz.Update(msg)
		m.wiz = &wiz
		return m, cmd
	}
	if m.labels != nil {
		open, done, cmd := m.labels.key(msg)
		if done {
			return m.closeLabels(open)
		}
		return m, cmd
	}
	if m.searchOpen {
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	if m.composing() {
		m.focus = focusChat
		m.mail, cmd = m.mail.key(msg)
		return m, cmd
	}
	paneFocused := m.paneOpen() && m.focus == focusChat
	chatFocused := m.chatOpen && m.focus == focusChat
	editing := m.agentOpen && (m.agentPane.editing || m.agentPane.confirm != nil ||
		m.agentPane.picker != nil) || m.mailOpen && m.mail.typing() && m.focus == focusChat

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
	if paneFocused && m.mailOpen {
		m.mail, cmd = m.mail.key(msg)
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
		if msg.archived != m.archived {
			return m, nil // loaded before switching between inbox and archive
		}
		if msg.err != nil {
			m.loadErr = msg.err
			// Every failed load lands here, so only the first starts a retry.
			if !backend.NetworkError(msg.err) || m.retryPending {
				return m, nil
			}
			m.retryPending = true
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
				return tuiRetryDialogs{}
			})
		}
		m.loadErr = nil
		old := m.list
		var selectedChat backend.ChatID
		if i, ok := old.chatIndex(); ok {
			selectedChat = old.entries[i].ID
		}
		m.entries = msg.entries
		m.archiveUnread = msg.archiveUnread
		m.list = newChatListModel(m.ctx, m.st, m.mgr, m.active, m.b, msg.entries, m.archived, msg.archiveUnread)
		m.list.width, m.list.height = old.width, old.height
		m.list.folded = old.folded
		m.list.selected = min(old.selected, m.list.maxIndex())
		m.list.offset = old.offset
		// New activity re-sorts the chats; stay on the chat, not the row.
		if selectedChat != (backend.ChatID{}) {
			for i, e := range m.list.entries {
				if e.ID == selectedChat {
					m.list = m.list.selectEntry(i)
					break
				}
			}
		}
		var tick tea.Cmd
		if m.list.loading && !m.spinning {
			m.spinning, tick = true, m.spin.Tick
		}
		label := m.goToLabel
		m.goToLabel = ""
		for i, e := range m.entries {
			if label != "" && e.ID.Id == label {
				m.list = m.list.selectEntry(i)
				return m.chooseChat(i)
			}
		}
		return m, tick

	case tuiRetryDialogs:
		m.retryPending = false
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListChoice:
		return m.chooseChat(msg.index)

	case chatListCompose:
		var load tea.Cmd
		if !m.mailOpen {
			m, load = m.chooseChat(0)
		}
		if !m.mailOpen {
			return m, nil
		}
		var cmd tea.Cmd
		m.mail.reading, m.mail.search = nil, nil
		m.mail, cmd = m.mail.act("c")
		m.focus = focusChat
		return m, tea.Batch(load, cmd)

	case chatListRefresh:
		return m, loadDialogs(m.ctx, m.b, m.archived)

	case chatListArchiveToggle:
		m = m.closeChat()
		m.archived = !m.archived
		m.list.selected, m.list.offset = m.list.firstChat(), 0
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

	case chatListSelf:
		sc, ok := m.b.(backend.SelfChatter)
		if !ok {
			return m, nil
		}
		ctx := m.ctx
		return m, func() tea.Msg {
			entry, err := sc.SelfChat(ctx)
			return selfChatOpened{entry, err}
		}

	case selfChatOpened:
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
			return m, nil
		}
		return m.openChat(msg.entry)

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
		return m.agentRow(msg.id)

	case chatListLabels:
		lb, ok := m.b.(backend.Labeler)
		if !ok {
			return m, nil
		}
		picker, err := newLabelPicker(m.st, lb)
		if err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.labels = picker
		return m.sized(), nil
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
	if m.profile != nil {
		return m.st.footer(profileModalHelp, "", m.width)
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
	case m.labels != nil:
		keys = m.labels.helpKeys()
	case m.searchOpen:
		keys = m.search.helpKeys()
	case m.unreadOpen && m.focus == focusChat:
		keys = m.unread.helpKeys()
		if m.twoPane() {
			keys = append(keys, helpKey{"tab", "chats"})
		}
	case m.mailOpen && m.focus == focusChat:
		keys = m.mail.helpKeys()
		if m.twoPane() && !m.mail.typing() {
			keys = append(keys, helpKey{"tab", "folders"})
		}
		notice = m.mail.notice
	case m.agentOpen && m.focus == focusChat:
		keys = m.agentPane.helpKeys()
		if m.twoPane() && m.agentPane.confirm == nil && m.agentPane.picker == nil &&
			!m.agentPane.editing {
			keys = append(keys, helpKey{"tab", "chats"})
		}
		notice = m.agentPane.notice
	case m.chatOpen && m.focus == focusChat:
		keys = m.chat.helpKeys()
		notice = m.chat.notice
		mode := " CHAT "
		if m.chat.subMode == chatSubThread {
			mode = " THREAD "
		}
		badge := m.st.badge.Render(mode) + " "
		return badge + m.st.footer(keys, notice, m.width-textfmt.DisplayWidth(badge))
	case m.list.confirm != nil:
		keys = m.list.confirm.modal.helpKeys()
	default:
		keys = append(keys, m.list.helpKeys()...)
		if m.unreadOpen {
			name := strings.ToLower(m.unread.name())
			keys = append(keys, helpKey{"tab", name}, helpKey{"esc", "close " + name})
		}
		if m.agentOpen {
			keys = append(keys, helpKey{"tab", "bot"}, helpKey{"esc", "close bot"})
		}
		if m.mailOpen {
			keys = append(keys, helpKey{"tab", "mail"}, helpKey{"esc", "close folder"})
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
	v.ReportFocus = true // terminals without focus events just never blur
	return v
}

func (m tuiModel) render() string {
	m.list.spin = m.spin.View()
	m.mail.spin = m.spin.View()
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
		case m.mailOpen:
			right = m.mail.view(!listFocused)
		case m.unreadOpen:
			right = m.unread.view(!listFocused)
		case m.agentOpen:
			right = m.agentPane.view(!listFocused)
		case m.chatOpen:
			right = m.chat.View()
		}
		body = joinColumns(list, right)
	case m.mailOpen:
		body = m.mail.view(true)
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
	if m.agentOpen && m.agentPane.picker != nil {
		x, y := m.pickerAt()
		body = m.st.fadeUnder(body, m.agentPane.picker.View(), x, y)
	}
	if m.labels != nil {
		x, y := m.labelsAt()
		body = m.st.fadeUnder(body, m.labels.View(), x, y)
	}
	if m.composing() {
		x, y, w, h := composeRect(m.width, bodyHeight)
		body = m.st.fadeUnder(body, m.mail.composer.view(w, h), x, y)
	}
	if m.wiz != nil {
		box, _ := m.wiz.modal(m.width, bodyHeight)
		body = m.st.overlay(body, box, m.width, bodyHeight)
	}
	if m.profile != nil {
		left, width := 0, m.width
		if m.chatOpen && m.twoPane() {
			left, width = sidebarWidth, m.chat.width
		}
		w, h := m.profile.size()
		x, y := modalRect(width, bodyHeight, w, h)
		body = m.st.fadeUnder(body, m.profile.View(), left+x, y)
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

// composing reports whether the mail composer modal is up.
func (m tuiModel) composing() bool { return m.mailOpen && m.mail.composing }

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

// labelsAt is the top-left corner of the Labels modal: centred over the screen.
func (m tuiModel) labelsAt() (x, y int) {
	w, h := m.labels.size()
	return modalRect(m.width, m.bodyHeight(), w, h)
}

// closeLabels closes the Labels modal and reloads the list for the labels it
// showed or hid, then opens label when one was picked. Closing the move
// picker on a folder moves the mail there.
func (m tuiModel) closeLabels(label string) (tuiModel, tea.Cmd) {
	m.labels = nil
	if ids := m.moving; ids != nil {
		m.moving = nil
		if label == "" || !m.mailOpen {
			return m, nil
		}
		name := label
		for _, e := range m.entries {
			if e.ID.Id == label {
				name = textfmt.Plain(e.Title)
			}
		}
		var cmd tea.Cmd
		m.mail, cmd = m.mail.move(ids, label, name)
		return m, cmd
	}
	m.goToLabel = label
	return m, loadDialogs(m.ctx, m.b, m.archived)
}

// chooseChat opens the list's chat at index: on email, a folder.
func (m tuiModel) chooseChat(index int) (tuiModel, tea.Cmd) {
	if index < 0 || index >= len(m.entries) {
		return m, nil
	}
	if mb, ok := m.b.(backend.Mailbox); ok {
		return m.openFolder(mb, m.entries[index])
	}
	return m.openChat(m.entries[index])
}

// pickerAt is the top-left corner of the bot's chat picker: centred over its pane.
func (m tuiModel) pickerAt() (x, y int) {
	left := 0
	if m.twoPane() {
		left = sidebarWidth
	}
	w, h := m.agentPane.picker.size()
	x, y = modalRect(m.agentPane.width, m.bodyHeight(), w, h)
	return left + x, y
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
	_, sug, _ := m.mgr.Agents.PendingFor(m.mgr.AccountKey(m.active), m.chat.entry.ID,
		m.chat.bot != nil)
	m.chat.suggestion = sug.Text
	return m
}

// withAgentSent rereads which of the open chat's messages an agent sent.
func (m tuiModel) withAgentSent() tuiModel {
	if !m.chatOpen || m.mgr == nil || m.mgr.Agents == nil {
		return m
	}
	sent, err := m.mgr.Agents.Db.SentBy(m.mgr.AccountKey(m.active),
		account.LogChatID(m.chat.entry.ID, m.chat.bot != nil))
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
	key, sug, ok := rt.PendingFor(m.mgr.AccountKey(m.active), m.chat.entry.ID, m.chat.bot != nil)
	if !ok {
		return m, nil
	}
	switch act {
	case "approve":
		m.chat.notice = "Sending the bot's reply…"
		return m, func() tea.Msg { return agentApproveDone{rt.Approve(key)} }
	case "edit":
		rt.Discard(key)
		m.chat.input.SetValue(sug.Text)
		m.chat.input.MoveToEnd()
		m.focus = focusChat
	case "discard":
		rt.Discard(key)
	}
	return m, nil
}

// agentRow opens the bot's pane, or with id 0 creates a bot first.
func (m tuiModel) agentRow(id int64) (tuiModel, tea.Cmd) {
	if !agentsAllowed(m.mgr, m.active) {
		return m, nil
	}
	if id != 0 {
		return m.openAgentPane(id)
	}
	providers, err := m.mgr.Agents.Db.ListProviders()
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	if len(providers) == 0 {
		m.alert = newAlertModal(m.st, "No AI provider",
			"Add an AI provider in Settings > AI providers first, then create the bot here.")
		return m, nil
	}
	taken := make(map[string]string)
	for _, rec := range m.mgr.Agents.AgentsFor(m.active) {
		taken[rec.Handle] = rec.Name
	}
	wiz := newWizard(m.st, "new bot for "+accountLabel(m.active),
		agentSteps(providers, m.mgr.Dir, taken), nil)
	m.wiz = &wiz
	return m, wiz.Init()
}

func (m tuiModel) openAgentPane(id int64) (tuiModel, tea.Cmd) {
	pane, err := newAgentPane(m.ctx, m.st, m.mgr, m.active, m.b, id, m.entries)
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
	m, cmd := m.openAgentPane(rec.ID)
	m.agentPane.notice = "Bot " + textfmt.Plain(rec.Name) + " created"
	if m.agentOpen && !rec.AllChats {
		// Other bots answer some of the account's chats, so this one starts with none.
		m.agentPane = m.agentPane.openPicker()
		m.agentPane.notice = "Bot " + textfmt.Plain(rec.Name) + " created; pick its chats"
	}
	return m, cmd
}
