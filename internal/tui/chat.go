package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type chatSubMode int

const (
	chatSubNone chatSubMode = iota
	chatSubMenu
	chatSubSearch
	chatSubEdit
	chatSubUpload
	chatSubThread
	chatSubConfirmChatDelete
	chatSubConfirmMsgDelete
)

type chatViewModel struct {
	st        *styles
	ctx       context.Context
	cancel    context.CancelFunc
	b         backend.Backend
	entry     backend.ChatEntry
	caps      backend.Capabilities
	histories *[]string

	messages         []backend.Message
	selected         int
	seen             map[string]bool
	changedMessages  map[string]bool
	flash            reactionKey // the reaction blinking after a react
	flashStep        int
	flashSeq         int                             // tells this blink's ticks from an earlier one's
	previews         map[string]*backend.LinkPreview // by URL; nil = asked, none yet
	images           map[string]*chatImage           // by message ID
	kitty            *kittyImages                    // nil without kitty graphics
	quotes           map[string]*backend.Message     // unloaded originals by ID; nil = asked
	threads          map[string][]backend.Message    // whole threads by root ID; nil = asked
	historyExhausted bool
	loadingOlder     bool            // a page of older history is on its way
	firstUnread      string          // ID of the oldest message unread on open
	unread           map[string]bool // IDs of the messages unread on open

	input textarea.Model
	sends chan chatOutgoing
	queue []chatOutgoing

	reads    *readReporter
	replying *backend.Message
	typing   bool
	early    []backend.Event  // arrived while the first history page was loading
	target   *backend.Message // selected once the first history page is in
	// seeded is set while cached messages stand in for the first page;
	// intact says the cache missed no events, so its older pages stay.
	seeded, intact bool
	loaded         bool // the server's first page is in, so the cache may keep it

	subMode chatSubMode
	notice  string

	menu msgMenuModel

	searchInput    textinput.Model
	searchQuery    string
	searchResults  []backend.Message
	searchSelected int

	editing   *backend.Message
	editInput textinput.Model

	upload  uploadPanel
	uploads []pendingUpload
	opening *mediaDownload
	// openedAt is when an attachment last went to the desktop's viewer, whose
	// window takes focus; the root skips the blur lock just after it.
	openedAt time.Time
	spin     spinner.Model

	threadRoot  string
	threadInput textarea.Model
	// threadScroll is how many lines the thread pane is scrolled up from its end.
	threadScroll int

	confirmRevoke bool
	// confirm asks before a delete; the root draws it over the chat.
	confirm *confirmModal

	privateMode    bool
	lastTypingSent time.Time

	// agentSent names the agent that sent each agent message, by ID, and
	// suggestion is the agent's reply waiting for approval. The root keeps
	// both current.
	agentSent  map[string]string
	suggestion string
	// bot is set while the chat runs on a registered bot's own connection:
	// its outgoing messages are the bot's, shown as ✦ NAME.
	bot *inboxBot

	// locked hides the chat until a key or click: set when the terminal
	// loses focus and by Alt+L.
	locked bool

	// embedded is set by the root model in two-pane mode: Esc then returns
	// focus to the sidebar instead of closing the chat.
	embedded bool
	focused  bool

	width, height int
}

type chatOutgoing struct {
	text    string
	path    string
	replyTo string
}

// chatTag names the chat a background result was started for, so a result
// that arrives after switching chats is dropped rather than applied to the
// new one.
type chatTag struct{ chat backend.ChatID }

func (t chatTag) forChat() backend.ChatID { return t.chat }

type chatSendResult struct {
	chatTag
	msg backend.Message
	err error
}

type chatHistoryLoaded struct {
	chatTag
	messages []backend.Message
	err      error
	older    bool
}

type quotesLoaded struct {
	chat     backend.ChatID
	messages []backend.Message
}

type threadLoaded struct {
	chat     backend.ChatID
	root     string
	messages []backend.Message
}

type linkPreviewLoaded struct {
	url     string
	preview *backend.LinkPreview
}

// chatEvent is one bus event for the open chat, routed by the root model.
type chatEvent struct{ ev backend.Event }

type chatReadTick struct{}
type chatTypingTimeout struct{}
type chatReturnToList struct{}
type chatForwardReq struct{ req forwardRequest }

// chatMentionReq asks the root to open the mention picker over the chat.
type chatMentionReq struct{}

type mediaReady struct {
	cmd     *exec.Cmd
	cleanup func()
}
type mediaFinished struct{ err error }

// chatClick is a click at column x and row y of the chat pane, counted from
// its left and top borders; right is set for the right button.
type chatClick struct {
	x, y  int
	right bool
}

type mediaOpened struct {
	filename string
	err      error
}

// mediaDownload is the click-to-open download in progress; bytes is polled
// from the partial file on disk.
type mediaDownload struct {
	filename string
	part     string
	bytes    int64
}

type mediaProgressTick struct{}

// reactionFlashTick steps the blink of the reaction just toggled.
type reactionFlashTick struct{ seq int }

// flashSteps is how many on and off phases a toggled reaction blinks for.
const flashSteps = 6

type chatReactionResult struct {
	chatTag
	messageID string
	emoticon  string
	reactions []backend.MessageReaction
	err       error
}

type chatEditResult struct {
	chatTag
	err error
}
type chatUploadResult struct {
	chatTag
	path string
	msg  backend.Message
	err  error
}
type chatToUnread struct{}

// chatSuggestion acts on the agent's waiting reply: approve, edit or discard.
type chatSuggestion struct{ act string }
type chatDeleteChatResult struct{ err error }
type chatDeleteMsgResult struct{ err error }
type chatPinResult struct {
	chatTag
	index int
	unpin bool
	err   error
}
type chatSaveResult struct {
	filename string
	err      error
}
type chatSearchResult struct {
	chatTag
	messages []backend.Message
	err      error
}
type chatSearchDebounce struct{}

func newChatViewModel(ctx context.Context, st *styles, b backend.Backend, entry backend.ChatEntry, histories *[]string, kitty *kittyImages) chatViewModel {
	ctx, cancel := context.WithCancel(ctx)
	w, h := termSize()
	ti := newComposerArea(st, "message "+chatName(entry)+"…")
	caps := b.Capabilities()
	return chatViewModel{
		st:              st,
		ctx:             ctx,
		cancel:          cancel,
		b:               b,
		entry:           entry,
		caps:            caps,
		histories:       histories,
		seen:            make(map[string]bool),
		changedMessages: make(map[string]bool),
		quotes:          make(map[string]*backend.Message),
		threads:         make(map[string][]backend.Message),
		previews:        make(map[string]*backend.LinkPreview),
		images:          make(map[string]*chatImage),
		kitty:           kitty,
		input:           ti,
		threadInput:     newComposerArea(st, "reply…"),
		sends:           make(chan chatOutgoing),
		spin:            spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(st.accent)),
		focused:         true,
		width:           w,
		height:          h,
	}
}

func (m chatViewModel) Init() tea.Cmd {
	return tea.Batch(m.loadHistory(), m.input.Focus(), m.spin.Tick)
}

func (m chatViewModel) loadHistory() tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		b.SetActive(entry)
		if err := b.RefreshReadState(ctx, entry); err != nil && ctx.Err() == nil {
		}
		msgs, err := b.History(ctx, entry)
		return chatHistoryLoaded{chatTag{entry.ID}, msgs, err, false}
	}
}

func (m chatViewModel) loadOlderHistory() tea.Cmd {
	if len(m.messages) == 0 || m.historyExhausted {
		return nil
	}
	ctx := m.ctx
	b := m.b
	entry := m.entry
	beforeID := m.messages[0].ID
	return func() tea.Msg {
		msgs, err := b.OlderHistory(ctx, entry, beforeID)
		return chatHistoryLoaded{chatTag{entry.ID}, msgs, err, true}
	}
}

// wants reports whether ev belongs to this chat. Telegram deletions outside
// channels name no chat, so they apply to whichever chat is open.
func (m chatViewModel) wants(ev backend.Event) bool {
	return ev.Chat == m.entry.ID || (ev.Kind == backend.EventDelete && ev.Chat == backend.ChatID{})
}

