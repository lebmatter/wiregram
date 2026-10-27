package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// mailSplitWidth is the narrowest folder pane that shows the reader beside
// the list; narrower, the reader takes the list's place.
const mailSplitWidth = 120

// mailListModel is an email folder (backend.Mailbox): its conversations in
// a list as wide as the pane, each a row that Space expands into its mails.
// Enter reads a mail, in the list's place or in a pane opened beside it, with
// a quick reply box under the text, and single keys reply, forward, move and
// mark. The root draws the composer as a modal over the screen.
type mailListModel struct {
	st     *styles
	ctx    context.Context
	b      backend.Backend
	mb     backend.Mailbox
	folder backend.ChatEntry

	threads []backend.MailThread
	loaded  bool
	err     error
	// seq numbers the loads, so an older one arriving late is dropped;
	// stale marks a reload asked for while one was running.
	seq     int
	loading bool
	stale   bool

	open   map[string]bool // expanded conversations, by their first mail's ID
	sel    int             // index into rows()
	offset int

	query  string           // the search the list shows; "" for the folder
	search *textinput.Model // the search being typed
	visual bool
	marked map[string]bool // mails picked in visual mode, by ID

	reading     *backend.Message
	readerTop   int
	readerFocus bool // in the split, keys go to the reader

	quick        textarea.Model // the reader's quick reply
	replying     bool           // keys go to the quick reply
	quickSending bool

	composer  *mailComposer
	composing bool // the composer is up; a kept draft waits while false
	opening   bool // an attachment is downloading to open
	notice    string
	focused   bool
	spin      string
	width     int
	height    int
}

// mailRow is a row of the list: a conversation, or one of its mails (msg
// at least 0) while it is expanded.
type mailRow struct {
	thread int
	msg    int
}

type mailLoaded struct {
	folder  backend.ChatID
	seq     int
	threads []backend.MailThread
	err     error
}

// mailDone reports a move, a mark or a send; notice says what was done.
type mailDone struct {
	notice string
	err    error
	sent   bool
	quick  bool // a quick reply was sent
	moved  bool
}

// mailMoveReq asks the root for the move picker for ids.
type mailMoveReq struct{ ids []string }

// mailClosed asks the root to close the folder.
type mailClosed struct{}

func newMailList(ctx context.Context, st *styles, b backend.Backend, mb backend.Mailbox,
	folder backend.ChatEntry, draft *mailComposer) (mailListModel, tea.Cmd) {
	m := mailListModel{st: st, ctx: ctx, b: b, mb: mb, folder: folder, composer: draft,
		open: make(map[string]bool), marked: make(map[string]bool),
		quick: newComposerArea(st, "quick reply")}
	m.quick.Blur()
	return m.reload()
}

// reload reads the folder again, or after a running load, once it is in.
func (m mailListModel) reload() (mailListModel, tea.Cmd) {
	if m.query != "" {
		return m, nil // the search results stand until it is closed
	}
	if m.loading {
		m.stale = true
		return m, nil
	}
	return m.load("")
}

func (m mailListModel) load(query string) (mailListModel, tea.Cmd) {
	m.seq++
	m.loading, m.stale = true, false
	ctx, mb, folder, seq := m.ctx, m.mb, m.folder, m.seq
	return m, func() tea.Msg {
		threads, err := mb.Threads(ctx, folder, query)
		return mailLoaded{folder.ID, seq, threads, err}
	}
}

// syncing reports whether the account's first sync is still filling the folder.
func (m mailListModel) syncing() bool {
	s, ok := m.b.(backend.Syncer)
	return ok && s.Syncing() && len(m.threads) == 0 && m.query == ""
}

// typing reports whether keys are text: the composer, the search field or
// the quick reply.
func (m mailListModel) typing() bool { return m.composing || m.search != nil || m.replying }

