// Package mail is the IMAP email backend: one chat per correspondent, cached
// in wiregram.db for the retention window set in Settings.
package mail

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	fetched     map[string]bool // peers whose older mail the server has no more of
	recentEmoji []string

	// connMu guards the command connection and what it has selected.
	connMu      sync.Mutex
	conn        *imapclient.Client
	selected    string
	uidValidity uint32
	sentBox     string
	trashBox    string

	nudge  chan struct{}
	events backend.Bus
}

// NewMailBackend prepares an account on imapHost whose cache is keyed by key.
func NewMailBackend(imapHost string, st *store.DB, key string) (*MailBackend, error) {
	db, err := openMailDB(st, key)
	if err != nil {
		return nil, err
	}
	imapHost = strings.ToLower(strings.TrimSpace(imapHost))
	return &MailBackend{
		Account: Identity{IMAPHost: imapHost, SMTPHost: smtpHostFor(imapHost)},
		Store:   st,
		db:      db,
		fetched: make(map[string]bool),
		nudge:   make(chan struct{}, 1),
	}, nil
}

func chatID(peer string) backend.ChatID { return backend.ChatID{Backend: backendName, Id: peer} }

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
	return backend.Capabilities{DeleteForMe: true, Search: true, MultilineCompose: true}
}

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

func (b *MailBackend) AutoLockEnabled() bool { return false }

func (b *MailBackend) Logout(ctx context.Context) error {
	if b.Account.Auth == authGoogle && b.Account.RefreshToken != "" {
		return b.revokeGoogle(ctx)
	}
	return nil
}

