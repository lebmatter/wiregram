package account

import (
	"context"

	"wiregram/internal/backend"
)

type inboxSub struct {
	backend backend.Backend
	sub     *backend.Subscription
}

// WatchInbox keeps one subscription on every online account, so each account
// consumes its events whatever the UI shows, and counts the incoming messages
// the user has not seen. It returns when ctx ends.
func (m *AccountManager) WatchInbox(ctx context.Context) {
	subs := make(map[*Account]inboxSub)
	defer func() {
		for _, s := range subs {
			s.sub.Close()
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
			current.sub.Close()
			delete(subs, a)
		}
		if !online {
			continue
		}
		sub := s.Backend.Events().Subscribe(backend.Filter{Kinds: backend.EventMessage})
		subs[a] = inboxSub{s.Backend, sub}
		go a.countUnseen(sub)
	}
	for a, s := range subs {
		if !live[a] {
			s.sub.Close()
			delete(subs, a)
		}
	}
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
