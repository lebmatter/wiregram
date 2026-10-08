package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"wiregram/internal/backend"
)

const (
	inbox = "INBOX"
	// initialSyncLimit caps the first sync of a mailbox to its newest mails.
	initialSyncLimit = 2000
	fetchBatch       = 50
	// idleRestart re-issues IDLE before Gmail drops it at about 29 minutes.
	idleRestart  = 25 * time.Minute
	syncInterval = 5 * time.Minute
	pruneEvery   = 24 * time.Hour
)

// dial opens an authenticated IMAP connection; it closes when ctx ends.
func (b *MailBackend) dial(ctx context.Context, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	host := b.Account.IMAPHost
	opts := &imapclient.Options{
		TLSConfig:             &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		WordDecoder:           &mime.WordDecoder{CharsetReader: charset.Reader},
		UnilateralDataHandler: handler,
	}
	c, err := imapclient.DialTLS(net.JoinHostPort(host, "993"), opts)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", host, err)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	if err := b.login(ctx, c); err != nil {
		stop()
		c.Close()
		return nil, err
	}
	return c, nil
}

func (b *MailBackend) login(ctx context.Context, c *imapclient.Client) error {
	if b.Account.Auth == authGoogle {
		token, err := b.accessToken(ctx)
		if err != nil {
			return err
		}
		if err := c.Authenticate(&xoauth2{user: b.Account.Email, token: token}); err != nil {
			return fmt.Errorf("sign in to %s: %w", b.Account.IMAPHost, err)
		}
		return nil
	}
	if err := c.Login(b.Account.Email, b.Account.Password).Wait(); err != nil {
		return fmt.Errorf("sign in to %s: %w", b.Account.IMAPHost, err)
	}
	return nil
}

// withConn runs fn on the shared command connection, reconnecting once when
// the old connection turns out to be dead. Server refusals are not retried.
func (b *MailBackend) withConn(ctx context.Context, fn func(*imapclient.Client) error) error {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	for attempt := 0; ; attempt++ {
		if b.conn == nil {
			c, err := b.dial(b.runCtx(), nil)
			if err != nil {
				return err
			}
			b.conn, b.selected = c, ""
			if err := b.findMailboxes(c); err != nil {
				return err
			}
		}
		err := fn(b.conn)
		var imapErr *imap.Error
		if err == nil || errors.As(err, &imapErr) || ctx.Err() != nil || attempt > 0 {
			return err
		}
		b.conn.Close()
		b.conn = nil
	}
}

// selectBox selects mailbox on c unless it is selected already and returns
// its UIDVALIDITY.
func (b *MailBackend) selectBox(c *imapclient.Client, mailbox string) (uint32, error) {
	if b.selected == mailbox {
		return b.uidValidity, nil
	}
	data, err := c.Select(mailbox, nil).Wait()
	if err != nil {
		b.selected = ""
		return 0, fmt.Errorf("open mailbox %s: %w", mailbox, err)
	}
	b.selected, b.uidValidity = mailbox, data.UIDValidity
	return data.UIDValidity, nil
}

// findMailboxes locates the Sent and Trash folders by their special-use flags.
func (b *MailBackend) findMailboxes(c *imapclient.Client) error {
	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return fmt.Errorf("list mailboxes: %w", err)
	}
	for _, box := range boxes {
		for _, attr := range box.Attrs {
			switch attr {
			case imap.MailboxAttrSent:
				b.sentBox = box.Mailbox
			case imap.MailboxAttrTrash:
				b.trashBox = box.Mailbox
			}
		}
	}
	return nil
}

// runSync keeps the cache current: at start, when IDLE reports a change,
// and every few minutes for read state changed elsewhere.
func (b *MailBackend) runSync(ctx context.Context) {
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	var pruned time.Time
	prunedDays := -1
	for {
		// A shorter window chosen in Settings applies on the next pass.
		if days := RetentionDays(b.Store); time.Since(pruned) > pruneEvery || days != prunedDays {
			if err := b.db.prune(days); err != nil {
				b.report(err)
			}
			pruned, prunedDays = time.Now(), days
		}
		if err := b.syncAll(ctx); err != nil && ctx.Err() == nil {
			b.report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-b.nudge:
		case <-ticker.C:
		}
	}
}

func (b *MailBackend) syncAll(ctx context.Context) error {
	if err := b.syncMailbox(ctx, inbox); err != nil {
		return err
	}
	if sent := b.sentMailbox(); sent != "" {
		return b.syncMailbox(ctx, sent)
	}
	return nil
}