func (m chatViewModel) sendMessage(outgoing chatOutgoing) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	// A bot answering the user's own chat waits for their messages there, but
	// Telegram and WhatsApp never echo a session's own sends; the chat dedupes
	// the echo of backends that do.
	announce := func(msg backend.Message, err error) {
		if err == nil && entry.IsSelf {
			b.Events().Publish(backend.Event{Kind: backend.EventMessage, Chat: entry.ID,
				Self: true, Message: &msg})
		}
	}
	return func() tea.Msg {
		if outgoing.path != "" {
			msg, err := b.SendUpload(ctx, entry, outgoing.path, outgoing.text, outgoing.replyTo)
			announce(msg, err)
			return chatUploadResult{chatTag{entry.ID}, outgoing.path, msg, err}
		}
		msg, err := b.Send(ctx, entry, outgoing.text, outgoing.replyTo)
		announce(msg, err)
		return chatSendResult{chatTag{entry.ID}, msg, err}
	}
}

func (m chatViewModel) startReadTimer() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return chatReadTick{}
	})
}

// markRead tells the server the chat is read up to its newest incoming
// message, unless it already knows or is being told.
func (m chatViewModel) markRead() tea.Cmd {
	r := m.reads
	id := newestIncoming(m.messages)
	if r == nil || r.inFlight || id == "" || id == r.marked {
		return nil
	}
	r.inFlight = true
	ctx, b, entry := m.ctx, m.b, m.entry
	return func() tea.Msg {
		return chatMarkedRead{chatTag{entry.ID}, id, b.MarkRead(ctx, entry, id)}
	}
}

func (m chatViewModel) typingTimeoutCmd() tea.Cmd {
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg {
		return chatTypingTimeout{}
	})
}

func (m chatViewModel) appendMessage(msg backend.Message) chatViewModel {
	if msg.Preview == nil {
		msg.Preview = m.previews[textfmt.FirstURL(msg.Text)]
	}
	if msg.ID != "" && msg.ID != "0" && m.seen[msg.ID] {
		for i, prev := range m.messages {
			if prev.ID == msg.ID && msg.EditDate >= prev.EditDate {
				visibleEdit := msg.EditDate > 0 && (msg.Text != prev.Text || msg.MediaLabel != prev.MediaLabel)
				if visibleEdit && i != m.selected {
					m.changedMessages[msg.ID] = true
				}
				m.messages[i] = msg
				if m.replying != nil && m.replying.ID == msg.ID {
					m.replying = &msg
				}
				break
			}
		}
		return m
	}
	if msg.ID != "" && msg.ID != "0" {
		m.seen[msg.ID] = true
	}
	follow := m.selected == len(m.messages)-1
	m.messages = append(m.messages, msg)
	if follow {
		m.selected = len(m.messages) - 1
	}
	if len(m.messages) > 500 {
		delete(m.changedMessages, m.messages[0].ID)
		m.messages = m.messages[1:]
		m.selected = max(0, m.selected-1)
	}
	return m
}

// unreadIDs returns the newest unread incoming messages and the oldest of
// them, or "" when nothing is unread.
func unreadIDs(messages []backend.Message, unread int) (map[string]bool, string) {
	ids := make(map[string]bool)
	first := ""
	for i := len(messages) - 1; i >= 0 && unread > 0; i-- {
		if !messages[i].Outgoing {
			first = messages[i].ID
			ids[first] = true
			unread--
		}
	}
	return ids, first
}

func (m chatViewModel) selectedMsg() (backend.Message, bool) {
	if m.selected >= 0 && m.selected < len(m.messages) {
		msg := m.messages[m.selected]
		if msg.ID != "" && msg.ID != "0" {
			return msg, true
		}
	}
	return backend.Message{}, false
}

func (m chatViewModel) clearSelectedChanged() chatViewModel {
	if m.selected >= 0 && m.selected < len(m.messages) {
		delete(m.changedMessages, m.messages[m.selected].ID)
	}
	return m
}

func (m chatViewModel) Update(msg tea.Msg) (chatViewModel, tea.Cmd) {
	m, cmd := m.update(msg)
	return m.markAgents(), cmd
}

// markAgents names the agent on the messages it sent.
func (m chatViewModel) markAgents() chatViewModel {
	if len(m.agentSent) == 0 && m.bot == nil {
		return m
	}
	for i := range m.messages {
		if !m.messages[i].Outgoing {
			continue
		}
		m.messages[i].Agent = m.agentSent[m.messages[i].ID]
		if m.messages[i].Agent == "" && m.bot != nil {
			m.messages[i].Agent = m.bot.name
		}
	}
	return m
}

// imagesMoved reports whether msg may have brought images into view; the
// root then fetches and places them once it has laid out the chat.
func imagesMoved(msg tea.Msg) bool {
	switch msg.(type) {
	case spinner.TickMsg, mediaProgressTick, reactionFlashTick:
		return false
	}
	return true
}

func (m chatViewModel) update(msg tea.Msg) (chatViewModel, tea.Cmd) {
	if r, ok := msg.(interface{ forChat() backend.ChatID }); ok && r.forChat() != m.entry.ID {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case chatHistoryLoaded:
		cm, cmd := m.handleHistoryLoaded(msg)
		fetch := msg.messages
		if !msg.older {
			fetch = cm.messages // older cached messages may have been kept
		}
		return cm.clearSelectedChanged(), tea.Batch(cmd, cm.fetchPreviews(fetch),
			cm.fetchQuotes(fetch))

	case quotesLoaded:
		if msg.chat != m.entry.ID {
			return m, nil
		}
		for i := range msg.messages {
			m.quotes[msg.messages[i].ID] = &msg.messages[i]
		}
		return m, nil

	case threadLoaded:
		if msg.chat == m.entry.ID {
			m.threads[msg.root] = msg.messages
		}
		return m, nil

	case chatImageLoaded:
		if c := m.images[msg.id]; c != nil {
			c.img, c.failed = msg.img, msg.err != nil
		}
		return m, nil

	case linkPreviewLoaded:
		if msg.preview == nil {
			return m, nil
		}
		m.previews[msg.url] = msg.preview
		for i := range m.messages {
			if m.messages[i].Preview == nil && textfmt.FirstURL(m.messages[i].Text) == msg.url {
				m.messages[i].Preview = msg.preview
			}
		}
		return m, nil

	case chatEvent:
		if m.messages == nil {
			m.early = append(m.early, msg.ev)
			return m, nil
		}
		if m.seeded {
			// The page on its way may predate it; replay it after the merge.
			m.early = append(m.early, msg.ev)
		}
		cm, cmd := m.handleEvent(msg.ev)
		return cm.clearSelectedChanged(), cmd

	case chatTypingTimeout:
		m.typing = false
		m.b.ClearTyping()

	case chatReadTick:
		if m.reads != nil && m.selected >= 0 && m.selected < len(m.messages) {
			delete(m.changedMessages, m.messages[m.selected].ID)
		}
		return m, tea.Batch(m.markRead(), m.startReadTimer())

	case chatMarkedRead:
		// A failure is retried on the next tick.
		if m.reads != nil {
			m.reads.inFlight = false
			if msg.err == nil {
				m.reads.marked = msg.id
			}
		}
		return m, nil

	case chatSendResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		m = m.appendMessage(msg.msg)

	case reactionFlashTick:
		if msg.seq != m.flashSeq || m.flash == (reactionKey{}) {
			return m, nil
		}
		m.flashStep++
		if m.flashStep >= flashSteps {
			m.flash = reactionKey{}
			return m, nil
		}
		return m, m.flashTickCmd()

	case chatReactionResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		for i, existing := range m.messages {
			if existing.ID == msg.messageID {
				copy := existing
				copy.Reactions = msg.reactions
				m.messages[i] = copy
				delete(m.changedMessages, msg.messageID)
				break
			}
		}
		m.b.RememberEmoji(msg.emoticon)

	case chatEditResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else if m.editing != nil {
			for i, prev := range m.messages {
				if prev.ID == m.editing.ID {
					copy := prev
					copy.Text = m.editInput.Value()
					m.messages[i] = copy
					delete(m.changedMessages, m.editing.ID)
					break
				}
			}
		}
		m.editing = nil
		m.subMode = chatSubNone

	case chatUploadResult:
		for i, u := range m.uploads {
			if u.path == msg.path {
				m.uploads = append(m.uploads[:i:i], m.uploads[i+1:]...)
				break
			}
		}
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m = m.appendMessage(msg.msg)
		}

	case spinner.TickMsg:
		// Let the spinner stop once nothing is loading or uploading.
		if m.messages != nil && len(m.uploads) == 0 && m.opening == nil && !m.threadLoading() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case chatDeleteChatResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		return m, func() tea.Msg { return chatReturnToList{} }

	case chatDeleteMsgResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
		}

	case chatPinResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else if msg.index >= 0 && msg.index < len(m.messages) {
			copy := m.messages[msg.index]
			copy.Pinned = !msg.unpin
			m.messages[msg.index] = copy
			if msg.unpin {
				m.notice = "Message unpinned"
			} else {
				m.notice = "Message pinned"
			}
		}

	case chatSaveResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("saved %s", msg.filename)
		}

	case chatSearchResult:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.searchResults = msg.messages
			m.searchSelected = 0
		}

	case chatSearchDebounce:
		if m.subMode == chatSubSearch && m.searchQuery != "" {
			return m, m.doSearch()
		}

	case mediaReady:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			if msg.cleanup != nil {
				msg.cleanup()
			}
			return mediaFinished{err}
		})

	case mediaFinished:
		if msg.err != nil && !isContextCanceled(msg.err) {
			m.notice = msg.err.Error()
		}

	case chatClick:
		return m.handleClick(msg)

	case mediaProgressTick:
		if m.opening == nil {
			return m, nil
		}
		m.opening.bytes = dirSize(m.opening.part)
		return m, mediaProgressCmd()

	case mediaOpened:
		m.opening = nil
		if msg.err != nil {
			if !isContextCanceled(msg.err) {
				m.notice = msg.err.Error()
			}
		} else {
			m.notice = "opened " + msg.filename
			m.openedAt = time.Now()
		}

	case tea.KeyPressMsg:
		cm, cmd := m.handleKey(msg)
		return cm.clearSelectedChanged(), cmd
	}
	return m, nil
}

