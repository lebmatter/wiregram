package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/gotd/td/tg"

	"wiregram/internal/backend"
)

func (c *Client) history(ctx context.Context, entry chat) ([]*tg.Message, error) {
	return c.fetchHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: entry.peer, Limit: 30})
}

func (c *Client) olderHistory(ctx context.Context, entry chat, offsetID int) ([]*tg.Message, error) {
	return c.fetchHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     entry.peer,
		Limit:    30,
		OffsetID: offsetID,
	})
}

// unreadHistory loads at most limit of the newest messages past the read marker.
func (c *Client) unreadHistory(ctx context.Context, entry chat, limit int) ([]*tg.Message, error) {
	return c.fetchHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  entry.peer,
		Limit: limit,
		MinID: c.readState(entry.key).inbox,
	})
}

// messagesByID loads the given messages of one chat; deleted ones are skipped.
func (c *Client) messagesByID(ctx context.Context, entry chat, ids []int) ([]*tg.Message, error) {
	inputs := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		inputs[i] = &tg.InputMessageID{ID: id}
	}
	var result tg.MessagesMessagesClass
	var err error
	// Channel message IDs are per channel, so they need the channel's own call.
	if peer, ok := entry.peer.(*tg.InputPeerChannel); ok {
		result, err = c.Api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash},
			ID:      inputs,
		})
	} else {
		result, err = c.Api.MessagesGetMessages(ctx, inputs)
	}
	if err != nil {
		return nil, fmt.Errorf("load quoted messages: %w", err)
	}
	return c.collectMessages(result)
}

func (c *Client) fetchHistory(ctx context.Context, request *tg.MessagesGetHistoryRequest) ([]*tg.Message, error) {
	result, err := c.Api.MessagesGetHistory(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	return c.collectMessages(result)
}

// collectMessages remembers the users and chats a result names and returns
// its messages oldest first.
func (c *Client) collectMessages(result tg.MessagesMessagesClass) ([]*tg.Message, error) {
	var messages []tg.MessageClass
	switch h := result.(type) {
	case *tg.MessagesMessages:
		c.Remember(h.Users, h.Chats)
		messages = h.Messages
	case *tg.MessagesMessagesSlice:
		c.Remember(h.Users, h.Chats)
		messages = h.Messages
	case *tg.MessagesChannelMessages:
		c.Remember(h.Users, h.Chats)
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

func (c *Client) send(ctx context.Context, entry chat, text string, replyTo int) (*tg.Message, error) {
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
		result, err = c.Api.MessagesSendMessage(ctx, request)
		if err == nil {
			break
		}
		if !backend.NetworkError(err) {
			return nil, fmt.Errorf("send message: %w", err)
		}
		c.Report(fmt.Errorf("Connection interrupted. Retrying message: %w", err))
		// Reuse the random ID so retrying cannot send a duplicate.
		if err := backend.WaitFor(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	message := &tg.Message{Out: true, Message: text, Date: int(time.Now().Unix())}
	return c.sentMessage(result, entry, request.RandomID, replyTo, message), nil
}

func (c *Client) sentMessage(
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
		c.Remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdatesCombined:
		c.Remember(u.Users, u.Chats)
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
