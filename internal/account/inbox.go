package account

import (
	"context"

	"wiregram/internal/backend"
)

type inboxSub struct {
	backend backend.Backend
	sub     *backend.Subscription
	chats   *backend.Subscription // feeds the account's ChatCache
}

// WatchInbox keeps subscriptions on every online account, so each account
// consumes its events whatever the UI shows: one counts the incoming messages
// the user has not seen, the other keeps the account's ChatCache current. It
// returns when ctx ends.
func (m *AccountManager) WatchInbox(ctx context.Context) {
	subs := make(map[*Account]inboxSub)
	defer func() {
		for _, s := range subs {
			s.close()
		}
	}()
	for {
		m.syncInbox(subs)
		select {
		case <-ctx.Done():
			return
		case <-m.inboxChanged:
		}
	}
}

func (m *AccountManager) syncInbox(subs map[*Account]inboxSub) {
	live := make(map[*Account]bool)
	for _, a := range m.List() {
		live[a] = true
		s := a.Snapshot()
		online := s.Backend != nil && s.Status == "online"
		current, ok := subs[a]
		if ok && online && current.backend == s.Backend {
			continue
		}
		if ok {
			current.close()
			delete(subs, a)
		}
		if !online {
			continue
		}
		sub := s.Backend.Events().Subscribe(backend.Filter{Kinds: backend.EventMessage})
		chats := s.Backend.Events().Subscribe(backend.Filter{Kinds: backend.EventMessage |
			backend.EventEdit | backend.EventDelete | backend.EventReaction | backend.EventResync})
		subs[a] = inboxSub{s.Backend, sub, chats}
		go a.countUnseen(sub)
		go a.Chats.follow(chats)
	}
	for a, s := range subs {
		if !live[a] {
			s.close()
			delete(subs, a)
		}
	}
}

func (s inboxSub) close() {
	s.sub.Close()
	s.chats.Close()
}

// countUnseen ends when the subscription is closed. It skips notify: the UI
// polls account state, and a signal per message would wake every listener.
func (a *Account) countUnseen(sub *backend.Subscription) {
	for ev := range sub.C {
		if ev.Message != nil && !ev.Message.Outgoing {
			a.mu.Lock()
			a.state.Unseen++
			a.mu.Unlock()
		}
	}
}

// ClearUnseen resets the count when the user opens or leaves the account.
func (a *Account) ClearUnseen() {
	a.mu.Lock()
	a.state.Unseen = 0
	a.mu.Unlock()
}
