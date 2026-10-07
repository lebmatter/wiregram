// Package base is the game-mode server: player discovery, invites and a relay
// between matched players. It never runs game logic.
package base

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"wiregram/internal/game"
	"wiregram/internal/textfmt"
)

const (
	maxPlayers   = 1000
	maxNameLen   = 32
	maxPerSecond = 60
	outBuffer    = 64
	pingEvery    = 20 * time.Second
	writeTimeout = 10 * time.Second
)

type player struct {
	name   string
	games  []game.GameRef
	out    chan []byte
	cancel context.CancelFunc
	match  *match
}

// match is a challenge until accepted, then a running game.
type match struct {
	id          string
	game        game.GameRef
	host, guest *player
	pending     bool
}

func (m *match) peer(p *player) *player {
	if p == m.host {
		return m.guest
	}
	return m.host
}

type server struct {
	token string
	log   *log.Logger

	mu      sync.Mutex
	players map[string]*player
	seq     uint64
}

// outgoing is a message to deliver once the lock is released.
type outgoing struct {
	to  *player
	env game.Envelope
}

// Run serves base on addr until ctx ends. An empty token lets anyone join.
func Run(ctx context.Context, addr, token string, logger *log.Logger) error {
	s := &server{token: token, log: logger, players: map[string]*player{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.serveWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Hijacked websockets outlive Shutdown; tying them to ctx ends them.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	logger.Printf("listening on %s", ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

func (s *server) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.SetReadLimit(game.ReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	helloCtx, helloCancel := context.WithTimeout(ctx, 10*time.Second)
	hello, err := readEnvelope(helloCtx, c)
	helloCancel()
	if err != nil {
		return
	}
	p := &player{games: hello.Games, out: make(chan []byte, outBuffer), cancel: cancel}
	p.name = strings.TrimSpace(hello.Name)
	if err := s.join(p, hello); err != nil {
		closeWith(ctx, c, err.Error())
		return
	}
	defer s.leave(p, true)
	go writeLoop(ctx, c, p.out, cancel)
	go pingLoop(ctx, c, cancel)

	window, count := time.Now(), 0
	for {
		env, err := readEnvelope(ctx, c)
		if err != nil {
			return
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, count = now, 0
		}
		if count++; count > maxPerSecond {
			closeWith(ctx, c, "too many messages")
			return
		}
		s.handle(p, env)
	}
}

func (s *server) join(p *player, hello game.Envelope) error {
	switch {
	case hello.T != game.THello:
		return errors.New("expected hello")
	case s.token != "" && subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.token)) != 1:
		return errors.New("wrong base token")
	case p.name == "" || len([]rune(p.name)) > maxNameLen || textfmt.Plain(p.name) != p.name:
		return fmt.Errorf("player name must be 1 to %d plain characters", maxNameLen)
	}
	s.mu.Lock()
	switch {
	case s.players[p.name] != nil:
		s.mu.Unlock()
		return fmt.Errorf("the name %s is already playing on this base", p.name)
	case len(s.players) >= maxPlayers:
		s.mu.Unlock()
		return errors.New("this base is full")
	}
	s.players[p.name] = p
	out := s.lobbyLocked()
	s.mu.Unlock()
	s.deliver(out)
	return nil
}

// leave ends p's match, if any, and removes p when gone is set.
func (s *server) leave(p *player, gone bool) {
	s.mu.Lock()
	var out []outgoing
	if m := p.match; m != nil {
		if other := m.peer(p); other != nil {
			other.match = nil
			out = append(out, outgoing{other, game.Envelope{T: game.TPeerLeft, ID: m.id}})
		}
		p.match = nil
	}
	if gone {
		delete(s.players, p.name)
	}
	out = append(out, s.lobbyLocked()...)
	s.mu.Unlock()
	s.deliver(out)
}

func (s *server) handle(p *player, env game.Envelope) {
	switch env.T {
	case game.TChallenge:
		s.challenge(p, env)
	case game.TAccept:
		s.accept(p, env.ID)
	case game.TDecline:
		s.decline(p, env.ID)
	case game.TGame:
		s.mu.Lock()
		var to *player
		if m := p.match; m != nil && !m.pending {
			to = m.peer(p)
		}
		s.mu.Unlock()
		if to != nil {
			s.deliver([]outgoing{{to, game.Envelope{T: game.TGame, Payload: env.Payload}}})
		}
	case game.TLeave:
		s.leave(p, false)
	}
}

func (s *server) challenge(p *player, env game.Envelope) {
	s.mu.Lock()
	target := s.players[env.To]
	var refuse string
	switch {
	case env.Game == nil:
		refuse = "no game chosen"
	case target == nil:
		refuse = env.To + " is no longer on this base"
	case target == p:
		refuse = "you cannot challenge yourself"
	case p.match != nil:
		refuse = "finish your current game first"
	case target.match != nil:
		refuse = target.name + " is busy"
	case !slices.Contains(p.games, *env.Game) || !slices.Contains(target.games, *env.Game):
		refuse = target.name + " does not have this game version"
	}
	if refuse != "" {
		s.mu.Unlock()
		s.deliver([]outgoing{{p, game.Envelope{T: game.TError, Error: refuse}}})
		return
	}
	s.seq++
	m := &match{id: strconv.FormatUint(s.seq, 10), game: *env.Game, host: p, guest: target, pending: true}
	p.match, target.match = m, m
	out := []outgoing{{target, game.Envelope{T: game.TChallenge, From: p.name, ID: m.id, Game: &m.game}}}
	out = append(out, s.lobbyLocked()...)
	s.mu.Unlock()
	s.deliver(out)
}

func (s *server) accept(p *player, id string) {
	s.mu.Lock()
	m := p.match
	if m == nil || m.id != id || m.guest != p || !m.pending {
		s.mu.Unlock()
		return
	}
	m.pending = false
	names := []string{m.host.name, m.guest.name}
	out := []outgoing{
		{m.host, game.Envelope{T: game.TMatched, ID: id, Game: &m.game, Seat: 1, Names: names}},
		{m.guest, game.Envelope{T: game.TMatched, ID: id, Game: &m.game, Seat: 2, Names: names}},
	}
	s.mu.Unlock()
	s.deliver(out)
}

func (s *server) decline(p *player, id string) {
	s.mu.Lock()
	m := p.match
	if m == nil || m.id != id || m.guest != p || !m.pending {
		s.mu.Unlock()
		return
	}
	m.host.match, m.guest.match = nil, nil
	out := []outgoing{{m.host, game.Envelope{T: game.TDeclined, ID: id, From: p.name}}}
	out = append(out, s.lobbyLocked()...)
	s.mu.Unlock()
	s.deliver(out)
}

// lobbyLocked builds the lobby snapshot for everyone. The caller holds mu.
func (s *server) lobbyLocked() []outgoing {
	players := make([]game.Player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, game.Player{Name: p.name, Games: p.games, Busy: p.match != nil})
	}
	slices.SortFunc(players, func(a, b game.Player) int { return strings.Compare(a.Name, b.Name) })
	out := make([]outgoing, 0, len(s.players))
	for _, p := range s.players {
		out = append(out, outgoing{p, game.Envelope{T: game.TLobby, Players: players}})
	}
	return out
}

// deliver queues messages without blocking; a player too slow to drain its
// queue is disconnected.
func (s *server) deliver(out []outgoing) {
	for _, o := range out {
		data, err := json.Marshal(o.env)
		if err != nil {
			s.log.Printf("encode %s: %v", o.env.T, err)
			continue
		}
		select {
		case o.to.out <- data:
		default:
			o.to.cancel()
		}
	}
}

func readEnvelope(ctx context.Context, c *websocket.Conn) (game.Envelope, error) {
	var env game.Envelope
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
	data, err := json.Marshal(game.Envelope{T: game.TError, Error: msg})
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		_ = c.Write(wctx, websocket.MessageText, data)
		cancel()
	}
	c.Close(websocket.StatusPolicyViolation, msg)
}

func writeLoop(ctx context.Context, c *websocket.Conn, out <-chan []byte, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-out:
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := c.Write(wctx, websocket.MessageText, data)
			wcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// pingLoop drops peers that stop answering. Ping needs the read loop running,
// which serveWS provides.
func pingLoop(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, pingEvery)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}
