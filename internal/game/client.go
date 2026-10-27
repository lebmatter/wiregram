package game

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nhooyr.io/websocket"
)

// dialFor is how long Dial keeps retrying: a new quick tunnel's name can take
// a few seconds to resolve everywhere.
const dialFor = 20 * time.Second

// Dial joins the game behind an invite as name.
func Dial(ctx context.Context, host, token, name string) (*Conn, error) {
	url := "wss://" + host + "/ws"
	deadline := time.Now().Add(dialFor)
	for {
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c, _, err := websocket.Dial(dctx, url, nil)
		cancel()
		if err == nil {
			conn := newConn(ctx, c)
			if err := conn.Send(Envelope{T: THello, Token: token, Name: name}); err != nil {
				conn.Close()
				return nil, err
			}
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("join game: %w", ctx.Err())
		}
		if time.Now().After(deadline) {
			return nil, errors.New("couldn't reach the game; the challenge may have expired")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("join game: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
