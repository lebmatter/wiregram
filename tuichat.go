package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type chatSubMode int

const (
	chatSubNone chatSubMode = iota
	chatSubEmoji
	chatSubSearch
	chatSubEdit
	chatSubUploadPath
	chatSubUploadCaption
	chatSubConfirmChatDelete
	chatSubConfirmMsgDelete
)

type chatViewModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	b         backend
	entry     chatEntry
	caps      capabilities
	histories *[]string

	messages         []message
	selected         int
	seen             map[string]bool
	changedMessages  map[string]bool
	historyExhausted bool

	input textinput.Model
	sends chan chatOutgoing
	queue []chatOutgoing

	reads    *genericReadReporter
	replying *message
	typing   bool

	subMode chatSubMode
	notice  string

	emoji emojiPickerModel

	searchInput    textinput.Model
	searchQuery    string
	searchResults  []message
	searchSelected int

	editing   *message
	editInput textinput.Model

	uploadPath  string
	uploadInput textinput.Model

	confirmRevoke bool

	privateMode    bool
	lastTypingSent time.Time

	aiStatus aiStatus

	locked       bool
	autoLock     bool
	lockDeadline time.Time

	width, height int
}

type chatOutgoing struct {
	text    string
	path    string
	replyTo string
}

type chatSendResult struct {
	msg message
	err error
}

type chatHistoryLoaded struct {
	messages []message
	err      error
	older    bool
}

type chatBackendUpdate struct {
	incoming  []message
	reactions []genericReactionUpdate
	deletions []string
	typing    bool
}

type chatReadTick struct{}
type chatReadDone struct{ err error }
type chatTypingTimeout struct{}
type chatReturnToList struct{}
type chatForwardReq struct{ req forwardReq }

type mediaReady struct {
	cmd     *exec.Cmd
	cleanup func()
}
type mediaFinished struct{ err error }
type lockTick struct{}

type chatReactionResult struct {
	messageID string
	emoticon  string
	reactions []messageReaction
	err       error
}

type chatEditResult struct{ err error }
type chatUploadResult struct {
	msg message
	err error
}
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
	messages []message
	err      error
}
type chatSearchDebounce struct{}

type chatAIResult struct {
	text string
	err  error
}
type chatAIDebounce struct{}

func newChatViewModel(ctx context.Context, b backend, entry chatEntry, histories *[]string) chatViewModel {
	ctx, cancel := context.WithCancel(ctx)
	w, h := termSize()
	ti := textinput.New()
	ti.SetValue("")
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
		width:           w,
		height:          h,
	}
}

func (m chatViewModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadHistory(), m.input.Focus()}
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
		b.Subscribe(entry)
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

func (m chatViewModel) listenBackendChat() tea.Cmd {
	b := m.b
	return func() tea.Msg {
		<-b.Changed()
		incoming, reactions, deletions := b.Drain()
		return chatBackendUpdate{
			incoming:  incoming,
			reactions: reactions,
			deletions: deletions,
			typing:    b.IsTyping(),
		}
	}
}

func (m chatViewModel) sendMessage(outgoing chatOutgoing) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		var msg message
		var err error
		if outgoing.path == "" {
			msg, err = b.Send(ctx, entry, outgoing.text, outgoing.replyTo)
		} else {
			msg, err = b.SendUpload(ctx, entry, outgoing.path, outgoing.text, outgoing.replyTo)
		}
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
		r.start(ctx)
		return nil
	}
}

func (m chatViewModel) typingTimeoutCmd() tea.Cmd {
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg {
		return chatTypingTimeout{}
	})
}

