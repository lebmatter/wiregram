package main

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

func (c *client) searchMessages(ctx context.Context, entry chat, query string) ([]*tg.Message, error) {
	result, err := c.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
		Peer:   entry.peer,
		Q:      query,
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  20,
	})
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	var raw []tg.MessageClass
	switch r := result.(type) {
	case *tg.MessagesMessages:
		c.remember(r.Users, r.Chats)
		raw = r.Messages
	case *tg.MessagesMessagesSlice:
		c.remember(r.Users, r.Chats)
		raw = r.Messages
	case *tg.MessagesChannelMessages:
		c.remember(r.Users, r.Chats)
		raw = r.Messages
	}
	messages := make([]*tg.Message, 0, len(raw))
	for _, m := range raw {
		if msg, ok := m.(*tg.Message); ok {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}
