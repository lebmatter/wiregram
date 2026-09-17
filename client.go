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
	telegram         *telegram.Client
	updates          *updates.Manager
	api              *tg.Client
	mu               sync.Mutex
	users            map[int64]*tg.User
	chats            map[peerKey]string
	reads            map[peerKey]readState
	contactUsers     []*tg.User
	frequentUsers    []*tg.User
	contactsLoaded   bool
	frequentLoaded   bool
	emojiRecent      []string
	active           peerKey
	pending          []*tg.Message
	pendingReactions []reactionUpdate
	unread           int
	changed          chan struct{}
	report           func(error)
}

func newClient(cfg config, dir string, report func(error)) (*client, error) {
	dir = filepath.Join(dir, "session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	c := &client{users: make(map[int64]*tg.User), chats: make(map[peerKey]string), changed: make(chan struct{}, 1)}
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

func (c *client) refreshTotalUnread(ctx context.Context) (int, error) {
	state, err := c.api.UpdatesGetState(ctx)
	if err != nil {
		return 0, fmt.Errorf("load unread count: %w", err)
	}
	c.mu.Lock()
	c.unread = state.UnreadCount
	c.mu.Unlock()
	return state.UnreadCount, nil
}

func (c *client) cachedTotalUnread() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unread
}

func (c *client) accept(value tg.MessageClass) {
	m, ok := value.(*tg.Message)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == (peerKey{}) || keyOf(m.PeerID) != c.active {
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
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerUser{UserID: u.UserID}, FromID: &tg.PeerUser{UserID: u.UserID}, Message: u.Message, Date: u.Date})
	case *tg.UpdateShortChatMessage:
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerChat{ChatID: u.ChatID}, FromID: &tg.PeerUser{UserID: u.FromID}, Message: u.Message, Date: u.Date})
	}
	for _, value := range updates {
		switch u := value.(type) {
		case *tg.UpdateNewMessage:
			c.accept(u.Message)
		case *tg.UpdateNewChannelMessage:
			c.accept(u.Message)
		case *tg.UpdateEditMessage:
			c.accept(u.Message)
		case *tg.UpdateEditChannelMessage:
			c.accept(u.Message)
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

func (c *client) runSession(ctx context.Context, t *terminal, histories map[peerKey][]string) error {
	self, err := c.telegram.Self(ctx)
	if err != nil {
		return fmt.Errorf("load current account: %w", err)
	}
	c.remember([]tg.UserClass{self}, nil)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	refreshUnread := func() {
		go func() {
			unread, err := c.refreshTotalUnread(sessionCtx)
			if err != nil {
				if sessionCtx.Err() == nil {
					c.report(err)
				}
				return
			}
			t.updateLock(unread)
		}()
	}
	t.enableInactivity(c.cachedTotalUnread, refreshUnread)
	defer t.disableInactivity()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		err := c.updates.Run(sessionCtx, c.api, self.ID, updates.AuthOptions{
			OnStart: func(context.Context) { close(started) },
		})
		done <- err
		cancel()
	}()
	select {
	case err := <-done:
		return err
	case <-started:
	}
	uiErr := c.runModes(sessionCtx, t, histories)
	cancel()
	updateErr := <-done
	if updateErr != nil && !errors.Is(updateErr, context.Canceled) {
		return fmt.Errorf("receive updates: %w", updateErr)
	}
	return uiErr
}
