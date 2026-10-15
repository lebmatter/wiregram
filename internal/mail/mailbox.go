package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"wiregram/internal/backend"
)

// Threads returns a folder's conversations, newest first. The mail sent in
// them comes along wherever it is filed; a conversation with nothing in the
// folder is left out.
func (b *MailBackend) Threads(ctx context.Context, folder backend.ChatEntry, query string) (
	[]backend.MailThread, error) {
	box := folder.ID.Id
	sent := b.sentMailbox()
	var rows []row
	var err error
	if query != "" {
		rows, err = b.searchFolder(ctx, box, query)
	} else {
		rows, err = b.db.folderRows(box, box == sent)
	}
	if err != nil {
		return nil, err
	}
	in := make(map[int64]bool, len(rows))
	for _, r := range rows {
		in[r.ID] = true
	}
	if query == "" && box != sent && sent != "" {
		mine, err := b.db.folderRows(sent, true)
		if err != nil {
			return nil, err
		}
		for _, r := range mine {
			if !in[r.ID] {
				rows = append(rows, r)
			}
		}
	}
	var out []backend.MailThread
	for _, group := range threadRows(rows) {
		t := backend.MailThread{Subject: group[0].Subject}
		inFolder := false
		for _, r := range group {
			inFolder = inFolder || in[r.ID]
			if !r.Seen && !r.Outgoing {
				t.Unread++
			}
			switch {
			case !r.Outgoing && r.FromName != "":
				t.With = r.FromName
			case !r.Outgoing:
				t.With = r.FromAddr
			case t.With == "":
				t.With = r.Peer
			}
		}
		if !inFolder {
			continue
		}
		t.Messages = b.toMessages(group)
		out = append(out, t)
	}
	return out, nil
}