func (b *MailBackend) sentMailbox() string {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return b.sentBox
}

// syncMailbox fetches the mails new since the last sync, or the newest ones
// inside the retention window on the first, and refreshes read state.
func (b *MailBackend) syncMailbox(ctx context.Context, mailbox string) error {
	var added []row
	unreadChanged := false
	err := b.withConn(ctx, func(c *imapclient.Client) error {
		uv, err := b.selectBox(c, mailbox)
		if err != nil {
			return err
		}
		savedUV, last, err := b.db.syncState(mailbox)
		if err != nil {
			return err
		}
		if savedUV != uv {
			if savedUV != 0 {
				if err := b.db.resetMailbox(mailbox); err != nil {
					return err
				}
			}
			last = 0
		}
		criteria := &imap.SearchCriteria{}
		if last == 0 {
			if days := RetentionDays(b.Store); days > 0 {
				criteria.Since = time.Now().AddDate(0, 0, -days)
			}
		} else {
			criteria.UID = []imap.UIDSet{{{Start: imap.UID(last + 1), Stop: 0}}}
		}
		found, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return fmt.Errorf("search %s: %w", mailbox, err)
		}
		var uids []imap.UID
		for _, uid := range found.AllUIDs() {
			if uint32(uid) > last {
				uids = append(uids, uid)
			}
		}
		if last == 0 && len(uids) > initialSyncLimit {
			uids = uids[len(uids)-initialSyncLimit:]
		}
		rows, err := b.fetchRows(c, mailbox, uv, uids)
		if err != nil {
			return err
		}
		newest := last
		for i := range rows {
			newest = max(newest, rows[i].UID)
			ok, err := b.db.insert(&rows[i])
			if err != nil {
				return err
			}
			if ok && last != 0 {
				added = append(added, rows[i])
			}
		}
		if err := b.db.saveSyncState(mailbox, uv, newest); err != nil {
			return err
		}
		if last == 0 && len(rows) > 0 {
			unreadChanged = true
		}
		if mailbox != inbox {
			return nil
		}
		unseen := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
		if days := RetentionDays(b.Store); days > 0 {
			unseen.Since = time.Now().AddDate(0, 0, -days)
		}
		found, err = c.UIDSearch(unseen, nil).Wait()
		if err != nil {
			return fmt.Errorf("search unread mail: %w", err)
		}
		set := make(map[uint32]bool)
		for _, uid := range found.AllUIDs() {
			set[uint32(uid)] = true
		}
		changed, err := b.db.setUnseen(mailbox, uv, set)
		unreadChanged = unreadChanged || changed
		return err
	})
	for _, r := range added {
		msg := b.toMessage(r)
		b.events.Publish(backend.Event{Kind: backend.EventMessage, Chat: chatID(r.Peer), Message: &msg})
	}
	if unreadChanged || len(added) > 0 {
		b.events.Publish(backend.Event{Kind: backend.EventUnread})
	}
	return err
}

