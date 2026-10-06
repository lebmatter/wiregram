package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/tg"
)

func (c *Client) forwardMessage(ctx context.Context, from, to tg.InputPeerClass, id int) error {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Errorf("create forward ID: %w", err)
	}
	_, err := c.Api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: from,
		ToPeer:   to,
		ID:       []int{id},
		RandomID: []int64{int64(binary.LittleEndian.Uint64(buf[:]))},
	})
	if err != nil {
		return fmt.Errorf("forward message: %w", err)
	}
	return nil
}
