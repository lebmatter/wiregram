package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"

	"wiregram/internal/terminal"
	"wiregram/internal/textfmt"
)

type forwardRequest struct {
	from tg.InputPeerClass
	id   int
}

func (forwardRequest) Error() string { return "forward message" }

func (c *Client) handleForward(ctx context.Context, t *terminal.Terminal, recentChats []chat, fwd forwardRequest) {
	target, selected, err := c.searchUsers(ctx, t, recentChats)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.Report(err)
		}
		return
	}
	if !selected {
		return
	}
	if err := c.forwardMessage(ctx, fwd.from, target.peer, fwd.id); err != nil {
		t.Report(err)
		return
	}
	t.Report(fmt.Errorf("Message forwarded to %s", textfmt.Plain(target.title)))
}

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
