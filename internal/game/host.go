package game

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const maxNameLen = 32

// Joined is the guest who joined a hosted game.
type Joined struct {
	Conn *Conn
	Name string
}

// Host serves one challenge on 127.0.0.1, for cloudflared to tunnel to. The
// first guest with the token plays; anyone after gets an error line.
type Host struct {
	Addr   string
	Joined chan Joined

	token  string
	srv    *http.Server
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	taken bool
}

// Listen starts a host for token. Close stops it and the guest's connection.
func Listen(ctx context.Context, token string) (*Host, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start the game server: %w", err)
	}
	hctx, cancel := context.WithCancel(ctx)
	h := &Host{Addr: ln.Addr().String(), Joined: make(chan Joined, 1), token: token,
		ctx: hctx, cancel: cancel}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.serveWS)
	h.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Hijacked websockets outlive Shutdown; tying them to ctx ends them.
		BaseContext: func(net.Listener) context.Context { return hctx },
	}
	go func() { _ = h.srv.Serve(ln) }()
	return h, nil
}

// Done is closed once the host has been closed.
func (h *Host) Done() <-chan struct{} { return h.ctx.Done() }

// Close stops the server and ends the guest's connection.
func (h *Host) Close() {
	h.cancel()
	go h.srv.Close()
}

func (h *Host) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(ReadLimit)
	helloCtx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	hello, err := readEnvelope(helloCtx, c)
	cancel()
	if err != nil {
		c.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	name := strings.TrimSpace(hello.Name)
	switch {
	case hello.T != THello ||
		subtle.ConstantTimeCompare([]byte(hello.Token), []byte(h.token)) != 1:
		closeWith(h.ctx, c, "this challenge is not valid")
		return
	case name == "" || len([]rune(name)) > maxNameLen:
		closeWith(h.ctx, c, fmt.Sprintf("player name must be 1 to %d characters", maxNameLen))
		return
	}
	h.mu.Lock()
	taken := h.taken
	h.taken = true
	h.mu.Unlock()
	if taken {
		closeWith(h.ctx, c, "this game has already started")
		return
	}
	conn := newConn(h.ctx, c)
	h.Joined <- Joined{Conn: conn, Name: name}
	// The handler holds the connection until the match ends.
	<-conn.Done()
}
