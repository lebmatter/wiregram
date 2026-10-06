package backend

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"wiregram/internal/config"
	"wiregram/internal/terminal"
	"wiregram/internal/textfmt"
)

type AccountIdentity struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

type ChatID struct {
	Backend string
	Id      string
}

type ChatEntry struct {
	ID      ChatID
	Title   string
	Unread  int
	LastAt  time.Time
	IsDM    bool
	IsGroup bool
	IsSelf  bool
	Raw     any
}

type ForwardReq struct {
	From ChatEntry
	Id   string
}

func (ForwardReq) Error() string { return "forward message" }

type Message struct {
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
	Reactions  []MessageReaction
	Raw        any
}

// userCandidate is one row in the user search.
type UserCandidate struct {
	Key   string // identity for de-duplicating local and remote results
	Label string
	Match string // lowercase text matched against the query
	Raw   any
}

// userSearchBase is what the search shows before and while typing: base when
// the query is empty, local filtered by the query until remote results arrive.
type UserSearchBase struct {
	Heading string
	Base    []UserCandidate
	Local   []UserCandidate
}

type MessageReaction struct {
	Emoji  string
	Count  int
	Chosen bool
}

type GenericReactionUpdate struct {
	MessageID string
	Reactions []MessageReaction
}

// backendEvent is a unified event delivered to subscribers.
type BackendEvent struct {
	Kind     string                 // "message", "edit", "delete", "reaction"
	Chat     ChatID                 // which chat the event belongs to
	Message  *Message               // for "message" and "edit"
	DeleteID string                 // message ID for "delete"
	Reaction *GenericReactionUpdate // for "reaction"
}

type backendSubscriber struct {
	filter func(ChatID) bool
	ch     chan BackendEvent
}

// subscriberSet manages a set of event subscribers. Each backend embeds one.
type SubscriberSet struct {
	mu   sync.Mutex
	subs map[string]*backendSubscriber
}

func (ss *SubscriberSet) Add(id string, filter func(ChatID) bool, bufSize int) <-chan BackendEvent {
	ch := make(chan BackendEvent, bufSize)
	ss.mu.Lock()
	if ss.subs == nil {
		ss.subs = make(map[string]*backendSubscriber)
	}
	ss.subs[id] = &backendSubscriber{filter: filter, ch: ch}
	ss.mu.Unlock()
	return ch
}

func (ss *SubscriberSet) Remove(id string) {
	ss.mu.Lock()
	if sub, ok := ss.subs[id]; ok {
		close(sub.ch)
		delete(ss.subs, id)
	}
	ss.mu.Unlock()
}

