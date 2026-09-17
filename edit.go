package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func editableMessage(m *tg.Message) error {
	if !m.Out || m.ID == 0 {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	_, forwarded := m.GetFwdFrom()
	if forwarded || m.ViaBotID != 0 || expiringMediaLabel(m.Media) != "" {
		return fmt.Errorf("this message cannot be edited in wiregram")
	}
	switch m.Media.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
	default:
		return fmt.Errorf("only text messages can be edited in wiregram")
	}
	if strings.ContainsAny(m.Message, "\r\n") {
		return fmt.Errorf("multiline messages cannot be edited in wiregram's single-line editor")
	}
	return nil
}

func (c *client) editMessage(ctx context.Context, entry chat, m *tg.Message, text string) error {
	if err := editableMessage(m); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("edited message cannot be empty")
	}
	if text == m.Message {
		return nil
	}
	request := &tg.MessagesEditMessageRequest{Peer: entry.peer, ID: m.ID}
	request.SetMessage(text)
	// Edits are plain text; old entity offsets no longer describe the new text.
	request.SetEntities([]tg.MessageEntityClass{})
	_, err := c.api.MessagesEditMessage(ctx, request)
	switch {
	case err == nil, tgerr.Is(err, "MESSAGE_NOT_MODIFIED"):
		return nil
	case tgerr.Is(err, "MESSAGE_EDIT_TIME_EXPIRED"):
		return fmt.Errorf("Telegram's editing time limit has expired for this message")
	case tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED", "MESSAGE_EDIT_FORBIDDEN"):
		return fmt.Errorf("Telegram does not allow editing this message")
	default:
		return fmt.Errorf("edit message: %w", err)
	}
}
