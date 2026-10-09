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
	"strings"
	"time"

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
	chatSubEmoji
	chatSubSearch
	chatSubEdit
	chatSubUpload
	chatSubThread
	chatSubConfirmChatDelete
	chatSubConfirmMsgDelete
	chatSubCompose
	chatSubReader
)

type chatViewModel struct {
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
	previews         map[string]*backend.LinkPreview // by URL; nil = asked, none yet
	images           map[string]*chatImage           // by message ID
	historyExhausted bool
	loadingOlder     bool            // a page of older history is on its way
	firstUnread      string          // ID of the oldest message unread on open
	unread           map[string]bool // IDs of the messages unread on open

	input textinput.Model
	// draft replaces input in email chats; reading is the mail in the reader.
	draft        textarea.Model
	sendingDraft bool
	reading      backend.Message
	readerTop    int
	sends        chan chatOutgoing
	queue        []chatOutgoing

	reads    *genericReadReporter
	replying *backend.Message
	typing   bool
	early    []backend.Event // arrived while the first history page was loading

	subMode chatSubMode
	notice  string

	emoji emojiPickerModel

	searchInput    textinput.Model
	searchQuery    string
	searchResults  []backend.Message
	searchSelected int

	editing   *backend.Message
	editInput textinput.Model

	upload  uploadPanel
	uploads []pendingUpload
	opening *mediaDownload
	spin    spinner.Model

	threadRoot  string
	threadInput textinput.Model

	confirmRevoke bool

	privateMode    bool
	lastTypingSent time.Time

	locked       bool
	autoLock     bool
	lockDeadline time.Time

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

type chatSendResult struct {
	msg backend.Message
	err error
}

type chatHistoryLoaded struct {
	messages []backend.Message
	err      error
	older    bool
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

type mediaReady struct {
	cmd     *exec.Cmd
	cleanup func()
}
type mediaFinished struct{ err error }

// chatClick is a left click at column x and row y of the chat pane, counted
// from its left and top borders.
type chatClick struct{ x, y int }

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
type lockTick struct{}

type chatReactionResult struct {
	messageID string
	emoticon  string
	reactions []backend.MessageReaction
	err       error
}

type chatEditResult struct{ err error }
type chatUploadResult struct {
	path string
	msg  backend.Message
	err  error
}
type chatFocusSidebar struct{}
type chatDeleteChatResult struct{ err error }
type chatDeleteMsgResult struct{ err error }
type chatPinResult struct {
	index int
	unpin bool
	err   error
}
type chatSaveResult struct {
	filename string
	err      error
}
type chatSearchResult struct {
	messages []backend.Message
	err      error
}
type chatSearchDebounce struct{}

func newChatViewModel(ctx context.Context, b backend.Backend, entry backend.ChatEntry, histories *[]string) chatViewModel {
	ctx, cancel := context.WithCancel(ctx)
	w, h := termSize()
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "Message " + textfmt.Plain(entry.Title) + "…"
	ti.Focus()
	caps := b.Capabilities()
	var draft textarea.Model
	if caps.MultilineCompose {
		draft = newDraft()
	}
	return chatViewModel{
		ctx:             ctx,
		cancel:          cancel,
		b:               b,
		entry:           entry,
		caps:            caps,
		histories:       histories,
		draft:           draft,
		seen:            make(map[string]bool),
		changedMessages: make(map[string]bool),
		previews:        make(map[string]*backend.LinkPreview),
		images:          make(map[string]*chatImage),
		input:           ti,
		sends:           make(chan chatOutgoing),
		autoLock:        b.AutoLockEnabled(),
		spin:            spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(styleAccent)),
		focused:         true,
		width:           w,
		height:          h,
	}
}

func (m chatViewModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadHistory(), m.input.Focus(), m.spin.Tick}
	if m.autoLock {
		m.lockDeadline = time.Now().Add(10 * time.Second)
		cmds = append(cmds, m.lockTickCmd())
	}
	return tea.Batch(cmds...)
}

func (m chatViewModel) lockTickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg {
		return lockTick{}
	})
}

func (m chatViewModel) resetLock() chatViewModel {
	if m.autoLock {
		m.lockDeadline = time.Now().Add(10 * time.Second)
	}
	return m
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
		return chatHistoryLoaded{msgs, err, false}
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
		return chatHistoryLoaded{msgs, err, true}
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
	return func() tea.Msg {
		if outgoing.path != "" {
			msg, err := b.SendUpload(ctx, entry, outgoing.path, outgoing.text, outgoing.replyTo)
			return chatUploadResult{outgoing.path, msg, err}
		}
		msg, err := b.Send(ctx, entry, outgoing.text, outgoing.replyTo)
		return chatSendResult{msg, err}
	}
}