// fetchPreviews asks backends that preview links on request (Matrix) about
// each new link once per chat; cards appear as the answers arrive.
func (m chatViewModel) fetchPreviews(msgs []backend.Message) tea.Cmd {
	lp, ok := m.b.(backend.LinkPreviewer)
	if !ok {
		return nil
	}
	var cmds []tea.Cmd
	for _, msg := range msgs {
		link := textfmt.FirstURL(msg.Text)
		if _, asked := m.previews[link]; msg.Preview != nil || link == "" || asked {
			continue
		}
		m.previews[link] = nil
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			p, err := lp.LinkPreview(ctx, link)
			if err != nil {
				p = nil // a missing preview is not worth a notice
			}
			return linkPreviewLoaded{link, p}
		})
	}
	return tea.Batch(cmds...)
}

// fetchQuotes loads the originals of replies in msgs that are not loaded,
// each asked for once.
func (m chatViewModel) fetchQuotes(msgs []backend.Message) tea.Cmd {
	qf, ok := m.b.(backend.QuoteFetcher)
	if !ok {
		return nil
	}
	var ids []string
	for _, msg := range msgs {
		id := msg.ReplyToID
		if _, asked := m.quotes[id]; id == "" || asked || m.seen[id] || msg.Quote != nil {
			continue
		}
		m.quotes[id] = nil
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	ctx, entry := m.ctx, m.entry
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		found, err := qf.Messages(ctx, entry, ids)
		if err != nil {
			found = nil // the placeholder stays; not worth a notice
		}
		return quotesLoaded{entry.ID, found}
	}
}