func (b *MailBackend) Dialogs(ctx context.Context, archived bool) ([]backend.ChatEntry, int, error) {
	if archived {
		return nil, 0, nil
	}
	cs, err := b.db.correspondents(200)
	if err != nil {
		return nil, 0, err
	}
	entries := make([]backend.ChatEntry, 0, len(cs))
	for _, c := range cs {
		entries = append(entries, c.entry())
	}
	return entries, 0, nil
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
	if p, ok := b.db.byMessageID(r.InReplyTo); ok && p.ID != r.ID {
		m.ReplyToID = p.msgID()
	}
	var labels []string
	for _, p := range r.Parts {
		kind := "attachment"
		if strings.HasPrefix(p.Type, "image/") {
			kind = "image"
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

// history returns peer's page before before, asking the server for older
// mail once the cache runs short.
func (b *MailBackend) history(ctx context.Context, peer string, before row) ([]backend.Message, error) {
	rows, err := b.db.page(peer, before, pageSize)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	done := b.fetched[peer]
	b.mu.Unlock()
	if len(rows) < pageSize && !done {
		var from time.Time
		if len(rows) > 0 {
			from = rows[0].Date
		} else if before.ID != 0 {
			from = before.Date
		}
		if err := b.fetchPeer(ctx, peer, from, pageSize); err != nil {
			if len(rows) == 0 {
				return nil, err
			}
			b.report(err)
		} else if more, err := b.db.page(peer, before, pageSize); err == nil {
			if len(more) == len(rows) {
				b.mu.Lock()
				b.fetched[peer] = true
				b.mu.Unlock()
			}
			rows = more
		}
	}
	return b.toMessages(rows), nil
}

func (b *MailBackend) History(ctx context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	return b.history(ctx, ch.ID.Id, row{})
}

func (b *MailBackend) OlderHistory(ctx context.Context, ch backend.ChatEntry, beforeID string) ([]backend.Message, error) {
	id, ok := rowIDOf(beforeID)
	if !ok {
		return nil, nil
	}
	before, err := b.db.byRowID(id)
	if err != nil {
		return nil, err
	}
	return b.history(ctx, ch.ID.Id, before)
}

func (b *MailBackend) UnreadMessages(ctx context.Context, ch backend.ChatEntry, limit int) ([]backend.Message, error) {
	rows, err := b.db.query(`peer = ? AND seen = 0 AND outgoing = 0 ORDER BY date DESC, id DESC LIMIT ?`,
		ch.ID.Id, limit)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date.Before(rows[j].Date) })
	return b.toMessages(rows), nil
}

func (b *MailBackend) Send(ctx context.Context, ch backend.ChatEntry, text string, replyTo string) (backend.Message, error) {
	peer := ch.ID.Id
	var parent *row
	if id, ok := rowIDOf(replyTo); ok {
		r, err := b.db.byRowID(id)
		if err != nil {
			return backend.Message{}, err
		}
		parent = &r
	} else if rows, err := b.db.page(peer, row{}, 1); err == nil && len(rows) > 0 {
		parent = &rows[0]
	}
	d := compose(peer, text, parent)
	if strings.TrimSpace(d.Subject) == "" {
		return backend.Message{}, fmt.Errorf("a new mail needs a subject on its first line")
	}
	var err error
	if d.MessageID, err = newMessageID(b.Account.Email); err != nil {
		return backend.Message{}, err
	}
	data, err := d.encode(b.Account.Email)
	if err != nil {
		return backend.Message{}, err
	}
	if err := b.submit(ctx, d.To, data); err != nil {
		return backend.Message{}, err
	}
	if !isGmail(b.Account.IMAPHost) {
		// Gmail files what it sends; other servers leave that to the client.
		if err := b.fileSent(ctx, data, d.Date); err != nil {
			b.report(err)
		}
	}
	r := row{
		Mailbox: sentPlaceholder, UID: uint32(time.Now().UnixNano()), MessageID: d.MessageID,
		InReplyTo: d.InReplyTo, Peer: peer, FromAddr: b.Account.Email, To: peer,
		Subject: d.Subject, Date: d.Date, Seen: true, Outgoing: true, Body: d.Body,
	}
	if _, err := b.db.insert(&r); err != nil {
		return backend.Message{}, err
	}
	b.wake()
	return b.toMessage(r), nil
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
	return backend.Message{}, fmt.Errorf("sending attachments is not supported for email yet")
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
	var deleted []string
	defer func() {
		if len(deleted) > 0 {
			b.events.Publish(backend.Event{Kind: backend.EventDelete, Chat: ch.ID, Deleted: deleted})
		}
	}()
	for _, id := range ids {
		rowID, ok := rowIDOf(id)
		if !ok {
			continue
		}
		r, err := b.db.byRowID(rowID)
		if err != nil {
			return err
		}
		if r.Mailbox != sentPlaceholder {
			err := b.withConn(ctx, func(c *imapclient.Client) error {
				if b.trashBox == "" {
					return fmt.Errorf("this server has no Trash folder")
				}
				uv, err := b.selectBox(c, r.Mailbox)
				if err != nil {
					return err
				}
				if uv != r.UIDValidity {
					return nil // the mailbox was rebuilt; the next sync forgets the row
				}
				if _, err := c.Move(imap.UIDSetNum(imap.UID(r.UID)), b.trashBox).Wait(); err != nil {
					return fmt.Errorf("move mail to Trash: %w", err)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if err := b.db.delete(r.ID); err != nil {
			return err
		}
		deleted = append(deleted, id)
	}
	return nil
}

func (b *MailBackend) DeleteChat(ctx context.Context, ch backend.ChatEntry, forEveryone bool) error {
	return fmt.Errorf("deleting a whole conversation is not supported for email")
}

func (b *MailBackend) MarkRead(ctx context.Context, ch backend.ChatEntry, upToID string) error {
	id, ok := rowIDOf(upToID)
	if !ok {
		return nil
	}
	upTo, err := b.db.byRowID(id)
	if err != nil {
		return err
	}
	rows, err := b.db.unseenUpTo(ch.ID.Id, upTo)
	if err != nil || len(rows) == 0 {
		return err
	}
	byBox := make(map[string][]row)
	for _, r := range rows {
		byBox[r.Mailbox] = append(byBox[r.Mailbox], r)
	}
	var seen []int64
	err = b.withConn(ctx, func(c *imapclient.Client) error {
		for box, rows := range byBox {
			uv, err := b.selectBox(c, box)
			if err != nil {
				return err
			}
			var set imap.UIDSet
			for _, r := range rows {
				if r.UIDValidity == uv {
					set.AddNum(imap.UID(r.UID))
					seen = append(seen, r.ID)
				}
			}
			if len(set) == 0 {
				continue
			}
			if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true,
				Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
				return fmt.Errorf("mark mail read: %w", err)
			}
		}
		return nil
	})
	if markErr := b.db.markSeen(seen); markErr != nil && err == nil {
		err = markErr
	}
	b.events.Publish(backend.Event{Kind: backend.EventUnread, Chat: ch.ID})
	return err
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

// SearchMessages asks the server, so mail older than the cache is found too.
func (b *MailBackend) SearchMessages(ctx context.Context, ch backend.ChatEntry, query string) ([]backend.Message, error) {
	peer := ch.ID.Id
	sent := b.sentMailbox()
	type hit struct {
		box  string
		uv   uint32
		uids []uint32
	}
	var hits []hit
	err := b.withConn(ctx, func(c *imapclient.Client) error {
		for _, box := range []struct{ name, header string }{{inbox, "From"}, {sent, "To"}} {
			if box.name == "" {
				continue
			}
			uv, err := b.selectBox(c, box.name)
			if err != nil {
				return err
			}
			found, err := c.UIDSearch(&imap.SearchCriteria{
				Header: []imap.SearchCriteriaHeaderField{{Key: box.header, Value: peer}},
				Text:   []string{query},
			}, nil).Wait()
			if err != nil {
				return fmt.Errorf("search mail: %w", err)
			}
			all := found.AllUIDs()
			all = all[max(0, len(all)-searchLimit):]
			var missing []imap.UID
			h := hit{box: box.name, uv: uv}
			for _, uid := range all {
				h.uids = append(h.uids, uint32(uid))
				if !b.db.hasUID(box.name, uv, uint32(uid)) {
					missing = append(missing, uid)
				}
			}
			rows, err := b.fetchRows(c, box.name, uv, missing)
			if err != nil {
				return err
			}
			for i := range rows {
				if _, err := b.db.insert(&rows[i]); err != nil {
					return err
				}
			}
			hits = append(hits, h)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rows []row
	for _, h := range hits {
		got, err := b.db.byUIDs(h.box, h.uv, h.uids)
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date.After(rows[j].Date) })
	return b.toMessages(rows), nil
}

func (b *MailBackend) SetActive(ch backend.ChatEntry) {}

func (b *MailBackend) Events() *backend.Bus { return &b.events }

func (b *MailBackend) IsTyping() bool { return false }

func (b *MailBackend) ClearTyping() {}

func (b *MailBackend) ReadMarker(ch backend.ChatEntry, msg backend.Message) string { return "" }

func (b *MailBackend) RefreshReadState(ctx context.Context, ch backend.ChatEntry) error { return nil }

func (b *MailBackend) RefreshChatUnread(ctx context.Context, ch backend.ChatEntry) (int, error) {
	return b.db.unread(ch.ID.Id), nil
}

func (b *MailBackend) CachedUnread(ch backend.ChatEntry) int { return b.db.unread(ch.ID.Id) }

func (b *MailBackend) InboxPosition(ch backend.ChatEntry) string {
	return b.db.readPosition(ch.ID.Id)
}

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
	if r.Mailbox == sentPlaceholder {
		return fmt.Errorf("this mail is still being filed in Sent; try again shortly")
	}
	p := r.Parts[0]
	var raw []byte
	err := b.withConn(ctx, func(c *imapclient.Client) error {
		uv, err := b.selectBox(c, r.Mailbox)
		if err != nil {
			return err
		}
		if uv != r.UIDValidity {
			return fmt.Errorf("this mail moved on the server; reopen the chat")
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
		return err
	}
	data, err := decodePart(raw, p.Type, p.Encoding, "")
	if err != nil {
		return err
	}
	dest := filepath.Join(destDir, safeName(p.Name))
	if err := os.WriteFile(dest, data, 0600); err != nil {
		return fmt.Errorf("save attachment: %w", err)
	}
	return nil
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
