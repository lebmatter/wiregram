package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chzyer/readline"
	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

func (c *client) history(ctx context.Context, entry chat) ([]*tg.Message, error) {
	result, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: entry.peer, Limit: 30})
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	var messages []tg.MessageClass
	switch h := result.(type) {
	case *tg.MessagesMessages:
		c.remember(h.Users, h.Chats)
		messages = h.Messages
	case *tg.MessagesMessagesSlice:
		c.remember(h.Users, h.Chats)
		messages = h.Messages
	case *tg.MessagesChannelMessages:
		c.remember(h.Users, h.Chats)
		messages = h.Messages
	default:
		return nil, fmt.Errorf("Telegram returned unexpected message history")
	}
	resultMessages := make([]*tg.Message, 0, len(messages))
	for _, value := range messages {
		if m, ok := value.(*tg.Message); ok {
			resultMessages = append(resultMessages, m)
		}
	}
	sort.Slice(resultMessages, func(i, j int) bool { return resultMessages[i].ID < resultMessages[j].ID })
	return resultMessages, nil
}

func (c *client) messageSender(m *tg.Message, entry chat) string {
	sender := "you"
	if !m.Out {
		c.mu.Lock()
		peer := m.FromID
		if peer == nil {
			peer = m.PeerID
		}
		switch p := peer.(type) {
		case *tg.PeerUser:
			u := c.users[p.UserID]
			sender = userName(u)
			if u != nil && u.Self {
				sender = "you"
			}
		default:
			sender = c.chats[keyOf(peer)]
			if sender == "" {
				sender = entry.title
			}
		}
		c.mu.Unlock()
	}
	return plain(sender)
}

func messageText(m *tg.Message) string {
	text := formatMedia(m.Media)
	if text != "" && m.Message != "" {
		text += " — " + m.Message
	} else if text == "" {
		text = m.Message
	}
	return plain(text)
}

func (c *client) messageLine(m *tg.Message, entry chat, messages map[int]*tg.Message) string {
	text := messageText(m)
	if reply := c.replyLabel(m, entry, messages); reply != "" {
		text = reply + " — " + text
	}
	return c.messageSender(m, entry) + "> " + text
}