func (m mailListModel) update(msg tea.Msg) (mailListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case mailLoaded:
		if msg.folder != m.folder.ID || msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		if m.notice == "searching…" {
			m.notice = ""
		}
		if msg.err == nil {
			m.threads, m.loaded = msg.threads, true
			m.sel = min(m.sel, max(0, len(m.rows())-1))
			m = m.scrolled()
		}
		if m.stale {
			return m.reload()
		}
	case mailDone:
		if msg.quick {
			m.quickSending = false
			if msg.err != nil {
				m.notice = "reply not sent: " + textfmt.CleanError(msg.err)
				return m, nil
			}
			m.quick.Reset()
			m.replying = false
			m.quick.Blur()
		}
		if msg.sent && m.composer != nil {
			m.composer.sending = false
			if msg.err == nil {
				m.composer, m.composing = nil, false
			} else {
				m.composer.notice = textfmt.CleanError(msg.err)
				// Said here too, in case the composer was closed meanwhile.
				m.notice = "mail not sent: " + textfmt.CleanError(msg.err) + "; c reopens the draft"
				return m, nil
			}
		}
		m.notice = msg.notice
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
		}
		if msg.moved && m.query != "" {
			return m.load(m.query) // the search results still show what moved
		}
	case mediaOpened:
		m.opening = false
		switch {
		case msg.err != nil && !isContextCanceled(msg.err):
			m.notice = textfmt.CleanError(msg.err)
		case msg.err == nil:
			m.notice = "opened " + msg.filename
		}
	case chatSaveResult:
		m.notice = "saved " + msg.filename + " to ~/Downloads"
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
		}
	default:
		// The field being typed in takes the rest, such as its cursor's blink.
		switch {
		case m.composing:
			return m, m.composer.pass(msg)
		case m.search != nil:
			in, cmd := m.search.Update(msg)
			m.search = &in
			return m, cmd
		case m.replying:
			var cmd tea.Cmd
			m.quick, cmd = m.quick.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// rows lays the conversations out: each one, then its mails while expanded.
func (m mailListModel) rows() []mailRow {
	var rows []mailRow
	for i, t := range m.threads {
		rows = append(rows, mailRow{i, -1})
		if len(t.Messages) > 1 && m.open[t.Messages[0].ID] {
			for j := range t.Messages {
				rows = append(rows, mailRow{i, j})
			}
		}
	}
	return rows
}

func (m mailListModel) selectedRow() (mailRow, bool) {
	rows := m.rows()
	if m.sel < 0 || m.sel >= len(rows) {
		return mailRow{}, false
	}
	return rows[m.sel], true
}

// shown is the mail a row stands for: its own, or a conversation's newest.
func (m mailListModel) shown(r mailRow) backend.Message {
	msgs := m.threads[r.thread].Messages
	if r.msg >= 0 {
		return msgs[r.msg]
	}
	return msgs[len(msgs)-1]
}

// rowIDs are the mails a row acts on: its own, or a whole conversation's.
func (m mailListModel) rowIDs(r mailRow) []string {
	msgs := m.threads[r.thread].Messages
	if r.msg >= 0 {
		return []string{msgs[r.msg].ID}
	}
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		ids = append(ids, msg.ID)
	}
	return ids
}

// targets are the mails an action takes: those picked in visual mode, or
// the mail being read, or the selected row's.
func (m mailListModel) targets() []string {
	if m.visual && len(m.marked) > 0 {
		var ids []string
		for _, r := range m.rows() {
			if r.msg < 0 {
				for _, id := range m.rowIDs(r) {
					if m.marked[id] {
						ids = append(ids, id)
					}
				}
			}
		}
		return ids
	}
	if m.reading != nil && (m.readerFocus || !m.split()) {
		return []string{m.reading.ID}
	}
	if r, ok := m.selectedRow(); ok {
		return m.rowIDs(r)
	}
	return nil
}

// split reports whether the reader sits beside the list.
func (m mailListModel) split() bool { return m.width >= mailSplitWidth }

func (m mailListModel) key(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	m.notice = ""
	if m.composing {
		return m.composeKey(msg)
	}
	if m.search != nil {
		return m.searchKey(msg)
	}
	if m.replying {
		return m.replyKey(msg)
	}
	if m.reading != nil && (m.readerFocus || !m.split()) {
		return m.readerKey(msg)
	}
	return m.listKey(msg)
}

