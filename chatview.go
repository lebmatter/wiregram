package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/gotd/td/tg"
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

func (c *client) messageLine(m *tg.Message, entry chat) string {
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
	text := formatMedia(m.Media)
	if text != "" && m.Message != "" {
		text += " — " + m.Message
	} else if text == "" {
		text = m.Message
	}
	return plain(sender) + "> " + plain(text)
}

func (c *client) send(ctx context.Context, entry chat, text string) (*tg.Message, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("create message ID: %w", err)
	}
	request := &tg.MessagesSendMessageRequest{Peer: entry.peer, Message: text, RandomID: int64(binary.LittleEndian.Uint64(random[:]))}
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
			if u.RandomID == request.RandomID {
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
	copy := *message
	copy.Out = true
	return &copy, nil
}

type sendResult struct {
	message *tg.Message
	err     error
}

func (c *client) view(ctx context.Context, t *terminal, entry chat, history *[]string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.subscribe(entry.key)
	defer c.subscribe(peerKey{})
	messages, err := c.history(ctx, entry)
	if err != nil {
		return err
	}
	lines := make([]string, 0, 30)
	seen := make(map[int]bool)
	for _, message := range messages {
		seen[message.ID] = true
		lines = append(lines, c.messageLine(message, entry))
	}
	t.unlock(lines)
	e, err := t.newEditor("", false, *history)
	if err != nil {
		return err
	}
	defer e.close()
	t.mu.Lock()
	t.editor = e.line
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.editor = nil; t.mu.Unlock() }()

	sends := make(chan string)
	sent := make(chan sendResult, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case text := <-sends:
				message, err := c.send(ctx, entry, text)
				select {
				case sent <- sendResult{message, err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	var queue []string
	locked := false
	lastKey := time.Now()
	lockSignal := make(chan struct{}, 1)
	timer := time.AfterFunc(10*time.Second, func() {
		select {
		case lockSignal <- struct{}{}:
		default:
		}
	})
	defer timer.Stop()
	defer func() { t.mu.Lock(); t.locked = false; t.mu.Unlock() }()
	appendMessage := func(message *tg.Message) {
		if message.ID != 0 && seen[message.ID] {
			return
		}
		if message.ID != 0 {
			seen[message.ID] = true
		}
		line := c.messageLine(message, entry)
		lines = append(lines, line)
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		if !locked {
			// Readline's writer cleans and refreshes the editing line under its buffer lock.
			fmt.Fprintln(e.line.Stdout(), line)
		}
	}
	for {
		var next chan string
		var text string
		if len(queue) > 0 {
			next, text = sends, queue[0]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case next <- text:
			queue = queue[1:]
		case result := <-sent:
			if result.err != nil {
				fmt.Fprintln(e.line.Stderr(), cleanError(result.err))
			} else {
				appendMessage(result.message)
			}
		case <-c.changed:
			for _, message := range c.drain() {
				appendMessage(message)
			}
		case <-lockSignal:
			remaining := 10*time.Second - time.Since(lastKey)
			if remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			if !locked {
				locked = true
				t.lock()
			}
		case key, ok := <-t.keys:
			if !ok {
				return context.Canceled
			}
			lastKey = time.Now()
			timer.Reset(10 * time.Second)
			if locked {
				locked = false
				t.unlock(lines)
				e.line.Refresh()
				continue
			}
			switch key {
			case "\x1b", "\x03", "\x04":
				return nil
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
				continue
			}
			*history = append(*history, result.text)
			if len(*history) > 500 {
				*history = (*history)[len(*history)-500:]
			}
			queue = append(queue, result.text)
		}
	}
}