func (m chatViewModel) startReadTimer() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return chatReadTick{}
	})
}

func (m chatViewModel) doMarkRead() tea.Cmd {
	r := m.reads
	if r == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		r.Start(ctx)
		return nil
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

// Update handles msg, then fetches any image it brought into view.
func (m chatViewModel) Update(msg tea.Msg) (chatViewModel, tea.Cmd) {
	m, cmd := m.update(msg)
	switch msg.(type) {
	case spinner.TickMsg, lockTick, mediaProgressTick:
		return m, cmd // nothing moved
	}
	return m, tea.Batch(cmd, m.loadVisibleImages())
}

func (m chatViewModel) update(msg tea.Msg) (chatViewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case chatHistoryLoaded:
		cm, cmd := m.handleHistoryLoaded(msg)
		return cm.clearSelectedChanged(), tea.Batch(cmd, cm.fetchPreviews(msg.messages))

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
		cm, cmd := m.handleEvent(msg.ev)
		return cm.clearSelectedChanged(), cmd

	case chatTypingTimeout:
		m.typing = false
		m.b.ClearTyping()

	case chatReadTick:
		if m.reads != nil {
			if m.selected >= 0 && m.selected < len(m.messages) {
				delete(m.changedMessages, m.messages[m.selected].ID)
			}
			var readID string
			for _, msg := range m.messages {
				if !msg.Outgoing && msg.ID > readID {
					readID = msg.ID
				}
			}
			m.reads.observe(readID)
		}
		return m, tea.Batch(m.doMarkRead(), m.startReadTimer())

	case chatSendResult:
		if m.caps.MultilineCompose {
			m = m.draftSent(msg.err)
		}
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		m = m.appendMessage(msg.msg)

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
		if m.messages != nil && len(m.uploads) == 0 && m.opening == nil {
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

	case lockTick:
		if m.autoLock && !m.locked && time.Now().After(m.lockDeadline) {
			m.locked = true
		}
		return m, m.lockTickCmd()

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
		}

	case emojiPicked:
		return m.handleEmojiPicked(msg)

	case emojiCancelled:
		m.subMode = chatSubNone

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
	m.messages = msg.messages
	m.selected = len(msg.messages) - 1
	m.unread, m.firstUnread = unreadIDs(msg.messages, m.entry.Unread)
	for _, msg := range msg.messages {
		m.seen[msg.ID] = true
	}
	m.reads = newGenericReadReporter(m.b, m.entry)
	cmds := []tea.Cmd{m.startReadTimer()}
	for _, ev := range m.early {
		var cmd tea.Cmd
		m, cmd = m.handleEvent(ev)
		cmds = append(cmds, cmd)
	}
	m.early = nil
	return m, tea.Batch(cmds...)
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
		return m, m.fetchPreviews([]backend.Message{*ev.Message})
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
	m = m.resetLock()

	if m.locked {
		m.locked = false
		return m, nil
	}

	m.notice = ""

	switch m.subMode {
	case chatSubEmoji:
		return m.handleEmojiKey(msg)
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
	case chatSubCompose:
		return m.handleComposeKey(msg)
	case chatSubReader:
		return m.handleReaderKey(msg)
	}

	switch msg.String() {
	case "esc":
		if m.replying != nil {
			m.replying = nil
			return m, nil
		}
		if m.embedded {
			return m, func() tea.Msg { return chatFocusSidebar{} }
		}
		return m, func() tea.Msg { return chatReturnToList{} }
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
		if m.caps.MultilineCompose {
			return m.openReader()
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
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
	case "alt+g", "alt+G":
		sel, ok := m.selectedMsg()
		if !ok || sel.ReplyToID == "" {
			return m, nil
		}
		for i, msg := range m.messages {
			if msg.ID == sel.ReplyToID {
				m.selected = i
				return m.followThread()
			}
		}
		m.notice = "the quoted message is not loaded; scroll up to load older messages"
	case "alt+t", "alt+T":
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		return m.openThread(sel)
	case "alt+m", "alt+M":
		sel, ok := m.selectedMsg()
		reactionMode := m.caps.Reactions && ok
		if reactionMode {
			m.emoji = newEmojiPicker(m.b, true, &sel)
		} else {
			m.emoji = newEmojiPicker(m.b, false, nil)
		}
		m.subMode = chatSubEmoji
	case "alt+r", "alt+R":
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		if m.replying != nil && m.replying.ID == sel.ID {
			m.replying = nil
		} else {
			m.replying = &sel
			if m.caps.MultilineCompose {
				return m.startCompose(nil)
			}
		}
	case "alt+e", "alt+E":
		if !m.caps.Edit {
			return m, nil
		}
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		if err := m.b.Editable(sel); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		m.subMode = chatSubEdit
		m.editing = &sel
		m.editInput = textinput.New()
		m.editInput.SetValue(sel.Text)
		m.editInput.Focus()
		return m, m.editInput.Focus()
	case "alt+f", "alt+F":
		if !m.caps.Forward {
			return m, nil
		}
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		req := forwardRequest{from: m.entry, msgID: sel.ID}
		return m, func() tea.Msg { return chatForwardReq{req} }
	case "alt+s", "alt+S":
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		filename, saveable := m.b.MediaSaveable(sel)
		if !saveable {
			m.notice = "this message has no downloadable attachment"
			return m, nil
		}
		m.notice = fmt.Sprintf("saving %s to ~/Downloads", filename)
		return m, m.doSave(sel, filename)
	case "alt+p", "alt+P":
		if !m.caps.Pin {
			return m, nil
		}
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		idx := m.selected
		unpin := sel.Pinned
		return m, m.doPin(sel, idx, unpin)
	case "alt+d", "alt+D":
		if !m.caps.DeleteChat || !m.entry.IsDM {
			return m, nil
		}
		m.subMode = chatSubConfirmChatDelete
		m.confirmRevoke = false
		m.notice = fmt.Sprintf("Delete chat with %s only for you? y to confirm; any other key cancels.", textfmt.Plain(m.entry.Title))
	case "ctrl+alt+d":
		if !m.caps.DeleteChat || !m.entry.IsDM {
			return m, nil
		}
		if m.entry.IsSelf {
			m.notice = "Saved Messages cannot be deleted for both sides"
			return m, nil
		}
		m.subMode = chatSubConfirmChatDelete
		m.confirmRevoke = true
		m.notice = fmt.Sprintf("Delete chat with %s for both sides? y to confirm; any other key cancels.", textfmt.Plain(m.entry.Title))
	case "delete":
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		_ = sel
		revoke := true
		if m.caps.DeleteForMe {
			revoke = false
		}
		m.subMode = chatSubConfirmMsgDelete
		m.confirmRevoke = revoke
		scope := "only for you"
		if revoke {
			scope = "for both sides"
		}
		m.notice = fmt.Sprintf("Delete this message %s? y to confirm; any other key cancels.", scope)
	case "shift+delete":
		sel, ok := m.selectedMsg()
		if !ok {
			return m, nil
		}
		_ = sel
		m.subMode = chatSubConfirmMsgDelete
		m.confirmRevoke = true
		m.notice = "Delete this message for both sides? y to confirm; any other key cancels."
	case "alt+h", "alt+H":
		m.privateMode = !m.privateMode
		if m.privateMode {
			m.selected = len(m.messages) - 1
		}
	case "alt+o", "alt+O":
		if m.caps.MultilineCompose {
			return m, nil
		}
		sel, ok := m.selectedMsg()
		if !ok || !m.b.MediaPlayable(sel) {
			return m, nil
		}
		player, err := exec.LookPath("mpv")
		if err != nil {
			m.notice = "install mpv with Kitty video output to open media"
			return m, nil
		}
		m.notice = "downloading…"
		return m, m.openMediaCmd(sel, player)
	default:
		if m.caps.MultilineCompose {
			if msg.Text == "" {
				return m, nil
			}
			return m.startCompose(&msg)
		}
		if m.caps.Typing && time.Since(m.lastTypingSent) > 5*time.Second {
			m.lastTypingSent = time.Now()
			m.b.SetTyping(m.ctx, m.entry)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m chatViewModel) handleEmojiKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	var cmd tea.Cmd
	m.emoji, cmd = m.emoji.Update(msg)
	return m, cmd
}

func (m chatViewModel) handleEmojiPicked(msg emojiPicked) (chatViewModel, tea.Cmd) {
	m.subMode = chatSubNone
	if msg.reactionMode && msg.target != nil {
		target := *msg.target
		emoticon := msg.value
		return m, m.doReact(target, emoticon)
	}
	if msg.value != "" {
		m.b.RememberEmoji(msg.value)
		if m.caps.MultilineCompose {
			m.draft.InsertString(msg.value)
			return m.startCompose(nil)
		}
		m.input.SetValue(m.input.Value() + msg.value)
	}
	return m, nil
}

func (m chatViewModel) handleSearchKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.subMode = chatSubNone
		m.searchResults = nil
		return m, nil
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
		return m.followThread()
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
		m.input, cmd = m.input.Update(msg)
	case chatSubSearch:
		return m.searchEdited(msg)
	case chatSubEdit:
		m.editInput, cmd = m.editInput.Update(msg)
	case chatSubThread:
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
	m.subMode = chatSubNone
	if msg.String() == "y" || msg.String() == "Y" {
		revoke := m.confirmRevoke
		return m, m.doDeleteChat(revoke)
	}
	m.notice = ""
	return m, nil
}

func (m chatViewModel) handleConfirmMsgDeleteKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	m.subMode = chatSubNone
	if msg.String() == "y" || msg.String() == "Y" {
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

func (m chatViewModel) doReact(target backend.Message, emoticon string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		reactions, err := b.React(ctx, entry, target, emoticon)
		return chatReactionResult{target.ID, emoticon, reactions, err}
	}
}

func (m chatViewModel) doEdit(text string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	editing := *m.editing
	return func() tea.Msg {
		err := b.Edit(ctx, entry, editing, text)
		return chatEditResult{err}
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
		return chatPinResult{idx, unpin, err}
	}
}

func (m chatViewModel) doSave(sel backend.Message, filename string) tea.Cmd {
	ctx := m.ctx
	b := m.b
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
		return chatSearchResult{msgs, err}
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

// handleClick selects the clicked message and, when it carries media, opens
// it in the desktop's default application.
func (m chatViewModel) handleClick(msg chatClick) (chatViewModel, tea.Cmd) {
	m = m.resetLock()
	switch m.subMode {
	case chatSubEmoji, chatSubSearch, chatSubUpload:
		return m.pickerClick(msg.y - 1)
	case chatSubReader:
		return m, nil
	}
	var follow tea.Cmd
	sel, ok := m.threadMessageAt(msg.x, msg.y)
	if !ok {
		i, ok := m.messageAt(msg.x, msg.y)
		if !ok {
			return m, nil
		}
		m.selected = i
		sel = m.messages[i]
		m, follow = m.followThread()
	}
	filename, ok := m.b.MediaSaveable(sel)
	if !ok && m.caps.MultilineCompose {
		return m.openReader()
	}
	if !ok || m.opening != nil {
		return m, follow
	}
	dir, err := mediaDir(m.b, m.entry, sel)
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, follow
	}
	m.notice = ""
	m.opening = &mediaDownload{filename: filename, part: dir + ".part"}
	return m, tea.Batch(follow, m.openExternalCmd(sel, dir, filename), mediaProgressCmd(),
		m.spin.Tick)
}

// pickerClick acts on a click at body row r of the emoji picker, the search
// results or the upload panel as if the row had been chosen with the keys.
func (m chatViewModel) pickerClick(r int) (chatViewModel, tea.Cmd) {
	inner, bodyHeight := m.width-4, m.height-2
	switch m.subMode {
	case chatSubEmoji:
		i, ok := m.emoji.choiceAt(r, m.height)
		if !ok {
			return m, nil
		}
		m.emoji.selected = i
		return m.handleEmojiKey(keyPress(tea.KeyEnter))
	case chatSubSearch:
		height := bodyHeight - len(composerBox("Search", nil, "/ ", m.searchInput, inner, colorQuote))
		lines := genericSearchResultLines(m.b, m.searchResults, m.searchSelected, m.entry,
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
		return m.spin.View() + styleSubtle.Render(text+"…"), true
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
	return styleAccent.Render("click") + styleSubtle.Render(" to download and open "+filename), true
}

// messageAt maps a row of the chat pane to the message drawn there, using
// the same layout as chatBox and messageLines.
func (m chatViewModel) messageAt(x, y int) (int, bool) {
	width, split := m.threadSplit()
	if (m.subMode != chatSubNone && !split) || m.privateMode || m.messages == nil {
		return 0, false
	}
	if x >= width {
		return 0, false
	}
	owners := m.chatOwners(width)
	row := y - 1
	if row < 0 || row >= len(owners) || owners[row] < 0 {
		return 0, false
	}
	return owners[row], true
}

// chatOwners maps each message row of a chat box width columns wide, below
// its top border, to the message drawn there (-1 for none).
func (m chatViewModel) chatOwners(width int) []int {
	_, split := m.threadSplit()
	// The chat beside an open thread draws its composer unfocused.
	inner, bodyHeight := width-4, m.height-2
	height := bodyHeight - len(m.composer(inner, m.focused && !split)) - len(m.uploads)
	_, owners := genericSelectedLines(m.b, m.messages, m.entry, m.selected, m.changedMessages,
		m.images, nil, false, m.firstUnread, m.unread, inner, height)
	return screenOwners(owners, height)
}

// screenOwners pads or trims line owners the way bottomLines does the lines.
func screenOwners(owners []int, height int) []int {
	height = max(0, height)
	if len(owners) > height {
		return owners[len(owners)-height:]
	}
	pad := make([]int, height-len(owners), height)
	for i := range pad {
		pad[i] = -1
	}
	return append(pad, owners...)
}

// openExternalCmd downloads the attachment into the media cache unless an
// earlier click or inline image already did, then hands it to xdg-open (open
// on macOS).
func (m chatViewModel) openExternalCmd(sel backend.Message, dir, filename string) tea.Cmd {
	ctx := m.ctx
	b := m.b
	return func() tea.Msg {
		path, err := fetchMedia(ctx, b, sel, dir, filename)
		if err != nil {
			return mediaOpened{err: err}
		}
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		if _, err := exec.LookPath(opener); err != nil {
			return mediaOpened{err: fmt.Errorf("install %s to open media", opener)}
		}
		// Not tied to ctx: the viewer should outlive a chat switch.
		cmd := exec.Command(opener, path)
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
	title := textfmt.Plain(m.entry.Title)
	if m.typing {
		title += " · typing…"
	}
	if m.privateMode {
		title += " · private"
	}
	inner, bodyHeight := width-4, height-2
	var body []string
	switch {
	case m.messages == nil:
		body = centerLines(inner, bodyHeight, m.spin.View()+"loading messages…")
	case m.subMode == chatSubEmoji:
		body = strings.Split(m.emoji.View(inner, bodyHeight+2), "\n")
	case m.subMode == chatSubSearch:
		composer := composerBox("Search", nil, "/ ", m.searchInput, inner, colorQuote)
		results := genericSearchResultLines(m.b, m.searchResults, m.searchSelected, m.entry,
			m.searchQuery, inner, bodyHeight-len(composer))
		body = append(bottomLines(results, bodyHeight-len(composer)), composer...)
	case m.subMode == chatSubUpload:
		body = m.uploadBody(inner, bodyHeight)
	case m.subMode == chatSubReader:
		body = m.readerBody(inner, bodyHeight)
	default:
		composer := m.composer(inner, focused)
		body = append(m.messageLines(inner, bodyHeight-len(composer)), composer...)
	}
	return renderBox(title, body, width, height, boxBorder(focused))
}

// messageLines is the scrolled message log plus any uploads in flight.
func (m chatViewModel) messageLines(width, height int) []string {
	height -= len(m.uploads)
	var lines []string
	if m.privateMode && len(m.messages) > 0 {
		last := len(m.messages) - 1
		lines, _ = genericSelectedLines(m.b, m.messages[last:], m.entry, 0, m.changedMessages, nil, nil, false, "", nil, width, height)
	} else {
		lines, _ = genericSelectedLines(m.b, m.messages, m.entry, m.selected, m.changedMessages, m.images, nil, false, m.firstUnread, m.unread, width, height)
	}
	if len(m.messages) == 0 {
		lines = centerLines(width, height, styleSubtle.Render("No messages yet. Say hi!"))
	}
	lines = bottomLines(lines, max(0, height))
	for _, u := range m.uploads {
		prefix := "  " + messageHeader(backend.Message{Outgoing: true, Timestamp: u.started}, m.entry, false) + " "
		lines = append(lines, textfmt.ClipLine(prefix+m.spin.View()+
			styleSubtle.Render(fmt.Sprintf("uploading %s (%s)…", filepath.Base(u.path), textfmt.FormatSize(u.size))), width))
	}
	return lines
}

// composer is the bordered input at the bottom of the chat, with the reply
// bar above the text while replying.
func (m chatViewModel) composer(width int, focused bool) []string {
	border := boxBorder(focused)
	if m.subMode == chatSubEdit {
		return composerBox("Editing message", nil, "edit › ", m.editInput, width, colorYellow)
	}
	if m.caps.MultilineCompose {
		return m.draftBox(width, border)
	}
	return composerBox("", m.replyBar(), "› ", m.input, width, border)
}

func (m chatViewModel) replyBar() []string {
	if m.replying == nil {
		return nil
	}
	return []string{styleQuoteBar.Render("↪ ") + styleSubtle.Render("Replying to ") +
		backend.GenericMessageSender(*m.replying) + styleSubtle.Render(": "+
		textfmt.ShortExcerpt(backend.GenericMessageText(*m.replying))+"  (esc cancels)")}
}

func composerBox(title string, above []string, prompt string, input textinput.Model, width int, border color.Color) []string {
	input.SetWidth(max(1, width-4-textfmt.DisplayWidth(prompt)-1))
	lines := append(above, styleAccent.Render(prompt)+input.View())
	return strings.Split(renderBox(title, lines, width, len(lines)+2, border), "\n")
}

// centerLines places text in the middle of a width×height area.
func centerLines(width, height int, text string) []string {
	block := lipgloss.Place(width, max(1, height), lipgloss.Center, lipgloss.Center, text)
	return strings.Split(block, "\n")
}

// lockView hides every chat, including the sidebar, until a key is pressed.
func (m chatViewModel) lockView(width, height int) string {
	unread := m.b.CachedUnread(m.entry)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center,
			renderLogo(width),
			"",
			styleSubtle.Render(fmt.Sprintf("locked · %d unread", unread)),
			styleDim.Render("press any key"),
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
	case chatSubEmoji:
		return emojiHelp
	case chatSubConfirmChatDelete, chatSubConfirmMsgDelete:
		return chatConfirmHelp
	case chatSubCompose:
		return chatComposeHelp
	case chatSubReader:
		return chatReaderHelp
	}
	if m.caps.MultilineCompose {
		return m.mailHelp()
	}
	keys := []helpKey{{"↑↓", "messages"}, {"⏎", "send"}}
	if m.embedded {
		keys = append(keys, helpKey{"tab", "chats"})
	}
	keys = append(keys, helpKey{"M-r", "reply"}, helpKey{"M-t", "thread"}, helpKey{"M-o", "media"})
	if m.caps.Upload {
		keys = append(keys, helpKey{"^U", "upload"})
	}
	if m.caps.Search {
		keys = append(keys, helpKey{"^F", "search"})
	}
	keys = append(keys, helpKey{"M-m", "emoji"}, helpKey{"M-g", "go to quote"})
	if m.caps.Edit {
		keys = append(keys, helpKey{"M-e", "edit"})
	}
	if m.caps.Forward {
		keys = append(keys, helpKey{"M-f", "forward"})
	}
	keys = append(keys, helpKey{"M-s", "save"})
	if m.caps.Pin {
		keys = append(keys, helpKey{"M-p", "pin"})
	}
	keys = append(keys, helpKey{"del", "delete"}, helpKey{"M-h", "private"},
		helpKey{"esc", "back"})
	return keys
}

// mailHelp is the chat help of an email account, which has no media player,
// edits or pins.
func (m chatViewModel) mailHelp() []helpKey {
	keys := []helpKey{{"↑↓", "mails"}, {"⏎", "read"}, {"type", "write"}}
	if m.embedded {
		keys = append(keys, helpKey{"tab", "chats"})
	}
	return append(keys, helpKey{"M-r", "reply"}, helpKey{"M-t", "thread"},
		helpKey{"^F", "search"}, helpKey{"M-g", "go to quote"}, helpKey{"M-s", "save"},
		helpKey{"del", "trash"}, helpKey{"esc", "back"})
}

func (m chatViewModel) cleanup() {
	if m.cancel != nil {
		m.cancel()
	}
	m.b.SetActive(backend.ChatEntry{})
	if m.reads != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.reads.Close(ctx)
	}
}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

var chatSearchHelp = []helpKey{
	{"↑↓", "results"},
	{"⏎", "jump"},
	{"Esc", "cancel"},
}

var chatEditHelp = []helpKey{
	{"⏎", "save"},
	{"Esc", "cancel"},
}

var chatThreadHelp = []helpKey{
	{"↑↓", "messages"},
	{"⏎", "reply in thread"},
	{"esc", "close thread"},
}

var chatConfirmHelp = []helpKey{
	{"y", "confirm"},
	{"any", "cancel"},
}