func (m mailListModel) listKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	rows := m.rows()
	page := max(1, m.listHeight())
	switch msg.String() {
	case "esc":
		switch {
		case m.reading != nil:
			m = m.closeReader()
		case m.visual:
			m.visual, m.marked = false, make(map[string]bool)
		case m.query != "":
			m.query = ""
			return m.load("")
		default:
			return m, func() tea.Msg { return mailClosed{} }
		}
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(max(0, len(rows)-1), m.sel+1)
	case "pgup":
		m.sel = max(0, m.sel-page)
	case "pgdown":
		m.sel = min(max(0, len(rows)-1), m.sel+page)
	case "home", "g":
		m.sel = 0
	case "end", "G":
		m.sel = max(0, len(rows)-1)
	case "space", " ":
		r, ok := m.selectedRow()
		if !ok {
			break
		}
		if m.visual {
			ids := m.rowIDs(r)
			on := !m.marked[ids[0]]
			for _, id := range ids {
				m.marked[id] = on
			}
			break
		}
		if key := m.threads[r.thread].Messages[0].ID; len(m.threads[r.thread].Messages) > 1 {
			m.open[key] = !m.open[key]
			if !m.open[key] {
				m.sel = m.headerIndex(r.thread)
			}
		}
	case "enter":
		return m.read()
	case "]":
		if m.split() && m.reading != nil {
			m.readerFocus = true
		}
	case "v":
		m.visual, m.marked = !m.visual, make(map[string]bool)
		if r, ok := m.selectedRow(); ok && m.visual {
			for _, id := range m.rowIDs(r) {
				m.marked[id] = true
			}
		}
	case "/":
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = "search " + textfmt.Plain(m.folder.Title)
		in.SetValue(m.query)
		m.search = &in
		return m, m.search.Focus()
	default:
		return m.act(msg.String())
	}
	return m.scrolled(), nil
}

// headerIndex is the row of conversation t's header.
func (m mailListModel) headerIndex(t int) int {
	for i, r := range m.rows() {
		if r.thread == t && r.msg < 0 {
			return i
		}
	}
	return 0
}

// read opens the selected row's mail and marks it read; a conversation is
// read whole.
func (m mailListModel) read() (mailListModel, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	msg := m.shown(r)
	if m.reading == nil || m.reading.ID != msg.ID {
		m = m.closeReader()
		m.reading = &msg
	}
	m.readerFocus = m.split()
	var unread []string
	ids := m.rowIDs(r)
	for _, t := range m.threads[r.thread].Messages {
		if containsID(ids, t.ID) && m.threads[r.thread].Unread > 0 && !t.Outgoing {
			unread = append(unread, t.ID)
		}
	}
	if len(unread) == 0 {
		return m, nil
	}
	return m, m.setUnread(unread, false, "")
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// closeReader hides the reader and drops its quick reply.
func (m mailListModel) closeReader() mailListModel {
	m.reading, m.readerTop, m.readerFocus = nil, 0, false
	m.replying = false
	m.quick.Reset()
	m.quick.Blur()
	return m
}

func (m mailListModel) readerKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	page := m.readerHeight()
	last := max(0, len(m.readerLines(m.readerWidth()-4))-page)
	switch msg.String() {
	case "esc":
		return m.closeReader(), nil
	case "enter":
		m.replying = true
		return m, m.quick.Focus()
	case "[":
		m.readerFocus = false
		return m, nil
	case "up", "k":
		m.readerTop--
	case "down", "j":
		m.readerTop++
	case "pgup", "b":
		m.readerTop -= page
	case "pgdown", "space", " ":
		m.readerTop += page
	case "home", "g":
		m.readerTop = 0
	case "end", "G":
		m.readerTop = last
	case "o":
		return m.openAttachment()
	case "s":
		if filename, ok := m.b.MediaSaveable(*m.reading); ok {
			m.notice = "saving " + filename + "…"
			return m, saveToDownloads(m.ctx, m.b, *m.reading, filename)
		}
	default:
		return m.act(msg.String())
	}
	m.readerTop = max(0, min(m.readerTop, last))
	return m, nil
}

func (m mailListModel) openAttachment() (mailListModel, tea.Cmd) {
	filename, ok := m.b.MediaSaveable(*m.reading)
	if !ok || m.opening {
		return m, nil
	}
	dir, err := mediaDir(m.b, m.folder, *m.reading)
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m.opening, m.notice = true, "opening "+filename+"…"
	return m, openExternal(m.ctx, m.b, *m.reading, dir, filename)
}

