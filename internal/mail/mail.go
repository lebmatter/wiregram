// Package mail is the IMAP email backend: its chats are folders (the inbox,
// Sent, the archive and the labels shown), each opened as conversations, and
// its mail is cached in wiregram.db for the retention window set in Settings.
package mail

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/oauth2"

	"wiregram/internal/backend"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

const (
	backendName = "email"
	pageSize    = 25
	searchLimit = 50
)

type MailBackend struct {
	Account Identity
	Store   *store.DB
	// Report shows a background error as the account's notice.
	Report func(error)

	db *mailDB

	mu          sync.Mutex
	ctx         context.Context
	tokens      oauth2.TokenSource
	recentEmoji []string
	// The special folders, named by findMailboxes. They sit under mu, not
	// connMu, so the screen never waits on a sync to read them.
	sentBox    string
	trashBox   string
	archiveBox string

	// connMu guards the command connection and what it has selected.
	connMu      sync.Mutex
	conn        *imapclient.Client
	selected    string
	uidValidity uint32

	nudge      chan struct{}
	labelNudge chan struct{}
	events     backend.Bus
	// synced is set once the first sync of the inbox has finished.
	synced atomic.Bool
}

// NewMailBackend prepares an account on imapHost whose cache is keyed by key.
func NewMailBackend(imapHost string, st *store.DB, key string) (*MailBackend, error) {
	db, err := openMailDB(st, key)
	if err != nil {
		return nil, err
	}
	imapHost = strings.ToLower(strings.TrimSpace(imapHost))
	return &MailBackend{
		Account:    Identity{IMAPHost: imapHost, SMTPHost: smtpHostFor(imapHost)},
		Store:      st,
		db:         db,
		nudge:      make(chan struct{}, 1),
		labelNudge: make(chan struct{}, 1),
	}, nil
}

// chatID names a chat: a folder, or for the CLI's send, a correspondent.
func chatID(id string) backend.ChatID { return backend.ChatID{Backend: backendName, Id: id} }

func (b *MailBackend) report(err error) {
	if b.Report != nil {
		b.Report(err)
	}
}

func (b *MailBackend) runCtx() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil {
		return context.Background()
	}
	return b.ctx
}

func (b *MailBackend) Name() string { return backendName }

func (b *MailBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{}
}

func (b *MailBackend) Syncing() bool { return !b.synced.Load() }

// Start begins syncing and watching the inbox until ctx ends.
func (b *MailBackend) Start(ctx context.Context) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	go b.runSync(ctx)
	go b.runIdle(ctx)
	context.AfterFunc(ctx, func() {
		b.connMu.Lock()
		if b.conn != nil {
			b.conn.Close()
			b.conn = nil
		}
		b.connMu.Unlock()
	})
}

