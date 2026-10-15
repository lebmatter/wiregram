package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// mailSplitWidth is the narrowest folder pane that shows the reader beside
// the list; narrower, the reader takes the list's place.
const mailSplitWidth = 140

// mailListModel is an email folder (backend.Mailbox): its conversations in
// a list, each a row that Space expands into its mails. Enter reads a mail,
// in the list's place or beside it in a wide pane, and single keys reply,
// forward, move and mark. The root draws the composer as a modal over the screen.
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
	moved  bool
}

// mailMoveReq asks the root for the move picker for ids.
type mailMoveReq struct{ ids []string }

// mailClosed asks the root to close the folder.
type mailClosed struct{}

func newMailList(ctx context.Context, st *styles, b backend.Backend, mb backend.Mailbox,
	folder backend.ChatEntry, draft *mailComposer) (mailListModel, tea.Cmd) {
	m := mailListModel{st: st, ctx: ctx, b: b, mb: mb, folder: folder, composer: draft,
		open: make(map[string]bool), marked: make(map[string]bool)}
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

// typing reports whether keys are text: the composer or the search field.
func (m mailListModel) typing() bool { return m.composing || m.search != nil }

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
			if !m.readerFocus {
				m = m.scrolled().previewed()
			}
		}
		if m.stale {
			return m.reload()
		}
	case mailDone:
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
	m = m.scrolled()
	return m.previewed(), nil
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

// previewed shows the selected row's mail in the split's reader.
func (m mailListModel) previewed() mailListModel {
	if !m.split() {
		return m
	}
	if r, ok := m.selectedRow(); ok {
		msg := m.shown(r)
		if m.reading == nil || m.reading.ID != msg.ID {
			m.reading, m.readerTop = &msg, 0
		}
	}
	return m
}

