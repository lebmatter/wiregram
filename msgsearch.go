package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

type searchResult struct {
	messages []*tg.Message
	err      error
}

func (c *client) searchResultLines(results []*tg.Message, selected int, entry chat, query string) []string {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 4 || rows < 4 {
		cols, rows = 80, 24
	}
	width := min(cols-1, maxChatLineWidth)
	available := max(1, rows-4)
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
				marker = "> "
			}
			text := c.messageLine(results[i], entry, nil)
			lines = append(lines, clipLine(marker+text, width))
		}
	}
	return lines
}

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
