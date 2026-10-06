package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	historyExhausted bool

	input textinput.Model
	sends chan chatOutgoing
	queue []chatOutgoing

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
	return chatViewModel{
		ctx:             ctx,
		cancel:          cancel,
		b:               b,
		entry:           entry,
		caps:            b.Capabilities(),
		histories:       histories,
		seen:            make(map[string]bool),
		changedMessages: make(map[string]bool),
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case chatHistoryLoaded:
		cm, cmd := m.handleHistoryLoaded(msg)
		return cm.clearSelectedChanged(), cmd

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
		if m.messages != nil && len(m.uploads) == 0 {
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

func (m chatViewModel) handleHistoryLoaded(msg chatHistoryLoaded) (chatViewModel, tea.Cmd) {
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
	case "ctrl+c", "ctrl+d":
		return m, func() tea.Msg { return chatReturnToList{} }
	case "up":
		if m.privateMode {
			m.privateMode = false
		}
		if m.selected > 0 {
			m.selected--
		} else if !m.historyExhausted && len(m.messages) > 0 {
			return m, m.loadOlderHistory()
		}
	case "down":
		if m.selected+1 < len(m.messages) {
			m.selected++
		}
	case "enter":
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
				return m, nil
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
	case "o":
		if m.input.Value() != "" {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
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
		m.input.SetValue(m.input.Value() + msg.value)
	}
	return m, nil
}

func (m chatViewModel) handleSearchKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+d":
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
		return m, nil
	default:
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
	return m, nil
}

func (m chatViewModel) handleEditKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+d":
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

// View renders the chat pane as a box exactly m.width×m.height cells.
func (m chatViewModel) View() string {
	if m.subMode == chatSubThread {
		if m.width >= 90 {
			chatWidth := m.width * 3 / 5
			return lipgloss.JoinHorizontal(lipgloss.Top,
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
		lines, _ = genericSelectedLines(m.b, m.messages[last:], m.entry, 0, m.changedMessages, nil, false, width, height)
	} else {
		lines, _ = genericSelectedLines(m.b, m.messages, m.entry, m.selected, m.changedMessages, nil, false, width, height)
	}
	if len(m.messages) == 0 {
		lines = centerLines(width, height, styleSubtle.Render("No messages yet. Say hi!"))
	}
	lines = bottomLines(lines, max(0, height))
	for _, u := range m.uploads {
		prefix := "  " + backend.GenericMessagePrefix(backend.Message{Outgoing: true, Timestamp: u.started}, m.entry) + " "
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
	}
	keys := []helpKey{{"↑↓", "messages"}, {"⏎", "send"}}
	if m.embedded {
		keys = append(keys, helpKey{"tab", "chats"})
	}
	keys = append(keys, helpKey{"M-r", "reply"}, helpKey{"M-t", "thread"}, helpKey{"o", "media"})
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
		helpKey{"M-1…9", "account"}, helpKey{"?", "more"}, helpKey{"esc", "back"})
	return keys
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
	{"⏎", "reply in thread"},
	{"esc", "close thread"},
}

var chatConfirmHelp = []helpKey{
	{"y", "confirm"},
	{"any", "cancel"},
}
