package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"golang.org/x/term"

	"wiregram/internal/backend"
	"wiregram/internal/terminal"
	"wiregram/internal/textfmt"
)

// viewChat is the generic chat view that works with any backend.
// It replaces the old (c *client).view() method.
func viewChat(ctx context.Context, b backend.Backend, t *terminal.Terminal, entry backend.ChatEntry, history *[]string) error {
	sessionCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.Subscribe(entry)
	defer b.Unsubscribe()
	if err := b.RefreshReadState(ctx, entry); err != nil {
		t.Report(err)
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
	e, err := t.NewEditor("you> ", false, *history)
	if err != nil {
		return err
	}
	defer e.Close()
	t.Mu.Lock()
	t.Editor = e.Line
	t.Mu.Unlock()
	defer func() { t.Mu.Lock(); t.Editor = nil; t.Mu.Unlock() }()
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
					t.Report(err)
				}
				return
			}
			t.UpdateLock(unread)
		}()
	}
	t.EnableChatLock(b.AutoLockEnabled(), func() int { return b.CachedUnread(entry) }, refreshUnread)
	defer func() {
		cancelLock()
		t.DisableInactivity()
	}()
	reads := newGenericReadReporter(b, entry)
	defer func() {
		if err := reads.Close(sessionCtx); err != nil && sessionCtx.Err() == nil {
			t.Report(err)
		}
	}()
	caps := b.Capabilities()
	readFailed := false
	privateMode := false
	var historyDone chan []backend.Message
	loadingHistory := false
	historyExhausted := false
	var emojiEditor *terminal.Editor
	var emojiQuery []rune
	var emojiChoices []backend.EmojiOption
	emojiSelected := 0
	emojiReactionMode := false
	var emojiTarget *backend.Message
	type genericReactionResult struct {
		MessageID string
		Emoticon  string
		Reactions []backend.MessageReaction
		Err       error
	}
	var reactionDone <-chan genericReactionResult
	var searchEditor *terminal.Editor
	var searchQuery []rune
	var searchResults []backend.Message
	searchSelected := 0
	type genericSearchResult struct {
		Messages []backend.Message
		Err      error
	}
	var searchDone <-chan genericSearchResult
	searchTimer := time.NewTimer(time.Hour)
	searchTimer.Stop()
	defer searchTimer.Stop()
	finishSearch := func() {
		if searchEditor != nil {
			searchEditor.Close()
			searchEditor = nil
			searchQuery = nil
			searchResults = nil
			searchDone = nil
			searchTimer.Stop()
			t.Mu.Lock()
			t.Editor = e.Line
			t.Mu.Unlock()
		}
	}
	defer finishSearch()
	var confirmDelete *bool
	var deleteDone <-chan error
	var confirmMsgDelete *bool
	var msgDeleteDone <-chan error
	var editEditor *terminal.Editor
	var editing *backend.Message
	var editResults <-chan terminal.LineResult
	var editDone <-chan error
	var editText string
	var uploadEditor *terminal.Editor
	var uploadResults <-chan terminal.LineResult
	var selectedUploadPath string
	var replying *backend.Message
	typingTimeout := time.NewTimer(time.Hour)
	typingTimeout.Stop()
	defer typingTimeout.Stop()
	var lastTypingSent time.Time
	var saveDone <-chan error
	finishEdit := func() {
		if editEditor != nil {
			editEditor.Close()
			editEditor, editing, editResults = nil, nil, nil
			t.Mu.Lock()
			t.Editor = e.Line
			t.Mu.Unlock()
		}
	}
	finishUploadPrompt := func() {
		if uploadEditor != nil {
			uploadResults = nil
			uploadEditor.Close()
			uploadEditor = nil
			selectedUploadPath = ""
			t.Mu.Lock()
			t.Editor = e.Line
			t.Mu.Unlock()
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
	var mediaMessage *backend.Message
	player := ""
	render := func() {
		if t.IsLocked() || mediaDone != nil {
			return
		}
		if emojiEditor != nil {
			hasRecent := len(emojiQuery) == 0 && len(b.RecentEmoji()) > 0
			t.Unlock(emojiPickerLines(emojiChoices, emojiSelected, hasRecent, emojiReactionMode))
			return
		}
		if searchEditor != nil {
			cols, rows := termSize()
			t.Unlock(genericSearchResultLines(b, searchResults, searchSelected, entry, string(searchQuery), cols-1, rows-2))
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
		t.Unlock(lines)
		reads.observe(readID)
	}
	finishEmoji := func(value string) error {
		if emojiEditor == nil {
			return nil
		}
		emojiEditor.Close()
		emojiEditor = nil
		emojiQuery = nil
		emojiReactionMode = false
		emojiTarget = nil
		t.Mu.Lock()
		t.Editor = e.Line
		t.Mu.Unlock()
		if value != "" {
			b.RememberEmoji(value)
			if _, err := io.WriteString(e.Input, value); err != nil {
				return fmt.Errorf("insert emoji: %w", err)
			}
		}
		return nil
	}
	defer func() { _ = finishEmoji("") }()
	startEmoji := func(reactionMode bool) {
		picker, err := t.NewEditor("emoji> ", false, nil)
		if err != nil {
			t.Report(err)
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
		t.Mu.Lock()
		t.Editor = picker.Line
		t.Mu.Unlock()
		render()
	}
	startUploadPrompt := func() {
		prompt, err := t.NewEditorWithCompleter(
			"File (Tab: complete, Enter: continue, Esc: cancel)> ",
			false,
			nil,
			backend.PathCompleter{},
		)
		if err != nil {
			t.Report(err)
			return
		}
		uploadEditor, uploadResults = prompt, prompt.Results
		t.Mu.Lock()
		t.Editor = prompt.Line
		t.Mu.Unlock()
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
			t.ClearMedia()
		}
	}
	defer stopMedia()
	startMedia := func(msg backend.Message) {
		probing = false
		probe.Stop()
		mediaCtx, stop := context.WithCancel(ctx)
		mediaCancel = stop
		done := make(chan error, 1)
		mediaDone = done
		t.MediaScreen("Media viewer")
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
			t.Report(fmt.Errorf("install mpv with Kitty video output to open media"))
			return
		}
		player = path
		mediaMessage = &msg
		// Check if audio-only for direct play vs graphics probe
		// The backend handles the details in PlayMedia
		probing = true
		fmt.Fprint(t.Out(), "\033_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\033\\")
		probe.Reset(time.Second)
	}
	render()
	type genericOutgoing struct {
		text    string
		path    string
		replyTo string
	}
	type genericSendResult struct {
		Message backend.Message
		Err     error
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
				var msg backend.Message
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
	appendMessage := func(msg backend.Message) {
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
			reads.Start(ctx)
		case err := <-reads.done:
			reads.complete()
			if err != nil && !readFailed {
				t.Report(err)
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
			if result.Err != nil {
				t.Report(result.Err)
			} else {
				searchResults = result.Messages
				searchSelected = 0
			}
			render()
		case result := <-editResults:
			if result.Err != nil {
				finishEdit()
				render()
				continue
			}
			if strings.TrimSpace(result.Text) == "" {
				t.Report(fmt.Errorf("edited message cannot be empty; press Esc to cancel"))
				continue
			}
			editText = result.Text
			editResults = nil
			editEditor.Line.SetPrompt("Saving edit... ")
			editEditor.Line.Refresh()
			done := make(chan error, 1)
			editDone = done
			msg := *editing
			text := editText
			go func() { done <- b.Edit(ctx, entry, msg, text) }()
		case err := <-editDone:
			editDone = nil
			if err != nil {
				editEditor.Line.SetPrompt("Edit (Enter: save, Esc: cancel)> ")
				editEditor.Line.Operation.SetBuffer(editText)
				editResults = editEditor.Results
				t.Report(err)
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
			if result.Err != nil {
				finishUploadPrompt()
				render()
				continue
			}
			if selectedUploadPath == "" {
				path, err := uploadPath(result.Text)
				if err != nil {
					uploadEditor.Line.Operation.SetBuffer(result.Text)
					t.Report(err)
					continue
				}
				uploadResults = nil
				uploadEditor.Close()
				caption, err := t.NewEditor("Caption (optional, Enter: send, Esc: cancel)> ", false, nil)
				if err != nil {
					uploadEditor = nil
					t.Mu.Lock()
					t.Editor = e.Line
					t.Mu.Unlock()
					t.Report(err)
					render()
					continue
				}
				selectedUploadPath = path
				uploadEditor, uploadResults = caption, caption.Results
				t.Mu.Lock()
				t.Editor = caption.Line
				t.Mu.Unlock()
				render()
				continue
			}
			path := selectedUploadPath
			caption := result.Text
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
				t.Report(err)
				render()
				continue
			}
			return nil
		case err := <-msgDeleteDone:
			msgDeleteDone = nil
			if err != nil {
				t.Report(err)
			}
			render()
		case err := <-saveDone:
			saveDone = nil
			if err != nil {
				t.Report(err)
			}
		case result := <-reactionDone:
			reactionDone = nil
			if result.Err != nil {
				t.Report(result.Err)
				render()
				continue
			}
			for index, msg := range messages {
				if msg.ID == result.MessageID {
					copy := msg
					copy.Reactions = result.Reactions
					messages[index] = copy
					delete(changedMessages, result.MessageID)
					break
				}
			}
			b.RememberEmoji(result.Emoticon)
			render()
		case next <- outgoing:
			queue = queue[1:]
		case result := <-sent:
			if result.Err != nil {
				t.Report(result.Err)
			} else {
				appendMessage(result.Message)
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
						oldReactions := backend.FormatMessageReactions(msg.Reactions)
						newReactions := backend.FormatMessageReactions(update.Reactions)
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
			t.UpdateLock(b.CachedUnread(entry))
		case <-probe.C:
			if probing {
				probing = false
				t.Report(fmt.Errorf("this terminal did not confirm Kitty graphics support"))
			}
		case err := <-mediaDone:
			mediaCancel()
			mediaCancel = nil
			mediaDone = nil
			msg := mediaMessage
			mediaMessage = nil
			t.ClearMedia()
			render()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Report(err)
			} else if msg != nil {
				b.AcknowledgeMedia(ctx, *msg)
			}
		case older := <-historyDone:
			loadingHistory = false
			historyDone = nil
			if len(older) == 0 {
				historyExhausted = true
			} else {
				var fresh []backend.Message
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
		case key, ok := <-t.Keys:
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
						t.Report(fmt.Errorf("this terminal cannot display Kitty graphics"))
					}
				}
				continue
			}
			if key == terminal.TerminalLockKey {
				stopMedia()
				continue
			}
			if key == terminal.TerminalUnlockKey {
				render()
				if mediaNotice != nil {
					t.Report(mediaNotice)
					mediaNotice = nil
				}
				continue
			}
			if key == "\x0c" {
				t.LockNow()
				continue
			}
			if mediaDone != nil || probing {
				switch key {
				case "\x1b", "\x03", "\x04":
					stopMedia()
					render()
					if mediaNotice != nil {
						t.Report(mediaNotice)
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
					if _, err := io.WriteString(editEditor.Input, key); err != nil {
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
					if _, err := io.WriteString(uploadEditor.Input, key); err != nil {
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
						value := emojiChoices[emojiSelected].Value
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
						emojiQuery = textfmt.AppendQuery(emojiQuery, key)
					}
				}
				if emojiEditor != nil {
					if emojiReactionMode {
						emojiChoices = b.ReactionChoices(string(emojiQuery))
					} else {
						emojiChoices = b.EmojiChoices(string(emojiQuery))
					}
					emojiSelected = min(emojiSelected, max(0, len(emojiChoices)-1))
					emojiEditor.Line.Operation.SetBuffer(string(emojiQuery))
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
						searchQuery = textfmt.AppendQuery(searchQuery, key)
						searchTimer.Reset(400 * time.Millisecond)
					}
				}
				if searchEditor != nil {
					searchEditor.Line.Operation.SetBuffer(string(searchQuery))
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
				picker, err := t.NewEditor("search> ", false, nil)
				if err != nil {
					t.Report(err)
					continue
				}
				searchEditor = picker
				searchQuery = nil
				searchResults = nil
				searchSelected = 0
				t.Mu.Lock()
				t.Editor = searchEditor.Line
				t.Mu.Unlock()
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
					t.Report(fmt.Errorf("pin message: %w", err))
				} else {
					copy := msg
					copy.Pinned = !unpin
					messages[selected] = copy
					if unpin {
						t.Report(fmt.Errorf("Message unpinned"))
					} else {
						t.Report(fmt.Errorf("Message pinned"))
					}
				}
				render()
				continue
			case "\x1bm", "\x1bM":
				if reactionDone != nil {
					t.Report(fmt.Errorf("reaction update is still in progress"))
					continue
				}
				reactionMode := caps.Reactions && selected >= 0 && selected < len(messages) && messages[selected].ID != "" && messages[selected].ID != "0"
				startEmoji(reactionMode)
				continue
			case "\x1bf", "\x1bF":
				if !caps.Forward || selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				return backend.ForwardReq{From: entry, Id: messages[selected].ID}
			case "\x1bs", "\x1bS":
				if saveDone != nil {
					t.Report(fmt.Errorf("a download is already in progress"))
					continue
				}
				if selected < 0 || selected >= len(messages) || messages[selected].ID == "" || messages[selected].ID == "0" {
					continue
				}
				filename, ok := b.MediaSaveable(messages[selected])
				if !ok {
					t.Report(fmt.Errorf("this message has no downloadable attachment"))
					continue
				}
				home, err := os.UserHomeDir()
				if err != nil {
					t.Report(fmt.Errorf("locate home directory: %w", err))
					continue
				}
				dir := filepath.Join(home, "Downloads")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Report(fmt.Errorf("create Downloads directory: %w", err))
					continue
				}
				done := make(chan error, 1)
				msg := messages[selected]
				go func() {
					err := b.SaveMedia(ctx, msg, dir)
					if err == nil {
						t.Report(fmt.Errorf("saved %s", filename))
					}
					done <- err
				}()
				saveDone = done
				t.Report(fmt.Errorf("saving %s to ~/Downloads", filename))
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
					t.Report(fmt.Errorf("chat deletion is currently supported only for private chats"))
					continue
				}
				if entry.IsSelf && revoke {
					t.Report(fmt.Errorf("Saved Messages cannot be deleted for both sides"))
					continue
				}
				confirmDelete = &revoke
				scope := "only for you"
				if revoke {
					scope = "for both sides"
				}
				t.Report(fmt.Errorf("Delete chat with %s %s? Press y to confirm; any other key cancels.", textfmt.Plain(entry.Title), scope))
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
				t.Report(fmt.Errorf("Delete this message %s? Press y to confirm; any other key cancels.", scope))
				continue
			case "\x1be", "\x1bE":
				if !caps.Edit || selected < 0 || selected >= len(messages) {
					continue
				}
				msg := messages[selected]
				if err := b.Editable(msg); err != nil {
					t.Report(err)
					continue
				}
				editEditor, err = t.NewEditor("Edit (Enter: save, Esc: cancel)> ", false, nil, msg.Text)
				if err != nil {
					t.Report(err)
					continue
				}
				editing = &msg
				editResults = editEditor.Results
				t.Mu.Lock()
				t.Editor = editEditor.Line
				t.Mu.Unlock()
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
					ch := make(chan []backend.Message, 1)
					historyDone = ch
					offsetID := messages[0].ID
					go func() {
						older, err := b.OlderHistory(ctx, entry, offsetID)
						if err != nil {
							if ctx.Err() == nil {
								t.Report(err)
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
			if _, err := io.WriteString(e.Input, key); err != nil {
				return fmt.Errorf("read message input: %w", err)
			}
		case result := <-e.Results:
			if errors.Is(result.Err, io.EOF) || errors.Is(result.Err, readline.ErrInterrupt) {
				return nil
			}
			if result.Err != nil {
				return fmt.Errorf("read message: %w", result.Err)
			}
			if strings.TrimSpace(result.Text) == "" {
				if !t.IsLocked() {
					openSelected()
				}
				continue
			}
			*history = append(*history, result.Text)
			if len(*history) > 500 {
				*history = (*history)[len(*history)-500:]
			}
			replyTo := ""
			if replying != nil {
				replyTo = replying.ID
				replying = nil
				render()
			}
			queue = append(queue, genericOutgoing{text: result.Text, replyTo: replyTo})
		}
	}
}

// Generic read reporter for any backend.
type genericReadReporter struct {
	b      backend.Backend
	entry  backend.ChatEntry
	target string
	timer  *time.Timer
	done   <-chan error
	cancel context.CancelFunc
}

func newGenericReadReporter(b backend.Backend, entry backend.ChatEntry) *genericReadReporter {
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

func (r *genericReadReporter) Start(ctx context.Context) {
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

func (r *genericReadReporter) Close(ctx context.Context) error {
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
func genericSearchResultLines(b backend.Backend, results []backend.Message, selected int, entry backend.ChatEntry, query string, width, height int) []string {
	width = min(width, textfmt.MaxChatLineWidth)
	available := max(1, height-2)
	var lines []string
	heading := fmt.Sprintf("Search: %s (%d results)", textfmt.Plain(query), len(results))
	lines = append(lines, textfmt.ClipLine(heading, width))
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
			text := backend.GenericMessageLine(results[i], entry, nil)
			lines = append(lines, textfmt.ClipLine(marker+text, width))
		}
	}
	return lines
}

// Generic selected lines rendering.
func genericSelectedLines(b backend.Backend, messages []backend.Message, entry backend.ChatEntry, selected int, changed map[string]bool, replying *backend.Message, typing bool, width, height int) ([]string, string) {
	lineWidth := min(width, textfmt.MaxChatLineWidth)
	reserved := 0
	if replying != nil {
		reserved++
	}
	if typing {
		reserved++
	}
	available := max(1, height-reserved)
	byID := make(map[string]*backend.Message, len(messages))
	replies := make(map[string]int)
	for i := range messages {
		byID[messages[i].ID] = &messages[i]
		if messages[i].ReplyToID != "" {
			replies[messages[i].ReplyToID]++
		}
	}
	const msgPrefixWidth = 7 + 2 + textfmt.NickWidth + 2
	messageLines := func(i int) []string {
		msg := messages[i]
		marker := "  "
		if changed[msg.ID] {
			marker = "* "
		}
		if i == selected {
			marker = styleAccent.Render(">") + " " + b.ReadMarker(entry, msg)
			if reactions := backend.FormatMessageReactions(msg.Reactions); reactions != "" {
				marker += "[" + reactions + "] "
			}
		}
		indent := textfmt.DisplayWidth(marker) + msgPrefixWidth
		body := backend.GenericMessageText(msg)
		if msg.Pinned {
			body += " [pinned]"
		}
		if n := replies[msg.ID]; n > 0 {
			body += styleSubtle.Render(fmt.Sprintf(" · %d %s", n, plural(n, "reply", "replies")))
		}
		prefix := marker + backend.GenericMessagePrefix(msg, entry) + " "
		if msg.ReplyToID == "" {
			return textfmt.WrapMessage(prefix, body, lineWidth, indent)
		}
		// Replies put the quoted original on the header row and the text below it.
		quote := textfmt.ClipLine(prefix+renderQuote(byID[msg.ReplyToID], entry), lineWidth)
		return append([]string{quote},
			textfmt.WrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		lines := []string{}
		if replying != nil {
			lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
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
		lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
	}
	if typing {
		lines = append(lines, textfmt.ClipLine(textfmt.Plain(entry.Title)+" is typing...", lineWidth))
	}
	return lines, readID
}

func emojiPickerLines(choices []backend.EmojiOption, selected int, hasRecent, reactionMode bool) []string {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 20 || rows < 8 {
		cols, rows = 80, 24
	}
	limit := max(1, rows-4)
	start := 0
	if selected >= limit {
		start = selected - limit + 1
	}
	end := min(len(choices), start+limit)
	heading := "Emoji for draft — type to filter; Tab: react"
	if reactionMode {
		heading = "React — type to filter; Tab: insert into draft"
	}
	if hasRecent {
		heading += "; recent first"
	}
	lines := []string{heading, ""}
	for index := start; index < end; index++ {
		marker := "  "
		if index == selected {
			marker = "> "
		}
		lines = append(lines, textfmt.ClipLine(fmt.Sprintf("%s%s  %s", marker, choices[index].Value, choices[index].Name), cols-1))
	}
	if len(choices) == 0 {
		lines = append(lines, "  No matching emoji")
	}
	return lines
}

func uploadPath(value string) (string, error) {
	path := backend.ExpandHome(strings.TrimSpace(value))
	if path == "" {
		return "", fmt.Errorf("enter a file path or press Esc to cancel")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve upload path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("open upload file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upload path must be a regular file")
	}
	return path, nil
}
