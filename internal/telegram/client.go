package telegram

import (
	"context"
	"errors"
	"fmt"
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
	"wiregram/internal/store"
)

type peerKey struct {
	kind byte
	id   int64
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
	AccountID      int64  // Set before publishing this connection to the UI.
	AccountName    string // Set before publishing this connection to the UI.
	cfg            config.Config
	Telegram       *telegram.Client
	Updates        *updates.Manager
	Api            *tg.Client
	mu             sync.Mutex
	users          map[int64]*tg.User
	chats          map[peerKey]string
	reads          map[peerKey]readState
	unreads        map[peerKey]int
	contactUsers   []*tg.User
	frequentUsers  []*tg.User
	contactsLoaded bool
	frequentLoaded bool
	emojiRecent    []string
	active         peerKey
	peerTyping     bool
	autoLock       bool
	events         backend.Bus
	Report         func(error)
}

// sessionStorage keeps the MTProto session in wiregram.db.
type sessionStorage struct {
	db      *store.DB
	account string
}

func (s sessionStorage) LoadSession(context.Context) ([]byte, error) {
	data, err := s.db.Get(s.account, store.TelegramSession)
	if errors.Is(err, store.ErrNotFound) {
		return nil, session.ErrNotFound
	}
	return data, err
}

func (s sessionStorage) StoreSession(_ context.Context, data []byte) error {
	return s.db.Put(s.account, store.TelegramSession, data)
}

func NewClient(cfg config.Config, db *store.DB, account string, report func(error)) (*Client, error) {
	c := &Client{
		users: make(map[int64]*tg.User), chats: make(map[peerKey]string),
		unreads: make(map[peerKey]int), autoLock: cfg.AutoLock == 1,
		cfg: cfg,
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
		SessionStorage: sessionStorage{db: db, account: account},
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

func (c *Client) setActive(key peerKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active, c.peerTyping = key, false
}

// isDM reports whether key is a one-to-one chat with a person: not Saved
// Messages and not a bot. Short updates carry no user object, so a user who
// is not cached yet counts as a person. Callers hold c.mu.
func (c *Client) isDM(key peerKey) bool {
	if key.kind != 'u' || key.id == c.AccountID {
		return false
	}
	u := c.users[key.id]
	return u == nil || (!u.Bot && !u.Self)
}

func (c *Client) rememberUnread(key peerKey, unread int) {
	c.mu.Lock()
	previous := c.unreads[key]
	c.unreads[key] = unread
	c.mu.Unlock()
	if previous != unread {
		c.events.Publish(backend.Event{Kind: backend.EventUnread, Chat: peerKeyToID(key)})
	}
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
	key := keyOf(m.PeerID)
	c.mu.Lock()
	if newMessage && !m.Out {
		c.unreads[key]++
	}
	dm := c.isDM(key)
	c.mu.Unlock()

	kind := backend.EventMessage
	if !newMessage {
		kind = backend.EventEdit
	}
	msg := tgToMessage(c, m)
	c.events.Publish(backend.Event{Kind: kind, Chat: peerKeyToID(key), DM: dm, Message: &msg})
}

func (c *Client) acceptTyping(userID int64) {
	key := peerKey{'u', userID}
	c.mu.Lock()
	if c.active == key {
		c.peerTyping = true
	}
	c.mu.Unlock()
	c.events.Publish(backend.Event{Kind: backend.EventTyping, Chat: peerKeyToID(key)})
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

// acceptDeletion publishes deleted IDs. Telegram does not say which private
// chat or group they came from, so key is zero except for channels and the
// event then carries no chat.
func (c *Client) acceptDeletion(key peerKey, ids []int) {
	deleted := make([]string, len(ids))
	for i, id := range ids {
		deleted[i] = strconv.Itoa(id)
	}
	ev := backend.Event{Kind: backend.EventDelete, Deleted: deleted}
	if key != (peerKey{}) {
		ev.Chat = peerKeyToID(key)
	}
	c.events.Publish(ev)
}

func (c *Client) acceptReaction(update *tg.UpdateMessageReactions) {
	key := keyOf(update.Peer)
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
	c.events.Publish(backend.Event{Kind: backend.EventReaction, Chat: peerKeyToID(key), Reaction: &ru})
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
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerUser{UserID: u.UserID}, FromID: &tg.PeerUser{UserID: u.UserID}, Message: u.Message, Date: u.Date, Entities: u.Entities}, true)
	case *tg.UpdateShortChatMessage:
		c.accept(&tg.Message{ID: u.ID, Out: u.Out, PeerID: &tg.PeerChat{ChatID: u.ChatID}, FromID: &tg.PeerUser{UserID: u.FromID}, Message: u.Message, Date: u.Date, Entities: u.Entities}, true)
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
			c.acceptDeletion(peerKey{}, u.Messages)
		case *tg.UpdateDeleteChannelMessages:
			c.acceptDeletion(peerKey{'c', u.ChannelID}, u.Messages)
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
