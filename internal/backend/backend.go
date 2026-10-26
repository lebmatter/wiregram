package backend

import (
	"context"
	"errors"
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
	// IsChannel marks a broadcast channel only its admins post in.
	IsChannel bool
	// Section is the server's grouping of the chat, such as its Mattermost
	// team; the sidebar shows it as a section and drops "[Section] " from Title.
	Section string
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
	// Mentioned marks a message that mentions the user or the whole chat.
	Mentioned bool
	// Agent names the Chat Agent that sent an outgoing message; the TUI
	// fills it in from wiregram.db.
	Agent string
	Raw   any
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
	Kind    CandidateKind
	Tag     string // a team or other scope, drawn at the row's right edge
	Raw     any
}

// CandidateKind is what a search result opens: a person, a channel or a group.
type CandidateKind int

const (
	CandidatePerson CandidateKind = iota
	CandidateChannel
	CandidateGroup
)

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
	// ChannelSearch puts channels beside people in the user search.
	ChannelSearch bool
	Typing        bool
	DeleteChat    bool
	Archive       bool
	// RevokeWindow is how long after sending the user may delete a message
	// for everyone; zero means no limit.
	RevokeWindow time.Duration
}

type Backend interface {
	Name() string
	Capabilities() Capabilities

	// Lifecycle
	Run(ctx context.Context) error
	Authenticate(ctx context.Context, prompt config.PromptFunc, report func(error)) error
	Identity() AccountIdentity
	AccountLabel() string

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

// DMFinder is implemented by backends that can search everyone a private chat
// could be had with, so a bot can be set to answer one that has not started.
// OpenUserCandidate on a result knows the DM's ID without telling the person
// anything; on other backends a new DM's ID is not known until it starts.
type DMFinder interface {
	FindPeople(ctx context.Context, query string) ([]UserCandidate, error)
}

// SelfChatter is implemented by backends with a private chat with oneself
// (Telegram's Saved Messages), which the sidebar's You row opens.
type SelfChatter interface {
	SelfChat(ctx context.Context) (ChatEntry, error)
}

// Profiler is implemented by backends whose server can describe a user, so
// clicking a sender opens their profile.
type Profiler interface {
	// Profile describes the user a message's SenderID names.
	Profile(ctx context.Context, userID string) (Profile, error)
}

// Profile is what the backend's user API tells about one person, as label
// and value rows in the order shown; empty values are left out.
type Profile struct {
	Name   string
	Fields []ProfileField
}

type ProfileField struct {
	Label string
	Value string
}

// Add appends a row unless value is blank.
func (p *Profile) Add(label, value string) {
	if value = strings.TrimSpace(value); value != "" {
		p.Fields = append(p.Fields, ProfileField{label, value})
	}
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

// MentionFetcher is implemented by backends that can list recent messages
// mentioning the user outside DMs.
type MentionFetcher interface {
	// Mentions returns at most limit mentions, newest first.
	Mentions(ctx context.Context, limit int) ([]Mention, error)
}

// Mention is one message that mentions the user, with the chat it is in.
type Mention struct {
	Chat    ChatEntry
	Message Message
}

// QuoteFetcher is implemented by backends that can load messages by ID, so a
// reply to a message older than the loaded history can still quote it.
type QuoteFetcher interface {
	// Messages returns those of ids that still exist, in any order.
	Messages(ctx context.Context, ch ChatEntry, ids []string) ([]Message, error)
}

// BotRegistrar is implemented by backends whose account can create bot
// accounts on the network, so a wiregram bot can reply as itself.
type BotRegistrar interface {
	// RegisterBot creates the bot, or takes back one of the user's own that
	// was unregistered, and issues it a token.
	RegisterBot(ctx context.Context, username, displayName string) (BotAccount, error)
	// RenameBot changes a registered bot's username and display name.
	RenameBot(ctx context.Context, bot BotAccount, username, displayName string) error
	// RotateBotToken issues a new token and revokes the old one.
	RotateBotToken(ctx context.Context, bot BotAccount) (BotAccount, error)
	// UnregisterBot revokes the bot's token and disables it.
	UnregisterBot(ctx context.Context, bot BotAccount) error
	// ConnectBot signs in as the bot; the caller runs the returned backend.
	ConnectBot(ctx context.Context, bot BotAccount) (Backend, error)
}

// BotManaged is implemented by backends whose bots are created and
// controlled through a manager bot the user owns (Telegram's managed bots).
type BotManaged interface {
	// CheckManager signs in as the manager bot and confirms it may manage bots.
	CheckManager(ctx context.Context, token string) (BotAccount, error)
	// WithManager is the registrar acting through manager; with none, the
	// calls that need it fail with ErrNoBotManager.
	WithManager(manager BotAccount) BotRegistrar
}

// ErrNoBotManager means the account needs a manager bot set first.
var ErrNoBotManager = errors.New("set a manager bot for this account first")

// BotAccount is a bot registered on the network and the token it signs in with.
type BotAccount struct {
	UserID   string
	Username string
	TokenID  string
	Token    string
}

// ThreadFetcher is implemented by backends that keep threads on the server,
// so the thread pane can show replies older than the loaded history.
type ThreadFetcher interface {
	// Thread returns the root and every reply to it, oldest first.
	Thread(ctx context.Context, ch ChatEntry, rootID string) ([]Message, error)
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

// Syncer is implemented by backends that fill their chat list in the
// background after connecting. Once the first sync is done they publish an
// EventUnread without a Chat, so the list reloads.
type Syncer interface {
	// Syncing reports whether the first sync since connecting is still running.
	Syncing() bool
}

// Labeler is implemented by backends with labels: Gmail labels, or another
// mail server's folders. The sidebar lists those the user chose.
type Labeler interface {
	// Labels returns every label by name, shown in the sidebar or not.
	Labels() ([]Label, error)
	// ShowLabel puts a label in the sidebar or takes it out.
	ShowLabel(name string, shown bool) error
}

// Mailbox is implemented by email. Its chats are folders (Dialogs), and a
// folder opens as a list of conversations instead of a chat. Message IDs are
// those its threads carry.
type Mailbox interface {
	// Threads returns the folder's conversations, newest first. A query asks
	// the server to search the folder instead.
	Threads(ctx context.Context, folder ChatEntry, query string) ([]MailThread, error)
	SetUnread(ctx context.Context, folder ChatEntry, ids []string, unread bool) error
	// MoveMail moves mails out of folder into the folder named to.
	MoveMail(ctx context.Context, folder ChatEntry, ids []string, to string) error
	// TrashFolder and ArchiveFolder name where d and a move mail; "" when
	// the server has none.
	TrashFolder() string
	ArchiveFolder() string
	// Draft starts a reply, reply to all or forward of msg.
	Draft(msg Message, kind DraftKind) MailDraft
	SendMail(ctx context.Context, d MailDraft) error
	// Addresses lists the addresses the account has mailed with, newest first.
	Addresses() ([]string, error)
}

// MailThread is one conversation, its mails oldest first. With names the
// other side: who wrote last, or whom the user wrote to. Unread counts the
// incoming mails not read yet.
type MailThread struct {
	Subject  string
	With     string
	Messages []Message
	Unread   int
}

type DraftKind int

const (
	DraftReply DraftKind = iota
	DraftReplyAll
	DraftForward
)

// MailDraft is a mail being written. To and Cc are comma-separated
// addresses; Attach are local files. ReplyTo is the ID of the mail answered,
// which threads the reply; Forward the ID of the mail whose attachments go
// along.
type MailDraft struct {
	To, Cc, Subject, Body string
	Attach                []string
	ReplyTo, Forward      string
}

// Label is one of a Labeler's labels.
type Label struct {
	Name   string
	Shown  bool
	Unread int
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
	nickPart := MessageNickColor(msg, ch) + "<" + textfmt.FormatNick(GenericMessageSender(msg)) +
		">\033[0m"
	return genericMessageTimestamp(msg.Timestamp) + " " + nickPart
}

// MessageNickColor is the SGR a message's sender is drawn in: per nick in
// groups, one colour each side in private chats.
func MessageNickColor(msg Message, ch ChatEntry) string {
	if ch.IsGroup {
		return textfmt.NickColor(GenericMessageSender(msg))
	}
	if msg.Outgoing {
		return textfmt.DMSelfColor
	}
	return textfmt.DMPeerColor
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
