package mail

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"wiregram/internal/backend"
	"wiregram/internal/store"
)

// RetentionSetting names the setting holding how many days of mail the local
// cache keeps; 0 keeps everything.
const RetentionSetting = "mail_retention_days"

// DefaultRetentionDays applies until the user picks another value in Settings.
const DefaultRetentionDays = 90

// RetentionChoices are the values Settings cycles through.
var RetentionChoices = []int{30, 90, 365, 0}

// sentPlaceholder is the mailbox of a sent mail stored before the Sent folder
// sync sees it; the sync adopts the row so its message ID never changes.
const sentPlaceholder = ":sent"

const currentDBVersion = 2

const schemaV1 = `
CREATE TABLE mail_messages (
	id          INTEGER PRIMARY KEY,
	account     TEXT NOT NULL,
	mailbox     TEXT NOT NULL,
	uidvalidity INTEGER NOT NULL,
	uid         INTEGER NOT NULL,
	message_id  TEXT NOT NULL,
	in_reply_to TEXT NOT NULL,
	peer        TEXT NOT NULL,
	from_name   TEXT NOT NULL,
	from_addr   TEXT NOT NULL,
	to_addrs    TEXT NOT NULL,
	cc_addrs    TEXT NOT NULL,
	subject     TEXT NOT NULL,
	date        INTEGER NOT NULL,
	seen        INTEGER NOT NULL,
	outgoing    INTEGER NOT NULL,
	body        TEXT NOT NULL,
	parts       TEXT NOT NULL,
	UNIQUE (account, mailbox, uidvalidity, uid)
);
CREATE INDEX mail_peer ON mail_messages (account, peer, date);
CREATE INDEX mail_msgid ON mail_messages (account, message_id);
CREATE TABLE mail_sync (
	account     TEXT NOT NULL,
	mailbox     TEXT NOT NULL,
	uidvalidity INTEGER NOT NULL,
	last_uid    INTEGER NOT NULL,
	PRIMARY KEY (account, mailbox)
);
`

// schemaV2 adds labels: Gmail labels or other servers' folders, each with
// whether its section is in the sidebar, and which mails each holds by UID
// in the label's own folder. Mails are cached once, so membership goes by
// Message-ID.
const schemaV2 = `
CREATE TABLE mail_labels (
	account TEXT NOT NULL,
	name    TEXT NOT NULL,
	shown   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, name)
);
CREATE TABLE mail_label_members (
	account     TEXT NOT NULL,
	label       TEXT NOT NULL,
	uidvalidity INTEGER NOT NULL,
	uid         INTEGER NOT NULL,
	message_id  TEXT NOT NULL,
	PRIMARY KEY (account, label, uidvalidity, uid)
);
CREATE INDEX mail_label_msgid ON mail_label_members (account, label, message_id);
`

// part is one attachment of a cached mail; Section is its IMAP body part path.
type part struct {
	Section  []int  `json:"section"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Charset  string `json:"charset,omitempty"`
	Size     int64  `json:"size"`
}

// row is one cached mail.
type row struct {
	ID          int64
	Mailbox     string
	UIDValidity uint32
	UID         uint32
	MessageID   string
	InReplyTo   string
	Peer        string
	FromName    string
	FromAddr    string
	To          string
	Cc          string
	Subject     string
	Date        time.Time
	Seen        bool
	Outgoing    bool
	Body        string
	Parts       []part
}

// msgID is the row's message ID: the date first so IDs sort by time, which
// the read reporter relies on, then the row ID to keep it unique.
func (r row) msgID() string {
	return fmt.Sprintf("%011d.%010d", r.Date.Unix(), r.ID)
}

func rowIDOf(msgID string) (int64, bool) {
	_, id, ok := strings.Cut(msgID, ".")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return n, err == nil
}

// mailDB is one account's view of the mail tables in wiregram.db.
type mailDB struct {
	db      *sql.DB
	account string
	// insertMu makes insert's check and write one step: the inbox and the
	// labels sync at once and may meet the same mail.
	insertMu sync.Mutex
}

func openMailDB(st *store.DB, account string) (*mailDB, error) {
	d := &mailDB{db: st.SQL(), account: account}
	if err := migrate(d.db); err != nil {
		return nil, fmt.Errorf("migrate mail database: %w", err)
	}
	return d, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mail_migrations (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create mail migrations table: %w", err)
	}
	version := 0
	err := db.QueryRow(`SELECT version FROM mail_migrations LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.Exec(`INSERT INTO mail_migrations (version) VALUES (0)`); err != nil {
			return fmt.Errorf("init mail migration version: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read mail migration version: %w", err)
	}
	steps := []string{schemaV1, schemaV2}
	for v := version; v < currentDBVersion; v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin mail schema v%d: %w", v+1, err)
		}
		if _, err := tx.Exec(steps[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply mail schema v%d: %w", v+1, err)
		}
		if _, err := tx.Exec(`UPDATE mail_migrations SET version = ?`, v+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("record mail schema v%d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit mail schema v%d: %w", v+1, err)
		}
	}
	return nil
}