// act runs a single-key action on the targets.
func (m mailListModel) act(k string) (mailListModel, tea.Cmd) {
	switch k {
	case "c":
		return m.compose("New mail", backend.MailDraft{}, "")
	case "r", "R", "f":
		msg, ok := m.answered()
		if !ok {
			return m, nil
		}
		kind, title, forwarded := backend.DraftReply, "Reply", ""
		switch k {
		case "R":
			kind, title = backend.DraftReplyAll, "Reply to all"
		case "f":
			kind, title, forwarded = backend.DraftForward, "Forward", msg.MediaLabel
		}
		return m.compose(title, m.mb.Draft(msg, kind), forwarded)
	}
	ids := m.targets()
	if len(ids) == 0 {
		return m, nil
	}
	switch k {
	case "d":
		return m.move(ids, m.mb.TrashFolder(), "Trash")
	case "a":
		if m.mb.ArchiveFolder() == "" {
			return m, nil
		}
		return m.move(ids, m.mb.ArchiveFolder(), "Archive")
	case "m":
		return m, func() tea.Msg { return mailMoveReq{ids} }
	case "u":
		return m, m.toggleUnread(ids)
	}
	return m, nil
}

// answered is the mail r, R and f answer: the one being read, or the
// selected row's newest.
func (m mailListModel) answered() (backend.Message, bool) {
	if m.reading != nil && (m.readerFocus || !m.split()) {
		return *m.reading, true
	}
	if r, ok := m.selectedRow(); ok {
		return m.shown(r), true
	}
	return backend.Message{}, false
}

// toggleUnread marks the targets read when any is unread; otherwise it marks
// unread the newest incoming mail among them.
func (m mailListModel) toggleUnread(ids []string) tea.Cmd {
	byID := make(map[string]backend.Message)
	anyUnread := false
	for _, t := range m.threads {
		for _, msg := range t.Messages {
			if containsID(ids, msg.ID) {
				byID[msg.ID] = msg
			}
		}
		if t.Unread > 0 {
			for _, msg := range t.Messages {
				anyUnread = anyUnread || containsID(ids, msg.ID)
			}
		}
	}
	if anyUnread {
		return m.setUnread(ids, false, "marked read")
	}
	for i := len(ids) - 1; i >= 0; i-- {
		if msg, ok := byID[ids[i]]; ok && !msg.Outgoing {
			return m.setUnread([]string{ids[i]}, true, "marked unread")
		}
	}
	return nil
}

func (m mailListModel) setUnread(ids []string, unread bool, notice string) tea.Cmd {
	ctx, mb, folder := m.ctx, m.mb, m.folder
	return func() tea.Msg {
		return mailDone{notice: notice, err: mb.SetUnread(ctx, folder, ids, unread)}
	}
}

// move moves ids to the folder to, named label in the notice, and leaves the
// reader and visual mode.
func (m mailListModel) move(ids []string, to, label string) (mailListModel, tea.Cmd) {
	if to == "" {
		m.notice = "this server has no " + label + " folder"
		return m, nil
	}
	m = m.closeReader()
	m.visual, m.marked = false, make(map[string]bool)
	ctx, mb, folder := m.ctx, m.mb, m.folder
	m.notice = "moving…"
	notice := fmt.Sprintf("moved %d %s to %s", len(ids), plural(len(ids), "mail", "mails"),
		label)
	return m, func() tea.Msg {
		return mailDone{notice: notice, err: mb.MoveMail(ctx, folder, ids, to), moved: true}
	}
}

// compose opens the composer on d, unless a kept draft is waiting: that one
// reopens instead.
func (m mailListModel) compose(title string, d backend.MailDraft, forwarded string) (
	mailListModel, tea.Cmd) {
	if m.composer != nil {
		m.composing = true
		m.composer.notice = "your earlier draft; ^X discards it"
		return m, m.composer.focusCmd()
	}
	addresses, err := m.mb.Addresses()
	if err != nil {
		m.notice = textfmt.CleanError(err)
	}
	m.composer = newMailComposer(m.st, title, d, addresses, forwarded)
	m.composing = true
	return m, m.composer.focusCmd()
}