func (ss *SubscriberSet) Broadcast(ev BackendEvent) {
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

type Capabilities struct {
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

type Backend interface {
	Name() string
	Capabilities() Capabilities

	// Lifecycle
	Run(ctx context.Context) error
	Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error
	Identity() AccountIdentity
	AccountLabel() string
	AutoLockEnabled() bool

	// Chat list
	Dialogs(ctx context.Context, archived bool) ([]ChatEntry, int, error)

	// Message operations
	History(ctx context.Context, ch ChatEntry) ([]Message, error)
	OlderHistory(ctx context.Context, ch ChatEntry, beforeID string) ([]Message, error)
	Send(ctx context.Context, ch ChatEntry, text string, replyTo string) (Message, error)
	SendUpload(ctx context.Context, ch ChatEntry, path, caption, replyTo string) (Message, error)
	UploadPlaceholder(path, caption string) Message
	Editable(msg Message) error
	Edit(ctx context.Context, ch ChatEntry, msg Message, text string) error
	DeleteMessages(ctx context.Context, ch ChatEntry, ids []string, forEveryone bool) error
	DeleteChat(ctx context.Context, ch ChatEntry, forEveryone bool) error
	MarkRead(ctx context.Context, ch ChatEntry, upToID string) error
	SetTyping(ctx context.Context, ch ChatEntry)

	// Reactions
	React(ctx context.Context, ch ChatEntry, msg Message, emoji string) ([]MessageReaction, error)

	// Forwarding
	Forward(ctx context.Context, from ChatEntry, to ChatEntry, msgID string) error

	// Pin
	Pin(ctx context.Context, ch ChatEntry, msg Message, unpin bool) error

	// Search
	SearchMessages(ctx context.Context, ch ChatEntry, query string) ([]Message, error)

	// Real-time events
	Subscribe(ch ChatEntry)
	Unsubscribe()
	Drain() (msgs []Message, reactions []GenericReactionUpdate, deletions []string)
	Changed() <-chan struct{}
	IsTyping() bool
	ClearTyping()

	// Read state
	ReadMarker(ch ChatEntry, msg Message) string
	RefreshReadState(ctx context.Context, ch ChatEntry) error
	RefreshChatUnread(ctx context.Context, ch ChatEntry) (int, error)
	CachedUnread(ch ChatEntry) int
	InboxPosition(ch ChatEntry) string

	// Media
	MediaPlayable(msg Message) bool
	PlayMedia(ctx context.Context, t *terminal.Terminal, msg Message, player string) error
	MediaExec(ctx context.Context, msg Message, player string) (cmd *exec.Cmd, cleanup func(), err error)
	MediaSaveable(msg Message) (filename string, ok bool)
	SaveMedia(ctx context.Context, msg Message, destDir string) error
	AcknowledgeMedia(ctx context.Context, msg Message)

	// User search. The TUI owns filtering and debounce; backends supply data.
	UserSearchBase(ctx context.Context, recentChats []ChatEntry) (UserSearchBase, error)
	UserSearchRemote(ctx context.Context, query string) ([]UserCandidate, error)
	OpenUserCandidate(ctx context.Context, c UserCandidate) (ChatEntry, error)
	HandleForward(ctx context.Context, t *terminal.Terminal, recentChats []ChatEntry, fromChat ChatEntry, msgID string)

	// Subscribers
	AddSubscriber(id string, filter func(ChatID) bool, bufSize int) <-chan BackendEvent
	RemoveSubscriber(id string)

	// Emoji
	RecentEmoji() []string
	RememberEmoji(value string)
	EmojiChoices(query string) []EmojiOption
	ReactionChoices(query string) []EmojiOption
}

// Generic rendering functions that work with any backend.

func GenericMessageSender(msg Message) string {
	if msg.Outgoing {
		return "you"
	}
	if msg.Sender != "" {
		return textfmt.Plain(msg.Sender)
	}
	return "Deleted Account"
}

func GenericMessageText(msg Message) string {
	text := msg.MediaLabel
	if text != "" && msg.Text != "" {
		text += " — " + msg.Text
	} else if text == "" {
		text = msg.Text
	}
	return textfmt.Plain(text)
}

func genericMessageTimestamp(t time.Time) string {
	return fmt.Sprintf("[%02d:%02d]", t.Hour(), t.Minute())
}

func GenericMessageLine(msg Message, ch ChatEntry, byID map[string]*Message) string {
	text := GenericMessageText(msg)
	if reply := genericReplyLabel(msg, ch, byID); reply != "" {
		text = reply + " — " + text
	}
	line := GenericMessagePrefix(msg, ch) + " " + text
	if msg.Pinned {
		line += " [pinned]"
	}
	return line
}

// genericMessagePrefix is the fixed-width "[HH:MM] <nick>" column.
func GenericMessagePrefix(msg Message, ch ChatEntry) string {
	sender := GenericMessageSender(msg)
	nickPart := "<" + textfmt.FormatNick(sender) + ">"
	if ch.IsGroup {
		nickPart = textfmt.NickColor(sender) + nickPart + "\033[0m"
	}
	return genericMessageTimestamp(msg.Timestamp) + " " + nickPart
}

func GenericReplySummary(msg Message, ch ChatEntry) string {
	return "<" + GenericMessageSender(msg) + "> " + textfmt.ShortExcerpt(GenericMessageText(msg))
}

func genericReplyLabel(msg Message, ch ChatEntry, byID map[string]*Message) string {
	if msg.ReplyToID == "" {
		return ""
	}
	if original, ok := byID[msg.ReplyToID]; ok && original != nil {
		return "↪ " + GenericReplySummary(*original, ch)
	}
	return fmt.Sprintf("↪ message #%s", msg.ReplyToID)
}

func GenericEditable(msg Message) error {
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

func FormatMessageReactions(reactions []MessageReaction) string {
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