func (m chatViewModel) appendMessage(msg message) chatViewModel {
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

func (m chatViewModel) selectedMsg() (message, bool) {
	if m.selected >= 0 && m.selected < len(m.messages) {
		msg := m.messages[m.selected]
		if msg.ID != "" && msg.ID != "0" {
			return msg, true
		}
	}
	return message{}, false
}

func (m chatViewModel) Update(msg tea.Msg) (chatViewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case chatHistoryLoaded:
		return m.handleHistoryLoaded(msg)

	case chatBackendUpdate:
		return m.handleBackendUpdate(msg)

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
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m = m.appendMessage(msg.msg)
		}

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

	case chatAIDebounce:
		if !m.aiStatus.thinking && m.b.AIOn(m.entry) {
			return m, m.doAIComplete()
		}

	case chatAIResult:
		m.aiStatus.thinking = false
		if msg.err != nil {
			if !isContextCanceled(msg.err) {
				m.notice = msg.err.Error()
			}
		} else {
			m.aiStatus.suggestion = msg.text
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
		return m.handleKey(msg)
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
		var fresh []message
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
	return m, tea.Batch(
		m.listenBackendChat(),
		m.startReadTimer(),
	)
}

func (m chatViewModel) handleBackendUpdate(msg chatBackendUpdate) (chatViewModel, tea.Cmd) {
	answerable := false
	for _, inc := range msg.incoming {
		if !inc.Outgoing {
			m.typing = false
		}
		m = m.appendMessage(inc)
		if m.b.AIShouldAnswer(inc) {
			answerable = true
		}
	}
	for _, update := range msg.reactions {
		for i, existing := range m.messages {
			if existing.ID == update.MessageID {
				old := formatMessageReactions(existing.Reactions)
				updated := formatMessageReactions(update.Reactions)
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
	}
	if len(msg.deletions) > 0 {
		removed := make(map[string]bool, len(msg.deletions))
		for _, id := range msg.deletions {
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
	}
	if msg.typing {
		m.typing = true
	}
	var cmds []tea.Cmd
	cmds = append(cmds, m.listenBackendChat())
	if msg.typing {
		cmds = append(cmds, m.typingTimeoutCmd())
	}
	if answerable && m.b.AIOn(m.entry) {
		m.aiStatus.suggestion = ""
		cmds = append(cmds, tea.Tick(aiDebounce, func(time.Time) tea.Msg {
			return chatAIDebounce{}
		}))
	}
	return m, tea.Batch(cmds...)
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
	case chatSubUploadPath, chatSubUploadCaption:
		return m.handleUploadKey(msg)
	case chatSubConfirmChatDelete:
		return m.handleConfirmChatDeleteKey(msg)
	case chatSubConfirmMsgDelete:
		return m.handleConfirmMsgDeleteKey(msg)
	}

	if m.aiStatus.suggestion != "" {
		return m.handleAISuggestionKey(msg)
	}

	switch msg.String() {
	case "esc":
		if m.replying != nil {
			m.replying = nil
			return m, nil
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
		m.cancelAI()
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
		m.subMode = chatSubUploadPath
		m.uploadInput = textinput.New()
		m.uploadInput.Focus()
		m.uploadPath = ""
		return m, m.uploadInput.Focus()
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
		req := forwardReq{from: m.entry, id: sel.ID}
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
		m.notice = fmt.Sprintf("Delete chat with %s only for you? y to confirm; any other key cancels.", plain(m.entry.Title))
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
		m.notice = fmt.Sprintf("Delete chat with %s for both sides? y to confirm; any other key cancels.", plain(m.entry.Title))
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
	case "alt+a", "alt+A":
		if !m.b.AIAvailable() {
			m.notice = "AI is not configured; add an ai section to config.json"
			return m, nil
		}
		if !m.entry.IsDM {
			m.notice = "AI replies are available only in private chats"
			return m, nil
		}
		on, err := m.b.AIToggle(m.entry)
		if !on {
			m.cancelAI()
		}
		m.aiStatus.on = on
		if err != nil {
			m.notice = err.Error()
		}
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

func (m chatViewModel) handleUploadKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+d":
		m.subMode = chatSubNone
		m.uploadPath = ""
		return m, nil
	case "enter":
		if m.subMode == chatSubUploadPath {
			path, err := uploadPath(m.uploadInput.Value())
			if err != nil {
				m.notice = err.Error()
				return m, nil
			}
			m.uploadPath = path
			m.subMode = chatSubUploadCaption
			m.uploadInput = textinput.New()
			m.uploadInput.Focus()
			return m, m.uploadInput.Focus()
		}
		caption := m.uploadInput.Value()
		path := m.uploadPath
		replyTo := ""
		if m.replying != nil {
			replyTo = m.replying.ID
			m.replying = nil
		}
		m.subMode = chatSubNone
		m.uploadPath = ""
		return m, m.sendMessage(chatOutgoing{text: caption, path: path, replyTo: replyTo})
	default:
		var cmd tea.Cmd
		m.uploadInput, cmd = m.uploadInput.Update(msg)
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

func (m chatViewModel) handleAISuggestionKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		text := m.aiStatus.suggestion
		m.aiStatus.suggestion = ""
		replyTo := ""
		if m.replying != nil {
			replyTo = m.replying.ID
			m.replying = nil
		}
		return m, m.sendMessage(chatOutgoing{text: text, replyTo: replyTo})
	case "n", "N", "esc":
		m.aiStatus.suggestion = ""
	default:
		m.aiStatus.suggestion = ""
	}
	return m, nil
}

func (m *chatViewModel) cancelAI() {
	m.aiStatus = aiStatus{}
}

func (m chatViewModel) doReact(target message, emoticon string) tea.Cmd {
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

func (m chatViewModel) doPin(sel message, idx int, unpin bool) tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	return func() tea.Msg {
		err := b.Pin(ctx, entry, sel, unpin)
		return chatPinResult{idx, unpin, err}
	}
}

func (m chatViewModel) doSave(sel message, filename string) tea.Cmd {
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

func (m chatViewModel) openMediaCmd(sel message, player string) tea.Cmd {
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

func (m chatViewModel) doAIComplete() tea.Cmd {
	ctx := m.ctx
	b := m.b
	entry := m.entry
	msgs := m.messages
	m.aiStatus.thinking = true
	return func() tea.Msg {
		request, err := b.AIRequest(entry, msgs)
		if err != nil {
			return chatAIResult{"", err}
		}
		text, err := b.AIComplete(ctx, request)
		return chatAIResult{text, err}
	}
}

func (m chatViewModel) View() string {
	if m.locked {
		unread := m.b.CachedUnread(m.entry)
		return fmt.Sprintf("[wiregram: locked — %d* — press any key]", unread)
	}

	if m.messages == nil {
		return "Loading messages…"
	}

	if m.subMode == chatSubEmoji {
		return m.emoji.View(m.width, m.height)
	}

	if m.subMode == chatSubSearch {
		return m.viewSearch()
	}

	if m.selected >= 0 && m.selected < len(m.messages) {
		delete(m.changedMessages, m.messages[m.selected].ID)
	}

	m.aiStatus.on = m.b.AIOn(m.entry)

	var lines []string
	if m.privateMode && len(m.messages) > 0 {
		last := len(m.messages) - 1
		lines, _ = genericSelectedLines(m.b, m.messages[last:], m.entry, 0, m.changedMessages, m.replying, m.aiStatus, m.typing)
	} else {
		lines, _ = genericSelectedLines(m.b, m.messages, m.entry, m.selected, m.changedMessages, m.replying, m.aiStatus, m.typing)
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	switch m.subMode {
	case chatSubEdit:
		b.WriteString("Edit (Enter: save, Esc: cancel)> ")
		b.WriteString(m.editInput.View())
	case chatSubUploadPath:
		b.WriteString("File (Enter: continue, Esc: cancel)> ")
		b.WriteString(m.uploadInput.View())
	case chatSubUploadCaption:
		b.WriteString("Caption (optional, Enter: send, Esc: cancel)> ")
		b.WriteString(m.uploadInput.View())
	default:
		b.WriteString("you> ")
		b.WriteString(m.input.View())
	}
	b.WriteByte('\n')

	if m.notice != "" {
		b.WriteString(m.notice)
		b.WriteByte('\n')
	}

	b.WriteString(renderHelpBar(m.currentHelp(), m.width))
	return b.String()
}

func (m chatViewModel) viewSearch() string {
	lines := genericSearchResultLines(m.b, m.searchResults, m.searchSelected, m.entry, m.searchQuery)
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("search> ")
	b.WriteString(m.searchInput.View())
	b.WriteByte('\n')
	b.WriteString(renderHelpBar(chatSearchHelp, m.width))
	return b.String()
}

func (m chatViewModel) currentHelp() []helpKey {
	switch m.subMode {
	case chatSubEdit:
		return chatEditHelp
	case chatSubUploadPath, chatSubUploadCaption:
		return chatUploadHelp
	case chatSubConfirmChatDelete, chatSubConfirmMsgDelete:
		return chatConfirmHelp
	}
	return chatViewHelp
}

func (m chatViewModel) cleanup() {
	if m.cancel != nil {
		m.cancel()
	}
	m.b.Unsubscribe()
	if m.reads != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.reads.close(ctx)
	}
}

var _ error = forwardReq{}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

var chatViewHelp = []helpKey{
	{"↑↓", "messages"},
	{"⏎", "send"},
	{"o", "media"},
	{"^F", "search"},
	{"^U", "upload"},
	{"M-m", "emoji"},
	{"M-r", "reply"},
	{"M-e", "edit"},
	{"Esc", "back"},
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

var chatUploadHelp = []helpKey{
	{"⏎", "continue"},
	{"Esc", "cancel"},
}

var chatConfirmHelp = []helpKey{
	{"y", "confirm"},
	{"any", "cancel"},
}