func (c *client) send(ctx context.Context, entry chat, text string, replyTo int) (*tg.Message, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("create message ID: %w", err)
	}
	request := &tg.MessagesSendMessageRequest{Peer: entry.peer, Message: text, RandomID: int64(binary.LittleEndian.Uint64(random[:]))}
	if replyTo > 0 {
		request.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	var result tg.UpdatesClass
	for {
		var err error
		result, err = c.api.MessagesSendMessage(ctx, request)
		if err == nil {
			break
		}
		if !networkError(err) {
			return nil, fmt.Errorf("send message: %w", err)
		}
		c.report(fmt.Errorf("Connection interrupted. Retrying message: %w", err))
		// Reuse the random ID so retrying cannot send a duplicate.
		if err := waitFor(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	message := &tg.Message{Out: true, Message: text}
	return c.sentMessage(result, entry, request.RandomID, replyTo, message), nil
}

func (c *client) sentMessage(
	result tg.UpdatesClass,
	entry chat,
	randomID int64,
	replyTo int,
	message *tg.Message,
) *tg.Message {
	var updates []tg.UpdateClass
	switch u := result.(type) {
	case *tg.UpdateShortSentMessage:
		message.ID = u.ID
	case *tg.Updates:
		c.remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdatesCombined:
		c.remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{u.Update}
	}
	for _, value := range updates {
		switch u := value.(type) {
		case *tg.UpdateMessageID:
			if u.RandomID == randomID {
				message.ID = u.ID
			}
		case *tg.UpdateNewMessage:
			if m, ok := u.Message.(*tg.Message); ok && keyOf(m.PeerID) == entry.key {
				message = m
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := u.Message.(*tg.Message); ok && keyOf(m.PeerID) == entry.key {
				message = m
			}
		}
	}
	resultMessage := *message
	resultMessage.Out = true
	if replyTo > 0 {
		if _, ok := resultMessage.GetReplyTo(); !ok {
			header := &tg.MessageReplyHeader{}
			header.SetReplyToMsgID(replyTo)
			resultMessage.SetReplyTo(header)
		}
	}
	return &resultMessage
}

type outgoingMessage struct {
	text    string
	path    string
	replyTo int
}

type sendResult struct {
	message *tg.Message
	err     error
}

func (c *client) view(ctx context.Context, t *terminal, entry chat, history *[]string) error {
	sessionCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.subscribe(entry.key)
	defer c.subscribe(peerKey{})
	if err := c.refreshReadState(ctx, entry); err != nil {
		t.report(err)
	}
	messages, err := c.history(ctx, entry)
	if err != nil {
		return err
	}
	selected := len(messages) - 1
	seen := make(map[int]bool)
	changedMessages := make(map[int]bool)
	for _, m := range messages {
		seen[m.ID] = true
	}
	e, err := t.newEditor("", false, *history)
	if err != nil {
		return err
	}
	defer e.close()
	t.mu.Lock()
	t.editor = e.line
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.editor = nil; t.mu.Unlock() }()
	lockCtx, cancelLock := context.WithCancel(ctx)
	var unreadMu sync.Mutex
	refreshingUnread := false
	refreshUnread := func() {
		unreadMu.Lock()
		if refreshingUnread {
			unreadMu.Unlock()
			return
		}
		refreshingUnread = true
		unreadMu.Unlock()
		go func() {
			defer func() {
				unreadMu.Lock()
				refreshingUnread = false
				unreadMu.Unlock()
			}()
			requestCtx, cancel := context.WithTimeout(lockCtx, 5*time.Second)
			defer cancel()
			unread, err := c.refreshChatUnread(requestCtx, entry)
			if err != nil {
				if lockCtx.Err() == nil {
					t.report(err)
				}
				return
			}
			t.updateLock(unread)
		}()
	}
	t.enableChatLock(c.autoLock, func() int { return c.cachedChatUnread(entry.key) }, refreshUnread)
	defer func() {
		cancelLock()
		t.disableInactivity()
	}()
	reads := newReadReporter(c, entry)
	defer func() {
		if err := reads.close(sessionCtx); err != nil && sessionCtx.Err() == nil {
			t.report(err)
		}
	}()
	readFailed := false
	var emojiEditor *editor
	var emojiQuery []rune
	var emojiChoices []emojiOption
	emojiSelected := 0
	emojiReactionMode := false
	var emojiTarget *tg.Message
	var reactionDone <-chan reactionResult
	var confirmDelete *bool
	var deleteDone <-chan error
	var editEditor *editor
	var editing *tg.Message
	var editResults <-chan lineResult
	var editDone <-chan error
	var editText string
	var uploadEditor *editor
	var uploadResults <-chan lineResult
	var selectedUploadPath string
	var replying *tg.Message
	finishEdit := func() {
		if editEditor != nil {
			editEditor.close()
			editEditor, editing, editResults = nil, nil, nil
			t.mu.Lock()
			t.editor = e.line
			t.mu.Unlock()
		}
	}
	finishUploadPrompt := func() {
		if uploadEditor != nil {
			uploadResults = nil
			uploadEditor.close()
			uploadEditor = nil
			selectedUploadPath = ""
			t.mu.Lock()
			t.editor = e.line
			t.mu.Unlock()
		}
	}
	defer func() {
		cancel()
		if editDone != nil {
			<-editDone
		}
		finishEdit()
		finishUploadPrompt()
	}()
	var mediaCancel context.CancelFunc
	var mediaDone <-chan error
	var mediaNotice error
	probe := time.NewTimer(time.Hour)
	probe.Stop()
	defer probe.Stop()
	probing := false
	var requested attachment
	player := ""
	render := func() {
		if t.isLocked() || mediaDone != nil {
			return
		}
		if emojiEditor != nil {
			hasRecent := len(emojiQuery) == 0 && len(c.recentEmoji()) > 0
			t.unlock(emojiPickerLines(emojiChoices, emojiSelected, hasRecent, emojiReactionMode))
			return
		}
		acknowledgeSelected(messages, selected, changedMessages)
		lines, readID := c.selectedLines(messages, entry, selected, changedMessages, replying)
		t.unlock(lines)
		reads.observe(readID)
	}
	finishEmoji := func(value string) error {
		if emojiEditor == nil {
			return nil
		}
		emojiEditor.close()
		emojiEditor = nil
		emojiQuery = nil
		emojiReactionMode = false
		emojiTarget = nil
		t.mu.Lock()
		t.editor = e.line
		t.mu.Unlock()
		if value != "" {
			c.rememberEmoji(value)
			if _, err := io.WriteString(e.input, value); err != nil {
				return fmt.Errorf("insert emoji: %w", err)
			}
		}
		return nil
	}
	defer func() { _ = finishEmoji("") }()
	startEmoji := func(reactionMode bool) {
		picker, err := t.newEditor("emoji> ", false, nil)
		if err != nil {
			t.report(err)
			return
		}
		emojiEditor = picker
		emojiQuery = nil
		emojiReactionMode = reactionMode
		if reactionMode {
			emojiTarget = messages[selected]
			emojiChoices = c.reactionChoices("")
		} else {
			emojiTarget = nil
			emojiChoices = c.emojiChoices("")
		}
		emojiSelected = 0
		t.mu.Lock()
		t.editor = picker.line
		t.mu.Unlock()
		render()
	}
	startUploadPrompt := func() {
		prompt, err := t.newEditorWithCompleter(
			"File (Tab: complete, Enter: continue, Esc: cancel)> ",
			false,
			nil,
			pathCompleter{},
		)
		if err != nil {
			t.report(err)
			return
		}
		uploadEditor, uploadResults = prompt, prompt.results
		t.mu.Lock()
		t.editor = prompt.line
		t.mu.Unlock()
		render()
	}
	stopMedia := func() {
		probing = false
		probe.Stop()
		if mediaCancel != nil {
			mediaCancel()
			err := <-mediaDone
			if err != nil && !errors.Is(err, context.Canceled) {
				mediaNotice = err
			}
			mediaDone = nil
			mediaCancel = nil
			t.clearMedia()
		}
	}
	defer stopMedia()
	startMedia := func() {
		probing = false
		probe.Stop()
		mediaCtx, stop := context.WithCancel(ctx)
		mediaCancel = stop
		done := make(chan error, 1)
		mediaDone = done
		t.mediaScreen("Media viewer")
		a := requested
		go func() { done <- c.playMedia(mediaCtx, t, a, player) }()
	}
	openSelected := func() {
		if selected < 0 || selected >= len(messages) || probing || mediaDone != nil {
			return
		}
		// Text-only messages leave Enter as a no-op when there is no draft.
		if formatMedia(messages[selected].Media) == "" {
			return
		}
		a, err := mediaAttachment(messages[selected])
		if err != nil {
			t.report(err)
			return
		}
		path, err := exec.LookPath("mpv")
		if err != nil {
			t.report(fmt.Errorf("install mpv with Kitty video output to open media"))
			return
		}
		requested, player = a, path
		if a.audio {
			startMedia()
			return
		}
		probing = true
		fmt.Fprint(t.out(), "\033_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\033\\")
		probe.Reset(time.Second)
	}
	render()
	sends := make(chan outgoingMessage)
	sent := make(chan sendResult, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case outgoing := <-sends:
				var message *tg.Message
				var err error
				if outgoing.path == "" {
					message, err = c.send(ctx, entry, outgoing.text, outgoing.replyTo)
				} else {
					message, err = c.sendUpload(
						ctx, entry, outgoing.path, outgoing.text, outgoing.replyTo,
					)
				}
				select {
				case sent <- sendResult{message, err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	var queue []outgoingMessage
	appendMessage := func(message *tg.Message) {
		if message.ID != 0 && seen[message.ID] {
			for i, previous := range messages {
				if previous.ID == message.ID && message.EditDate >= previous.EditDate {
					visibleEdit := message.EditDate > 0 && (message.Message != previous.Message || formatMedia(message.Media) != formatMedia(previous.Media))
					if visibleEdit && i != selected {
						changedMessages[message.ID] = true
					}
					messages[i] = message
					if replying != nil && replying.ID == message.ID {
						replying = message
					}
					render()
					break
				}
			}
			return
		}
		if message.ID != 0 {
			seen[message.ID] = true
		}
		follow := selected == len(messages)-1
		messages = append(messages, message)
		if follow {
			selected = len(messages) - 1
		}
		if len(messages) > 30 {
			delete(changedMessages, messages[0].ID)
			messages = messages[1:]
			selected = max(0, selected-1)
		}
		render()
	}
	for {
		var next chan outgoingMessage
		var outgoing outgoingMessage
		if len(queue) > 0 {
			next, outgoing = sends, queue[0]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reads.timer.C:
			reads.start(ctx)
		case err := <-reads.done:
			reads.complete()
			if err != nil && !readFailed {
				t.report(err)
			}
			readFailed = err != nil
		case result := <-editResults:
			if result.err != nil {
				finishEdit()
				render()
				continue
			}
			if strings.TrimSpace(result.text) == "" {
				t.report(fmt.Errorf("edited message cannot be empty; press Esc to cancel"))
				continue
			}
			editText = result.text
			editResults = nil
			editEditor.line.SetPrompt("Saving edit... ")
			editEditor.line.Refresh()
			done := make(chan error, 1)
			editDone = done
			message, text := editing, editText
			go func() { done <- c.editMessage(ctx, entry, message, text) }()
		case err := <-editDone:
			editDone = nil
			if err != nil {
				editEditor.line.SetPrompt("Edit (Enter: save, Esc: cancel)> ")
				editEditor.line.Operation.SetBuffer(editText)
				editResults = editEditor.results
				t.report(err)
			} else {
				// The update hook supplies the authoritative message when available.
				for i, previous := range messages {
					if previous.ID == editing.ID && previous.Message == editing.Message {
						copy := *previous
						copy.Message = editText
						if editText != editing.Message {
							copy.Entities = nil
						}
						messages[i] = &copy
						break
					}
				}
				delete(changedMessages, editing.ID)
				finishEdit()
			}
			render()
		case result := <-uploadResults:
			if result.err != nil {
				finishUploadPrompt()
				render()
				continue
			}
			if selectedUploadPath == "" {
				path, err := uploadPath(result.text)
				if err != nil {
					uploadEditor.line.Operation.SetBuffer(result.text)
					t.report(err)
					continue
				}
				uploadResults = nil
				uploadEditor.close()
				caption, err := t.newEditor("Caption (optional, Enter: send, Esc: cancel)> ", false, nil)
				if err != nil {
					uploadEditor = nil
					t.mu.Lock()
					t.editor = e.line
					t.mu.Unlock()
					t.report(err)
					render()
					continue
				}
				selectedUploadPath = path
				uploadEditor, uploadResults = caption, caption.results
				t.mu.Lock()
				t.editor = caption.line
				t.mu.Unlock()
				render()
				continue
			}
			path := selectedUploadPath
			caption := result.text
			finishUploadPrompt()
			replyTo := 0
			if replying != nil {
				replyTo = replying.ID
				replying = nil
			}
			queue = append(queue, outgoingMessage{text: caption, path: path, replyTo: replyTo})
			render()
		case err := <-deleteDone:
			deleteDone = nil
			if err != nil {
				t.report(err)
				render()
				continue
			}
			return nil
		case result := <-reactionDone:
			reactionDone = nil
			if result.err != nil {
				t.report(result.err)
				render()
				continue
			}
			for index, message := range messages {
				if message.ID == result.messageID {
					copy := *message
					copy.SetReactions(result.reactions)
					messages[index] = &copy
					delete(changedMessages, result.messageID)
					break
				}
			}
			c.rememberEmoji(result.emoticon)
			render()
		case next <- outgoing:
			queue = queue[1:]
		case result := <-sent:
			if result.err != nil {
				t.report(result.err)
			} else {
				appendMessage(result.message)
			}
		case <-c.changed:
			incoming, reactions := c.drain()
			for _, m := range incoming {
				appendMessage(m)
			}
			for _, update := range reactions {
				for index, message := range messages {
					if message.ID == update.messageID {
						if !visibleReactionChange(message, update.reactions) {
							break
						}
						copy := *message
						copy.SetReactions(update.reactions)
						messages[index] = &copy
						if index != selected {
							changedMessages[message.ID] = true
						}
						break
					}
				}
			}
			render()
			t.updateLock(c.cachedChatUnread(entry.key))
		case <-probe.C:
			if probing {
				probing = false
				t.report(fmt.Errorf("this terminal did not confirm Kitty graphics support"))
			}
		case err := <-mediaDone:
			mediaCancel()
			mediaCancel = nil
			mediaDone = nil
			t.clearMedia()
			render()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.report(err)
			}
		case key, ok := <-t.keys:
			if !ok {
				return context.Canceled
			}
			// Terminal replies are not keyboard activity and must never reach the editor.
			if strings.HasPrefix(key, "\033_") {
				if probing && strings.HasPrefix(key, "\033_Gi=31;") {
					if key == "\033_Gi=31;OK\033\\" {
						startMedia()
					} else {
						probing = false
						probe.Stop()
						t.report(fmt.Errorf("this terminal cannot display Kitty graphics"))
					}
				}
				continue
			}
			if key == terminalLockKey {
				stopMedia()
				continue
			}
			if key == terminalUnlockKey {
				render()
				if mediaNotice != nil {
					t.report(mediaNotice)
					mediaNotice = nil
				}
				continue
			}
			if key == "\x0c" {
				t.lockNow()
				continue
			}
			if mediaDone != nil || probing {
				switch key {
				case "\x1b", "\x03", "\x04":
					stopMedia()
					render()
					if mediaNotice != nil {
						t.report(mediaNotice)
						mediaNotice = nil
					}
				}
				continue
			}
			if editEditor != nil {
				if editDone != nil {
					continue
				}
				switch key {
				case "\x1b", "\x03", "\x04":
					finishEdit()
					render()
				case "\x1be", "\x1bE":
				default:
					if _, err := io.WriteString(editEditor.input, key); err != nil {
						return fmt.Errorf("read edit input: %w", err)
					}
				}
				continue
			}
			if uploadEditor != nil {
				switch key {
				case "\x1b", "\x03", "\x04":
					finishUploadPrompt()
					render()
				default:
					if _, err := io.WriteString(uploadEditor.input, key); err != nil {
						return fmt.Errorf("read upload input: %w", err)
					}
				}
				continue
			}
			if emojiEditor != nil {
				switch key {
				case "\x1b", "\x03", "\x04", "\x1bm", "\x1bM":
					if err := finishEmoji(""); err != nil {
						return err
					}
				case "\x1b[A", "\x1bOA":
					if emojiSelected > 0 {
						emojiSelected--
					}
				case "\x1b[B", "\x1bOB":
					if emojiSelected+1 < len(emojiChoices) {
						emojiSelected++
					}
				case "\r", "\n":
					if emojiSelected < len(emojiChoices) {
						value := emojiChoices[emojiSelected].value
						if emojiReactionMode {
							target := emojiTarget
							if err := finishEmoji(""); err != nil {
								return err
							}
							done := make(chan reactionResult, 1)
							reactionDone = done
							go func() {
								reactions, err := c.sendReaction(ctx, entry, target, value)
								done <- reactionResult{target.ID, value, reactions, err}
							}()
						} else if err := finishEmoji(value); err != nil {
							return err
						}
					}
				case "\t":
					if emojiReactionMode {
						emojiReactionMode = false
						emojiTarget = nil
					} else if selected >= 0 && selected < len(messages) && messages[selected].ID > 0 {
						emojiReactionMode = true
						emojiTarget = messages[selected]
					}
					emojiSelected = 0
				case "\x7f", "\x08":
					if len(emojiQuery) > 0 {
						emojiQuery = emojiQuery[:len(emojiQuery)-1]
					}
				case "\x15":
					emojiQuery = nil
				default:
					if !strings.HasPrefix(key, "\x1b") {
						emojiQuery = appendQuery(emojiQuery, key)
					}
				}
				if emojiEditor != nil {
					if emojiReactionMode {
						emojiChoices = c.reactionChoices(string(emojiQuery))
					} else {
						emojiChoices = c.emojiChoices(string(emojiQuery))
					}
					emojiSelected = min(emojiSelected, max(0, len(emojiChoices)-1))
					emojiEditor.line.Operation.SetBuffer(string(emojiQuery))
				}
				render()
				continue
			}
			if confirmDelete != nil {
				revoke := *confirmDelete
				confirmDelete = nil
				if key == "y" || key == "Y" {
					done := make(chan error, 1)
					deleteDone = done
					go func() { done <- c.deleteChat(ctx, entry, revoke) }()
				} else {
					render()
				}
				continue
			}
			if deleteDone != nil {
				continue
			}
			switch key {
			case "\x1b":
				if replying != nil {
					replying = nil
					render()
					continue
				}
				return nil
			case "\x03", "\x04":
				return nil
			case "\x15":
				startUploadPrompt()
				continue
			case "\x1bm", "\x1bM":
				if reactionDone != nil {
					t.report(fmt.Errorf("reaction update is still in progress"))
					continue
				}
				reactionMode := selected >= 0 && selected < len(messages) && messages[selected].ID > 0
				startEmoji(reactionMode)
				continue
			case "\x1br", "\x1bR":
				if selected < 0 || selected >= len(messages) || messages[selected].ID <= 0 {
					continue
				}
				if replying != nil && replying.ID == messages[selected].ID {
					replying = nil
				} else {
					replying = messages[selected]
				}
				render()
				continue
			case "\x1bd", "\x1bD", "\x1b\x04":
				revoke := key == "\x1b\x04"
				if entry.key.kind != 'u' {
					t.report(fmt.Errorf("chat deletion is currently supported only for private chats"))
					continue
				}
				if _, self := entry.peer.(*tg.InputPeerSelf); self && revoke {
					t.report(fmt.Errorf("Saved Messages cannot be deleted for both sides"))
					continue
				}
				confirmDelete = &revoke
				scope := "only for you"
				if revoke {
					scope = "for both sides"
				}
				t.report(fmt.Errorf("Delete chat with %s %s? Press y to confirm; any other key cancels.", plain(entry.title), scope))
				continue
			case "\x1be", "\x1bE":
				if selected < 0 || selected >= len(messages) {
					continue
				}
				message := messages[selected]
				if err := editableMessage(message); err != nil {
					t.report(err)
					continue
				}
				editEditor, err = t.newEditor("Edit (Enter: save, Esc: cancel)> ", false, nil, message.Message)
				if err != nil {
					t.report(err)
					continue
				}
				editing, editResults = message, editEditor.results
				t.mu.Lock()
				t.editor = editEditor.line
				t.mu.Unlock()
				render()
				continue
			case "\x1b[A", "\x1bOA":
				if selected > 0 {
					selected--
				}
				render()
				continue
			case "\x1b[B", "\x1bOB":
				if selected+1 < len(messages) {
					selected++
				}
				render()
				continue
			case "\x1b[1;3A", "\x1b\x1b[A", "\x1b\x1bOA":
				key = "\x1b[A"
			case "\x1b[1;3B", "\x1b\x1b[B", "\x1b\x1bOB":
				key = "\x1b[B"
			}
			if _, err := io.WriteString(e.input, key); err != nil {
				return fmt.Errorf("read message input: %w", err)
			}
		case result := <-e.results:
			if errors.Is(result.err, io.EOF) || errors.Is(result.err, readline.ErrInterrupt) {
				return nil
			}
			if result.err != nil {
				return fmt.Errorf("read message: %w", result.err)
			}
			if strings.TrimSpace(result.text) == "" {
				if !t.isLocked() {
					openSelected()
				}
				continue
			}
			*history = append(*history, result.text)
			if len(*history) > 500 {
				*history = (*history)[len(*history)-500:]
			}
			replyTo := 0
			if replying != nil {
				replyTo = replying.ID
				replying = nil
				render()
			}
			queue = append(queue, outgoingMessage{text: result.text, replyTo: replyTo})
		}
	}
}

func acknowledgeSelected(messages []*tg.Message, selected int, changed map[int]bool) {
	if selected >= 0 && selected < len(messages) {
		delete(changed, messages[selected].ID)
	}
}

func (c *client) selectedLines(messages []*tg.Message, entry chat, selected int, changed map[int]bool, replying *tg.Message) ([]string, int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 4 || rows < 4 {
		cols, rows = 80, 24
	}
	reserved := 2
	if replying != nil {
		reserved++
	}
	count := min(len(messages), max(0, rows-reserved))
	start := max(0, selected-count+1)
	if start+count > len(messages) {
		start = max(0, len(messages)-count)
	}
	lines := make([]string, 0, count+1)
	byID := make(map[int]*tg.Message, len(messages))
	for _, message := range messages {
		byID[message.ID] = message
	}
	readID := 0
	for i := start; i < start+count; i++ {
		marker := "  "
		if changed[messages[i].ID] {
			marker = "* "
		}
		if i == selected {
			marker = "> " + c.readMarker(entry, messages[i])
			if reactions := formatReactions(messages[i]); reactions != "" {
				marker += "[" + reactions + "] "
			}
		}
		lines = append(lines, clipLine(marker+c.messageLine(messages[i], entry, byID), cols-1))
		if !messages[i].Out {
			readID = max(readID, messages[i].ID)
		}
	}
	if replying != nil {
		lines = append(lines, clipLine("replying to "+c.replySummary(replying, entry), cols-1))
	}
	return lines, readID
}

func clipLine(text string, width int) string {
	var out strings.Builder
	used := 0
	for _, r := range text {
		cells := 1
		if unicode.Is(unicode.Mn, r) || r == 0x200d {
			cells = 0
		} else if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0x1f000) {
			cells = 2
		}
		if used+cells > width {
			break
		}
		out.WriteRune(r)
		used += cells
	}
	return out.String()
}