func (m chatViewModel) handleHistoryLoaded(msg chatHistoryLoaded) (chatViewModel, tea.Cmd) {
	if msg.older {
		m.loadingOlder = false
	}
	if msg.err != nil {
		if !msg.older {
			return m, func() tea.Msg { return chatReturnToList{} }
		}
		return m, nil
	}
	if msg.older {
		if len(msg.messages) == 0 {
			m.historyExhausted = true
			return m, nil
		}
		var fresh []backend.Message
		for _, msg := range msg.messages {
			if !m.seen[msg.ID] {
				m.seen[msg.ID] = true
				fresh = append(fresh, msg)
			}
		}
		if len(fresh) > 0 {
			m.messages = append(fresh, m.messages...)
			m.selected += len(fresh)
		} else {
			m.historyExhausted = true
		}
		return m, nil
	}
	keep := ""
	if m.seeded && m.selected >= 0 && m.selected < len(m.messages)-1 {
		keep = m.messages[m.selected].ID
	}
	m.messages = mergePage(m.messages, msg.messages, m.seeded && m.intact)
	m.seeded, m.loaded = false, true
	m.selected = len(m.messages) - 1
	m.unread, m.firstUnread = unreadIDs(m.messages, m.entry.Unread)
	m.seen = make(map[string]bool, len(m.messages))
	for i, msg := range m.messages {
		m.seen[msg.ID] = true
		if msg.ID == keep {
			m.selected = i
		}
	}
	m.reads = &readReporter{marked: m.b.InboxPosition(m.entry)}
	cmds := []tea.Cmd{m.startReadTimer()}
	for _, ev := range m.early {
		var cmd tea.Cmd
		m, cmd = m.handleEvent(ev)
		cmds = append(cmds, cmd)
	}
	m.early = nil
	if t := m.target; t != nil {
		m.target = nil
		for i, msg := range m.messages {
			if msg.ID == t.ID {
				m.selected = i
			}
		}
		// The reply's root may be older than the page; the server has it.
		if _, ok := m.b.(backend.ThreadFetcher); ok && t.ReplyToID != "" {
			var cmd tea.Cmd
			m, cmd = m.openThread(backend.Message{ID: t.ReplyToID})
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// seed shows cached messages while the server's first page loads.
func (m chatViewModel) seed(msgs []backend.Message, intact bool) chatViewModel {
	if msgs == nil {
		return m
	}
	m.messages, m.seeded, m.intact = msgs, true, intact
	m.selected = len(msgs) - 1
	for _, msg := range msgs {
		m.seen[msg.ID] = true
	}
	return m
}

// mergePage lays the server's newest page over the cached messages. Older
// cached ones stay only when keepOlder and the page starts among them;
// cached ones within the page's span that it lacks were deleted.
func mergePage(cached, page []backend.Message, keepOlder bool) []backend.Message {
	if len(page) == 0 || !keepOlder {
		return page
	}
	for k, msg := range cached {
		if msg.ID == page[0].ID {
			return append(slices.Clone(cached[:k]), page...)
		}
	}
	return page
}

// cacheable is what the chat cache may keep once the chat closes: nothing
// until the server's page is in, and no message without an ID.
func (m chatViewModel) cacheable() ([]backend.Message, bool) {
	if !m.loaded {
		return nil, false
	}
	msgs := make([]backend.Message, 0, len(m.messages))
	for _, msg := range m.messages {
		if msg.ID != "" && msg.ID != "0" {
			msgs = append(msgs, msg)
		}
	}
	return msgs, true
}

func (m chatViewModel) handleEvent(ev backend.Event) (chatViewModel, tea.Cmd) {
	switch ev.Kind {
	case backend.EventMessage, backend.EventEdit:
		if ev.Message == nil {
			break
		}
		if !ev.Message.Outgoing {
			m.typing = false
		}
		m = m.appendMessage(*ev.Message)
		return m, tea.Batch(m.fetchPreviews([]backend.Message{*ev.Message}),
			m.fetchQuotes([]backend.Message{*ev.Message}))
	case backend.EventReaction:
		update := ev.Reaction
		if update == nil {
			break
		}
		for i, existing := range m.messages {
			if existing.ID == update.MessageID {
				old := backend.FormatMessageReactions(existing.Reactions)
				updated := backend.FormatMessageReactions(update.Reactions)
				if old != updated {
					copy := existing
					copy.Reactions = update.Reactions
					m.messages[i] = copy
					if i != m.selected {
						m.changedMessages[existing.ID] = true
					}
				}
				break
			}
		}
	case backend.EventDelete:
		removed := make(map[string]bool, len(ev.Deleted))
		for _, id := range ev.Deleted {
			removed[id] = true
			delete(m.seen, id)
			delete(m.changedMessages, id)
		}
		for root, thread := range m.threads {
			m.threads[root] = slices.DeleteFunc(thread, func(msg backend.Message) bool {
				return removed[msg.ID]
			})
		}
		filtered := m.messages[:0]
		for _, existing := range m.messages {
			if !removed[existing.ID] {
				filtered = append(filtered, existing)
			}
		}
		m.messages = filtered
		if m.selected >= len(m.messages) {
			m.selected = max(0, len(m.messages)-1)
		}
	case backend.EventTyping:
		if m.b.IsTyping() {
			m.typing = true
			return m, m.typingTimeoutCmd()
		}
	}
	return m, nil
}

func (m chatViewModel) handleKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {

	if m.locked {
		m.locked = false
		return m, nil
	}

	m.notice = ""

	if m.suggestion != "" && m.subMode == chatSubNone && m.input.Value() == "" {
		act := map[string]string{"ctrl+y": "approve", "ctrl+e": "edit", "ctrl+x": "discard"}[msg.String()]
		if act != "" {
			return m, func() tea.Msg { return chatSuggestion{act} }
		}
	}

	switch m.subMode {
	case chatSubMenu:
		return m.handleMenuKey(msg)
	case chatSubSearch:
		return m.handleSearchKey(msg)
	case chatSubEdit:
		return m.handleEditKey(msg)
	case chatSubUpload:
		return m.handleUploadKey(msg)
	case chatSubThread:
		return m.handleThreadKey(msg)
	case chatSubConfirmChatDelete:
		return m.handleConfirmChatDeleteKey(msg)
	case chatSubConfirmMsgDelete:
		return m.handleConfirmMsgDeleteKey(msg)
	}

	switch msg.String() {
	case "esc":
		if m.replying != nil {
			m.replying = nil
			return m, nil
		}
		return m, func() tea.Msg { return chatToUnread{} }
	case "ctrl+c":
		return m, func() tea.Msg { return chatReturnToList{} }
	case "up":
		if m.privateMode {
			m.privateMode = false
		}
		if m.selected > 0 {
			m.selected--
		} else if !m.historyExhausted && !m.loadingOlder && len(m.messages) > 0 {
			// The wheel repeats up quickly; one page at a time.
			m.loadingOlder = true
			var cmd tea.Cmd
			m, cmd = m.followThread()
			return m, tea.Batch(cmd, m.loadOlderHistory())
		}
		return m.followThread()
	case "down":
		if m.selected+1 < len(m.messages) {
			m.selected++
		}
		return m.followThread()
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m.openMenu()
		}
		replyTo := ""
		if m.replying != nil {
			replyTo = m.replying.ID
			m.replying = nil
		}
		*m.histories = append(*m.histories, text)
		if len(*m.histories) > 500 {
			*m.histories = (*m.histories)[len(*m.histories)-500:]
		}
		m.input.SetValue("")
		return m, m.sendMessage(chatOutgoing{text: text, replyTo: replyTo})
	case "ctrl+f":
		if !m.caps.Search {
			return m, nil
		}
		m.subMode = chatSubSearch
		m.searchInput = textinput.New()
		m.searchInput.Focus()
		m.searchQuery = ""
		m.searchResults = nil
		m.searchSelected = 0
		return m, m.searchInput.Focus()
	case "ctrl+u":
		if !m.caps.Upload {
			return m, nil
		}
		return m.startUpload()
	case "alt+d", "alt+D":
		if !m.caps.DeleteChat || !m.entry.IsDM {
			return m, nil
		}
		m = m.askDeleteChat(false)
	case "ctrl+alt+d":
		if !m.caps.DeleteChat || !m.entry.IsDM {
			return m, nil
		}
		if m.entry.IsSelf {
			m.notice = "Saved Messages cannot be deleted for both sides"
			return m, nil
		}
		m = m.askDeleteChat(true)
	case "alt+t", "alt+T":
		if sel, ok := m.selectedMsg(); ok && m.inThread(sel) && !m.privateMode {
			return m.openThread(sel)
		}
	case "alt+h", "alt+H":
		m.privateMode = !m.privateMode
		if m.privateMode {
			m.selected = len(m.messages) - 1
		}
	default:
		if msg.Text == "@" && m.mentionAllowed() {
			return m, func() tea.Msg { return chatMentionReq{} }
		}
		var typing tea.Cmd
		if m.caps.Typing && time.Since(m.lastTypingSent) > 5*time.Second {
			m.lastTypingSent = time.Now()
			// A network round trip; run it off the key handler.
			ctx, b, entry := m.ctx, m.b, m.entry
			typing = func() tea.Msg {
				b.SetTyping(ctx, entry)
				return nil
			}
		}
		var cmd tea.Cmd
		m.fitInputs()
		m.input, cmd = m.input.Update(msg)
		return m, tea.Batch(typing, cmd)
	}
	return m, nil
}

// mentionAllowed reports whether an @ typed now starts a mention: the chat
// is one its backend can mention in, and the @ would begin a word.
func (m chatViewModel) mentionAllowed() bool {
	mn, ok := m.b.(backend.Mentioner)
	if !ok || !mn.CanMention(m.entry) {
		return false
	}
	in := m.activeInput()
	lines := strings.Split(in.Value(), "\n")
	line := []rune(lines[min(in.Line(), len(lines)-1)])
	before := line[:min(in.Column(), len(line))]
	return len(before) == 0 || unicode.IsSpace(before[len(before)-1])
}

// activeInput is the input being typed in: the thread's while it is open.
func (m *chatViewModel) activeInput() *textarea.Model {
	if m.subMode == chatSubThread {
		return &m.threadInput
	}
	return &m.input
}

// insertMention puts text into the composer at the cursor.
func (m chatViewModel) insertMention(text string) chatViewModel {
	m.activeInput().InsertString(text)
	return m
}

// openMenu opens the actions modal on the selected message.
func (m chatViewModel) openMenu() (chatViewModel, tea.Cmd) {
	sel, ok := m.selectedMsg()
	if !ok {
		return m, nil
	}
	return m.openMenuOn(sel)
}

func (m chatViewModel) openMenuOn(sel backend.Message) (chatViewModel, tea.Cmd) {
	m.menu = newMsgMenu(m.st, m.b, m.caps, sel, m.subMode)
	m.subMode = chatSubMenu
	if !m.menu.reactions {
		return m, nil
	}
	return m, m.menu.search.Focus()
}

func (m chatViewModel) menuOpen() bool { return m.subMode == chatSubMenu }

func (m chatViewModel) handleMenuKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	var cmd tea.Cmd
	var choice *msgMenuChoice
	var done bool
	m.menu, cmd, choice, done = m.menu.key(msg)
	m, act := m.menuDone(choice, done)
	return m, tea.Batch(cmd, act)
}

// menuClick acts on a click at column x and row y of the open modal, counted
// from its top-left corner; a click outside it closes the modal.
func (m chatViewModel) menuClick(x, y int, inside bool) (chatViewModel, tea.Cmd) {
	if !inside {
		return m.menuDone(nil, true)
	}
	menu, ok := m.menu.hit(x, y, m.width)
	if !ok {
		return m, nil
	}
	m.menu = menu
	return m.menuDone(m.menu.choice(), true)
}

func (m chatViewModel) menuDone(choice *msgMenuChoice, done bool) (chatViewModel, tea.Cmd) {
	if !done {
		return m, nil
	}
	m.subMode = m.menu.prev
	if choice == nil {
		return m, nil
	}
	return m.runMenuAction(*choice)
}

// runMenuAction does what the modal chose to its message, which becomes the
// chat's selection first.
func (m chatViewModel) runMenuAction(c msgMenuChoice) (chatViewModel, tea.Cmd) {
	sel := m.menu.target
	idx := -1
	for i, msg := range m.messages {
		if msg.ID == sel.ID {
			m.selected, idx = i, i
			break
		}
	}
	switch c.kind {
	case actReact:
		return m.reactWithFlash(sel, c.emoji)
	case actReply:
		return m.startReply(sel)
	case actThread:
		return m.openThread(sel)
	case actQuote:
		return m.goToQuote(sel)
	case actEdit:
		return m.startEdit(sel)
	case actForward:
		req := forwardRequest{from: m.entry, msgID: sel.ID}
		return m, func() tea.Msg { return chatForwardReq{req} }
	case actOpen:
		return m.openAttachment(sel)
	case actPlay:
		return m.playMedia(sel)
	case actSave:
		return m.saveMedia(sel)
	case actPin:
		return m, m.doPin(sel, idx, sel.Pinned)
	case actDeleteForMe, actDeleteForAll:
		return m.askDelete(c.kind == actDeleteForAll), nil
	}
	return m, nil
}

func (m chatViewModel) goToQuote(sel backend.Message) (chatViewModel, tea.Cmd) {
	for i, msg := range m.messages {
		if msg.ID == sel.ReplyToID {
			m.selected = i
			return m.followThread()
		}
	}
	m.notice = "the quoted message is not loaded; scroll up to load older messages"
	return m, nil
}

func (m chatViewModel) startReply(sel backend.Message) (chatViewModel, tea.Cmd) {
	if m.replying != nil && m.replying.ID == sel.ID {
		m.replying = nil
		return m, nil
	}
	m.replying = &sel
	return m, nil
}

func (m chatViewModel) startEdit(sel backend.Message) (chatViewModel, tea.Cmd) {
	if err := m.b.Editable(sel); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.subMode = chatSubEdit
	m.editing = &sel
	m.editInput = textinput.New()
	text := sel.Text
	if sel.Source != "" {
		text = sel.Source
	}
	m.editInput.SetValue(text)
	return m, m.editInput.Focus()
}

func (m chatViewModel) saveMedia(sel backend.Message) (chatViewModel, tea.Cmd) {
	filename, ok := m.b.MediaSaveable(sel)
	if !ok {
		m.notice = "this message has no downloadable attachment"
		return m, nil
	}
	m.notice = fmt.Sprintf("saving %s to ~/Downloads", filename)
	return m, m.doSave(sel, filename)
}

func (m chatViewModel) playMedia(sel backend.Message) (chatViewModel, tea.Cmd) {
	player, err := exec.LookPath("mpv")
	if err != nil {
		m.notice = "install mpv with Kitty video output to open media"
		return m, nil
	}
	m.notice = "downloading…"
	return m, m.openMediaCmd(sel, player)
}

// askDelete asks before deleting the selected message, for both sides when
// revoke is set.
func (m chatViewModel) askDelete(revoke bool) chatViewModel {
	m.subMode = chatSubConfirmMsgDelete
	m.confirmRevoke = revoke
	scope := "only for you"
	if revoke {
		scope = "for both sides"
	}
	m.confirm = newConfirmModal(m.st, "Delete message",
		fmt.Sprintf("Delete this message %s?", scope), "Delete", true)
	return m
}

// askDeleteChat asks before deleting this chat, for both sides when revoke
// is set.
func (m chatViewModel) askDeleteChat(revoke bool) chatViewModel {
	m.subMode = chatSubConfirmChatDelete
	m.confirmRevoke = revoke
	m.confirm = newConfirmModal(m.st, "Delete chat", deleteChatQuestion(m.entry, revoke),
		"Delete", true)
	return m
}

// deleteChatQuestion asks whether to delete the chat with entry.
func deleteChatQuestion(entry backend.ChatEntry, revoke bool) string {
	scope := "only for you"
	if revoke {
		scope = "for both sides"
	}
	return fmt.Sprintf("Delete chat with %s %s?", textfmt.Plain(entry.Title), scope)
}

func (m chatViewModel) handleSearchKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.subMode = chatSubNone
		m.searchResults = nil
		return m.followThread()
	case "up":
		if m.searchSelected > 0 {
			m.searchSelected--
		}
	case "down":
		if m.searchSelected+1 < len(m.searchResults) {
			m.searchSelected++
		}
	case "enter":
		if m.searchSelected < len(m.searchResults) {
			sel := m.searchResults[m.searchSelected]
			for i, msg := range m.messages {
				if msg.ID == sel.ID {
					m.selected = i
					break
				}
			}
		}
		m.subMode = chatSubNone
		m.searchResults = nil
		return m, nil
	default:
		return m.searchEdited(msg)
	}
	return m, nil
}

