package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type chatID struct {
	backend string
	id      string
}

type chatEntry struct {
	ID      chatID
	Title   string
	Unread  int
	LastAt  time.Time
	IsDM    bool
	IsGroup bool
	IsSelf  bool
	Raw     any
}

type forwardReq struct {
	from chatEntry
	id   string
}

func (forwardReq) Error() string { return "forward message" }

type message struct {
	ID         string
	Sender     string
	SenderID   string
	Text       string
	Timestamp  time.Time
	Outgoing   bool
	Pinned     bool
	EditDate   int
	ReplyToID  string
	Forwarded  bool
	ViaBot     bool
	MediaLabel string
	Multiline  bool
	Reactions  []messageReaction
	Raw        any
}

type messageReaction struct {
	Emoji  string
	Count  int
	Chosen bool
}

type genericReactionUpdate struct {
	MessageID string
	Reactions []messageReaction
}

// backendEvent is a unified event delivered to subscribers.
type backendEvent struct {
	Kind     string                 // "message", "edit", "delete", "reaction"
	Chat     chatID                 // which chat the event belongs to
	Message  *message               // for "message" and "edit"
	DeleteID string                 // message ID for "delete"
	Reaction *genericReactionUpdate // for "reaction"
}

type backendSubscriber struct {
	filter func(chatID) bool
	ch     chan backendEvent
}

// subscriberSet manages a set of event subscribers. Each backend embeds one.
type subscriberSet struct {
	mu   sync.Mutex
	subs map[string]*backendSubscriber
}

func (ss *subscriberSet) add(id string, filter func(chatID) bool, bufSize int) <-chan backendEvent {
	ch := make(chan backendEvent, bufSize)
	ss.mu.Lock()
	if ss.subs == nil {
		ss.subs = make(map[string]*backendSubscriber)
	}
	ss.subs[id] = &backendSubscriber{filter: filter, ch: ch}
	ss.mu.Unlock()
	return ch
}

func (ss *subscriberSet) remove(id string) {
	ss.mu.Lock()
	if sub, ok := ss.subs[id]; ok {
		close(sub.ch)
		delete(ss.subs, id)
	}
	ss.mu.Unlock()
}