// RetentionDays reads the retention setting, falling back to the default.
func RetentionDays(st *store.DB) int {
	v, err := st.Setting(RetentionSetting)
	if err != nil {
		return DefaultRetentionDays
	}
	days, err := strconv.Atoi(v)
	if err != nil || days < 0 {
		return DefaultRetentionDays
	}
	return days
}

// PurgeAccount deletes every cached mail of a removed account.
func PurgeAccount(st *store.DB, account string) error {
	if err := migrate(st.SQL()); err != nil {
		return fmt.Errorf("migrate mail database: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM mail_messages WHERE account = ?`,
		`DELETE FROM mail_sync WHERE account = ?`,
		`DELETE FROM mail_labels WHERE account = ?`,
		`DELETE FROM mail_label_members WHERE account = ?`,
	} {
		if _, err := st.SQL().Exec(q, account); err != nil {
			return fmt.Errorf("delete cached mail: %w", err)
		}
	}
	return nil
}

// prune drops cached mail older than the retention window. Mail on the
// server is never touched.
func (d *mailDB) prune(days int) error {
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	if _, err := d.db.Exec(`DELETE FROM mail_messages WHERE account = ? AND date < ?`,
		d.account, cutoff); err != nil {
		return fmt.Errorf("prune cached mail: %w", err)
	}
	return nil
}

func (d *mailDB) syncState(mailbox string) (uidvalidity, lastUID uint32, err error) {
	err = d.db.QueryRow(`SELECT uidvalidity, last_uid FROM mail_sync WHERE account = ? AND mailbox = ?`,
		d.account, mailbox).Scan(&uidvalidity, &lastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read mail sync state: %w", err)
	}
	return uidvalidity, lastUID, nil
}

func (d *mailDB) saveSyncState(mailbox string, uidvalidity, lastUID uint32) error {
	_, err := d.db.Exec(`INSERT INTO mail_sync (account, mailbox, uidvalidity, last_uid) VALUES (?, ?, ?, ?)
		ON CONFLICT (account, mailbox) DO UPDATE SET uidvalidity = excluded.uidvalidity,
		last_uid = excluded.last_uid`, d.account, mailbox, uidvalidity, lastUID)
	if err != nil {
		return fmt.Errorf("save mail sync state: %w", err)
	}
	return nil
}

// resetMailbox forgets a mailbox whose UIDVALIDITY changed: its UIDs no
// longer name the same mails.
func (d *mailDB) resetMailbox(mailbox string) error {
	if _, err := d.db.Exec(`DELETE FROM mail_messages WHERE account = ? AND mailbox = ?`,
		d.account, mailbox); err != nil {
		return fmt.Errorf("reset cached mailbox: %w", err)
	}
	return d.saveSyncState(mailbox, 0, 0)
}

// insert stores r unless the same mail is cached already. A row sent from
// wiregram and stored before the Sent sync saw it is adopted instead. added
// is false when nothing new was stored.
func (d *mailDB) insert(r *row) (added bool, err error) {
	d.insertMu.Lock()
	defer d.insertMu.Unlock()
	if r.MessageID != "" {
		res, err := d.db.Exec(`UPDATE mail_messages SET mailbox = ?, uidvalidity = ?, uid = ?
			WHERE account = ? AND message_id = ? AND mailbox = ?`,
			r.Mailbox, r.UIDValidity, r.UID, d.account, r.MessageID, sentPlaceholder)
		if err != nil {
			return false, fmt.Errorf("adopt sent mail: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return false, nil
		}
		var exists int
		err = d.db.QueryRow(`SELECT 1 FROM mail_messages WHERE account = ? AND message_id = ? LIMIT 1`,
			d.account, r.MessageID).Scan(&exists)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("check cached mail: %w", err)
		}
	}
	parts, err := json.Marshal(r.Parts)
	if err != nil {
		return false, fmt.Errorf("encode attachments: %w", err)
	}
	res, err := d.db.Exec(`INSERT OR IGNORE INTO mail_messages (account, mailbox, uidvalidity, uid,
		message_id, in_reply_to, peer, from_name, from_addr, to_addrs, cc_addrs, subject, date,
		seen, outgoing, body, parts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.account, r.Mailbox, r.UIDValidity, r.UID, r.MessageID, r.InReplyTo, r.Peer, r.FromName,
		r.FromAddr, r.To, r.Cc, r.Subject, r.Date.Unix(), r.Seen, r.Outgoing, r.Body, string(parts))
	if err != nil {
		return false, fmt.Errorf("cache mail: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	r.ID, err = res.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("cache mail: %w", err)
	}
	return true, nil
}

const rowColumns = `id, mailbox, uidvalidity, uid, message_id, in_reply_to, peer, from_name,
	from_addr, to_addrs, cc_addrs, subject, date, seen, outgoing, body, parts`

func scanRows(rows *sql.Rows) ([]row, error) {
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var date int64
		var parts string
		if err := rows.Scan(&r.ID, &r.Mailbox, &r.UIDValidity, &r.UID, &r.MessageID, &r.InReplyTo,
			&r.Peer, &r.FromName, &r.FromAddr, &r.To, &r.Cc, &r.Subject, &date, &r.Seen,
			&r.Outgoing, &r.Body, &parts); err != nil {
			return nil, fmt.Errorf("read cached mail: %w", err)
		}
		r.Date = time.Unix(date, 0)
		if err := json.Unmarshal([]byte(parts), &r.Parts); err != nil {
			return nil, fmt.Errorf("read attachments: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cached mail: %w", err)
	}
	return out, nil
}

func (d *mailDB) query(where string, args ...any) ([]row, error) {
	rows, err := d.db.Query(`SELECT `+rowColumns+` FROM mail_messages WHERE account = ? AND `+where,
		append([]any{d.account}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("read cached mail: %w", err)
	}
	return scanRows(rows)
}

func (d *mailDB) byRowID(id int64) (row, error) {
	rows, err := d.query(`id = ?`, id)
	if err != nil {
		return row{}, err
	}
	if len(rows) == 0 {
		return row{}, fmt.Errorf("this mail is no longer cached")
	}
	return rows[0], nil
}

// page returns up to limit of peer's mails older than before (all when
// before is 0), oldest first.
func (d *mailDB) page(peer string, before row, limit int) ([]row, error) {
	var rows []row
	var err error
	if before.ID == 0 {
		rows, err = d.query(`peer = ? ORDER BY date DESC, id DESC LIMIT ?`, peer, limit)
	} else {
		rows, err = d.query(`peer = ? AND (date < ? OR (date = ? AND id < ?))
			ORDER BY date DESC, id DESC LIMIT ?`,
			peer, before.Date.Unix(), before.Date.Unix(), before.ID, limit)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, err
}

// correspondent is one sidebar row: everyone the account mails with.
type correspondent struct {
	Peer   string
	Name   string
	Last   time.Time
	Unread int
}

func (d *mailDB) correspondents(limit int) ([]correspondent, error) {
	return d.correspondentsIn("", limit)
}

// correspondentsIn lists everyone the account mails with, or with label
// set, those with a mail under it.
func (d *mailDB) correspondentsIn(label string, limit int) ([]correspondent, error) {
	where, args := "", []any{d.account}
	if label != "" {
		where = ` AND message_id IN (SELECT message_id FROM mail_label_members
			WHERE account = m.account AND label = ?)`
		args = append(args, label)
	}
	rows, err := d.db.Query(`SELECT peer, MAX(date),
		SUM(CASE WHEN seen = 0 AND outgoing = 0 THEN 1 ELSE 0 END),
		COALESCE((SELECT from_name FROM mail_messages n WHERE n.account = m.account
			AND n.peer = m.peer AND n.outgoing = 0 AND n.from_name <> ''
			ORDER BY n.date DESC LIMIT 1), '')
		FROM mail_messages m WHERE account = ?`+where+`
		GROUP BY peer ORDER BY MAX(date) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list correspondents: %w", err)
	}
	defer rows.Close()
	var out []correspondent
	for rows.Next() {
		var c correspondent
		var last int64
		if err := rows.Scan(&c.Peer, &last, &c.Unread, &c.Name); err != nil {
			return nil, fmt.Errorf("list correspondents: %w", err)
		}
		c.Last = time.Unix(last, 0)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list correspondents: %w", err)
	}
	return out, nil
}

func (d *mailDB) unread(peer string) int {
	var n int
	d.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE account = ? AND peer = ?
		AND seen = 0 AND outgoing = 0`, d.account, peer).Scan(&n)
	return n
}

// readPosition is the ID of peer's newest mail that needs no read receipt.
func (d *mailDB) readPosition(peer string) string {
	rows, err := d.query(`peer = ? AND (seen = 1 OR outgoing = 1) ORDER BY date DESC, id DESC LIMIT 1`, peer)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].msgID()
}

// unseenUpTo returns peer's unread incoming mails at or before upTo.
func (d *mailDB) unseenUpTo(peer string, upTo row) ([]row, error) {
	return d.query(`peer = ? AND seen = 0 AND outgoing = 0 AND (date < ? OR (date = ? AND id <= ?))`,
		peer, upTo.Date.Unix(), upTo.Date.Unix(), upTo.ID)
}

func (d *mailDB) markSeen(ids []int64) error {
	for _, id := range ids {
		if _, err := d.db.Exec(`UPDATE mail_messages SET seen = 1 WHERE id = ?`, id); err != nil {
			return fmt.Errorf("mark mail read: %w", err)
		}
	}
	return nil
}

// setUnseen makes exactly the given UIDs of a mailbox unread, so mail read
// in another client stops counting here.
func (d *mailDB) setUnseen(mailbox string, uidvalidity uint32, unseen map[uint32]bool) (bool, error) {
	rows, err := d.query(`mailbox = ? AND uidvalidity = ? AND outgoing = 0`, mailbox, uidvalidity)
	if err != nil {
		return false, err
	}
	changed := false
	for _, r := range rows {
		if r.Seen != !unseen[r.UID] {
			if _, err := d.db.Exec(`UPDATE mail_messages SET seen = ? WHERE id = ?`,
				!unseen[r.UID], r.ID); err != nil {
				return false, fmt.Errorf("update read state: %w", err)
			}
			changed = true
		}
	}
	return changed, nil
}

func (d *mailDB) delete(id int64) error {
	if _, err := d.db.Exec(`DELETE FROM mail_messages WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete cached mail: %w", err)
	}
	return nil
}

