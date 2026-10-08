package backend

import (
	"strings"
	"sync"
)

// EventKind is a bit so a Filter can select several kinds at once.
type EventKind uint8

const (
	EventMessage EventKind = 1 << iota
	EventEdit
	EventDelete
	EventReaction
	EventTyping // wake-up only; read the state with IsTyping
	EventUnread // unread counts changed; no payload. Without a Chat, chats may also have appeared
)

// Event is the one format every backend publishes.
type Event struct {
	Kind     EventKind
	Chat     ChatID
	DM       bool                   // one-to-one with a real person: never self, bot or group
	Message  *Message               // EventMessage, EventEdit
	Deleted  []string               // EventDelete
	Reaction *GenericReactionUpdate // EventReaction
}

// Filter selects the events a subscriber receives. The zero Filter matches
// everything.
type Filter struct {
	Kinds    EventKind // bitmask; 0 means all kinds
	Chat     ChatID    // zero means every chat
	DMOnly   bool
	TextOnly bool // messages and edits must carry text and no media
}

func (f Filter) Match(ev Event) bool {
	if f.Kinds != 0 && f.Kinds&ev.Kind == 0 {
		return false
	}
	if f.Chat != (ChatID{}) && f.Chat != ev.Chat {
		return false
	}
	if f.DMOnly && !ev.DM {
		return false
	}
	if f.TextOnly && (ev.Kind == EventMessage || ev.Kind == EventEdit) {
		m := ev.Message
		if m == nil || m.MediaLabel != "" || strings.TrimSpace(m.Text) == "" {
			return false
		}
	}
	return true
}

// maxQueued bounds a stalled subscriber's backlog; beyond it the oldest events
// are dropped and counted.
const maxQueued = 4096

// Bus fans one backend's events out to its subscribers. Publish never blocks:
// each subscriber has its own queue drained by its own goroutine.
//
// Lock order: Bus.mu, then Subscription.mu. Neither is held across a blocking
// channel operation or while taking any other lock, and backends must release
// their own locks before calling Publish.
type Bus struct {
	mu   sync.Mutex
	subs map[*Subscription]Filter
}

func (b *Bus) Subscribe(f Filter) *Subscription {
	out := make(chan Event)
	s := &Subscription{
		C: out, bus: b,
		ready: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[*Subscription]Filter)
	}
	b.subs[s] = f
	b.mu.Unlock()
	go s.pump(out)
	return s
}

func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s, f := range b.subs {
		if f.Match(ev) {
			s.push(ev)
		}
	}
}

// Subscription delivers matching events on C, in publish order. C is closed
// after Close.
type Subscription struct {
	C <-chan Event

	bus     *Bus
	mu      sync.Mutex
	queue   []Event
	dropped int
	ready   chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (s *Subscription) push(ev Event) {
	s.mu.Lock()
	if len(s.queue) >= maxQueued {
		s.queue = s.queue[1:]
		s.dropped++
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// pump is the only writer to C and the only goroutine that closes it.
func (s *Subscription) pump(out chan<- Event) {
	defer close(out)
	for {
		select {
		case <-s.done:
			return
		case <-s.ready:
		}
		for {
			s.mu.Lock()
			if len(s.queue) == 0 {
				s.mu.Unlock()
				break
			}
			ev := s.queue[0]
			s.queue[0] = Event{}
			s.queue = s.queue[1:]
			s.mu.Unlock()
			select {
			case out <- ev:
			case <-s.done:
				return
			}
		}
	}
}

// Dropped returns how many events overflowed the queue since the last call.
func (s *Subscription) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropped
	s.dropped = 0
	return n
}

// Close unsubscribes and ends delivery. It is safe to call more than once.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
		close(s.done)
	})
}