func (m mailListModel) composeKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	act, cmd := m.composer.key(msg)
	switch act {
	case composeKeep:
		m.composing = false
		m.notice = "draft kept; c opens it again"
	case composeDiscard:
		m.composer, m.composing = nil, false
		m.notice = "draft discarded"
	case composeSend:
		m.composer.sending = true
		ctx, mb, d := m.ctx, m.mb, m.composer.value()
		return m, func() tea.Msg {
			return mailDone{notice: "sent", err: mb.SendMail(ctx, d), sent: true}
		}
	}
	return m, cmd
}

// replyKey types the quick reply; Enter sends it as a reply to the mail
// being read, quoting it as r would.
func (m mailListModel) replyKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.replying = false
		m.quick.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.quick.Value())
		if text == "" || m.quickSending || m.reading == nil {
			return m, nil
		}
		d := m.mb.Draft(*m.reading, backend.DraftReply)
		d.Body = text + d.Body
		m.quickSending, m.notice = true, "sending…"
		ctx, mb := m.ctx, m.mb
		return m, func() tea.Msg {
			return mailDone{notice: "reply sent", err: mb.SendMail(ctx, d), quick: true}
		}
	}
	m.quick.SetWidth(m.quickWidth())
	var cmd tea.Cmd
	m.quick, cmd = m.quick.Update(msg)
	return m, cmd
}

func (m mailListModel) searchKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.search = nil
		return m, nil
	case "enter":
		q := strings.TrimSpace(m.search.Value())
		m.search = nil
		m = m.closeReader()
		m.query, m.sel, m.offset = q, 0, 0
		m.notice = ""
		if q != "" {
			m.notice = "searching…"
		}
		return m.load(q)
	}
	var cmd tea.Cmd
	in, cmd := m.search.Update(msg)
	m.search = &in
	return m, cmd
}

func (m mailListModel) paste(msg tea.PasteMsg) (mailListModel, tea.Cmd) {
	switch {
	case m.composing:
		return m, m.composer.paste(msg)
	case m.search != nil:
		in, cmd := m.search.Update(msg)
		m.search = &in
		return m, cmd
	case m.replying:
		var cmd tea.Cmd
		m.quick, cmd = m.quick.Update(msg)
		return m, cmd
	}
	return m, nil
}

// click selects the row at y of the pane and reads it, as Enter would; in the
// reader beside the list it focuses the reader, or the quick reply under it.
func (m mailListModel) click(x, y int) (mailListModel, tea.Cmd) {
	if m.composing || m.search != nil {
		return m, nil
	}
	if m.reading != nil && (!m.split() || x >= m.listWidth()) {
		m.readerFocus = m.split()
		if y >= 1+m.readerHeight() && y < m.height-1 && !m.replying {
			m.replying = true
			return m, m.quick.Focus()
		}
		return m, nil
	}
	_, owners := m.listLines(m.listWidth()-4, m.listHeight(), false)
	i := y - 1 - m.listTop()
	if i < 0 || i >= len(owners) || owners[i] < 0 {
		return m, nil
	}
	m.sel = owners[i]
	return m.read()
}

// wheel scrolls the list or the reader under the pointer as ↑↓ would.
func (m mailListModel) wheel(x int, k tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	if m.composing || m.search != nil {
		return m, nil
	}
	if m.reading != nil && (!m.split() || x >= m.listWidth()) {
		focus := m.readerFocus
		m.readerFocus = true
		m, cmd := m.readerKey(k)
		m.readerFocus = focus
		return m, cmd
	}
	focus := m.readerFocus
	m.readerFocus = false
	m, cmd := m.listKey(k)
	m.readerFocus = focus
	return m, cmd
}

// resized fits the folder to a pane width×height.
func (m mailListModel) resized(width, height int) mailListModel {
	m.width, m.height = width, height
	m.quick.SetWidth(m.quickWidth())
	return m.scrolled()
}

// beside reports whether the reader is open beside the list.
func (m mailListModel) beside() bool { return m.reading != nil && m.split() }

func (m mailListModel) listWidth() int {
	if m.beside() {
		return m.width * 2 / 5
	}
	return m.width
}

func (m mailListModel) readerWidth() int {
	if m.beside() {
		return m.width - m.listWidth()
	}
	return m.width
}