func (b *MailBackend) Run(ctx context.Context) error {
	b.Start(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (b *MailBackend) Identity() backend.AccountIdentity {
	return backend.AccountIdentity{Name: b.Account.Email}
}

func (b *MailBackend) AccountLabel() string { return "[EM] " + b.Account.Email }

func (b *MailBackend) Logout(ctx context.Context) error {
	if b.Account.Auth == authGoogle && b.Account.RefreshToken != "" {
		return b.revokeGoogle(ctx)
	}
	return nil
}

// Dialogs lists the folders: the inbox, Sent and the archive once the
// server has named them, then the labels shown in the sidebar.
func (b *MailBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}
	sent, archive := b.sentMailbox(), b.archiveMailbox()
	entries := []backend.ChatEntry{b.folderEntry(inbox, "Inbox")}
	if sent != "" {
		entries = append(entries, b.folderEntry(sent, "Sent"))
	}
	if archive != "" && !isGmail(b.Account.IMAPHost) {
		entries = append(entries, b.folderEntry(archive, "Archive"))
	}
	labels, err := b.db.labels()
	if err != nil {
		return nil, 0, err
	}
	for _, l := range labels {
		if l.Shown {
			entries = append(entries, backend.ChatEntry{ID: chatID(l.Name), Title: l.Name,
				Unread: l.Unread})
		}
	}
	return entries, 0, nil
}

func (b *MailBackend) folderEntry(box, title string) backend.ChatEntry {
	return backend.ChatEntry{ID: chatID(box), Title: title, Unread: b.db.folderUnread(box)}
}

func (b *MailBackend) Labels() ([]backend.Label, error) { return b.db.labels() }

// ShowLabel saves the choice; a label put in the sidebar syncs at once.
func (b *MailBackend) ShowLabel(name string, shown bool) error {
	if err := b.db.showLabel(name, shown); err != nil {
		return err
	}
	if shown {
		b.wakeLabels()
	}
	return nil
}

func (c correspondent) entry() backend.ChatEntry {
	title := c.Name
	if title == "" {
		title = c.Peer
	}
	return backend.ChatEntry{ID: chatID(c.Peer), Title: title, Unread: c.Unread, LastAt: c.Last,
		IsDM: true, Raw: c}
}

func (b *MailBackend) toMessage(r row) backend.Message {
	sender := r.FromName
	if sender == "" {
		sender = r.FromAddr
	}
	m := backend.Message{
		ID: r.msgID(), Sender: sender, SenderID: r.FromAddr, Text: trimQuoted(r.Body),
		Timestamp: r.Date, Outgoing: r.Outgoing, Subject: r.Subject, Multiline: true, Raw: r,
	}
	var labels []string
	for _, p := range r.Parts {
		kind := "attachment"
		if strings.HasPrefix(p.Type, "image/") {
			kind = "image"
		} else if strings.HasPrefix(p.Type, "video/") {
			kind = "video"
		}
		labels = append(labels, fmt.Sprintf("[%s] %s, %s", kind, textfmt.Plain(p.Name),
			textfmt.FormatBytes(p.Size)))
	}
	m.MediaLabel = strings.Join(labels, ", ")
	if len(r.Parts) > 0 {
		switch r.Parts[0].Type {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			m.Image = r.Parts[0].Size <= backend.MaxInlineImage
		}
	}
	return m
}

func (b *MailBackend) toMessages(rows []row) []backend.Message {
	out := make([]backend.Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, b.toMessage(r))
	}
	return out
}

var reWrote = regexp.MustCompile(`(?i)^(on .*|.*)wrote:$`)

// trimQuoted drops the quoted original a reply ends with, so the chat shows
// only what is new; the reader keeps the whole mail.
func trimQuoted(body string) string {
	lines := strings.Split(body, "\n")
	end := len(lines)
	quoted := false
	for end > 0 {
		line := strings.TrimSpace(lines[end-1])
		if line == "" || strings.HasPrefix(line, ">") {
			quoted = quoted || line != ""
			end--
			continue
		}
		break
	}
	if !quoted {
		return body
	}
	if end > 0 && reWrote.MatchString(strings.TrimSpace(lines[end-1])) {
		end--
		// Clients wrap a long attribution before "wrote:".
		if strings.EqualFold(strings.TrimSpace(lines[end]), "wrote:") && end > 0 {
			end--
		}
	}
	trimmed := strings.TrimSpace(strings.Join(lines[:end], "\n"))
	if trimmed == "" {
		return body
	}
	return trimmed
}

// FullMessage gives the reader a mail's headers and its complete text.
func (b *MailBackend) FullMessage(msg backend.Message) ([]string, string) {
	r, ok := msg.Raw.(row)
	if !ok {
		return nil, msg.Text
	}
	headers := []string{"From: " + strings.TrimSpace(r.FromName+" <"+r.FromAddr+">")}
	if r.To != "" {
		headers = append(headers, "To: "+r.To)
	}
	if r.Cc != "" {
		headers = append(headers, "Cc: "+r.Cc)
	}
	headers = append(headers, "Date: "+r.Date.Local().Format("Mon 2 Jan 2006 15:04"))
	if msg.MediaLabel != "" {
		headers = append(headers, "Attached: "+msg.MediaLabel)
	}
	return headers, r.Body
}

// History is the newest mail in a folder, oldest first.
func (b *MailBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	rows, err := b.db.folderRows(ch.ID.Id, false)
	if err != nil {
		return nil, err
	}
	return b.toMessages(rows[max(0, len(rows)-pageSize):]), nil
}

