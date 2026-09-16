package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
)

type readState struct{ inbox, outbox int }

func (c *client) readState(key peerKey) readState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[key]
}

func (c *client) rememberRead(key peerKey, inbox, outbox int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reads == nil {
		c.reads = make(map[peerKey]readState)
	}
	previous := c.reads[key]
	next := readState{max(previous.inbox, inbox), max(previous.outbox, outbox)}
	c.reads[key] = next
	if previous != next && c.active == key {
		select {
		case c.changed <- struct{}{}:
		default:
		}
	}
}

func (c *client) refreshReadState(ctx context.Context, entry chat) error {
	result, err := c.api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{
		&tg.InputDialogPeer{Peer: entry.peer},
	})
	if err != nil {
		return fmt.Errorf("load read receipts: %w", err)
	}
	for _, value := range result.Dialogs {
		if d, ok := value.(*tg.Dialog); ok {
			c.rememberRead(keyOf(d.Peer), d.ReadInboxMaxID, d.ReadOutboxMaxID)
		}
	}
	return nil
}

func (c *client) readMarker(entry chat, m *tg.Message) string {
	// Private conversations have an unambiguous recipient; channels and Saved
	// Messages must not imply another person has read a message.
	if !m.Out || m.ID <= 0 || entry.key.kind != 'u' {
		return ""
	}
	if _, self := entry.peer.(*tg.InputPeerSelf); self {
		return ""
	}
	if m.ID <= c.readState(entry.key).outbox {
		return "✓✓ "
	}
	return "✓ "
}

func (c *client) markRead(ctx context.Context, entry chat, id int) error {
	if id <= 0 || id <= c.readState(entry.key).inbox {
		return nil
	}
	var err error
	switch peer := entry.peer.(type) {
	case *tg.InputPeerChannel:
		_, err = c.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, MaxID: id,
		})
	default:
		_, err = c.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: entry.peer, MaxID: id})
	}
	if err != nil {
		return fmt.Errorf("mark messages read: %w", err)
	}
	c.rememberRead(entry.key, id, 0)
	return nil
}

// Only messages actually drawn by the chat view advance the read position.
// Telegram's readHistory is cumulative: it also covers earlier messages.
type readReporter struct {
	client *client
	entry  chat
	target int
	timer  *time.Timer
	done   <-chan error
	cancel context.CancelFunc
}

func newReadReporter(c *client, entry chat) *readReporter {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &readReporter{client: c, entry: entry, target: c.readState(entry.key).inbox, timer: timer}
}

func (r *readReporter) observe(id int) {
	if id > r.target {
		r.target = id
		r.timer.Reset(3 * time.Second)
	}
}

func (r *readReporter) start(ctx context.Context) {
	if r.done != nil || r.target <= r.client.readState(r.entry.key).inbox {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	done := make(chan error, 1)
	r.done = done
	id := r.target
	go func() { done <- r.client.markRead(ctx, r.entry, id) }()
}

func (r *readReporter) complete() {
	r.cancel()
	r.done, r.cancel = nil, nil
	if r.target > r.client.readState(r.entry.key).inbox {
		r.timer.Reset(3 * time.Second)
	}
}

func (r *readReporter) close(ctx context.Context) error {
	r.timer.Stop()
	if r.done != nil {
		r.cancel()
		<-r.done
	}
	// Flush already-viewed messages on leaving the chat, without hanging exit.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return r.client.markRead(ctx, r.entry, r.target)
}
