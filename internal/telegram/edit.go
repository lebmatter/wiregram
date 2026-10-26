package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// editWindow is how long Telegram lets a message be edited, except channel
// posts and Saved Messages, which have no limit.
const editWindow = 48 * time.Hour

// editableMessage reports why m cannot be edited; self is the user's own ID.
func editableMessage(m *tg.Message, self int64) error {
	if !m.Out || m.ID == 0 {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	peer, saved := m.PeerID.(*tg.PeerUser)
	saved = saved && peer.UserID == self
	if !m.Post && !saved && time.Since(time.Unix(int64(m.Date), 0)) > editWindow {
		return fmt.Errorf("Telegram only lets messages be edited for 48 hours")
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

func (c *Client) editMessage(ctx context.Context, entry chat, m *tg.Message, text string) error {
	if err := editableMessage(m, c.AccountID); err != nil {
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
	_, err := c.Api.MessagesEditMessage(ctx, request)
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