func (b *MailBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	return nil, nil
}

func (b *MailBackend) Messages(ctx context.Context, ch backend.ChatEntry, ids []string) ([]backend.Message, error) {
	var rows []row
	for _, id := range ids {
		n, ok := rowIDOf(id)
		if !ok {
			continue
		}
		if r, err := b.db.byRowID(n); err == nil {
			rows = append(rows, r)
		}
	}
	return b.toMessages(rows), nil
}

func (b *MailBackend) UnreadMessages(ctx context.Context, ch backend.ChatEntry, limit int) ([]backend.Message, error) {
	rows, err := b.db.folderRows(ch.ID.Id, false)
	if err != nil {
		return nil, err
	}
	var unread []row
	for _, r := range rows {
		if !r.Seen && !r.Outgoing {
			unread = append(unread, r)
		}
	}
	return b.toMessages(unread[max(0, len(unread)-limit):]), nil
}

// Send is the CLI's send to an address: it answers the newest mail with
// them, unless the text starts a new conversation (see compose).
func (b *MailBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	peer := ch.ID.Id
	if !strings.Contains(peer, "@") {
		return backend.Message{}, fmt.Errorf("send email to an address, not a folder")
	}
	var parent *row
	if id, ok := rowIDOf(replyTo); ok {
		r, err := b.db.byRowID(id)
		if err != nil {
			return backend.Message{}, err
		}
		parent = &r
	} else if r, ok := b.db.latest(peer); ok {
		parent = &r
	}
	d := compose(peer, text, parent)
	if strings.TrimSpace(d.Subject) == "" {
		return backend.Message{}, fmt.Errorf("a new mail needs a subject on its first line")
	}
	r, err := b.deliver(ctx, d)
	if err != nil {
		return backend.Message{}, err
	}
	return b.toMessage(r), nil
}

// deliver sends d, files it in Sent where the server does not, and caches it
// until the Sent folder's sync sees it.
func (b *MailBackend) deliver(ctx context.Context, d draft) (row, error) {
	to, toHeader, err := recipients(d.To)
	if err != nil {
		return row{}, err
	}
	cc, ccHeader, err := recipients(d.Cc)
	if err != nil {
		return row{}, err
	}
	d.To, d.Cc = toHeader, ccHeader
	if len(to) == 0 {
		return row{}, fmt.Errorf("add a recipient in To")
	}
	if d.MessageID, err = newMessageID(b.Account.Email); err != nil {
		return row{}, err
	}
	data, err := d.encode(b.Account.Email)
	if err != nil {
		return row{}, err
	}
	if err := b.submit(ctx, append(to, cc...), data); err != nil {
		return row{}, err
	}
	if !isGmail(b.Account.IMAPHost) {
		// Gmail files what it sends; other servers leave that to the client.
		if err := b.fileSent(ctx, data, d.Date); err != nil {
			b.report(err)
		}
	}
	r := row{
		Mailbox: sentPlaceholder, UID: uint32(time.Now().UnixNano()), MessageID: d.MessageID,
		InReplyTo: d.InReplyTo, Peer: strings.ToLower(to[0]), FromAddr: b.Account.Email,
		To: d.To, Cc: d.Cc, Subject: d.Subject, Date: d.Date, Seen: true, Outgoing: true,
		Body: d.Body,
	}
	for _, a := range d.Attach {
		r.Parts = append(r.Parts, part{Name: a.Name, Type: a.Type, Size: int64(len(a.Data))})
	}
	if _, err := b.db.insert(&r); err != nil {
		return row{}, err
	}
	b.events.Publish(backend.Event{Kind: backend.EventUnread})
	b.wake()
	return r, nil
}

func (b *MailBackend) fileSent(ctx context.Context, data []byte, date time.Time) error {
	sent := b.sentMailbox()
	if sent == "" {
		return nil
	}
	return b.withConn(ctx, func(c *imapclient.Client) error {
		cmd := c.Append(sent, int64(len(data)), &imap.AppendOptions{
			Flags: []imap.Flag{imap.FlagSeen}, Time: date})
		if _, err := cmd.Write(data); err != nil {
			cmd.Close()
			return fmt.Errorf("file sent mail: %w", err)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("file sent mail: %w", err)
		}
		if _, err := cmd.Wait(); err != nil {
			return fmt.Errorf("file sent mail: %w", err)
		}
		return nil
	})
}