// quickWidth is the quick reply's text width: the reader's edges and
// padding, the reply box's, and its prompt.
func (m mailListModel) quickWidth() int { return max(1, m.readerWidth()-4-4-2) }

// quickBox is the quick reply under the mail, its border lit while typing.
func (m mailListModel) quickBox(width int, focused bool) []string {
	border := m.st.dimColor
	if focused && m.replying {
		border = m.st.accentColor
	}
	title := "reply to " + textfmt.Plain(backend.GenericMessageSender(*m.reading))
	if m.quickSending {
		title = m.spin + "sending…"
	}
	return m.st.areaBox(title, nil, m.quick, width, border, "")
}

// readerHeight is how many rows of the mail show above the quick reply.
func (m mailListModel) readerHeight() int {
	if m.reading == nil {
		return max(1, m.height-2)
	}
	return max(1, m.height-2-len(m.quickBox(m.readerWidth()-4, false)))
}

// listTop is how many lines sit above the rows: the search field.
func (m mailListModel) listTop() int {
	if m.search != nil || m.query != "" {
		return 2
	}
	return 0
}

func (m mailListModel) listHeight() int { return max(1, m.height-2-m.listTop()) }

// dayBreak is the day heading above row i, when it starts a new day: only
// conversations get one, from the date of their newest mail.
func (m mailListModel) dayBreak(rows []mailRow, i int) (string, bool) {
	r := rows[i]
	if r.msg >= 0 {
		return "", false
	}
	day := m.shown(r).Timestamp.Local()
	if r.thread > 0 {
		prev := m.shown(mailRow{r.thread - 1, -1}).Timestamp.Local()
		if prev.Year() == day.Year() && prev.YearDay() == day.YearDay() {
			return "", false
		}
	}
	return dayHeading(day, time.Now()), true
}

// dayHeading names a day: today, yesterday, else its date.
func dayHeading(t, now time.Time) string {
	now = now.Local()
	switch y := now.AddDate(0, 0, -1); {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return "today"
	case t.Year() == y.Year() && t.YearDay() == y.YearDay():
		return "yesterday"
	case t.Year() == now.Year():
		return strings.ToLower(t.Format("Mon 2 Jan"))
	}
	return strings.ToLower(t.Format("2 Jan 2006"))
}

// rowHeight is the lines row i takes: two, and its day heading.
func (m mailListModel) rowHeight(rows []mailRow, i int) int {
	if _, ok := m.dayBreak(rows, i); ok {
		return 3
	}
	return 2
}

func (m mailListModel) scrolled() mailListModel {
	rows := m.rows()
	m.offset = max(0, min(m.offset, len(rows)-1))
	if len(rows) == 0 {
		return m
	}
	if m.sel < m.offset {
		m.offset = m.sel
	}
	// The first row from which the selection still fits.
	top, used := m.sel, m.rowHeight(rows, m.sel)
	for top > 0 && used+m.rowHeight(rows, top-1) <= m.listHeight() {
		top--
		used += m.rowHeight(rows, top)
	}
	m.offset = max(m.offset, top)
	return m
}

func (m mailListModel) unreadCount() int {
	n := 0
	for _, t := range m.threads {
		if t.Unread > 0 {
			n++
		}
	}
	return n
}

// title is the folder's tag and name, its unread count and the mode.
func (m mailListModel) title() string {
	title := m.st.badge.Render(" EM ") + " " + m.st.brand.Render(textfmt.Plain(m.folder.Title))
	switch {
	case m.visual:
		n := 0
		for _, on := range m.marked {
			if on {
				n++
			}
		}
		title += m.st.subtle.Render(fmt.Sprintf(" · VISUAL (%d selected)", n))
	case m.query != "":
		title += m.st.subtle.Render(" · search")
	default:
		if n := m.unreadCount(); n > 0 {
			title += m.st.dim.Render(fmt.Sprintf(" · %d unread", n))
		}
	}
	return title
}

func (m mailListModel) view(focused bool) string {
	m.focused = focused
	if m.reading != nil && !m.split() {
		return m.readerView(m.width, focused)
	}
	listFocused := focused && !(m.beside() && m.readerFocus)
	lines, _ := m.listLines(m.listWidth()-4, m.listHeight(), listFocused)
	list := renderTitledBox(m.title(), "", append(m.searchLines(m.listWidth()-4), lines...),
		m.listWidth(), m.height, m.st.border(listFocused))
	if !m.beside() {
		return list
	}
	return joinColumns(list, m.readerView(m.readerWidth(), focused && m.readerFocus))
}

