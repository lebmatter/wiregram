package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nhooyr.io/websocket"
)

const (
	maxPerSecond = 120
	outBuffer    = 256
	pingEvery    = 20 * time.Second
	writeTimeout = 10 * time.Second
)

// Conn is one side of a match's websocket. Events closes when the connection
// ends, for whatever reason.
type Conn struct {
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	Events chan Envelope
}

func newConn(ctx context.Context, c *websocket.Conn) *Conn {
	c.SetReadLimit(ReadLimit)
	cctx, cancel := context.WithCancel(ctx)
	conn := &Conn{c: c, ctx: cctx, cancel: cancel, out: make(chan []byte, outBuffer),
		Events: make(chan Envelope, 32)}
	go conn.readLoop()
	go conn.writeLoop()
	go conn.pingLoop()
	return conn
}

// Send queues one envelope without blocking. Envelopes go out in the order
// they were queued, which frame diffs rely on.
func (c *Conn) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode %s: %w", env.T, err)
	}
	select {
	case c.out <- data:
		return nil
	default:
		c.cancel()
		return errors.New("the game connection is too slow")
	}
}

// Done is closed once the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// Close sends leave and disconnects in the background, so Update never waits
// on the network.
func (c *Conn) Close() {
	go func() {
		if data, err := json.Marshal(Envelope{T: TLeave}); err == nil {
			wctx, cancel := context.WithTimeout(c.ctx, time.Second)
			_ = c.c.Write(wctx, websocket.MessageText, data)
			cancel()
		}
		c.cancel()
		c.c.Close(websocket.StatusNormalClosure, "")
	}()
}

// readLoop drops a peer that floods it: no game needs more than a few
// messages a frame.
func (c *Conn) readLoop() {
	defer close(c.Events)
	defer c.cancel()
	window, count := time.Now(), 0
	for {
		env, err := readEnvelope(c.ctx, c.c)
		if err != nil {
			return
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, count = now, 0
		}
		if count++; count > maxPerSecond {
			closeWith(c.ctx, c.c, "too many messages")
			return
		}
		select {
		case c.Events <- env:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Conn) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case data := <-c.out:
			wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.c.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

// pingLoop drops a peer that stops answering. Ping needs readLoop running.
func (c *Conn) pingLoop() {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(c.ctx, pingEvery)
			err := c.c.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func readEnvelope(ctx context.Context, c *websocket.Conn) (Envelope, error) {
	var env Envelope
	typ, data, err := c.Read(ctx)
	if err != nil {
		return env, fmt.Errorf("read: %w", err)
	}
	if typ != websocket.MessageText {
		return env, errors.New("expected a text message")
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return env, fmt.Errorf("decode: %w", err)
	}
	return env, nil
}

// closeWith sends one error line, then closes the connection.
func closeWith(ctx context.Context, c *websocket.Conn, msg string) {
	data, err := json.Marshal(Envelope{T: TError, Error: msg})
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		_ = c.Write(wctx, websocket.MessageText, data)
		cancel()
	}
	c.Close(websocket.StatusPolicyViolation, msg)
}
