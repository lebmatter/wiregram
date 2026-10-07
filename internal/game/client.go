package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

// Event is one envelope from base. Err is set once, on disconnect, and then
// Events closes.
type Event struct {
	Env Envelope
	Err error
}

// Client is a connection to one base.
type Client struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	Events chan Event
}

// Dial connects to base and introduces this player. url may be ws://, wss://,
// http://, https:// or a bare host:port.
func Dial(ctx context.Context, url, name, token string, games []GameRef) (*Client, error) {
	switch {
	case strings.HasPrefix(url, "https://"):
		url = "wss://" + strings.TrimPrefix(url, "https://")
	case strings.HasPrefix(url, "http://"):
		url = "ws://" + strings.TrimPrefix(url, "http://")
	case !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://"):
		url = "ws://" + url
	}
	url = strings.TrimSuffix(url, "/") + "/ws"

	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to base: %w", err)
	}
	conn.SetReadLimit(ReadLimit)
	cctx, cancel := context.WithCancel(ctx)
	c := &Client{conn: conn, ctx: cctx, cancel: cancel, out: make(chan []byte, 256),
		Events: make(chan Event, 32)}
	if err := c.Send(Envelope{T: THello, Name: name, Token: token, Games: games}); err != nil {
		c.Close()
		return nil, err
	}
	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.Events)
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() == nil {
				select {
				case c.Events <- Event{Err: errors.New("lost the connection to base")}:
				case <-c.ctx.Done():
				}
			}
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		select {
		case c.Events <- Event{Env: env}:
		case <-c.ctx.Done():
			return
		}
	}
}

// Send queues one envelope without blocking. Envelopes go out in the order
// they were queued, which frame diffs rely on.
func (c *Client) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode %s: %w", env.T, err)
	}
	select {
	case c.out <- data:
		return nil
	default:
		return errors.New("the connection to base is too slow")
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case data := <-c.out:
			ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
			err := c.conn.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				// readLoop sees the closed connection and reports it.
				c.conn.Close(websocket.StatusGoingAway, "")
				return
			}
		}
	}
}

// Close disconnects without waiting for the close handshake.
func (c *Client) Close() {
	c.cancel()
	go c.conn.Close(websocket.StatusNormalClosure, "")
}