// readerView is the mail being read in a box width wide, the quick reply
// at its foot.
func (m mailListModel) readerView(width int, focused bool) string {
	inner := width - 4
	box := m.quickBox(inner, focused)
	height := m.height - 2 - len(box)
	body := append(fillLines(m.readerBody(inner, height), height), box...)
	return renderBox("", body, width, m.height, m.st.border(focused))
}

// searchLines is the search field being typed, or the search shown.
func (m mailListModel) searchLines(width int) []string {
	switch {
	case m.search != nil:
		m.search.SetWidth(max(1, width-10))
		return []string{m.st.accent.Render("Search    ") + m.search.View(), ""}
	case m.query != "":
		return []string{m.st.subtle.Render(textfmt.ClipLine("Search    "+m.query+
			"  · esc clears", width)), ""}
	}
	return nil
}

// listLines draws the rows from the offset under their day headings, with
// the row each line belongs to (-1 for a heading or a notice).
func (m mailListModel) listLines(width, height int, focused bool) ([]string, []int) {
	rows := m.rows()
	var notice string
	switch {
	case m.err != nil && len(rows) == 0:
		notice = styleError.Render(textfmt.CleanError(m.err))
	case m.syncing() || (!m.loaded && m.loading):
		notice = m.spin + m.st.subtle.Render(" Loading emails…")
	case len(rows) == 0 && m.query != "":
		notice = m.st.subtle.Render("Nothing found")
	case len(rows) == 0:
		notice = m.st.subtle.Render("No mail here")
	}
	if notice != "" {
		return centerLines(width, height, notice), nil
	}
	var lines []string
	var owners []int
	now := time.Now()
	for i := m.offset; i < len(rows) && len(lines) < height; i++ {
		if day, ok := m.dayBreak(rows, i); ok {
			lines = append(lines, " "+m.st.modalRule(day, width-2))
			owners = append(owners, -1)
		}
		for _, line := range m.rowLines(rows[i], i == m.sel, focused, width, now) {
			lines = append(lines, line)
			owners = append(owners, i)
		}
	}
	return lines, owners
}

// rowLines draws a row on two lines: the selection bar, a visual mark and an
// unread dot, who the conversation is with and the time; then its subject
// and size. A mail of an expanded conversation shows its sender and an
// excerpt, further in. Unread rows are bold, read ones dim.
func (m mailListModel) rowLines(r mailRow, selected, focused bool, width int,
	now time.Time) []string {
	t := m.threads[r.thread]
	msg := m.shown(r)
	bar, mark, dot := " ", " ", "  "
	if selected {
		bar = m.st.accent.Render("▎")
	}
	if m.visual && m.marked[m.rowIDs(r)[0]] {
		mark = m.st.accent.Render("*")
	}
	who, text, indent := textfmt.Plain(t.With), textfmt.Plain(t.Subject), ""
	unread := t.Unread > 0
	date := msg.Timestamp.Local().Format("15:04")
	if r.msg >= 0 {
		who, indent = backend.GenericMessageSender(msg), "  "
		text = textfmt.ShortExcerpt(textfmt.Plain(msg.Text))
		unread = !msg.Outgoing && unread && m.unreadIn(msg)
		date = mailDate(msg.Timestamp, now)
	} else {
		if text == "" {
			text = "(no subject)"
		}
		if n := len(t.Messages); n > 1 {
			text += fmt.Sprintf(" (%d)", n)
		}
	}
	if unread {
		dot = m.st.accent.Render("●") + " "
	}
	lead := bar + mark + dot + indent
	room := max(1, width-textfmt.DisplayWidth(lead))
	who = truncate(who, max(1, room-textfmt.DisplayWidth(date)-2))
	gap := strings.Repeat(" ", max(2, room-textfmt.DisplayWidth(who)-textfmt.DisplayWidth(date)))
	text = truncate(text, room)
	dateStyle := m.st.subtle
	if unread {
		who = boldLine(who)
	} else {
		who, text = m.st.subtle.Render(who), m.st.dim.Render(text)
		dateStyle = m.st.dim
	}
	if selected {
		dateStyle = m.st.accent
	}
	first := lead + who + gap + dateStyle.Render(date)
	second := " " + mark + "  " + indent + text
	if selected && focused {
		return []string{shadeLine(first, m.st.selBgSGR, width),
			shadeLine(bar+second[1:], m.st.selBgSGR, width)}
	}
	if selected {
		second = bar + second[1:]
	}
	return []string{first, second}
}