// searchFolder asks the server for the newest mails in box matching query,
// caching those not cached yet.
func (b *MailBackend) searchFolder(ctx context.Context, box, query string) ([]row, error) {
	var ids []string
	err := b.withConn(ctx, func(c *imapclient.Client) error {
		uv, err := b.selectBox(c, box)
		if err != nil {
			return err
		}
		found, err := c.UIDSearch(&imap.SearchCriteria{Text: []string{query}}, nil).Wait()
		if err != nil {
			return fmt.Errorf("search mail: %w", err)
		}
		all := found.AllUIDs()
		_, members, err := b.labelMail(c, box, uv, all[max(0, len(all)-searchLimit):],
			box == b.sentMailbox())
		if err != nil {
			return err
		}
		// Hits older than the synced window are in no folder yet, and
		// actions find a mail's UID through its folder.
		if err := b.db.addMembers(box, uv, members); err != nil {
			return err
		}
		for _, m := range members {
			ids = append(ids, m.messageID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b.db.byMessageIDs(ids)
}

// errNotInFolder is returned when none of the mails acted on is known to be in
// the folder, rather than reporting an action that never reached the server.
var errNotInFolder = errors.New("this mail is no longer in this folder; reopen it and try again")

// locate finds the cached mails named by ids that are in folder, and where
// each sits there.
func (b *MailBackend) locate(folder string, ids []string) ([]row, []folderKey, error) {
	var rows []row
	var keys []folderKey
	for _, id := range ids {
		n, ok := rowIDOf(id)
		if !ok {
			continue
		}
		r, err := b.db.byRowID(n)
		if err != nil {
			return nil, nil, err
		}
		k, err := b.db.memberKeys(folder, r.MessageID)
		if err != nil {
			return nil, nil, err
		}
		if len(k) > 0 {
			rows, keys = append(rows, r), append(keys, k...)
		}
	}
	return rows, keys, nil
}

// uidSet is the UIDs of keys under uidvalidity, the folder's current one.
func uidSet(keys []folderKey, uidvalidity uint32) imap.UIDSet {
	var set imap.UIDSet
	for _, k := range keys {
		if k.uidvalidity == uidvalidity {
			set.AddNum(imap.UID(k.uid))
		}
	}
	return set
}

func (b *MailBackend) SetUnread(ctx context.Context, folder backend.ChatEntry, ids []string,
	unread bool) error {
	rows, keys, err := b.locate(folder.ID.Id, ids)
	if err != nil {
		return err
	}
	if len(ids) > 0 && len(keys) == 0 {
		return errNotInFolder
	}
	if len(keys) > 0 {
		err := b.withConn(ctx, func(c *imapclient.Client) error {
			uv, err := b.selectBox(c, folder.ID.Id)
			if err != nil {
				return err
			}
			set := uidSet(keys, uv)
			if len(set) == 0 {
				return nil
			}
			op := imap.StoreFlagsAdd
			if unread {
				op = imap.StoreFlagsDel
			}
			if err := c.Store(set, &imap.StoreFlags{Op: op, Silent: true,
				Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
				return fmt.Errorf("mark mail read: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	var changed []int64
	for _, r := range rows {
		if !r.Outgoing {
			changed = append(changed, r.ID)
		}
	}
	if err := b.db.setSeen(changed, !unread); err != nil {
		return err
	}
	b.events.Publish(backend.Event{Kind: backend.EventUnread, Chat: folder.ID})
	return nil
}

// MoveMail moves mails out of folder into to on the server. Mail moved to
// Trash leaves the cache, so no other folder shows it until it is restored.
func (b *MailBackend) MoveMail(ctx context.Context, folder backend.ChatEntry, ids []string,
	to string) error {
	box := folder.ID.Id
	if to == "" {
		return fmt.Errorf("this server has no folder for that")
	}
	if to == box {
		return nil
	}
	rows, keys, err := b.locate(box, ids)
	if err != nil {
		return err
	}
	if len(ids) > 0 && len(keys) == 0 {
		return errNotInFolder
	}
	if len(keys) > 0 {
		err := b.withConn(ctx, func(c *imapclient.Client) error {
			uv, err := b.selectBox(c, box)
			if err != nil {
				return err
			}
			set := uidSet(keys, uv)
			if len(set) == 0 {
				b.wake()
				b.wakeLabels()
				return fmt.Errorf("this folder changed on the server; try again once it syncs")
			}
			if _, err := c.Move(set, to).Wait(); err != nil {
				return fmt.Errorf("move mail to %s: %w", to, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := b.db.dropMembers(box, keys); err != nil {
		return err
	}
	if to == b.TrashFolder() {
		for _, r := range rows {
			if err := b.db.deleteUnfiled(r.ID); err != nil {
				return err
			}
		}
	}
	b.events.Publish(backend.Event{Kind: backend.EventUnread})
	b.wake()
	b.wakeLabels()
	return nil
}

func (b *MailBackend) TrashFolder() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.trashBox
}

func (b *MailBackend) ArchiveFolder() string { return b.archiveMailbox() }

// Draft starts a reply, a reply to all or a forward of msg, quoting it.
func (b *MailBackend) Draft(msg backend.Message, kind backend.DraftKind) backend.MailDraft {
	r, ok := msg.Raw.(row)
	if !ok {
		return backend.MailDraft{}
	}
	from := r.FromAddr
	if r.FromName != "" {
		from = (&mail.Address{Name: r.FromName, Address: r.FromAddr}).String()
	}
	date := r.Date.Local().Format("Mon 2 Jan 2006 15:04")
	if kind == backend.DraftForward {
		head := []string{"", "", "---------- Forwarded message ----------", "From: " + from,
			"Date: " + date, "Subject: " + r.Subject, "To: " + r.To}
		if r.Cc != "" {
			head = append(head, "Cc: "+r.Cc)
		}
		return backend.MailDraft{Subject: withPrefix(r.Subject, "Fwd:"),
			Body: strings.Join(head, "\n") + "\n\n" + r.Body, Forward: msg.ID}
	}
	d := backend.MailDraft{Subject: withPrefix(r.Subject, "Re:"), ReplyTo: msg.ID}
	var quoted []string
	for _, line := range strings.Split(strings.TrimRight(r.Body, "\n"), "\n") {
		quoted = append(quoted, strings.TrimRight("> "+line, " "))
	}
	who := strings.TrimSpace(r.FromName + " <" + r.FromAddr + ">")
	d.Body = "\n\nOn " + date + ", " + who + " wrote:\n" + strings.Join(quoted, "\n")
	me := strings.ToLower(b.Account.Email)
	to := []string{from}
	if r.Outgoing {
		// Answering one's own mail goes to whom it went to.
		to = nil
	}
	if r.Outgoing || kind == backend.DraftReplyAll {
		to = append(to, others(r.To, me)...)
	}
	d.To = strings.Join(to, ", ")
	if kind == backend.DraftReplyAll || r.Outgoing {
		d.Cc = strings.Join(others(r.Cc, me), ", ")
	}
	return d
}

// others is the addresses in list but me's.
func others(list, me string) []string {
	addrs, err := mail.ParseAddressList(list)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if !strings.EqualFold(a.Address, me) {
			out = append(out, a.String())
		}
	}
	return out
}

// SendMail sends d with its files and, for a forward, the forwarded mail's
// attachments; one that cannot be downloaded is left out and reported.
func (b *MailBackend) SendMail(ctx context.Context, d backend.MailDraft) error {
	if strings.TrimSpace(d.Subject) == "" {
		return fmt.Errorf("add a subject")
	}
	out := draft{To: d.To, Cc: d.Cc, Subject: strings.TrimSpace(d.Subject), Body: d.Body,
		Date: time.Now()}
	if id, ok := rowIDOf(d.ReplyTo); ok {
		if parent, err := b.db.byRowID(id); err == nil {
			out.threadUnder(parent)
		}
	}
	size := 0
	for _, path := range d.Attach {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("attach %s: %w", filepath.Base(path), err)
		}
		typ := mime.TypeByExtension(filepath.Ext(path))
		if typ == "" {
			typ = "application/octet-stream"
		}
		out.Attach = append(out.Attach, attachment{Name: filepath.Base(path), Type: typ, Data: data})
		size += len(data)
	}
	if id, ok := rowIDOf(d.Forward); ok {
		if r, err := b.db.byRowID(id); err == nil {
			for _, p := range r.Parts {
				data, err := b.fetchPart(ctx, r, p)
				if err != nil {
					b.report(fmt.Errorf("attachment %s left out: %w", safeName(p.Name), err))
					continue
				}
				out.Attach = append(out.Attach, attachment{Name: safeName(p.Name), Type: p.Type,
					Data: data})
				size += len(data)
			}
		}
	}
	if size > maxAttach {
		return fmt.Errorf("attachments come to %d MB; mail servers take %d MB",
			size>>20, maxAttach>>20)
	}
	_, err := b.deliver(ctx, out)
	return err
}

// Addresses lists the addresses the account has mailed with, newest first.
func (b *MailBackend) Addresses() ([]string, error) {
	lists, err := b.db.addressRows(2000)
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(b.Account.Email)
	seen := map[string]bool{me: true}
	var out []string
	for _, list := range lists {
		addrs, err := mail.ParseAddressList(list)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			addr := strings.ToLower(a.Address)
			if !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out, nil
}