// searchEdited passes msg to the search box and re-runs the query if it changed.
func (m chatViewModel) searchEdited(msg tea.Msg) (chatViewModel, tea.Cmd) {
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	newQuery := strings.TrimSpace(m.searchInput.Value())
	if newQuery != m.searchQuery {
		m.searchQuery = newQuery
		if newQuery != "" {
			return m, tea.Batch(cmd, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return chatSearchDebounce{}
			}))
		}
		m.searchResults = nil
		m.searchSelected = 0
	}
	return m, cmd
}

// updateInput hands a non-key message, such as a paste, to the input on screen.
func (m chatViewModel) updateInput(msg tea.Msg) (chatViewModel, tea.Cmd) {
	var cmd tea.Cmd
	switch m.subMode {
	case chatSubNone:
		m.fitInputs()
		m.input, cmd = m.input.Update(msg)
	case chatSubMenu:
		m.menu, cmd, _, _ = m.menu.edited(msg)
	case chatSubSearch:
		return m.searchEdited(msg)
	case chatSubEdit:
		m.editInput, cmd = m.editInput.Update(msg)
	case chatSubThread:
		m.fitInputs()
		m.threadInput, cmd = m.threadInput.Update(msg)
	case chatSubUpload:
		if m.upload.step == uploadTyping {
			m.upload.matches = nil
		}
		if m.upload.step != uploadPick {
			m.upload.input, cmd = m.upload.input.Update(msg)
		}
	}
	return m, cmd
}

func (m chatViewModel) handleEditKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.subMode = chatSubNone
		m.editing = nil
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.editInput.Value())
		if text == "" {
			m.notice = "edited message cannot be empty; press Esc to cancel"
			return m, nil
		}
		return m, m.doEdit(text)
	default:
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		return m, cmd
	}
}

func (m chatViewModel) handleConfirmChatDeleteKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	yes, done := m.confirm.key(msg.String())
	if !done {
		return m, nil
	}
	m.subMode, m.confirm = chatSubNone, nil
	if yes {
		revoke := m.confirmRevoke
		return m, m.doDeleteChat(revoke)
	}
	m.notice = ""
	return m, nil
}

func (m chatViewModel) handleConfirmMsgDeleteKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	yes, done := m.confirm.key(msg.String())
	if !done {
		return m, nil
	}
	m.subMode, m.confirm = chatSubNone, nil
	if yes {
		sel, ok := m.selectedMsg()
		if ok {
			revoke := m.confirmRevoke
			idx := m.selected
			delete(m.seen, sel.ID)
			delete(m.changedMessages, sel.ID)
			m.messages = append(m.messages[:idx], m.messages[idx+1:]...)
			if m.selected >= len(m.messages) {
				m.selected = max(0, len(m.messages)-1)
			}
			return m, m.doDeleteMsg(sel.ID, revoke)
		}
	}
	m.notice = ""
	return m, nil
}

// reactWithFlash toggles the user's emoticon reaction on target and blinks
// it while the backend answers.
func (m chatViewModel) reactWithFlash(target backend.Message, emoticon string) (chatViewModel, tea.Cmd) {
	m.flashSeq++
	m.flash, m.flashStep = reactionKey{target.ID, emoticon}, 0
	return m, tea.Batch(m.doReact(target, emoticon), m.flashTickCmd())
}

