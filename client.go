package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type peerKey struct {
	kind byte
	id   int64
}

type reactionUpdate struct {
	messageID int
	reactions tg.MessageReactions
}

func keyOf(peer tg.PeerClass) peerKey {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return peerKey{'u', p.UserID}
	case *tg.PeerChat:
		return peerKey{'g', p.ChatID}
	case *tg.PeerChannel:
		return peerKey{'c', p.ChannelID}
	}
	return peerKey{}
}

type client struct {
	accountID        int64  // Set before publishing this connection to the UI.
	accountName      string // Set before publishing this connection to the UI.
	cfg              config
	ai               *aiConfig
	telegram         *telegram.Client
	updates          *updates.Manager
	api              *tg.Client
	mu               sync.Mutex
	users            map[int64]*tg.User
	chats            map[peerKey]string
	reads            map[peerKey]readState
	unreads          map[peerKey]int
	contactUsers     []*tg.User
	frequentUsers    []*tg.User
	contactsLoaded   bool
	frequentLoaded   bool
	emojiRecent      []string
	active           peerKey
	pending          []*tg.Message
	pendingReactions []reactionUpdate
	autoLock         bool
	changed          chan struct{}
	report           func(error)
}

func newClient(cfg config, dir string, report func(error)) (*client, error) {
	dir = filepath.Join(dir, "session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	c := &client{
		users: make(map[int64]*tg.User), chats: make(map[peerKey]string),
		unreads: make(map[peerKey]int), autoLock: cfg.AutoLock == 1,
		changed: make(chan struct{}, 1), cfg: cfg, ai: cfg.AI,
	}
	middleware := telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			for {
				err := next.Invoke(ctx, input, output)
				if wait, ok := tgerr.AsFloodWait(err); ok {
					report(fmt.Errorf("Rate limited. Retry in %ds.", int(wait.Seconds())))
					if err := waitFor(ctx, wait); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
	})
	c.updates = updates.New(updates.Config{Handler: telegram.UpdateHandlerFunc(c.handleUpdates)})
	c.telegram = telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(dir, "session.json")},
		UpdateHandler:  c.updates,
		Middlewares:    []telegram.Middleware{middleware, hook.UpdateHook(c.updates.Handle)},
	})
	c.api = c.telegram.API()
	c.report = report
	return c, nil
}

func waitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func networkError(err error) bool {
	var e net.Error
	return errors.As(err, &e) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func (c *client) remember(users []tg.UserClass, chats []tg.ChatClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, value := range users {
		if u, ok := value.(*tg.User); ok {
			if previous := c.users[u.ID]; previous != nil && !previous.Min && u.Min {
				continue
			}
			c.users[u.ID] = u
		}
	}
	for _, value := range chats {
		switch g := value.(type) {
		case *tg.Chat:
			c.chats[peerKey{'g', g.ID}] = g.Title
		case *tg.Channel:
			title := g.Title
			if g.Broadcast {
				title = "Channel: " + title
			}
			c.chats[peerKey{'c', g.ID}] = title
		}
	}
}

func (c *client) subscribe(key peerKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active, c.pending, c.pendingReactions = key, nil, nil
}

func (c *client) drain() ([]*tg.Message, []reactionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages := c.pending
	reactions := c.pendingReactions
	c.pending = nil
	c.pendingReactions = nil
	return messages, reactions
}

func (c *client) rememberUnread(key peerKey, unread int) {
	c.mu.Lock()
	previous := c.unreads[key]
	c.unreads[key] = unread
	if previous != unread && c.active == key {
		select {
		case c.changed <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
}

func (c *client) cachedChatUnread(key peerKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unreads[key]
}

func (c *client) accept(value tg.MessageClass, newMessage bool) {
	m, ok := value.(*tg.Message)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := keyOf(m.PeerID)
	if newMessage && !m.Out {
		c.unreads[key]++
	}
	if c.active == (peerKey{}) || key != c.active {
		return
	}
	c.pending = append(c.pending, m)
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *client) acceptReaction(update *tg.UpdateMessageReactions) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == (peerKey{}) || keyOf(update.Peer) != c.active {
		return
	}
	c.pendingReactions = append(c.pendingReactions, reactionUpdate{update.MsgID, update.Reactions})
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *client) handleUpdates(_ context.Context, value tg.UpdatesClass) error {
	var updates []tg.UpdateClass
	switch u := value.(type) {
	case *tg.Updates:
		c.remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdatesCombined:
		c.remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{u.Update}
	case *tg.UpdateShortMessage:
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerUser{UserID: u.UserID}, FromID: &tg.PeerUser{UserID: u.UserID}, Message: u.Message, Date: u.Date}, true)
	case *tg.UpdateShortChatMessage:
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerChat{ChatID: u.ChatID}, FromID: &tg.PeerUser{UserID: u.FromID}, Message: u.Message, Date: u.Date}, true)
	}
	for _, value := range updates {
		switch u := value.(type) {
		case *tg.UpdateNewMessage:
			c.accept(u.Message, true)
		case *tg.UpdateNewChannelMessage:
			c.accept(u.Message, true)
		case *tg.UpdateEditMessage:
			c.accept(u.Message, false)
		case *tg.UpdateEditChannelMessage:
			c.accept(u.Message, false)
		case *tg.UpdateMessageReactions:
			c.acceptReaction(u)
		case *tg.UpdateReadHistoryOutbox:
			c.rememberRead(keyOf(u.Peer), 0, u.MaxID)
		case *tg.UpdateReadHistoryInbox:
			c.rememberRead(keyOf(u.Peer), u.MaxID, 0)
		case *tg.UpdateReadChannelOutbox:
			c.rememberRead(peerKey{'c', u.ChannelID}, 0, u.MaxID)
		case *tg.UpdateReadChannelInbox:
			c.rememberRead(peerKey{'c', u.ChannelID}, u.MaxID, 0)
		}
	}
	return nil
}
