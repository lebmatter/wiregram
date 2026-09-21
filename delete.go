package main

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func (c *client) deleteChat(ctx context.Context, entry chat, revoke bool) error {
	if entry.key.kind != 'u' {
		return fmt.Errorf("chat deletion is currently supported only for private chats")
	}
	if _, self := entry.peer.(*tg.InputPeerSelf); self && revoke {
		return fmt.Errorf("Saved Messages cannot be deleted for both sides")
	}
	for {
		result, err := c.api.MessagesDeleteHistory(ctx, &tg.MessagesDeleteHistoryRequest{
			Revoke: revoke,
			Peer:   entry.peer,
			MaxID:  0,
		})
		switch {
		case err == nil:
			if result.Offset == 0 {
				return nil
			}
		case tgerr.Is(err, "CHAT_ADMIN_REQUIRED"):
			return fmt.Errorf("Telegram does not allow deleting this chat for both sides")
		default:
			return fmt.Errorf("delete chat: %w", err)
		}
	}
}

func (c *client) deleteMessages(ctx context.Context, entry chat, ids []int, revoke bool) error {
	switch entry.key.kind {
	case 'c':
		channel, ok := entry.peer.(*tg.InputPeerChannel)
		if !ok {
			return fmt.Errorf("delete messages: unexpected peer type for channel")
		}
		_, err := c.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      ids,
		})
		if err != nil {
			return fmt.Errorf("delete messages: %w", err)
		}
	default:
		_, err := c.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
			Revoke: revoke,
			ID:     ids,
		})
		if err != nil {
			return fmt.Errorf("delete messages: %w", err)
		}
	}
	return nil
}