// flashing is the reaction to draw highlighted now: the blinking one in its
// on phases.
func (m chatViewModel) flashing() reactionKey {
	if m.flashStep%2 == 1 {
		return reactionKey{}
	}
	return m.flash
}

func (m chatViewModel) flashTickCmd() tea.Cmd {
	seq := m.flashSeq
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
		return reactionFlashTick{seq}
	})
}

func (m chatViewModel) doReact(target backend.Message, emoticon string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		reactions, err := b.React(ctx, entry, target, emoticon)
		return chatReactionResult{chatTag{entry.ID}, target.ID, emoticon, reactions, err}
	}
}

func (m chatViewModel) doEdit(text string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	editing := *m.editing
	return func() tea.Msg {
		err := b.Edit(ctx, entry, editing, text)
		return chatEditResult{chatTag{entry.ID}, err}
	}
}

func (m chatViewModel) doDeleteChat(revoke bool) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		return chatDeleteChatResult{b.DeleteChat(ctx, entry, revoke)}
	}
}

func (m chatViewModel) doDeleteMsg(id string, revoke bool) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		return chatDeleteMsgResult{b.DeleteMessages(ctx, entry, []string{id}, revoke)}
	}
}

func (m chatViewModel) doPin(sel backend.Message, idx int, unpin bool) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		err := b.Pin(ctx, entry, sel, unpin)
		return chatPinResult{chatTag{entry.ID}, idx, unpin, err}
	}
}

func (m chatViewModel) doSave(sel backend.Message, filename string) tea.Cmd {
	return saveToDownloads(m.ctx, m.b, sel, filename)
}

// saveToDownloads saves sel's attachment, filename, in ~/Downloads.
func saveToDownloads(ctx context.Context, b backend.Backend, sel backend.Message,
	filename string) tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return chatSaveResult{"", fmt.Errorf("locate home directory: %w", err)}
		}
		dir := filepath.Join(home, "Downloads")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return chatSaveResult{"", fmt.Errorf("create Downloads directory: %w", err)}
		}
		err = b.SaveMedia(ctx, sel, dir)
		return chatSaveResult{filename, err}
	}
}

func (m chatViewModel) doSearch() tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	query := m.searchQuery
	return func() tea.Msg {
		msgs, err := b.SearchMessages(ctx, entry, query)
		return chatSearchResult{chatTag{entry.ID}, msgs, err}
	}
}

func (m chatViewModel) openMediaCmd(sel backend.Message, player string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	return func() tea.Msg {
		cmd, cleanup, err := b.MediaExec(ctx, sel, player)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return mediaFinished{err}
		}
		return mediaReady{cmd, cleanup}
	}
}

// handleClick selects the clicked message; a left click opens its attachment
// in the desktop's default application and a right click opens its actions.
func (m chatViewModel) handleClick(msg chatClick) (chatViewModel, tea.Cmd) {
	switch m.subMode {
	case chatSubSearch, chatSubUpload:
		if msg.right {
			return m, nil
		}
		return m.pickerClick(msg.y - 1)
	case chatSubMenu, chatSubConfirmChatDelete, chatSubConfirmMsgDelete:
		return m, nil
	}
	if act, ok := m.suggestionAt(msg.x, msg.y); ok && !msg.right {
		return m, func() tea.Msg { return chatSuggestion{act} }
	}
	var follow tea.Cmd
	sel, hit, ok := m.threadMessageAt(msg.x, msg.y)
	if !ok {
		var i int
		i, hit, ok = m.messageAt(msg.x, msg.y)
		if !ok {
			return m, nil
		}
		m.selected = i
		sel = m.messages[i]
		if hit.kind == rowMeta && !msg.right && repliesAt(sel, m.threadInfo(sel.ID), hit.col) {
			return m.openThread(sel)
		}
		m, follow = m.followThread()
	}
	if hit.kind&rowHeader != 0 && !msg.right && m.senderAt(sel, hit.col) {
		if _, ok := m.b.(backend.Profiler); ok {
			return m, tea.Batch(follow, func() tea.Msg { return profileOpen{m.b, m.ctx, sel} })
		}
	}
	if hit.kind == rowMeta && !msg.right {
		if emoji, ok := reactionAt(sel, hit.col); ok {
			var cmd tea.Cmd
			m, cmd = m.reactWithFlash(sel, emoji)
			return m, tea.Batch(follow, cmd)
		}
	}
	if !msg.right {
		if _, saveable := m.b.MediaSaveable(sel); hit.kind&rowMedia == 0 || !saveable {
			return m, follow
		}
		var cmd tea.Cmd
		m, cmd = m.openAttachment(sel)
		return m, tea.Batch(follow, cmd)
	}
	m, cmd := m.openMenuOn(sel)
	return m, tea.Batch(follow, cmd)
}

// senderAt reports whether column col of sel's header row, counted from
// the start of its text, falls on the sender's name. Only other people's
// messages count: the user is always "you".
func (m chatViewModel) senderAt(sel backend.Message, col int) bool {
	if sel.Outgoing || sel.SenderID == "" {
		return false
	}
	for i, msg := range m.messages {
		if msg.ID == sel.ID && continues(m.messages, i) {
			return false // the name is left out
		}
	}
	header := strings.TrimRight(ansi.Strip(m.st.messageHeader(sel, m.entry, false, false,
		false)), " :")
	// The gutter and "HH:MM " come before the name.
	x := col + m.st.textIndent(m.entry)
	return x >= 2+timeWidth && x < 2+textfmt.DisplayWidth(header)
}

// openAttachment downloads sel's attachment and opens it in the desktop's
// default application.
func (m chatViewModel) openAttachment(sel backend.Message) (chatViewModel, tea.Cmd) {
	filename, ok := m.b.MediaSaveable(sel)
	if !ok || m.opening != nil {
		return m, nil
	}
	dir, err := mediaDir(m.b, m.entry, sel)
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m.notice = ""
	m.opening = &mediaDownload{filename: filename, part: dir + ".part"}
	return m, tea.Batch(m.openExternalCmd(sel, dir, filename), mediaProgressCmd(), m.spin.Tick)
}

// pickerClick acts on a click at body row r of the search results or the
// upload panel as if the row had been chosen with the keys.
func (m chatViewModel) pickerClick(r int) (chatViewModel, tea.Cmd) {
	inner, bodyHeight := m.width-4, m.height-2
	switch m.subMode {
	case chatSubSearch:
		height := bodyHeight - len(m.st.composerBox("Search", nil, "/ ", m.searchInput, inner, colorQuote))
		lines := m.st.genericSearchResultLines(m.b, m.searchResults, m.searchSelected, m.entry,
			m.searchQuery, inner, height)
		// The same layout bottomLines gives the results: heading, blank, rows.
		row := r - (height - len(lines)) - 2
		start := max(0, m.searchSelected-max(1, height-2)+1)
		if r >= height || row < 0 || row >= len(lines)-2 || len(m.searchResults) == 0 {
			return m, nil
		}
		m.searchSelected = start + row
		return m.handleSearchKey(keyPress(tea.KeyEnter))
	}
	u := &m.upload
	switch u.step {
	case uploadTyping:
		limit := max(1, bodyHeight-5) // under the heading, a blank and the path box
		i := max(0, u.matchIdx-limit+1) + r - 5
		if r < 5 || i >= min(len(u.matches), max(0, u.matchIdx-limit+1)+limit) {
			return m, nil
		}
		u.matchIdx = i
		return m.handleUploadKey(keyPress(tea.KeyTab))
	case uploadPick:
		// The picker keeps its cursor private, so walk the keys to the row.
		view := u.picker.View()
		lines := strings.Split(view, "\n")
		row := r - 3 // under the heading, the folder and a blank
		cur, ok := cursorRow(view, u.picker.Cursor)
		if !ok || row < 0 || row >= len(lines) || strings.TrimSpace(ansi.Strip(lines[row])) == "" {
			return m, nil
		}
		step := keyPress(tea.KeyDown)
		if row < cur {
			step = keyPress(tea.KeyUp)
		}
		for range max(row-cur, cur-row) {
			m, _ = m.handleUploadKey(step)
		}
		return m.handleUploadKey(keyPress(tea.KeyEnter))
	}
	return m, nil
}

func mediaProgressCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return mediaProgressTick{} })
}

// dirSize sums the sizes of the files directly inside dir.
func dirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// footerLine replaces the help bar while a clicked attachment downloads, or
// says how to open the selected message's attachment.
func (m chatViewModel) footerLine() (string, bool) {
	if m.opening != nil {
		text := "downloading " + m.opening.filename
		if m.opening.bytes > 0 {
			text += " · " + textfmt.FormatSize(m.opening.bytes)
		}
		return m.spin.View() + m.st.subtle.Render(text+"…"), true
	}
	if m.notice != "" || m.subMode != chatSubNone || m.privateMode {
		return "", false
	}
	sel, ok := m.selectedMsg()
	if !ok {
		return "", false
	}
	filename, ok := m.b.MediaSaveable(sel)
	if !ok {
		return "", false
	}
	line := m.st.accent.Render("click") + m.st.subtle.Render(" the attachment to open "+filename) +
		m.st.subtle.Render(" · ") + m.st.accent.Render("enter") + m.st.subtle.Render(" actions")
	return line, true
}

// msgHit is where a click landed in a message: the kind of row, and the
// column counted from the start of the message's text.
type msgHit struct {
	kind rowKind
	col  int
}

// messageAt maps a point of the chat pane to the message drawn there, and
// where it was hit, using the same layout as chatBox and messageLines.
func (m chatViewModel) messageAt(x, y int) (int, msgHit, bool) {
	width, split := m.threadSplit()
	if (m.subMode != chatSubNone && !split) || m.privateMode || m.messages == nil {
		return 0, msgHit{}, false
	}
	if x >= width {
		return 0, msgHit{}, false
	}
	owners, kinds := m.chatOwners(width)
	row := y - 1
	if row < 0 || row >= len(owners) || owners[row] < 0 {
		return 0, msgHit{}, false
	}
	return owners[row], msgHit{kinds[row], x - 2 - m.st.textIndent(m.entry)}, true
}

// chatOwners maps each message row of a chat box width columns wide, below
// its top border, to the message drawn there (-1 for none) and what the row
// draws.
func (m chatViewModel) chatOwners(width int) ([]int, []rowKind) {
	_, split := m.threadSplit()
	// The chat beside an open thread draws its composer unfocused.
	inner, bodyHeight := width-4, m.height-2
	height := bodyHeight - len(m.composer(inner, m.focused && !split)) - len(m.uploads)
	_, owners, kinds := m.st.genericSelectedLines(m.b, m.messages, m.entry, m.selected,
		m.changedMessages, m.images, m.quotes, m.openRoot(), m.firstUnread, m.unread, m.flashing(),
		inner, height)
	return screenRows(owners, height, -1), screenRows(kinds, height, rowText)
}

// screenRows pads with blank or trims per-line values the way bottomLines
// does the lines.
func screenRows[T any](rows []T, height int, blank T) []T {
	height = max(0, height)
	if len(rows) > height {
		return rows[len(rows)-height:]
	}
	pad := make([]T, height-len(rows), height)
	for i := range pad {
		pad[i] = blank
	}
	return append(pad, rows...)
}

// openExternalCmd downloads the attachment into the media cache unless an
// earlier click or inline image already did, then hands it to xdg-open (open
// on macOS, rundll32 on Windows).
func (m chatViewModel) openExternalCmd(sel backend.Message, dir, filename string) tea.Cmd {
	return openExternal(m.ctx, m.b, sel, dir, filename)
}

func openExternal(ctx context.Context, b backend.Backend, sel backend.Message, dir,
	filename string) tea.Cmd {
	return func() tea.Msg {
		path, err := fetchMedia(ctx, b, sel, dir, filename)
		if err != nil {
			return mediaOpened{err: err}
		}
		opener, args := "xdg-open", []string{path}
		switch runtime.GOOS {
		case "darwin":
			opener = "open"
		case "windows":
			opener, args = "rundll32", []string{"url.dll,FileProtocolHandler", path}
		}
		if _, err := exec.LookPath(opener); err != nil {
			return mediaOpened{err: fmt.Errorf("install %s to open media", opener)}
		}
		// Not tied to ctx: the viewer should outlive a chat switch.
		cmd := exec.Command(opener, args...)
		if err := cmd.Start(); err != nil {
			return mediaOpened{err: fmt.Errorf("run %s: %w", opener, err)}
		}
		go func() { _ = cmd.Wait() }()
		return mediaOpened{filename: filename}
	}
}

func (m chatViewModel) View() string {
	if m.subMode == chatSubThread {
		if chatWidth, split := m.threadSplit(); split {
			return joinColumns(
				m.chatBox(chatWidth, m.height, false),
				m.threadBox(m.width-chatWidth, m.height, m.focused))
		}
		return m.threadBox(m.width, m.height, m.focused)
	}
	return m.chatBox(m.width, m.height, m.focused)
}

func (m chatViewModel) chatBox(width, height int, focused bool) string {
	name := chatName(m.entry)
	title := m.st.dim.Render(name[:1]) + styleTitle.Render(name[1:])
	if m.entry.Section != "" {
		title += " " + m.st.tag.Render(" "+strings.ToLower(m.entry.Section)+" ")
	}
	if m.bot != nil {
		title = m.st.agent.Render(agentIcon+" @"+m.bot.handle) + m.st.dim.Render(" · ") + title
	}
	if m.typing {
		title += m.st.subtle.Render(" · typing…")
	}
	if m.privateMode {
		title += m.st.subtle.Render(" · private")
	}
	inner, bodyHeight := width-4, height-2
	var body []string
	switch {
	case m.messages == nil:
		body = centerLines(inner, bodyHeight, m.spin.View()+"loading messages…")
	case m.subMode == chatSubSearch:
		composer := m.st.composerBox("Search", nil, "/ ", m.searchInput, inner, colorQuote)
		results := m.st.genericSearchResultLines(m.b, m.searchResults, m.searchSelected, m.entry,
			m.searchQuery, inner, bodyHeight-len(composer))
		body = append(bottomLines(results, bodyHeight-len(composer)), composer...)
	case m.subMode == chatSubUpload:
		body = m.uploadBody(inner, bodyHeight)
	default:
		composer := m.composer(inner, focused)
		body = append(m.messageLines(inner, bodyHeight-len(composer)), composer...)
	}
	return renderTitledBox(title, "", body, width, height, m.st.border(focused))
}

// messageLines is the scrolled message log plus any uploads in flight.
func (m chatViewModel) messageLines(width, height int) []string {
	height -= len(m.uploads)
	var lines []string
	if m.privateMode && len(m.messages) > 0 {
		last := len(m.messages) - 1
		lines, _, _ = m.st.genericSelectedLines(m.b, m.messages[last:], m.entry, 0, m.changedMessages, nil, nil, "", "", nil, reactionKey{}, width, height)
	} else {
		lines, _, _ = m.st.genericSelectedLines(m.b, m.messages, m.entry, m.selected, m.changedMessages, m.images, m.quotes, m.openRoot(), m.firstUnread, m.unread, m.flashing(), width, height)
	}
	if len(m.messages) == 0 {
		lines = centerLines(width, height, m.st.subtle.Render("No messages yet. Say hi!"))
	}
	lines = bottomLines(lines, max(0, height))
	for _, u := range m.uploads {
		prefix := "  " + m.st.messageHeader(backend.Message{Outgoing: true, Timestamp: u.started},
			m.entry, false, false, false) + " "
		lines = append(lines, textfmt.ClipLine(prefix+m.spin.View()+
			m.st.subtle.Render(fmt.Sprintf("uploading %s (%s)…", filepath.Base(u.path), textfmt.FormatSize(u.size))), width))
	}
	return lines
}