func (b *MailBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, path, caption, replyTo string) (backend.Message, error) {
	return backend.Message{}, fmt.Errorf("attach files to a mail in the composer")
}

func (b *MailBackend) UploadPlaceholder(path, caption string) backend.Message {
	return backend.Message{Outgoing: true, Timestamp: time.Now(), MediaLabel: "[uploading]", Text: caption}
}

func (b *MailBackend) Editable(msg backend.Message) error {
	return fmt.Errorf("sent mail cannot be edited")
}

func (b *MailBackend) Edit(ctx context.Context, ch backend.ChatEntry, msg backend.Message, text string) error {
	return fmt.Errorf("sent mail cannot be edited")
}

// DeleteMessages moves the mails to the server's Trash, whichever scope
// was asked for: mail cannot be unsent.
func (b *MailBackend) DeleteMessages(ctx context.Context, ch backend.ChatEntry, ids []string, forEveryone bool) error {
	return b.MoveMail(ctx, ch, ids, b.TrashFolder())
}

func (b *MailBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("deleting a whole conversation is not supported for email")
}

func (b *MailBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	return b.SetUnread(ctx, ch, []string{upToID}, false)
}

func (b *MailBackend) SetTyping(ctx context.Context, ch backend.ChatEntry) {}

func (b *MailBackend) React(ctx context.Context, ch backend.ChatEntry, msg backend.Message, emoji string) ([]backend.MessageReaction, error) {
	return nil, fmt.Errorf("reactions are not supported for email")
}

func (b *MailBackend) Forward(ctx context.Context, from backend.ChatEntry, to backend.ChatEntry, msgID string) error {
	return fmt.Errorf("forwarding is not supported for email yet")
}

func (b *MailBackend) Pin(ctx context.Context, ch backend.ChatEntry, msg backend.Message, unpin bool) error {
	return fmt.Errorf("pinning is not supported for email")
}

// SearchMessages searches the folder on the server, so mail older than the
// cache is found too.
func (b *MailBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
	threads, err := b.Threads(ctx, ch, query)
	if err != nil {
		return nil, err
	}
	var out []backend.Message
	for _, t := range threads {
		out = append(out, t.Messages...)
	}
	return out, nil
}

func (b *MailBackend) SetActive(ch backend.ChatEntry) {}

func (b *MailBackend) Events() *backend.Bus { return &b.events }

func (b *MailBackend) IsTyping() bool { return false }

func (b *MailBackend) ClearTyping() {}

func (b *MailBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string { return "" }

func (b *MailBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error { return nil }

func (b *MailBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	return b.db.folderUnread(ch.ID.Id), nil
}

func (b *MailBackend) CachedUnread(ch backend.ChatEntry) int { return b.db.folderUnread(ch.ID.Id) }

func (b *MailBackend) InboxPosition(ch backend.ChatEntry) string { return "" }

func (b *MailBackend) MediaPlayable(msg backend.Message) bool { return false }

func (b *MailBackend) MediaExec(ctx context.Context, msg backend.Message, player string) (*exec.Cmd, func(), error) {
	return nil, nil, fmt.Errorf("media playback is not supported for email")
}

// MediaSaveable offers a mail's first attachment.
func (b *MailBackend) MediaSaveable(msg backend.Message) (string, bool) {
	r, ok := msg.Raw.(row)
	if !ok || len(r.Parts) == 0 {
		return "", false
	}
	return safeName(r.Parts[0].Name), true
}

// safeName keeps an attachment's name from leaving the folder it is saved in.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		return "attachment"
	}
	return name
}

func (b *MailBackend) SaveMedia(ctx context.Context, msg backend.Message, destDir string) error {
	r, ok := msg.Raw.(row)
	if !ok || len(r.Parts) == 0 {
		return fmt.Errorf("this mail has no attachment")
	}
	data, err := b.fetchPart(ctx, r, r.Parts[0])
	if err != nil {
		return err
	}
	dest := filepath.Join(destDir, safeName(r.Parts[0].Name))
	if err := os.WriteFile(dest, data, 0600); err != nil {
		return fmt.Errorf("save attachment: %w", err)
	}
	return nil
}