// byUIDs returns the cached rows of a mailbox among uids.
func (d *mailDB) byUIDs(mailbox string, uidvalidity uint32, uids []uint32) ([]row, error) {
	var out []row
	for _, uid := range uids {
		rows, err := d.query(`mailbox = ? AND uidvalidity = ? AND uid = ?`, mailbox, uidvalidity, uid)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (d *mailDB) byMessageID(messageID string) (row, bool) {
	if messageID == "" {
		return row{}, false
	}
	rows, err := d.query(`message_id = ? LIMIT 1`, messageID)
	if err != nil || len(rows) == 0 {
		return row{}, false
	}
	return rows[0], true
}

func (d *mailDB) hasUID(mailbox string, uidvalidity, uid uint32) bool {
	var n int
	d.db.QueryRow(`SELECT 1 FROM mail_messages WHERE account = ? AND mailbox = ? AND uidvalidity = ?
		AND uid = ?`, d.account, mailbox, uidvalidity, uid).Scan(&n)
	return n == 1
}

// saveLabels records the server's labels: new ones start hidden, and those
// gone from the server are forgotten with what they held.
func (d *mailDB) saveLabels(names []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("save labels: %w", err)
	}
	defer tx.Rollback()
	current := make(map[string]bool)
	for _, name := range names {
		current[name] = true
		if _, err := tx.Exec(`INSERT OR IGNORE INTO mail_labels (account, name) VALUES (?, ?)`,
			d.account, name); err != nil {
			return fmt.Errorf("save labels: %w", err)
		}
	}
	rows, err := tx.Query(`SELECT name FROM mail_labels WHERE account = ?`, d.account)
	if err != nil {
		return fmt.Errorf("save labels: %w", err)
	}
	var gone []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("save labels: %w", err)
		}
		if !current[name] {
			gone = append(gone, name)
		}
	}
	rows.Close()
	for _, name := range gone {
		for _, q := range []string{
			`DELETE FROM mail_labels WHERE account = ? AND name = ?`,
			`DELETE FROM mail_label_members WHERE account = ? AND label = ?`,
			`DELETE FROM mail_sync WHERE account = ? AND mailbox = ?`,
		} {
			if _, err := tx.Exec(q, d.account, name); err != nil {
				return fmt.Errorf("forget label %s: %w", name, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save labels: %w", err)
	}
	return nil
}

// labels returns every label by name, with its unread incoming mail.
func (d *mailDB) labels() ([]backend.Label, error) {
	rows, err := d.db.Query(`SELECT l.name, l.shown,
		(SELECT COUNT(*) FROM mail_messages m WHERE m.account = l.account AND m.seen = 0
			AND m.outgoing = 0 AND m.message_id IN (SELECT message_id FROM mail_label_members
				WHERE account = l.account AND label = l.name))
		FROM mail_labels l WHERE l.account = ? ORDER BY l.name COLLATE NOCASE`, d.account)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer rows.Close()
	var out []backend.Label
	for rows.Next() {
		var l backend.Label
		if err := rows.Scan(&l.Name, &l.Shown, &l.Unread); err != nil {
			return nil, fmt.Errorf("list labels: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	return out, nil
}

func (d *mailDB) showLabel(name string, shown bool) error {
	if _, err := d.db.Exec(`UPDATE mail_labels SET shown = ? WHERE account = ? AND name = ?`,
		shown, d.account, name); err != nil {
		return fmt.Errorf("save label %s: %w", name, err)
	}
	return nil
}

func (d *mailDB) shownLabels() ([]string, error) {
	rows, err := d.db.Query(`SELECT name FROM mail_labels WHERE account = ? AND shown = 1`,
		d.account)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list labels: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	return out, nil
}

// labelUIDs returns the UIDs of label's folder known to hold mail, under
// uidvalidity.
func (d *mailDB) labelUIDs(label string, uidvalidity uint32) (map[uint32]bool, error) {
	rows, err := d.db.Query(`SELECT uid FROM mail_label_members WHERE account = ? AND label = ?
		AND uidvalidity = ?`, d.account, label, uidvalidity)
	if err != nil {
		return nil, fmt.Errorf("read label %s: %w", label, err)
	}
	defer rows.Close()
	out := make(map[uint32]bool)
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("read label %s: %w", label, err)
		}
		out[uid] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read label %s: %w", label, err)
	}
	return out, nil
}

// labelMember is one mail in a label's folder.
type labelMember struct {
	uid       uint32
	messageID string
}

// setLabelMembers makes label hold the mails added plus those of present
// it held already, under uidvalidity; anything else left the label.
func (d *mailDB) setLabelMembers(label string, uidvalidity uint32, present map[uint32]bool,
	added []labelMember) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("save label %s: %w", label, err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT uidvalidity, uid FROM mail_label_members
		WHERE account = ? AND label = ?`, d.account, label)
	if err != nil {
		return fmt.Errorf("save label %s: %w", label, err)
	}
	type key struct{ uv, uid uint32 }
	var gone []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.uv, &k.uid); err != nil {
			rows.Close()
			return fmt.Errorf("save label %s: %w", label, err)
		}
		if k.uv != uidvalidity || !present[k.uid] {
			gone = append(gone, k)
		}
	}
	rows.Close()
	for _, k := range gone {
		if _, err := tx.Exec(`DELETE FROM mail_label_members WHERE account = ? AND label = ?
			AND uidvalidity = ? AND uid = ?`, d.account, label, k.uv, k.uid); err != nil {
			return fmt.Errorf("save label %s: %w", label, err)
		}
	}
	for _, a := range added {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO mail_label_members (account, label,
			uidvalidity, uid, message_id) VALUES (?, ?, ?, ?, ?)`,
			d.account, label, uidvalidity, a.uid, a.messageID); err != nil {
			return fmt.Errorf("save label %s: %w", label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save label %s: %w", label, err)
	}
	return nil
}

func (d *mailDB) hasMessageID(messageID string) bool {
	var n int
	d.db.QueryRow(`SELECT 1 FROM mail_messages WHERE account = ? AND message_id = ? LIMIT 1`,
		d.account, messageID).Scan(&n)
	return n == 1
}