// composer is the bordered input at the bottom of the chat, with the reply
// bar above the text while replying.
func (m chatViewModel) composer(width int, focused bool) []string {
	border := m.st.border(focused)
	if m.subMode == chatSubEdit {
		return m.st.composerBox("Editing message", nil, "edit › ", m.editInput, width, colorYellow)
	}
	box := m.st.areaBox("message "+chatName(m.entry), m.replyBar(), m.input, width, border)
	if m.suggestion != "" && m.subMode == chatSubNone {
		box = append(m.suggestionCard(width), box...)
	}
	return box
}

// suggestionMaxLines caps the agent's reply in its card; Edit shows it all.
const suggestionMaxLines = 6

// suggestionButtons are the card's buttons and their offsets in its last row.
var suggestionButtons = []struct {
	label, act string
	at         int
}{{"[Approve ^Y]", "approve", 0}, {"[Edit ^E]", "edit", 14}, {"[Discard ^X]", "discard", 25}}

// suggestionCard is the agent's waiting reply over the composer, with its
// Approve, Edit and Discard buttons.
func (m chatViewModel) suggestionCard(width int) []string {
	text := strings.Split(lipgloss.NewStyle().Width(max(1, width-4)).Render(m.suggestion), "\n")
	if len(text) > suggestionMaxLines {
		text = append(text[:suggestionMaxLines-1], m.st.dim.Render("…"))
	}
	buttons := ""
	for i, b := range suggestionButtons {
		style := m.st.subtle
		if i == 0 {
			style = m.st.agent.Bold(true)
		}
		buttons += strings.Repeat(" ", b.at-textfmt.DisplayWidth(buttons)) + style.Render(b.label)
	}
	lines := append(text, buttons)
	return strings.Split(renderBox(agentIcon+" Bot suggests", lines, width, len(lines)+2,
		lipgloss.Color(m.st.theme.agent)), "\n")
}

// suggestionAt is the card button at column x and row y of the chat pane.
func (m chatViewModel) suggestionAt(x, y int) (string, bool) {
	if m.suggestion == "" || m.subMode != chatSubNone {
		return "", false
	}
	inner, bodyHeight := m.width-4, m.height-2
	composer := m.composer(inner, m.focused)
	card := m.suggestionCard(inner)
	// Under the chat box's top edge; the buttons are the card's last inner row.
	if y != 1+bodyHeight-len(composer)+len(card)-2 {
		return "", false
	}
	col := x - 4 // the chat box's and the card's edges and padding
	for _, b := range suggestionButtons {
		if col >= b.at && col < b.at+textfmt.DisplayWidth(b.label) {
			return b.act, true
		}
	}
	return "", false
}

func (m chatViewModel) replyBar() []string {
	if m.replying == nil {
		return nil
	}
	return []string{styleQuoteBar.Render("↪ ") + m.st.subtle.Render("Replying to ") +
		backend.GenericMessageSender(*m.replying) + m.st.subtle.Render(": "+
		textfmt.ShortExcerpt(backend.GenericMessageText(*m.replying))+"  (esc cancels)")}
}

func (s *styles) composerBox(title string, above []string, prompt string, input textinput.Model, width int, border color.Color) []string {
	input.SetWidth(max(1, width-4-textfmt.DisplayWidth(prompt)-1))
	lines := append(above, s.accent.Render(prompt)+input.View())
	return strings.Split(renderBox(title, lines, width, len(lines)+2, border), "\n")
}

// composerMaxRows is the most rows a message input grows to before it scrolls.
const composerMaxRows = 8

// newComposerArea is a message input that wraps its text and grows with it,
// up to composerMaxRows. Enter is the chat's, so it never inserts a newline.
func newComposerArea(st *styles, placeholder string) textarea.Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.Placeholder = placeholder
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = composerMaxRows
	ta.EndOfBufferCharacter = ' '
	ta.KeyMap.InsertNewline.SetEnabled(false)
	s := textarea.DefaultStyles(true)
	plain := lipgloss.NewStyle()
	s.Focused.CursorLine, s.Focused.EndOfBuffer, s.Focused.Text = plain, plain, plain
	s.Focused.Placeholder = st.dim
	s.Blurred = s.Focused
	ta.SetStyles(s)
	ta.Focus()
	return ta
}

// fitInputs gives the inputs the width their composers draw them at, so
// their height and scrolling match what is on screen.
func (m *chatViewModel) fitInputs() {
	const frame = 4 + 4 + 2 // the pane's and the composer's edges and padding, the prompt
	chatWidth, split := m.threadSplit()
	m.input.SetWidth(max(1, chatWidth-frame))
	threadWidth := m.width
	if split {
		threadWidth -= chatWidth
	}
	m.threadInput.SetWidth(max(1, threadWidth-frame))
}

// areaBox is composerBox for a growing input: the prompt on its first row,
// later rows under the text.
func (s *styles) areaBox(title string, above []string, area textarea.Model, width int,
	border color.Color) []string {
	const prompt = "❯ "
	area.SetWidth(max(1, width-4-textfmt.DisplayWidth(prompt)))
	lines := above
	for i, row := range strings.Split(area.View(), "\n") {
		lead := "  "
		if i == 0 {
			lead = s.accent.Render(prompt)
		}
		lines = append(lines, lead+row)
	}
	return strings.Split(renderBox(title, lines, width, len(lines)+2, border), "\n")
}

// centerLines places text in the middle of a width×height area.
func centerLines(width, height int, text string) []string {
	block := lipgloss.Place(width, max(1, height), lipgloss.Center, lipgloss.Center, text)
	return strings.Split(block, "\n")
}

// lockView hides every chat, including the sidebar, until a key is pressed.
func (m chatViewModel) lockView(width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center,
			m.st.logo(width),
			"",
			m.st.subtle.Render("locked"),
			m.st.dim.Render("press any key"),
		))
}

// helpKeys lists the shortcuts for the current mode, hiding any the backend
// cannot do.
func (m chatViewModel) helpKeys() []helpKey {
	switch m.subMode {
	case chatSubEdit:
		return chatEditHelp
	case chatSubUpload:
		return m.uploadHelp()
	case chatSubThread:
		return chatThreadHelp
	case chatSubSearch:
		return chatSearchHelp
	case chatSubMenu:
		if !m.menu.reactions {
			return []helpKey{msgMenuHelp[0], msgMenuHelp[3], msgMenuHelp[4]}
		}
		return msgMenuHelp
	case chatSubConfirmChatDelete, chatSubConfirmMsgDelete:
		return m.confirm.helpKeys()
	}
	keys := []helpKey{{"↑↓", "messages"}, {"enter", "send · actions"}}
	if sel, ok := m.selectedMsg(); ok && m.inThread(sel) {
		keys = append(keys, helpKey{"M-t", "open thread"})
	}
	if m.suggestion != "" {
		keys = []helpKey{{"^Y", "approve"}, {"^E", "edit"}, {"^X", "discard"}, keys[0]}
	}
	if m.embedded {
		keys = append(keys, helpKey{"tab", "chats"})
	}
	if m.caps.Upload {
		keys = append(keys, helpKey{"^U", "upload"})
	}
	if m.caps.Search {
		keys = append(keys, helpKey{"^F", "search"})
	}
	if m.caps.DeleteChat && m.entry.IsDM {
		keys = append(keys, helpKey{"M-d", "delete chat"})
	}
	keys = append(keys, helpKey{"M-h", "private"}, helpKey{"M-l", "lock"},
		helpKey{"esc", "back"})
	return keys
}

func (m chatViewModel) cleanup() {
	if m.cancel != nil {
		m.cancel()
	}
	m.b.SetActive(backend.ChatEntry{})
	// Sent before returning, so it is not lost when the app quits.
	if id := newestIncoming(m.messages); m.reads != nil && id != "" && id != m.reads.marked {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.b.MarkRead(ctx, m.entry, id)
	}
}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

var chatSearchHelp = []helpKey{
	{"↑↓", "results"},
	{"enter", "jump"},
	{"Esc", "cancel"},
}

var chatEditHelp = []helpKey{
	{"enter", "save"},
	{"Esc", "cancel"},
}

var chatThreadHelp = []helpKey{
	{"↑↓", "messages"},
	{"enter", "reply in thread"},
	{"esc", "close thread"},
}