// fetchRows downloads the envelope, structure and readable text of uids.
func (b *MailBackend) fetchRows(c *imapclient.Client, mailbox string, uv uint32, uids []imap.UID) ([]row, error) {
	var rows []row
	for start := 0; start < len(uids); start += fetchBatch {
		batch := uids[start:min(len(uids), start+fetchBatch)]
		msgs, err := c.Fetch(imap.UIDSetNum(batch...), &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, InternalDate: true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return nil, fmt.Errorf("fetch mail from %s: %w", mailbox, err)
		}
		// The text parts go out in one pipelined round trip.
		type pending struct {
			row  row
			text *textPart
			item *imap.FetchItemBodySection
			cmd  *imapclient.FetchCommand
		}
		var batchRows []pending
		for _, m := range msgs {
			if m.Envelope == nil {
				continue
			}
			p := pending{row: b.envelopeRow(m, mailbox, uv)}
			if m.BodyStructure != nil {
				p.text, p.row.Parts = layout(m.BodyStructure)
			}
			if p.text != nil {
				p.item = &imap.FetchItemBodySection{Part: p.text.Section, Peek: true,
					Partial: &imap.SectionPartial{Offset: 0, Size: 2 * maxBody}}
				p.cmd = c.Fetch(imap.UIDSetNum(m.UID), &imap.FetchOptions{UID: true,
					BodySection: []*imap.FetchItemBodySection{p.item}})
			}
			batchRows = append(batchRows, p)
		}
		for _, p := range batchRows {
			if p.cmd != nil {
				got, err := p.cmd.Collect()
				if err != nil {
					return nil, fmt.Errorf("fetch mail text: %w", err)
				}
				if len(got) > 0 {
					p.row.Body = bodyText(got[0].FindBodySection(p.item), p.text)
				}
			}
			rows = append(rows, p.row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
	return rows, nil
}

func (b *MailBackend) envelopeRow(m *imapclient.FetchMessageBuffer, mailbox string, uv uint32) row {
	env := m.Envelope
	me := strings.ToLower(b.Account.Email)
	r := row{
		Mailbox: mailbox, UIDValidity: uv, UID: uint32(m.UID),
		MessageID: strings.Trim(env.MessageID, "<>"),
		Subject:   strings.TrimSpace(env.Subject),
		Date:      env.Date,
		To:        joinAddrs(env.To),
		Cc:        joinAddrs(env.Cc),
	}
	if len(env.InReplyTo) > 0 {
		r.InReplyTo = strings.Trim(env.InReplyTo[0], "<>")
	}
	if r.Date.IsZero() {
		r.Date = m.InternalDate
	}
	if len(env.From) > 0 {
		r.FromName = strings.TrimSpace(env.From[0].Name)
		r.FromAddr = strings.ToLower(env.From[0].Addr())
	}
	for _, f := range m.Flags {
		if f == imap.FlagSeen {
			r.Seen = true
		}
	}
	r.Outgoing = mailbox != inbox || r.FromAddr == me
	r.Peer = r.FromAddr
	if r.Outgoing {
		r.Peer = me
		for _, a := range env.To {
			if addr := strings.ToLower(a.Addr()); addr != "" {
				r.Peer = addr
				break
			}
		}
	}
	if r.Peer == "" {
		r.Peer = "unknown sender"
	}
	return r
}

func joinAddrs(list []imap.Address) string {
	var out []string
	for _, a := range list {
		addr := a.Addr()
		if addr == "" {
			continue
		}
		if a.Name != "" {
			addr = a.Name + " <" + addr + ">"
		}
		out = append(out, addr)
	}
	return strings.Join(out, ", ")
}

// fetchPeer downloads up to limit of peer's mails older than before (now
// when zero) that are not cached, from the inbox and the Sent folder.
func (b *MailBackend) fetchPeer(ctx context.Context, peer string, before time.Time, limit int) error {
	sent := b.sentMailbox()
	return b.withConn(ctx, func(c *imapclient.Client) error {
		for _, box := range []struct{ name, header string }{{inbox, "From"}, {sent, "To"}} {
			if box.name == "" {
				continue
			}
			uv, err := b.selectBox(c, box.name)
			if err != nil {
				return err
			}
			criteria := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{
				{Key: box.header, Value: peer}}}
			if !before.IsZero() {
				criteria.Before = before.AddDate(0, 0, 1) // SEARCH compares dates only
			}
			found, err := c.UIDSearch(criteria, nil).Wait()
			if err != nil {
				return fmt.Errorf("search %s: %w", box.name, err)
			}
			var uids []imap.UID
			all := found.AllUIDs()
			for i := len(all) - 1; i >= 0 && len(uids) < limit; i-- {
				if !b.db.hasUID(box.name, uv, uint32(all[i])) {
					uids = append(uids, all[i])
				}
			}
			rows, err := b.fetchRows(c, box.name, uv, uids)
			if err != nil {
				return err
			}
			for i := range rows {
				if _, err := b.db.insert(&rows[i]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// runIdle holds a second connection in IDLE on the inbox and nudges the sync
// whenever the server reports a change.
func (b *MailBackend) runIdle(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := b.idleOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.report(err)
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		if backend.WaitFor(ctx, delay) != nil {
			return
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (b *MailBackend) idleOnce(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := b.dial(ctx, &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				b.wake()
			}
		},
		Expunge: func(uint32) { b.wake() },
	})
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Select(inbox, nil).Wait(); err != nil {
		return fmt.Errorf("open inbox: %w", err)
	}
	for {
		idle, err := c.Idle()
		if err != nil {
			return fmt.Errorf("watch inbox: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- idle.Wait() }()
		timer := time.NewTimer(idleRestart)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case err := <-done:
			timer.Stop()
			return fmt.Errorf("watch inbox: %w", err)
		case <-timer.C:
		}
		if err := idle.Close(); err != nil {
			return fmt.Errorf("watch inbox: %w", err)
		}
		if err := <-done; err != nil {
			return fmt.Errorf("watch inbox: %w", err)
		}
	}
}

// wake asks the sync loop for a pass without blocking.
func (b *MailBackend) wake() {
	select {
	case b.nudge <- struct{}{}:
	default:
	}
}
