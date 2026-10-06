package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

type readState struct{ inbox, outbox int }

func (c *Client) readState(key peerKey) readState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[key]
}

func (c *Client) rememberRead(key peerKey, inbox, outbox int) {
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

func (c *Client) refreshReadState(ctx context.Context, entry chat) error {
	_, err := c.refreshChatUnread(ctx, entry)
	return err
}

func (c *Client) refreshChatUnread(ctx context.Context, entry chat) (int, error) {
	result, err := c.Api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{
		&tg.InputDialogPeer{Peer: entry.peer},
	})
	if err != nil {
		return 0, fmt.Errorf("load chat unread count: %w", err)
	}
	for _, value := range result.Dialogs {
		if d, ok := value.(*tg.Dialog); ok {
			key := keyOf(d.Peer)
			c.rememberRead(key, d.ReadInboxMaxID, d.ReadOutboxMaxID)
			c.rememberUnread(key, d.UnreadCount)
			return d.UnreadCount, nil
		}
	}
	return 0, fmt.Errorf("Telegram did not return the selected chat")
}

func (c *Client) readMarker(entry chat, m *tg.Message) string {
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

func (c *Client) markRead(ctx context.Context, entry chat, id int) error {
	if id <= 0 || id <= c.readState(entry.key).inbox {
		return nil
	}
	var err error
	switch peer := entry.peer.(type) {
	case *tg.InputPeerChannel:
		_, err = c.Api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, MaxID: id,
		})
	default:
		_, err = c.Api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: entry.peer, MaxID: id})
	}
	if err != nil {
		return fmt.Errorf("mark messages read: %w", err)
	}
	c.rememberRead(entry.key, id, 0)
	if _, err := c.refreshChatUnread(ctx, entry); err != nil {
		// Reading succeeded. Keep that result even if the best-effort counter
		// refresh fails; the lock screen will retry its own refresh.
		if ctx.Err() == nil {
			c.Report(fmt.Errorf("refresh unread count after marking messages read: %w", err))
		}
	}
	return nil
}
