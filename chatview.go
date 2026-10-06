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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chzyer/readline"
	"github.com/gotd/td/tg"
)

// viewChat is the generic chat view that works with any backend.
// It replaces the old (c *client).view() method.
func viewChat(ctx context.Context, b backend, t *terminal, entry chatEntry, history *[]string) error {
	sessionCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.Subscribe(entry)
	defer b.Unsubscribe()
	if err := b.RefreshReadState(ctx, entry); err != nil {
		t.report(err)
	}
	messages, err := b.History(ctx, entry)
	if err != nil {
		return err
	}
	selected := len(messages) - 1
	seen := make(map[string]bool)
	changedMessages := make(map[string]bool)
	for _, m := range messages {
		seen[m.ID] = true
	}
	e, err := t.newEditor("you> ", false, *history)
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
			unread, err := b.RefreshChatUnread(requestCtx, entry)
			if err != nil {
				if lockCtx.Err() == nil {
					t.report(err)
				}
				return
			}
			t.updateLock(unread)
		}()
	}
	t.enableChatLock(b.AutoLockEnabled(), func() int { return b.CachedUnread(entry) }, refreshUnread)
	defer func() {
		cancelLock()
		t.disableInactivity()
	}()
	reads := newGenericReadReporter(b, entry)
	defer func() {
		if err := reads.close(sessionCtx); err != nil && sessionCtx.Err() == nil {
			t.report(err)
		}
	}()
	caps := b.Capabilities()
	readFailed := false
	privateMode := false
	var historyDone chan []message
	loadingHistory := false
	historyExhausted := false
	var emojiEditor *editor
	var emojiQuery []rune
	var emojiChoices []emojiOption
	emojiSelected := 0
	emojiReactionMode := false
	var emojiTarget *message
	type genericReactionResult struct {
		messageID string
		emoticon  string
		reactions []messageReaction
		err       error
	}
	var reactionDone <-chan genericReactionResult
	var searchEditor *editor
	var searchQuery []rune
	var searchResults []message
	searchSelected := 0
	type genericSearchResult struct {
		messages []message
		err      error
	}
	var searchDone <-chan genericSearchResult
	searchTimer := time.NewTimer(time.Hour)
	searchTimer.Stop()
	defer searchTimer.Stop()
	finishSearch := func() {
		if searchEditor != nil {
			searchEditor.close()
			searchEditor = nil
			searchQuery = nil
			searchResults = nil
			searchDone = nil
			searchTimer.Stop()
			t.mu.Lock()
			t.editor = e.line
			t.mu.Unlock()
		}
	}
	defer finishSearch()
	var confirmDelete *bool
	var deleteDone <-chan error
	var confirmMsgDelete *bool
	var msgDeleteDone <-chan error
	var editEditor *editor
	var editing *message
	var editResults <-chan lineResult
	var editDone <-chan error
	var editText string
	var uploadEditor *editor
	var uploadResults <-chan lineResult
	var selectedUploadPath string
	var replying *message
	typingTimeout := time.NewTimer(time.Hour)
	typingTimeout.Stop()
	defer typingTimeout.Stop()
	var lastTypingSent time.Time
	var saveDone <-chan error
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
	var mediaMessage *message
	player := ""
	render := func() {
		if t.isLocked() || mediaDone != nil {
			return
		}
		if emojiEditor != nil {
			hasRecent := len(emojiQuery) == 0 && len(b.RecentEmoji()) > 0
			t.unlock(emojiPickerLines(emojiChoices, emojiSelected, hasRecent, emojiReactionMode))
			return
		}
		if searchEditor != nil {
			cols, rows := termSize()
			t.unlock(genericSearchResultLines(b, searchResults, searchSelected, entry, string(searchQuery), cols-1, rows-2))
			return
		}
		if selected >= 0 && selected < len(messages) {
			delete(changedMessages, messages[selected].ID)
		}
		var lines []string
		var readID string
		cols, rows := termSize()
		if privateMode && len(messages) > 0 {
			last := len(messages) - 1
			lines, readID = genericSelectedLines(b, messages[last:], entry, 0, changedMessages, replying, b.IsTyping(), cols-1, rows-2)
		} else {
			lines, readID = genericSelectedLines(b, messages, entry, selected, changedMessages, replying, b.IsTyping(), cols-1, rows-2)
		}
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
			b.RememberEmoji(value)
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
			target := messages[selected]
			emojiTarget = &target
			emojiChoices = b.ReactionChoices("")
		} else {
			emojiTarget = nil
			emojiChoices = b.EmojiChoices("")
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
			mediaMessage = nil
			t.clearMedia()
		}
	}
	defer stopMedia()
	startMedia := func(msg message) {
		probing = false
		probe.Stop()
		mediaCtx, stop := context.WithCancel(ctx)
		mediaCancel = stop
		done := make(chan error, 1)
		mediaDone = done
		t.mediaScreen("Media viewer")
		go func() { done <- b.PlayMedia(mediaCtx, t, msg, player) }()
	}
	openSelected := func() {
		if selected < 0 || selected >= len(messages) || probing || mediaDone != nil {
			return
		}
		msg := messages[selected]
		if !b.MediaPlayable(msg) {
			return
		}
		path, err := exec.LookPath("mpv")
		if err != nil {
			t.report(fmt.Errorf("install mpv with Kitty video output to open media"))
			return
		}
		player = path
		mediaMessage = &msg
		// Check if audio-only for direct play vs graphics probe
		// The backend handles the details in PlayMedia
		probing = true
		fmt.Fprint(t.out(), "\033_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\033\\")
		probe.Reset(time.Second)
	}
	render()
	type genericOutgoing struct {
		text    string
		path    string
		replyTo string
	}
	type genericSendResult struct {
		message message
		err     error
	}
	sends := make(chan genericOutgoing)
	sent := make(chan genericSendResult, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case outgoing := <-sends:
				var msg message
				var err error
				if outgoing.path == "" {
					msg, err = b.Send(ctx, entry, outgoing.text, outgoing.replyTo)
				} else {
					msg, err = b.SendUpload(ctx, entry, outgoing.path, outgoing.text, outgoing.replyTo)
				}
				select {
				case sent <- genericSendResult{msg, err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	var queue []genericOutgoing
	appendMessage := func(msg message) {
		if msg.ID != "" && msg.ID != "0" && seen[msg.ID] {
			for i, previous := range messages {
				if previous.ID == msg.ID && msg.EditDate >= previous.EditDate {
					visibleEdit := msg.EditDate > 0 && (msg.Text != previous.Text || msg.MediaLabel != previous.MediaLabel)
					if visibleEdit && i != selected {
						changedMessages[msg.ID] = true
					}
					messages[i] = msg
					if replying != nil && replying.ID == msg.ID {
						replying = &msg
					}
					render()
					break
				}
			}
			return
		}
		if msg.ID != "" && msg.ID != "0" {
			seen[msg.ID] = true
		}
		follow := selected == len(messages)-1
		messages = append(messages, msg)
		if follow {
			selected = len(messages) - 1
		}
		if len(messages) > 500 {
			delete(changedMessages, messages[0].ID)
			messages = messages[1:]
			selected = max(0, selected-1)
		}
		render()
	}
	for {
		var next chan genericOutgoing
		var outgoing genericOutgoing
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
		case <-searchTimer.C:
			query := strings.TrimSpace(string(searchQuery))
			if query == "" || searchEditor == nil {
				continue
			}
			done := make(chan genericSearchResult, 1)
			searchDone = done
			go func() {
				msgs, err := b.SearchMessages(ctx, entry, query)
				done <- genericSearchResult{msgs, err}
			}()
		case result := <-searchDone:
			searchDone = nil
			if result.err != nil {
				t.report(result.err)
			} else {
				searchResults = result.messages
				searchSelected = 0
			}
			render()
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
			msg := *editing
			text := editText
			go func() { done <- b.Edit(ctx, entry, msg, text) }()
		case err := <-editDone:
			editDone = nil
			if err != nil {
				editEditor.line.SetPrompt("Edit (Enter: save, Esc: cancel)> ")
				editEditor.line.Operation.SetBuffer(editText)
				editResults = editEditor.results
				t.report(err)
			} else {
				for i, previous := range messages {
					if previous.ID == editing.ID && previous.Text == editing.Text {
						copy := previous
						copy.Text = editText
						messages[i] = copy
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
			replyTo := ""
			if replying != nil {
				replyTo = replying.ID
				replying = nil
			}
			queue = append(queue, genericOutgoing{text: caption, path: path, replyTo: replyTo})
			render()
		case err := <-deleteDone:
			deleteDone = nil
			if err != nil {
				t.report(err)
				render()
				continue
			}
			return nil
		case err := <-msgDeleteDone:
			msgDeleteDone = nil
			if err != nil {
				t.report(err)
			}
			render()
		case err := <-saveDone:
			saveDone = nil
			if err != nil {
				t.report(err)
			}
		case result := <-reactionDone:
			reactionDone = nil
			if result.err != nil {
				t.report(result.err)
				render()
				continue
			}
			for index, msg := range messages {
				if msg.ID == result.messageID {
					copy := msg
					copy.Reactions = result.reactions
					messages[index] = copy
					delete(changedMessages, result.messageID)
					break
				}
			}
			b.RememberEmoji(result.emoticon)
			render()
		case next <- outgoing:
			queue = queue[1:]
		case result := <-sent:
			if result.err != nil {
				t.report(result.err)
			} else {
				appendMessage(result.message)
			}
		case <-typingTimeout.C:
			b.ClearTyping()
			render()
		case <-b.Changed():
			incoming, reactions, deletions := b.Drain()
			for _, m := range incoming {
				if !m.Outgoing {
					b.ClearTyping()
					typingTimeout.Stop()
				}
				appendMessage(m)
			}
			if b.IsTyping() {
				typingTimeout.Reset(6 * time.Second)
			}
			for _, update := range reactions {
				for index, msg := range messages {
					if msg.ID == update.MessageID {
						oldReactions := formatMessageReactions(msg.Reactions)
						newReactions := formatMessageReactions(update.Reactions)
						if oldReactions == newReactions {
							break
						}
						copy := msg
						copy.Reactions = update.Reactions
						messages[index] = copy
						if index != selected {
							changedMessages[msg.ID] = true
						}
						break
					}
				}
			}
			if len(deletions) > 0 {
				removed := make(map[string]bool, len(deletions))
				for _, id := range deletions {
					removed[id] = true
					delete(seen, id)
					delete(changedMessages, id)
				}
				filtered := messages[:0]
				for _, m := range messages {
					if !removed[m.ID] {
						filtered = append(filtered, m)
					}
				}
				messages = filtered
				if selected >= len(messages) {
					selected = max(0, len(messages)-1)
				}
			}
			render()
			t.updateLock(b.CachedUnread(entry))
		case <-probe.C:
			if probing {
				probing = false
				t.report(fmt.Errorf("this terminal did not confirm Kitty graphics support"))
			}
		case err := <-mediaDone:
			mediaCancel()
			mediaCancel = nil
			mediaDone = nil
			msg := mediaMessage
			mediaMessage = nil
			t.clearMedia()
			render()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.report(err)
			} else if msg != nil {
				b.AcknowledgeMedia(ctx, *msg)
			}
		case older := <-historyDone:
			loadingHistory = false
			historyDone = nil
			if len(older) == 0 {
				historyExhausted = true
			} else {
				var fresh []message
				for _, m := range older {
					if !seen[m.ID] {
						seen[m.ID] = true
						fresh = append(fresh, m)
					}
				}
				if len(fresh) > 0 {
					messages = append(fresh, messages...)
					selected += len(fresh)
				} else {
					historyExhausted = true
				}
			}
			render()
		case key, ok := <-t.keys:
			if !ok {
				return context.Canceled
			}
			if strings.HasPrefix(key, "\033_") {
				if probing && strings.HasPrefix(key, "\033_Gi=31;") {
					if key == "\033_Gi=31;OK\033\\" {
						if mediaMessage != nil {
							startMedia(*mediaMessage)
						}
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
						if emojiReactionMode && emojiTarget != nil {
							target := *emojiTarget
							if err := finishEmoji(""); err != nil {
								return err
							}
							done := make(chan genericReactionResult, 1)
							reactionDone = done
							go func() {
								reactions, err := b.React(ctx, entry, target, value)
								done <- genericReactionResult{target.ID, value, reactions, err}
							}()
						} else if err := finishEmoji(value); err != nil {
							return err
						}
					}
				case "\t":
					if emojiReactionMode {
						emojiReactionMode = false
						emojiTarget = nil
					} else if selected >= 0 && selected < len(messages) && messages[selected].ID != "" && messages[selected].ID != "0" {
						emojiReactionMode = true
						target := messages[selected]
						emojiTarget = &target
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
						emojiChoices = b.ReactionChoices(string(emojiQuery))
					} else {
						emojiChoices = b.EmojiChoices(string(emojiQuery))
					}
					emojiSelected = min(emojiSelected, max(0, len(emojiChoices)-1))
					emojiEditor.line.Operation.SetBuffer(string(emojiQuery))
				}
				render()
				continue
			}
			if searchEditor != nil {
				switch key {
				case "\x1b", "\x03", "\x04":
					finishSearch()
					render()
					continue
				case "\x1b[A", "\x1bOA":
					if searchSelected > 0 {
						searchSelected--
					}
				case "\x1b[B", "\x1bOB":
					if searchSelected+1 < len(searchResults) {
						searchSelected++
					}
				case "\r", "\n":
					finishSearch()
					render()
					continue
				case "\x7f", "\x08":
					if len(searchQuery) > 0 {
						searchQuery = searchQuery[:len(searchQuery)-1]
						searchTimer.Reset(400 * time.Millisecond)
					}
				case "\x15":
					searchQuery = nil
					searchResults = nil
					searchSelected = 0
					searchTimer.Stop()
				default:
					if !strings.HasPrefix(key, "\x1b") {
						searchQuery = appendQuery(searchQuery, key)
						searchTimer.Reset(400 * time.Millisecond)
					}
				}
				if searchEditor != nil {
					searchEditor.line.Operation.SetBuffer(string(searchQuery))
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
					go func() { done <- b.DeleteChat(ctx, entry, revoke) }()
				} else {
					render()
				}
				continue
			}
			if deleteDone != nil {
				continue
			}
			if confirmMsgDelete != nil {
				revoke := *confirmMsgDelete
				idx := selected
				confirmMsgDelete = nil
				if (key == "y" || key == "Y") && idx >= 0 && idx < len(messages) && messages[idx].ID != "" && messages[idx].ID != "0" {
					id := messages[idx].ID
					delete(seen, id)
					delete(changedMessages, id)
					messages = append(messages[:idx], messages[idx+1:]...)
					if selected >= len(messages) {
						selected = max(0, len(messages)-1)
					}
					done := make(chan error, 1)
					msgDeleteDone = done
					go func() { done <- b.DeleteMessages(ctx, entry, []string{id}, revoke) }()
				}
				render()
				continue
			}
			if msgDeleteDone != nil {
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
			case "\x06":
				if searchEditor != nil || !caps.Search {
					continue
				}
				picker, err := t.newEditor("search> ", false, nil)
				if err != nil {
					t.report(err)
					continue
				}
				searchEditor = picker
				searchQuery = nil
				searchResults = nil
				searchSelected = 0
				t.mu.Lock()
				t.editor = searchEditor.line
				t.mu.Unlock()
				render()
				continue
			case "\x15":
				if !caps.Upload {
					continue
				}
				startUploadPrompt()
				continue
			case "\x1bp", "\x1bP":
				if !caps.Pin || selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				msg := messages[selected]
				unpin := msg.Pinned
				done := make(chan error, 1)
				go func() { done <- b.Pin(ctx, entry, msg, unpin) }()
				if err := <-done; err != nil {
					t.report(fmt.Errorf("pin message: %w", err))
				} else {
					copy := msg
					copy.Pinned = !unpin
					messages[selected] = copy
					if unpin {
						t.report(fmt.Errorf("Message unpinned"))
					} else {
						t.report(fmt.Errorf("Message pinned"))
					}
				}
				render()
				continue
			case "\x1bm", "\x1bM":
				if reactionDone != nil {
					t.report(fmt.Errorf("reaction update is still in progress"))
					continue
				}
				reactionMode := caps.Reactions && selected >= 0 && selected < len(messages) && messages[selected].ID != "" && messages[selected].ID != "0"
				startEmoji(reactionMode)
				continue
			case "\x1bf", "\x1bF":
				if !caps.Forward || selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				return forwardReq{from: entry, id: messages[selected].ID}
			case "\x1bs", "\x1bS":
				if saveDone != nil {
					t.report(fmt.Errorf("a download is already in progress"))
					continue
				}
				if selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				filename, ok := b.MediaSaveable(messages[selected])
				if !ok {
					t.report(fmt.Errorf("this message has no downloadable attachment"))
					continue
				}
				home, err := os.UserHomeDir()
				if err != nil {
					t.report(fmt.Errorf("locate home directory: %w", err))
					continue
				}
				dir := filepath.Join(home, "Downloads")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.report(fmt.Errorf("create Downloads directory: %w", err))
					continue
				}
				done := make(chan error, 1)
				msg := messages[selected]
				go func() {
					err := b.SaveMedia(ctx, msg, dir)
					if err == nil {
						t.report(fmt.Errorf("saved %s", filename))
					}
					done <- err
				}()
				saveDone = done
				t.report(fmt.Errorf("saving %s to ~/Downloads", filename))
				continue
			case "\x1br", "\x1bR":
				if selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				if replying != nil && replying.ID == messages[selected].ID {
					replying = nil
				} else {
					msg := messages[selected]
					replying = &msg
				}
				render()
				continue
			case "\x1bd", "\x1bD", "\x1b\x04":
				if !caps.DeleteChat {
					continue
				}
				revoke := key == "\x1b\x04"
				if !entry.IsDM {
					t.report(fmt.Errorf("chat deletion is currently supported only for private chats"))
					continue
				}
				if entry.IsSelf && revoke {
					t.report(fmt.Errorf("Saved Messages cannot be deleted for both sides"))
					continue
				}
				confirmDelete = &revoke
				scope := "only for you"
				if revoke {
					scope = "for both sides"
				}
				t.report(fmt.Errorf("Delete chat with %s %s? Press y to confirm; any other key cancels.", plain(entry.Title), scope))
				continue
			case "\x1b[3~", "\x1b[3;2~":
				if msgDeleteDone != nil || selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				revoke := key == "\x1b[3;2~"
				if !caps.DeleteForMe && !revoke {
					revoke = true
				}
				confirmMsgDelete = &revoke
				scope := "only for you"
				if revoke {
					scope = "for both sides"
				}
				t.report(fmt.Errorf("Delete this message %s? Press y to confirm; any other key cancels.", scope))
				continue
			case "\x1be", "\x1bE":
				if !caps.Edit || selected < 0 || selected >= len(messages) {
					continue
				}
				msg := messages[selected]
				if err := b.Editable(msg); err != nil {
					t.report(err)
					continue
				}
				editEditor, err = t.newEditor("Edit (Enter: save, Esc: cancel)> ", false, nil, msg.Text)
				if err != nil {
					t.report(err)
					continue
				}
				editing = &msg
				editResults = editEditor.results
				t.mu.Lock()
				t.editor = editEditor.line
				t.mu.Unlock()
				render()
				continue
			case "\x1bh", "\x1bH":
				privateMode = !privateMode
				if privateMode {
					selected = len(messages) - 1
				}
				render()
				continue
			case "\x1b[A", "\x1bOA":
				if privateMode {
					privateMode = false
				}
				if selected > 0 {
					selected--
				} else if !loadingHistory && !historyExhausted && len(messages) > 0 {
					loadingHistory = true
					ch := make(chan []message, 1)
					historyDone = ch
					offsetID := messages[0].ID
					go func() {
						older, err := b.OlderHistory(ctx, entry, offsetID)
						if err != nil {
							if ctx.Err() == nil {
								t.report(err)
							}
							ch <- nil
							return
						}
						ch <- older
					}()
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
			if caps.Typing && time.Since(lastTypingSent) > 5*time.Second {
				lastTypingSent = time.Now()
				b.SetTyping(ctx, entry)
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
			replyTo := ""
			if replying != nil {
				replyTo = replying.ID
				replying = nil
				render()
			}
			queue = append(queue, genericOutgoing{text: result.text, replyTo: replyTo})
		}
	}
}

// Generic read reporter for any backend.
type genericReadReporter struct {
	b      backend
	entry  chatEntry
	target string
	timer  *time.Timer
	done   <-chan error
	cancel context.CancelFunc
}

func newGenericReadReporter(b backend, entry chatEntry) *genericReadReporter {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &genericReadReporter{b: b, entry: entry, target: b.InboxPosition(entry), timer: timer}
}

func (r *genericReadReporter) observe(id string) {
	if id != "" && id > r.target {
		r.target = id
		r.timer.Reset(3 * time.Second)
	}
}

func (r *genericReadReporter) start(ctx context.Context) {
	if r.done != nil {
		return
	}
	current := r.b.InboxPosition(r.entry)
	if r.target == "" || r.target <= current {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	done := make(chan error, 1)
	r.done = done
	id := r.target
	go func() { done <- r.b.MarkRead(ctx, r.entry, id) }()
}

func (r *genericReadReporter) complete() {
	r.cancel()
	r.done, r.cancel = nil, nil
	current := r.b.InboxPosition(r.entry)
	if r.target != "" && r.target > current {
		r.timer.Reset(3 * time.Second)
	}
}

func (r *genericReadReporter) close(ctx context.Context) error {
	r.timer.Stop()
	if r.done != nil {
		r.cancel()
		<-r.done
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return r.b.MarkRead(ctx, r.entry, r.target)
}

// Generic search result lines.
func genericSearchResultLines(b backend, results []message, selected int, entry chatEntry, query string, width, height int) []string {
	width = min(width, maxChatLineWidth)
	available := max(1, height-2)
	var lines []string
	heading := fmt.Sprintf("Search: %s (%d results)", plain(query), len(results))
	lines = append(lines, clipLine(heading, width))
	lines = append(lines, "")
	if len(results) == 0 {
		if query != "" {
			lines = append(lines, "  No matching messages")
		}
	} else {
		start := max(0, selected-available+1)
		for i := start; i < min(len(results), start+available); i++ {
			marker := "  "
			if i == selected {
				marker = styleAccent.Render("> ")
			}
			text := genericMessageLine(results[i], entry, nil)
			lines = append(lines, clipLine(marker+text, width))
		}
	}
	return lines
}

// Generic selected lines rendering.
func genericSelectedLines(b backend, messages []message, entry chatEntry, selected int, changed map[string]bool, replying *message, typing bool, width, height int) ([]string, string) {
	lineWidth := min(width, maxChatLineWidth)
	reserved := 0
	if replying != nil {
		reserved++
	}
	if typing {
		reserved++
	}
	available := max(1, height-reserved)
	byID := make(map[string]*message, len(messages))
	replies := make(map[string]int)
	for i := range messages {
		byID[messages[i].ID] = &messages[i]
		if messages[i].ReplyToID != "" {
			replies[messages[i].ReplyToID]++
		}
	}
	const msgPrefixWidth = 7 + 2 + nickWidth + 2
	messageLines := func(i int) []string {
		msg := messages[i]
		marker := "  "
		if changed[msg.ID] {
			marker = "* "
		}
		if i == selected {
			marker = styleAccent.Render(">") + " " + b.ReadMarker(entry, msg)
			if reactions := formatMessageReactions(msg.Reactions); reactions != "" {
				marker += "[" + reactions + "] "
			}
		}
		indent := displayWidth(marker) + msgPrefixWidth
		body := genericMessageText(msg)
		if msg.Pinned {
			body += " [pinned]"
		}
		if n := replies[msg.ID]; n > 0 {
			body += styleSubtle.Render(fmt.Sprintf(" · %d %s", n, plural(n, "reply", "replies")))
		}
		prefix := marker + genericMessagePrefix(msg, entry) + " "
		if msg.ReplyToID == "" {
			return wrapMessage(prefix, body, lineWidth, indent)
		}
		// Replies put the quoted original on the header row and the text below it.
		quote := clipLine(prefix+renderQuote(byID[msg.ReplyToID], entry), lineWidth)
		return append([]string{quote},
			wrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		lines := []string{}
		if replying != nil {
			lines = append(lines, clipLine("replying to "+genericReplySummary(*replying, entry), lineWidth))
		}
		return lines, ""
	}
	blocks := make(map[int][]string)
	block := func(i int) []string {
		if lines := blocks[i]; lines != nil {
			return lines
		}
		lines := messageLines(i)
		blocks[i] = lines
		return lines
	}
	selectedBlock := block(selected)
	if len(selectedBlock) > available {
		selectedBlock = selectedBlock[:available]
	}
	start, end, used := selected, selected+1, len(selectedBlock)
	for start > 0 {
		previous := block(start - 1)
		if used+len(previous) > available {
			break
		}
		start--
		used += len(previous)
	}
	for end < len(messages) {
		next := block(end)
		if used+len(next) > available {
			break
		}
		end++
		used += len(next)
	}
	lines := make([]string, 0, used+1)
	readID := ""
	for i := start; i < end; i++ {
		visible := block(i)
		if i == selected && len(visible) > available {
			visible = selectedBlock
		}
		lines = append(lines, visible...)
		if !messages[i].Outgoing && messages[i].ID > readID {
			readID = messages[i].ID
		}
	}
	if replying != nil {
		lines = append(lines, clipLine("replying to "+genericReplySummary(*replying, entry), lineWidth))
	}
	if typing {
		lines = append(lines, clipLine(plain(entry.Title)+" is typing...", lineWidth))
	}
	return lines, readID
}

func (c *client) history(ctx context.Context, entry chat) ([]*tg.Message, error) {
	return c.fetchHistory(ctx, entry, 0)
}

func (c *client) olderHistory(ctx context.Context, entry chat, offsetID int) ([]*tg.Message, error) {
	return c.fetchHistory(ctx, entry, offsetID)
}

func (c *client) fetchHistory(ctx context.Context, entry chat, offsetID int) ([]*tg.Message, error) {
	result, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     entry.peer,
		Limit:    30,
		OffsetID: offsetID,
	})
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

func messageText(m *tg.Message) string {
	text := formatMedia(m.Media)
	if text != "" && m.Message != "" {
		text += " — " + m.Message
	} else if text == "" {
		text = m.Message
	}
	return plain(text)
}

var nickColors = [...]string{
	"\033[31m", "\033[32m", "\033[33m", "\033[34m", "\033[35m", "\033[36m",
	"\033[91m", "\033[92m", "\033[93m", "\033[94m", "\033[95m", "\033[96m",
}

func nickColor(name string) string {
	h := uint32(0)
	for _, r := range name {
		h = h*31 + uint32(r)
	}
	return nickColors[h%uint32(len(nickColors))]
}

const nickWidth = 12

func formatNick(name string) string {
	runes := []rune(name)
	if len(runes) > nickWidth {
		runes = runes[:nickWidth]
	}
	padded := string(runes)
	for len([]rune(padded)) < nickWidth {
		padded += " "
	}
	return padded
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
	message := &tg.Message{Out: true, Message: text, Date: int(time.Now().Unix())}
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
		message.Date = u.Date
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

const maxChatLineWidth = 120

func wrapLine(text string, width int) []string {
	return wrapLineIndent(text, width, 0)
}

// wrapMessage wraps body under a fixed-width prefix. Wrapping the prefix too
// would collapse the padding that keeps the nick column aligned.
func wrapMessage(prefix, body string, width, indent int) []string {
	pad := strings.Repeat(" ", indent)
	lines := wrapLineIndent(pad+body, width, indent)
	if indent < width-4 && strings.HasPrefix(lines[0], pad) {
		lines[0] = prefix + lines[0][indent:]
	} else {
		lines[0] = clipLine(prefix+strings.TrimLeft(lines[0], " "), width)
	}
	return lines
}

func wrapLineIndent(text string, width int, indent int) []string {
	if width < 1 {
		return []string{""}
	}
	continuation := "  "
	if indent > 0 && indent < width-4 {
		continuation = strings.Repeat(" ", indent)
	} else if width < 4 {
		continuation = " "
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	leading := text[:len(text)-len(strings.TrimLeft(text, " \t\r\n"))]
	var lines []string
	line := leading
	used := displayWidth(leading)
	hasWord := false
	flush := func() {
		lines = append(lines, line)
		line = continuation
		used = displayWidth(continuation)
		hasWord = false
	}
	for _, word := range words {
		wordWidth := displayWidth(word)
		separator := 0
		if hasWord {
			separator = 1
		}
		if used+separator+wordWidth <= width {
			if separator != 0 {
				line += " "
				used++
			}
			line += word
			used += wordWidth
			hasWord = true
			continue
		}
		if hasWord {
			flush()
		}
		for word != "" {
			part, rest := takeCells(word, width-used)
			line += part
			used += displayWidth(part)
			hasWord = true
			word = rest
			if word != "" {
				flush()
			}
		}
	}
	if hasWord {
		lines = append(lines, line)
	}
	return lines
}

func displayWidth(text string) int {
	width := 0
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		width += runeWidth(r)
	}
	return width
}

func takeCells(text string, limit int) (string, string) {
	if limit < 1 {
		limit = 1
	}
	used, end := 0, 0
	inEscape := false
	for index, r := range text {
		if r == '\033' {
			inEscape = true
			end = index + len(string(r))
			continue
		}
		if inEscape {
			end = index + len(string(r))
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		cells := runeWidth(r)
		if used > 0 && used+cells > limit {
			break
		}
		used += cells
		end = index + len(string(r))
	}
	return text[:end], text[end:]
}

func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0x1f000) {
		return 2
	}
	return 1
}

func clipLine(text string, width int) string {
	var out strings.Builder
	used := 0
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			out.WriteRune(r)
			continue
		}
		if inEscape {
			out.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		cells := runeWidth(r)
		if used+cells > width {
			break
		}
		out.WriteRune(r)
		used += cells
	}
	return out.String()
}