// fetchPart downloads and decodes one attachment of r.
func (b *MailBackend) fetchPart(ctx context.Context, r row, p part) ([]byte, error) {
	if r.Mailbox == sentPlaceholder {
		return nil, fmt.Errorf("this mail is still being filed in Sent; try again shortly")
	}
	var raw []byte
	err := b.withConn(ctx, func(c *imapclient.Client) error {
		uv, err := b.selectBox(c, r.Mailbox)
		if err != nil {
			return err
		}
		if uv != r.UIDValidity {
			return fmt.Errorf("this mail moved on the server; reopen the folder")
		}
		item := &imap.FetchItemBodySection{Part: p.Section, Peek: true}
		msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(r.UID)), &imap.FetchOptions{
			UID: true, BodySection: []*imap.FetchItemBodySection{item}}).Collect()
		if err != nil {
			return fmt.Errorf("download attachment: %w", err)
		}
		if len(msgs) == 0 {
			return fmt.Errorf("this mail is no longer on the server")
		}
		raw = msgs[0].FindBodySection(item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return decodePart(raw, p.Type, p.Encoding, "")
}

func (b *MailBackend) AcknowledgeMedia(ctx context.Context, msg backend.Message) {}

func (c correspondent) candidate() backend.UserCandidate {
	label := c.Peer
	if c.Name != "" {
		label = c.Name + "  " + c.Peer
	}
	return backend.UserCandidate{Key: c.Peer, Label: label,
		Match: strings.ToLower(c.Name + " " + c.Peer), Raw: c}
}

func (b *MailBackend) UserSearchBase(ctx context.Context, recentChats []backend.ChatEntry) (backend.UserSearchBase, error) {
	cs, err := b.db.correspondents(500)
	if err != nil {
		return backend.UserSearchBase{}, err
	}
	var base []backend.UserCandidate
	for _, c := range cs {
		base = append(base, c.candidate())
	}
	return backend.UserSearchBase{Heading: "People you mail", Base: base, Local: base}, nil
}

// UserSearchRemote turns a typed address into a new conversation.
func (b *MailBackend) UserSearchRemote(ctx context.Context, query string) ([]backend.UserCandidate, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(query))
	if err != nil || !strings.Contains(addr.Address, "@") {
		return nil, nil
	}
	c := correspondent{Peer: strings.ToLower(addr.Address), Name: addr.Name}
	return []backend.UserCandidate{c.candidate()}, nil
}

func (b *MailBackend) OpenUserCandidate(ctx context.Context, c backend.UserCandidate) (backend.ChatEntry, error) {
	who, ok := c.Raw.(correspondent)
	if !ok {
		return backend.ChatEntry{}, fmt.Errorf("open conversation: unexpected search result")
	}
	return who.entry(), nil
}

func (b *MailBackend) RecentEmoji() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.recentEmoji...)
}

func (b *MailBackend) RememberEmoji(value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recent := []string{value}
	for _, v := range b.recentEmoji {
		if v != value && len(recent) < 20 {
			recent = append(recent, v)
		}
	}
	b.recentEmoji = recent
}

func (b *MailBackend) EmojiChoices(query string) []backend.EmojiOption {
	catalog := backend.EmojiCatalog()
	if query == "" {
		var choices []backend.EmojiOption
		for _, v := range b.RecentEmoji() {
			choices = append(choices, backend.EmojiOption{Value: v, Name: v})
		}
		if len(choices) == 0 {
			return catalog[:min(10, len(catalog))]
		}
		return choices
	}
	q := strings.ToLower(query)
	var matches []backend.EmojiOption
	for _, e := range catalog {
		if strings.Contains(strings.ToLower(e.Name), q) && len(matches) < 10 {
			matches = append(matches, e)
		}
	}
	return matches
}

func (b *MailBackend) ReactionChoices(query string) []backend.EmojiOption {
	return nil
}
