package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/updates/hook"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"wiregram/internal/backend"
	"wiregram/internal/config"
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

type Client struct {
	AccountID        int64  // Set before publishing this connection to the UI.
	AccountName      string // Set before publishing this connection to the UI.
	cfg              config.Config
	Telegram         *telegram.Client
	Updates          *updates.Manager
	Api              *tg.Client
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
	pendingDeletions []int
	peerTyping       bool
	autoLock         bool
	changed          chan struct{}
	subs             backend.SubscriberSet
	Report           func(error)
}

func NewClient(cfg config.Config, dir string, report func(error)) (*Client, error) {
	dir = filepath.Join(dir, "session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	c := &Client{
		users: make(map[int64]*tg.User), chats: make(map[peerKey]string),
		unreads: make(map[peerKey]int), autoLock: cfg.AutoLock == 1,
		changed: make(chan struct{}, 1), cfg: cfg,
	}
	middleware := telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			for {
				err := next.Invoke(ctx, input, output)
				if wait, ok := tgerr.AsFloodWait(err); ok {
					report(fmt.Errorf("Rate limited. Retry in %ds.", int(wait.Seconds())))
					if err := backend.WaitFor(ctx, wait); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
	})
	c.Updates = updates.New(updates.Config{Handler: telegram.UpdateHandlerFunc(c.handleUpdates)})
	c.Telegram = telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(dir, "session.json")},
		UpdateHandler:  c.Updates,
		Middlewares:    []telegram.Middleware{middleware, hook.UpdateHook(c.Updates.Handle)},
	})
	c.Api = c.Telegram.API()
	c.Report = report
	return c, nil
}

func (c *Client) Remember(users []tg.UserClass, chats []tg.ChatClass) {
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

func (c *Client) subscribe(key peerKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active, c.pending, c.pendingReactions, c.peerTyping = key, nil, nil, false
}

func (c *Client) drain() ([]*tg.Message, []reactionUpdate, []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages := c.pending
	reactions := c.pendingReactions
	deletions := c.pendingDeletions
	c.pending = nil
	c.pendingReactions = nil
	c.pendingDeletions = nil
	return messages, reactions, deletions
}

func (c *Client) rememberUnread(key peerKey, unread int) {
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

func (c *Client) cachedChatUnread(key peerKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unreads[key]
}

func (c *Client) accept(value tg.MessageClass, newMessage bool) {
	m, ok := value.(*tg.Message)
	if !ok {
		return
	}
	c.mu.Lock()
	key := keyOf(m.PeerID)
	if newMessage && !m.Out {
		c.unreads[key]++
	}
	if c.active != (peerKey{}) && key == c.active {
		c.pending = append(c.pending, m)
		select {
		case c.changed <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()

	kind := "message"
	if !newMessage {
		kind = "edit"
	}
	msg := tgToMessage(c, m)
	c.subs.Broadcast(backend.BackendEvent{
		Kind: kind, Chat: peerKeyToID(key), DM: key.kind == 'u', Message: &msg,
	})
}

func (c *Client) acceptTyping(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == (peerKey{}) || c.active != (peerKey{'u', userID}) {
		return
	}
	c.peerTyping = true
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *Client) clearTyping() {
	c.mu.Lock()
	c.peerTyping = false
	c.mu.Unlock()
}

func (c *Client) isTyping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peerTyping
}

func (c *Client) acceptDeletion(ids []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == (peerKey{}) {
		return
	}
	c.pendingDeletions = append(c.pendingDeletions, ids...)
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *Client) acceptReaction(update *tg.UpdateMessageReactions) {
	key := keyOf(update.Peer)
	c.mu.Lock()
	if c.active != (peerKey{}) && key == c.active {
		c.pendingReactions = append(c.pendingReactions, reactionUpdate{update.MsgID, update.Reactions})
		select {
		case c.changed <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()

	var reactions []backend.MessageReaction
	for _, count := range update.Reactions.Results {
		label := reactionLabel(count.Reaction)
		if label == "" {
			continue
		}
		_, chosen := count.GetChosenOrder()
		reactions = append(reactions, backend.MessageReaction{Emoji: label, Count: count.Count, Chosen: chosen})
	}
	ru := backend.GenericReactionUpdate{MessageID: strconv.Itoa(update.MsgID), Reactions: reactions}
	c.subs.Broadcast(backend.BackendEvent{Kind: "reaction", Chat: peerKeyToID(key), Reaction: &ru})
}

func (c *Client) handleUpdates(_ context.Context, value tg.UpdatesClass) error {
	var updates []tg.UpdateClass
	switch u := value.(type) {
	case *tg.Updates:
		c.Remember(u.Users, u.Chats)
		updates = u.Updates
	case *tg.UpdatesCombined:
		c.Remember(u.Users, u.Chats)
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
		case *tg.UpdateDeleteMessages:
			c.acceptDeletion(u.Messages)
		case *tg.UpdateDeleteChannelMessages:
			c.acceptDeletion(u.Messages)
		case *tg.UpdateUserTyping:
			if _, ok := u.Action.(*tg.SendMessageCancelAction); ok {
				c.clearTyping()
			} else {
				c.acceptTyping(u.UserID)
			}
		}
	}
	return nil
}
