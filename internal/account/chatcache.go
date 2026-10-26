package account

import (
	"slices"
	"sync"
	"time"

	"wiregram/internal/backend"
)

// chatCacheChats bounds how many chats an account keeps, the least recently
// used going first; chatCacheMessages matches what an open chat holds.
const (
	chatCacheChats    = 64
	chatCacheMessages = 500
)

type cachedChat struct {
	messages []backend.Message // oldest first
	intact   bool              // no event for the chat was missed since Put
	used     time.Time
}

// ChatCache keeps the messages of chats opened this session, so reopening one
// draws at once while the server is asked again. Its subscription keeps it
// current while the account is online; a missed event leaves chats not
// intact, and then only the server's newest page is trusted. Nothing is ever
// final: Telegram, Matrix and Mattermost let messages be deleted at any age.
type ChatCache struct {
	mu    sync.Mutex
	chats map[backend.ChatID]*cachedChat
	// breaks counts the times events may have been missed, so a chat that
	// was open through one is not stored as intact.
	breaks uint64
}

func newChatCache() *ChatCache {
	return &ChatCache{chats: make(map[backend.ChatID]*cachedChat)}
}

// Get returns a copy of the chat's messages and whether every event since
// they were stored was applied.
func (c *ChatCache) Get(id backend.ChatID) (msgs []backend.Message, intact, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cc := c.chats[id]
	if cc == nil {
		return nil, false, false
	}
	cc.used = time.Now()
	return slices.Clone(cc.messages), cc.intact, true
}

// Breaks is a token for Put: take it when the chat's history is loaded.
func (c *ChatCache) Breaks() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.breaks
}

// Put stores what an open chat held as it closed. It is intact when no
// events may have been missed since since, a token from Breaks.
func (c *ChatCache) Put(id backend.ChatID, msgs []backend.Message, since uint64) {
	if len(msgs) > chatCacheMessages {
		msgs = msgs[len(msgs)-chatCacheMessages:]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chats[id] = &cachedChat{
		messages: slices.Clone(msgs), intact: c.breaks == since, used: time.Now(),
	}
	if len(c.chats) <= chatCacheChats {
		return
	}
	var oldest backend.ChatID
	var at time.Time
	for key, cc := range c.chats {
		if at.IsZero() || cc.used.Before(at) {
			oldest, at = key, cc.used
		}
	}
	delete(c.chats, oldest)
}

// Clear drops every chat, for when the cache is switched off.
func (c *ChatCache) Clear() {
	c.mu.Lock()
	c.chats = make(map[backend.ChatID]*cachedChat)
	c.mu.Unlock()
}

// broken marks every chat as having missed events.
func (c *ChatCache) broken() {
	c.mu.Lock()
	c.brokenLocked()
	c.mu.Unlock()
}

func (c *ChatCache) brokenLocked() {
	c.breaks++
	for _, cc := range c.chats {
		cc.intact = false
	}
}

// follow applies sub's events until it is closed, then marks the chats
// broken: whatever happens next goes unseen until a new subscription.
func (c *ChatCache) follow(sub *backend.Subscription) {
	for ev := range sub.C {
		if sub.Dropped() > 0 {
			c.broken()
		}
		c.apply(ev)
	}
	c.broken()
}

// apply mirrors what an open chat does with ev. Chats not cached are left
// out: one event would make a history with a hole in it.
func (c *ChatCache) apply(ev backend.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Kind == backend.EventResync && ev.Chat == (backend.ChatID{}) {
		c.brokenLocked()
		return
	}
	if ev.Kind == backend.EventDelete && ev.Chat == (backend.ChatID{}) {
		// Telegram does not say which private chat or group a deletion came
		// from, and channels number their messages apart, so an ID alone may
		// name another chat's message: let the server settle it.
		for _, cc := range c.chats {
			if slices.ContainsFunc(cc.messages, func(m backend.Message) bool {
				return slices.Contains(ev.Deleted, m.ID)
			}) {
				cc.intact = false
			}
		}
		return
	}
	if ev.Kind == backend.EventResync {
		c.breaks++ // the chat may be open, outside the cache
	}
	cc := c.chats[ev.Chat]
	if cc == nil {
		return
	}
	switch ev.Kind {
	case backend.EventMessage, backend.EventEdit:
		msg := ev.Message
		if msg == nil {
			return
		}
		for i, prev := range cc.messages {
			if prev.ID == msg.ID {
				if msg.EditDate >= prev.EditDate {
					cc.messages[i] = *msg
				}
				return
			}
		}
		if ev.Kind == backend.EventEdit {
			return
		}
		cc.messages = append(cc.messages, *msg)
		if len(cc.messages) > chatCacheMessages {
			cc.messages = slices.Clone(cc.messages[len(cc.messages)-chatCacheMessages:])
		}
	case backend.EventDelete:
		cc.messages = slices.DeleteFunc(cc.messages, func(m backend.Message) bool {
			return slices.Contains(ev.Deleted, m.ID)
		})
	case backend.EventReaction:
		if ev.Reaction == nil {
			return
		}
		for i := range cc.messages {
			if cc.messages[i].ID == ev.Reaction.MessageID {
				cc.messages[i].Reactions = ev.Reaction.Reactions
			}
		}
	case backend.EventResync:
		cc.intact = false
	}
}