// read opens the selected row's mail and marks it read; a conversation is
// read whole.
func (m mailListModel) read() (mailListModel, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	msg := m.shown(r)
	m.reading, m.readerTop = &msg, 0
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

func (m mailListModel) readerKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	page := max(1, m.height-2)
	last := max(0, len(m.readerLines(m.readerWidth()-4))-page)
	switch msg.String() {
	case "esc", "q":
		if m.split() {
			m.readerFocus = false
		} else {
			m.reading = nil
		}
		return m, nil
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
	m.reading, m.readerFocus = nil, false
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

func (m mailListModel) searchKey(msg tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.search = nil
		return m, nil
	case "enter":
		q := strings.TrimSpace(m.search.Value())
		m.search = nil
		m.query, m.sel, m.offset, m.reading = q, 0, 0, nil
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
	}
	return m, nil
}

// click selects the row at y of the pane and reads it, as Enter would.
func (m mailListModel) click(x, y int) (mailListModel, tea.Cmd) {
	if m.typing() {
		return m, nil
	}
	if m.reading != nil && !m.split() {
		return m, nil
	}
	if m.split() && x >= m.listWidth() {
		m.readerFocus = m.reading != nil
		return m, nil
	}
	i := y - 1 - m.listTop() + m.offset
	if y < 1+m.listTop() || i >= len(m.rows()) {
		return m, nil
	}
	m.sel, m.readerFocus = i, false
	return m.read()
}

// wheel scrolls the list or the reader under the pointer as ↑↓ would.
func (m mailListModel) wheel(x int, k tea.KeyPressMsg) (mailListModel, tea.Cmd) {
	if m.typing() {
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

func (m mailListModel) listWidth() int {
	if m.split() {
		return m.width * 2 / 5
	}
	return m.width
}

func (m mailListModel) readerWidth() int {
	if m.split() {
		return m.width - m.listWidth()
	}
	return m.width
}

// listTop is how many lines sit above the rows: the search field.
func (m mailListModel) listTop() int {
	if m.search != nil || m.query != "" {
		return 2
	}
	return 0
}

func (m mailListModel) listHeight() int { return max(1, m.height-2-m.listTop()) }

func (m mailListModel) scrolled() mailListModel {
	h := m.listHeight()
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+h {
		m.offset = m.sel - h + 1
	}
	m.offset = max(0, m.offset)
	return m
}

func (m mailListModel) title() string {
	title := textfmt.Plain(m.folder.Title)
	switch {
	case m.visual:
		n := 0
		for _, on := range m.marked {
			if on {
				n++
			}
		}
		title += fmt.Sprintf(" · VISUAL (%d selected)", n)
	case m.query != "":
		title += " · search"
	}
	return title
}

func (m mailListModel) view(focused bool) string {
	m.focused = focused
	if m.reading != nil && !m.split() {
		return renderBox(textfmt.Plain(m.folder.Title), m.readerBody(m.width-4, m.height-2),
			m.width, m.height, m.st.border(focused))
	}
	listFocused := focused && !(m.split() && m.readerFocus)
	list := renderBox(m.title(), m.listBody(m.listWidth()-4, m.height-2, listFocused),
		m.listWidth(), m.height, m.st.border(listFocused))
	if !m.split() {
		return list
	}
	var body []string
	if m.reading != nil {
		body = m.readerBody(m.readerWidth()-4, m.height-2)
	} else {
		body = centerLines(m.readerWidth()-4, m.height-2, m.st.subtle.Render("No mail selected"))
	}
	reader := renderBox("", body, m.readerWidth(), m.height,
		m.st.border(focused && m.readerFocus))
	return joinColumns(list, reader)
}

// listBody is the search line, if any, then the rows.
func (m mailListModel) listBody(width, height int, focused bool) []string {
	var lines []string
	switch {
	case m.search != nil:
		m.search.SetWidth(max(1, width-10))
		lines = append(lines, m.st.accent.Render("Search    ")+m.search.View(), "")
	case m.query != "":
		lines = append(lines, m.st.subtle.Render(textfmt.ClipLine("Search    "+m.query+
			"  · esc clears", width)), "")
	}
	rows := m.rows()
	switch {
	case m.err != nil && len(rows) == 0:
		return append(lines, centerLines(width, height-len(lines),
			styleError.Render(textfmt.CleanError(m.err)))...)
	case m.syncing() || (!m.loaded && m.loading):
		return append(lines, centerLines(width, height-len(lines),
			m.spin+m.st.subtle.Render(" Loading emails…"))...)
	case len(rows) == 0 && m.query != "":
		return append(lines, centerLines(width, height-len(lines),
			m.st.subtle.Render("Nothing found"))...)
	case len(rows) == 0:
		return append(lines, centerLines(width, height-len(lines),
			m.st.subtle.Render("No mail here"))...)
	}
	now := time.Now()
	for i := m.offset; i < min(len(rows), m.offset+height-len(lines)); i++ {
		lines = append(lines, m.rowLine(rows[i], i == m.sel, focused, width, now))
	}
	return lines
}

// rowLine draws a row: the cursor and visual marks, an unread dot, who the
// conversation is with, its subject and size, and the date at the right.
func (m mailListModel) rowLine(r mailRow, selected, focused bool, width int, now time.Time) string {
	t := m.threads[r.thread]
	marker := "  "
	if selected {
		marker = m.st.accent.Render(">") + " "
	}
	ids := m.rowIDs(r)
	if m.visual && m.marked[ids[0]] {
		marker = marker[:len(marker)-1] + m.st.accent.Render("*")
		if !selected {
			marker = " " + m.st.accent.Render("*")
		}
	}
	msg := m.shown(r)
	unread := r.msg < 0 && t.Unread > 0
	who, text := textfmt.Plain(t.With), textfmt.Plain(t.Subject)
	if r.msg >= 0 {
		who = "  " + backend.GenericMessageSender(msg)
		text = textfmt.ShortExcerpt(textfmt.Plain(msg.Text))
		unread = !msg.Outgoing && t.Unread > 0 && m.unreadIn(msg)
	} else {
		if text == "" {
			text = "(no subject)"
		}
		if n := len(t.Messages); n > 1 {
			text += fmt.Sprintf(" (%d)", n)
		}
	}
	dot := "  "
	if unread {
		dot = m.st.accent.Render("●") + " "
	}
	date := mailDate(msg.Timestamp, now)
	whoWidth := min(22, max(8, width/4))
	who = truncate(who, whoWidth)
	who += strings.Repeat(" ", whoWidth-textfmt.DisplayWidth(who))
	room := max(1, width-2-2-whoWidth-2-textfmt.DisplayWidth(date)-1)
	text = truncate(text, room)
	gap := strings.Repeat(" ", max(1, room-textfmt.DisplayWidth(text)+1))
	line := who + "  " + text + gap + date
	switch {
	case selected && focused:
		return marker + dot + m.st.selectedRow.Render(line)
	case unread:
		return marker + dot + boldLine(textfmt.NickColor(t.With)+who+"\x1b[0m"+"  "+text+gap+date)
	case r.msg >= 0:
		return marker + dot + m.st.subtle.Render(line)
	}
	return marker + dot + textfmt.NickColor(t.With) + who + "\x1b[0m" + "  " + text + gap +
		m.st.subtle.Render(date)
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
	}
	actions := []helpKey{{"r", "reply"}, {"R", "all"}, {"f", "forward"}, {"d", "trash"}}
	if m.mb.ArchiveFolder() != "" {
		actions = append(actions, helpKey{"a", "archive"})
	}
	actions = append(actions, helpKey{"m", "move"}, helpKey{"u", "unread"})
	if m.reading != nil && (m.readerFocus || !m.split()) {
		keys := []helpKey{{"↑↓", "scroll"}}
		if _, ok := m.b.MediaSaveable(*m.reading); ok {
			keys = append(keys, helpKey{"o", "open file"}, helpKey{"s", "save file"})
		}
		keys = append(keys, actions...)
		if m.split() {
			return append(keys, helpKey{"[", "list"}, helpKey{"esc", "list"})
		}
		return append(keys, helpKey{"esc", "back"})
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
