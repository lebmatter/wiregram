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
	c := &Client{conn: conn, ctx: cctx, cancel: cancel, Events: make(chan Event, 32)}
	if err := c.Send(Envelope{T: THello, Name: name, Token: token, Games: games}); err != nil {
		c.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.Events)
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() == nil {
				c.Events <- Event{Err: errors.New("lost the connection to base")}
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

// Send writes one envelope. It is safe to call from several goroutines.
func (c *Client) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode %s: %w", env.T, err)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send to base: %w", err)
	}
	return nil
}

func (c *Client) Close() {
	c.cancel()
	c.conn.Close(websocket.StatusNormalClosure, "")
}
