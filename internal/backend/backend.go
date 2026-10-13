package backend

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"wiregram/internal/config"
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
	// Image marks a still image that SaveMedia fetches and that is small
	// enough (MaxInlineImage) to draw inline.
	Image     bool
	Multiline bool
	// Subject is an email's subject line, empty for chat messages.
	Subject   string
	Reactions []MessageReaction
	Preview   *LinkPreview
	// Quote is the replied-to message as the reply itself carries it, for
	// backends whose originals cannot be fetched by ID.
	Quote *Message
	// Spans style ranges of Text: bold, code, links and the like.
	Spans []Span
	// Source is the text as written, markup included, when Text has had the
	// markup taken out; an edit starts from it.
	Source string
	Raw    any
}

// MaxInlineImage is the largest image file, in bytes, drawn inline.
const MaxInlineImage = 10 << 20

// LinkPreview is a server-supplied summary of a link in a message.
type LinkPreview struct {
	URL         string
	Site        string
	Title       string
	Description string
}

// userCandidate is one row in the user search.
type UserCandidate struct {
	Key   string // identity for de-duplicating local and remote results
	Label string
	Match string // lowercase text matched against the query
	// Mention is the text a pick inserts into the composer, for Mentioner.
	Mention string
	Raw     any
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
	// MultilineCompose gives the chat a multiline composer that sends with
	// Ctrl+S, and a reader for long messages.
	MultilineCompose bool
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

	// Real-time events. Every event for every chat is published on Events,
	// whatever is open. SetActive names the open chat (zero entry for none) so
	// the backend can suppress its unread count and scope IsTyping.
	Events() *Bus
	SetActive(ch ChatEntry)
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
	MediaExec(ctx context.Context, msg Message, player string) (cmd *exec.Cmd, cleanup func(), err error)
	MediaSaveable(msg Message) (filename string, ok bool)
	SaveMedia(ctx context.Context, msg Message, destDir string) error
	AcknowledgeMedia(ctx context.Context, msg Message)

	// User search. The TUI owns filtering and debounce; backends supply data.
	UserSearchBase(ctx context.Context, recentChats []ChatEntry) (UserSearchBase, error)
	UserSearchRemote(ctx context.Context, query string) ([]UserCandidate, error)
	OpenUserCandidate(ctx context.Context, c UserCandidate) (ChatEntry, error)

	// Emoji
	RecentEmoji() []string
	RememberEmoji(value string)
	EmojiChoices(query string) []EmojiOption
	ReactionChoices(query string) []EmojiOption
}

// Mentioner is implemented by backends that can suggest the people of a group
// chat to mention in it. MentionSearch returns nil when MentionBase already
// lists everyone, leaving the filtering to the TUI.
type Mentioner interface {
	CanMention(ch ChatEntry) bool
	MentionBase(ctx context.Context, ch ChatEntry) (UserSearchBase, error)
	MentionSearch(ctx context.Context, ch ChatEntry, query string) ([]UserCandidate, error)
}

// LogoutBackend is implemented by backends that can end their server session,
// so a removed account no longer shows among the user's active sessions.
type LogoutBackend interface {
	Logout(ctx context.Context) error
}

// UnreadFetcher is implemented by backends whose API can fetch a chat's
// unread messages directly, cheaper than a History page.
type UnreadFetcher interface {
	// UnreadMessages returns at most limit of the newest incoming unread
	// messages, oldest first.
	UnreadMessages(ctx context.Context, ch ChatEntry, limit int) ([]Message, error)
}

// QuoteFetcher is implemented by backends that can load messages by ID, so a
// reply to a message older than the loaded history can still quote it.
type QuoteFetcher interface {
	// Messages returns those of ids that still exist, in any order.
	Messages(ctx context.Context, ch ChatEntry, ids []string) ([]Message, error)
}

// LinkPreviewer is implemented by backends whose server can summarise a URL
// on request, for messages that arrive without a preview.
type LinkPreviewer interface {
	// LinkPreview returns nil when the server has nothing for url.
	LinkPreview(ctx context.Context, url string) (*LinkPreview, error)
}

// FullMessager is implemented by backends whose messages are too long for
// the chat, so the reader can show all of one.
type FullMessager interface {
	// FullMessage returns header lines and the complete text of msg.
	FullMessage(msg Message) (headers []string, body string)
}

// NewestIncoming keeps the last limit messages the user did not send.
func NewestIncoming(msgs []Message, limit int) []Message {
	var in []Message
	for _, msg := range msgs {
		if !msg.Outgoing {
			in = append(in, msg)
		}
	}
	return in[max(0, len(in)-limit):]
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