func (ss *subscriberSet) broadcast(ev backendEvent) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, sub := range ss.subs {
		if sub.filter != nil && !sub.filter(ev.Chat) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

type capabilities struct {
	Forward       bool
	DeleteForMe   bool
	OutboxReceipt bool
	ViewOnce      bool
	Reactions     bool
	Edit          bool
	Pin           bool
	Upload        bool
	Search        bool
	Typing        bool
	DeleteChat    bool
	Archive       bool
}

type backend interface {
	Name() string
	Capabilities() capabilities

	// Lifecycle
	Run(ctx context.Context) error
	Authenticate(ctx context.Context, prompt promptFunc, report func(error)) error
	Identity() accountIdentity
	AccountLabel() string
	AutoLockEnabled() bool

	// Chat list
	Dialogs(ctx context.Context, archived bool) ([]chatEntry, int, error)

	// Message operations
	History(ctx context.Context, ch chatEntry) ([]message, error)
	OlderHistory(ctx context.Context, ch chatEntry, beforeID string) ([]message, error)
	Send(ctx context.Context, ch chatEntry, text string, replyTo string) (message, error)
	SendUpload(ctx context.Context, ch chatEntry, path, caption, replyTo string) (message, error)
	UploadPlaceholder(path, caption string) message
	Editable(msg message) error
	Edit(ctx context.Context, ch chatEntry, msg message, text string) error
	DeleteMessages(ctx context.Context, ch chatEntry, ids []string, forEveryone bool) error
	DeleteChat(ctx context.Context, ch chatEntry, forEveryone bool) error
	MarkRead(ctx context.Context, ch chatEntry, upToID string) error
	SetTyping(ctx context.Context, ch chatEntry)

	// Reactions
	React(ctx context.Context, ch chatEntry, msg message, emoji string) ([]messageReaction, error)

	// Forwarding
	Forward(ctx context.Context, from chatEntry, to chatEntry, msgID string) error

	// Pin
	Pin(ctx context.Context, ch chatEntry, msg message, unpin bool) error

	// Search
	SearchMessages(ctx context.Context, ch chatEntry, query string) ([]message, error)

	// Real-time events
	Subscribe(ch chatEntry)
	Unsubscribe()
	Drain() (msgs []message, reactions []genericReactionUpdate, deletions []string)
	Changed() <-chan struct{}
	IsTyping() bool
	ClearTyping()

	// Read state
	ReadMarker(ch chatEntry, msg message) string
	RefreshReadState(ctx context.Context, ch chatEntry) error
	RefreshChatUnread(ctx context.Context, ch chatEntry) (int, error)
	CachedUnread(ch chatEntry) int
	InboxPosition(ch chatEntry) string

	// Media
	MediaPlayable(msg message) bool
	PlayMedia(ctx context.Context, t *terminal, msg message, player string) error
	MediaExec(ctx context.Context, msg message, player string) (cmd *exec.Cmd, cleanup func(), err error)
	MediaSaveable(msg message) (filename string, ok bool)
	SaveMedia(ctx context.Context, msg message, destDir string) error
	AcknowledgeMedia(ctx context.Context, msg message)

	// User search (for forward target selection, etc.)
	SearchUsers(ctx context.Context, t *terminal, recentChats []chatEntry) (chatEntry, bool, error)
	HandleForward(ctx context.Context, t *terminal, recentChats []chatEntry, fromChat chatEntry, msgID string)

	// Subscribers
	AddSubscriber(id string, filter func(chatID) bool, bufSize int) <-chan backendEvent
	RemoveSubscriber(id string)

	// Emoji
	RecentEmoji() []string
	RememberEmoji(value string)
	EmojiChoices(query string) []emojiOption
	ReactionChoices(query string) []emojiOption
}

// Generic rendering functions that work with any backend.

func genericMessageSender(msg message) string {
	if msg.Outgoing {
		return "you"
	}
	if msg.Sender != "" {
		return plain(msg.Sender)
	}
	return "Deleted Account"
}

func genericMessageText(msg message) string {
	text := msg.MediaLabel
	if text != "" && msg.Text != "" {
		text += " — " + msg.Text
	} else if text == "" {
		text = msg.Text
	}
	return plain(text)
}

func genericMessageTimestamp(t time.Time) string {
	return fmt.Sprintf("[%02d:%02d]", t.Hour(), t.Minute())
}

func genericMessageLine(msg message, ch chatEntry, byID map[string]*message) string {
	text := genericMessageText(msg)
	if reply := genericReplyLabel(msg, ch, byID); reply != "" {
		text = reply + " — " + text
	}
	sender := genericMessageSender(msg)
	nick := formatNick(sender)
	nickPart := "<" + nick + ">"
	if ch.IsGroup {
		nickPart = nickColor(sender) + nickPart + "\033[0m"
	}
	line := genericMessageTimestamp(msg.Timestamp) + " " + nickPart + " " + text
	if msg.Pinned {
		line += " [pinned]"
	}
	return line
}

func genericReplySummary(msg message, ch chatEntry) string {
	return "<" + genericMessageSender(msg) + "> " + shortExcerpt(genericMessageText(msg))
}

func genericReplyLabel(msg message, ch chatEntry, byID map[string]*message) string {
	if msg.ReplyToID == "" {
		return ""
	}
	if original, ok := byID[msg.ReplyToID]; ok && original != nil {
		return "↪ " + genericReplySummary(*original, ch)
	}
	return fmt.Sprintf("↪ message #%s", msg.ReplyToID)
}

func genericEditable(msg message) error {
	if !msg.Outgoing || msg.ID == "" || msg.ID == "0" {
		return fmt.Errorf("select one of your sent messages to edit")
	}
	if msg.Forwarded || msg.ViaBot || msg.MediaLabel != "" {
		return fmt.Errorf("this message cannot be edited")
	}
	if msg.Multiline {
		return fmt.Errorf("multiline messages cannot be edited in wiregram's single-line editor")
	}
	return nil
}

func formatMessageReactions(reactions []messageReaction) string {
	if len(reactions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reactions))
	for _, r := range reactions {
		if r.Emoji == "" || r.Count < 1 {
			continue
		}
		part := r.Emoji + " " + strconv.Itoa(r.Count)
		if r.Chosen {
			part += "*"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "  ")
}