// unreadIn reports whether msg is among its conversation's unread mails;
// the backend counts them, so the newest incoming ones are taken.
func (m mailListModel) unreadIn(msg backend.Message) bool {
	for _, t := range m.threads {
		n := t.Unread
		for i := len(t.Messages) - 1; i >= 0 && n > 0; i-- {
			if t.Messages[i].Outgoing {
				continue
			}
			if t.Messages[i].ID == msg.ID {
				return true
			}
			n--
		}
	}
	return false
}

// mailDate is a list date: the time today, the weekday this week, else the day.
func mailDate(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour && t.Before(now):
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 06")
}

// readerLines lays out the mail being read: subject, headers, then the full
// text with its line breaks.
func (m mailListModel) readerLines(width int) []string {
	width = min(width, textfmt.MaxChatLineWidth)
	msg := *m.reading
	headers, body := []string(nil), msg.Text
	if fm, ok := m.b.(backend.FullMessager); ok {
		headers, body = fm.FullMessage(msg)
	}
	var lines []string
	if msg.Subject != "" {
		for _, line := range textfmt.WrapLineIndent(textfmt.Plain(msg.Subject), width, 0) {
			lines = append(lines, styleTitle.Render(line))
		}
	}
	for _, h := range headers {
		lines = append(lines, m.st.subtle.Render(textfmt.ClipLine(textfmt.Plain(h), width)))
	}
	if len(lines) > 0 {
		lines = append(lines, m.st.dim.Render(strings.Repeat("─", width)))
	}
	var text []string
	for _, para := range strings.Split(body, "\n") {
		para = styleLinks(textfmt.Plain(para))
		text = append(text, textfmt.WrapLineIndent(para, width, 0)...)
	}
	return append(lines, carryStyles(text)...)
}

func (m mailListModel) readerBody(width, height int) []string {
	lines := m.readerLines(width)
	top := min(m.readerTop, max(0, len(lines)-height))
	return lines[top:min(len(lines), top+height)]
}

func (m mailListModel) helpKeys() []helpKey {
	switch {
	case m.composing:
		return m.composer.helpKeys()
	case m.search != nil:
		return []helpKey{{"enter", "search"}, {"esc", "cancel"}}
	case m.replying:
		return []helpKey{{"enter", "send reply"}, {"esc", "stop typing"}}
	}
	actions := []helpKey{{"r", "reply"}, {"R", "all"}, {"f", "forward"}, {"d", "trash"}}
	if m.mb.ArchiveFolder() != "" {
		actions = append(actions, helpKey{"a", "archive"})
	}
	actions = append(actions, helpKey{"m", "move"}, helpKey{"u", "unread"})
	if m.reading != nil && (m.readerFocus || !m.split()) {
		keys := []helpKey{{"enter", "quick reply"}, {"↑↓", "scroll"}}
		if _, ok := m.b.MediaSaveable(*m.reading); ok {
			keys = append(keys, helpKey{"o", "open file"}, helpKey{"s", "save file"})
		}
		keys = append(keys, actions...)
		if m.split() {
			keys = append(keys, helpKey{"[", "list"})
		}
		return append(keys, helpKey{"esc", "close"})
	}
	if m.visual {
		keys := []helpKey{{"space", "pick"}, {"d", "trash"}}
		if m.mb.ArchiveFolder() != "" {
			keys = append(keys, helpKey{"a", "archive"})
		}
		return append(keys, helpKey{"m", "move"}, helpKey{"u", "unread"},
			helpKey{"esc", "leave visual"})
	}
	keys := []helpKey{{"enter", "read"}, {"space", "expand"}, {"c", "compose"}}
	keys = append(keys, actions...)
	return append(keys, helpKey{"v", "select"}, helpKey{"/", "search"}, helpKey{"esc", "back"})
}
