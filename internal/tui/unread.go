package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

const (
	unreadMaxChats  = 24
	unreadCardLines = 3
)

type chatKind int

const (
	kindGroup chatKind = iota
	kindDM
	kindChannel
)

// kindOf derives the chat kind; Telegram marks channels only by their title.
func kindOf(e backend.ChatEntry) (chatKind, string) {
	title := textfmt.Plain(e.Title)
	if e.IsDM {
		return kindDM, title
	}
	if rest, ok := strings.CutPrefix(title, "Channel: "); ok {
		return kindChannel, rest
	}
	return kindGroup, title
}

type unreadCard struct {
	entry backend.ChatEntry
	msgs  []backend.Message
	count int // the pill: unread messages, or mentions in the Mentions view
	err   error
	// waiting marks a bot inbox chat with a reply waiting for approval.
	waiting bool
}

type unreadLoaded struct {
	gen   int
	cards []unreadCard
	err   error
}
type unreadPicked struct{ card unreadCard }
type unreadClosed struct{}

// unreadModel is a column of full-width cards, one per chat with unread
// messages or, in the Mentions view, with messages mentioning the user, or,
// in a bot's inbox, one per private chat of the bot.
type unreadModel struct {
	st       *styles
	ctx      context.Context
	b        backend.Backend
	mentions bool
	bot      *inboxBot // the bot whose inbox this is, over b, its connection
	waiting  map[backend.ChatID]bool
	entries  []backend.ChatEntry
	cards    []unreadCard
	err      error
	loading  bool
	gen      int
	selected int
	offset   int // first visible card
	width    int
	height   int
}

func newUnread(ctx context.Context, st *styles, b backend.Backend, entries []backend.ChatEntry) (unreadModel, tea.Cmd) {
	var unread []backend.ChatEntry
	for _, e := range entries {
		if e.Unread > 0 && len(unread) < unreadMaxChats {
			unread = append(unread, e)
		}
	}
	m := unreadModel{st: st, ctx: ctx, b: b, entries: unread}
	return m.load()
}

func (m unreadModel) load() (unreadModel, tea.Cmd) {
	m.gen++
	m.loading = true
	ctx, b, entries, gen := m.ctx, m.b, m.entries, m.gen
	if m.bot != nil {
		waiting := m.waiting
		return m, func() tea.Msg {
			cards, err := inboxCards(ctx, b, waiting)
			return unreadLoaded{gen, cards, err}
		}
	}
	if m.mentions {
		return m, func() tea.Msg {
			cards, err := mentionCards(ctx, b)
			return unreadLoaded{gen, cards, err}
		}
	}
	return m, func() tea.Msg {
		cards := make([]unreadCard, len(entries))
		var wg sync.WaitGroup
		for i, e := range entries {
			wg.Go(func() {
				msgs, err := unreadMessages(ctx, b, e, unreadCardLines)
				cards[i] = unreadCard{entry: e, msgs: msgs, count: e.Unread, err: err}
			})
		}
		wg.Wait()
		return unreadLoaded{gen, cards, nil}
	}
}

// unreadMessages returns up to n of the chat's newest unread messages, from
// the backend's unread API when it has one, else from the unread tail of a
// History page.
func unreadMessages(ctx context.Context, b backend.Backend, e backend.ChatEntry,
	n int) ([]backend.Message, error) {
	if f, ok := b.(backend.UnreadFetcher); ok {
		return f.UnreadMessages(ctx, e, min(e.Unread, n))
	}
	msgs, err := b.History(ctx, e)
	return backend.NewestIncoming(msgs[max(0, len(msgs)-e.Unread):], n), err
}

func (m unreadModel) resized(width, height int) unreadModel {
	m.width, m.height = width, height
	return m.scrolled()
}

// cardHeight is the rows card i takes: its lines, its keys when selected,
// and its border.
func (m unreadModel) cardHeight(i int) int {
	return len(m.cardLines(i, max(1, m.width-8))) + 2
}

// scrolled keeps the selected card whole inside the pane.
func (m unreadModel) scrolled() unreadModel {
	if m.selected < m.offset {
		m.offset = m.selected
	}
	room := m.height - 2
	for m.offset < m.selected {
		used := 0
		for i := m.offset; i <= m.selected; i++ {
			used += m.cardHeight(i)
		}
		if used <= room {
			break
		}
		m.offset++
	}
	return m
}

// cardAt maps a click at pane row y to a card.
func (m unreadModel) cardAt(y int) (int, bool) {
	if y < 1 {
		return 0, false
	}
	top := 1
	for i := m.offset; i < len(m.cards); i++ {
		h := m.cardHeight(i)
		if y < top+h {
			return i, true
		}
		top += h
	}
	return 0, false
}

type unreadThread struct {
	card unreadCard
	msg  backend.Message
}
type unreadMarked struct{ err error }

// threadReply is the newest reply in the card, when the backend keeps its
// thread on the server for t to open.
func (m unreadModel) threadReply(c unreadCard) (backend.Message, bool) {
	if _, ok := m.b.(backend.ThreadFetcher); !ok || m.bot != nil {
		return backend.Message{}, false
	}
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if c.msgs[i].ReplyToID != "" {
			return c.msgs[i], true
		}
	}
	return backend.Message{}, false
}

// canMark reports whether m marks the card's chat read: unread chats of the
// user's own account.
func (m unreadModel) canMark(c unreadCard) bool {
	return m.bot == nil && !m.mentions && c.count > 0
}

func (m unreadModel) Update(msg tea.Msg) (unreadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case unreadLoaded:
		if msg.gen != m.gen {
			return m, nil
		}
		m.loading = false
		m.cards, m.err = msg.cards, msg.err
		m.selected = min(m.selected, max(0, len(m.cards)-1))
		return m.scrolled(), nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, func() tea.Msg { return unreadClosed{} }
		case "r":
			return m.load()
		case "/":
			return m, func() tea.Msg { return chatListSearch{} }
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(max(0, len(m.cards)-1), m.selected+1)
		case "enter":
			if m.selected < len(m.cards) {
				card := m.cards[m.selected]
				return m, func() tea.Msg { return unreadPicked{card} }
			}
		case "t":
			if m.selected < len(m.cards) {
				card := m.cards[m.selected]
				if reply, ok := m.threadReply(card); ok {
					return m, func() tea.Msg { return unreadThread{card, reply} }
				}
			}
		case "m":
			if m.selected < len(m.cards) && m.canMark(m.cards[m.selected]) {
				return m.markRead()
			}
		}
		return m.scrolled(), nil
	}
	return m, nil
}

// markRead marks the selected card's chat read up to its newest message and
// drops the card.
func (m unreadModel) markRead() (unreadModel, tea.Cmd) {
	c := m.cards[m.selected]
	newest := ""
	var at time.Time
	for _, msg := range c.msgs {
		if newest == "" || !msg.Timestamp.Before(at) {
			newest, at = msg.ID, msg.Timestamp
		}
	}
	m.cards = append(m.cards[:m.selected:m.selected], m.cards[m.selected+1:]...)
	m.selected = min(m.selected, max(0, len(m.cards)-1))
	ctx, b := m.ctx, m.b
	return m.scrolled(), func() tea.Msg {
		return unreadMarked{b.MarkRead(ctx, c.entry, newest)}
	}
}

func (m unreadModel) name() string {
	if m.bot != nil {
		return agentIcon + " @" + m.bot.handle + " inbox"
	}
	if m.mentions {
		return "Mentions"
	}
	return "Unread"
}

func (m unreadModel) view(focused bool) string {
	title := m.name()
	var body []string
	if m.err != nil {
		body = []string{styleError.Render(textfmt.CleanError(m.err))}
	}
	switch {
	case m.loading && len(m.cards) == 0:
		body = append(body, m.st.subtle.Render("Loading…"))
	case len(m.cards) == 0 && m.bot != nil:
		body = append(body, m.st.subtle.Render("No one has messaged @"+m.bot.handle+" yet"))
	case len(m.cards) == 0 && m.mentions:
		body = append(body, m.st.subtle.Render("No mentions"))
	case len(m.cards) == 0:
		body = append(body, m.st.subtle.Render("Nothing unread"))
	default:
		for i := m.offset; i < len(m.cards) && len(body) < m.height-2; i++ {
			body = append(body, m.card(i, m.width-4, focused)...)
		}
		title += fmt.Sprintf(" · %d %s", len(m.cards), plural(len(m.cards), "chat", "chats"))
		if m.loading {
			title += " · refreshing…"
		}
	}
	return renderBox(title, body, m.width, m.height, m.st.border(focused))
}

// card draws card i width cells wide: the chat's name, team and any reply
// waiting in its top border, with the time of its newest message and the
// count on the right, then its messages.
func (m unreadModel) card(i, width int, focused bool) []string {
	c := m.cards[i]
	selected := i == m.selected
	border := m.st.dimColor
	nameStyle := styleTitle
	if selected {
		border = m.st.subtle.GetForeground()
		if focused {
			border, nameStyle = m.st.accentColor, m.st.brand
		}
	}
	right := ""
	if at := newestAt(c.msgs); !at.IsZero() {
		right = m.st.subtle.Render(whenText(at, time.Now()))
	}
	if c.count > 0 {
		right = strings.TrimPrefix(right+" "+m.st.badge.Render(fmt.Sprintf(" %d ", c.count)), " ")
	}
	lead := ""
	if selected {
		lead = nameStyle.Render("› ")
	}
	glyph := "#"
	if c.entry.IsDM || c.entry.IsSelf {
		glyph = "@"
	}
	lead += m.st.dim.Render(glyph)
	tail := ""
	if c.entry.Section != "" {
		tail += " " + m.st.tag.Render(" "+strings.ToLower(c.entry.Section)+" ")
	}
	if c.waiting {
		tail += " " + m.st.agent.Render(agentIcon+" reply waiting")
	}
	// ╭─ lead name tail ──── right ─╮
	room := width - 6 - textfmt.DisplayWidth(lead) - textfmt.DisplayWidth(tail) -
		textfmt.DisplayWidth(right) - 1
	name := truncate(sidebarTitle(c.entry), max(1, room))
	bs := lipgloss.NewStyle().Foreground(border)
	head := " " + lead + nameStyle.Render(name) + tail + " "
	fill := max(1, width-3-textfmt.DisplayWidth(head)-textfmt.DisplayWidth(right)-2)
	top := bs.Render("╭─") + head + bs.Render(strings.Repeat("─", fill))
	if right != "" {
		top += " " + right + " "
	}
	top += bs.Render(strings.Repeat("─", max(0, width-1-textfmt.DisplayWidth(top))) + "╮")

	inner := width - 4
	side := bs.Render("│")
	lines := []string{top}
	for _, l := range m.cardLines(i, inner) {
		l = textfmt.ClipLine(l, inner)
		lines = append(lines, side+" "+l+strings.Repeat(" ", max(0, inner-textfmt.DisplayWidth(l)))+
			" "+side)
	}
	return append(lines, bs.Render("╰"+strings.Repeat("─", width-2)+"╯"))
}

// unreadNickWidth is the author column, so every message starts in line.
const unreadNickWidth = 16

// cardLines is card i's body: its messages, a run of the same message from
// the same sender as one line with ×N, and on the selected card its keys.
func (m unreadModel) cardLines(i, width int) []string {
	c := m.cards[i]
	var lines []string
	if c.err != nil {
		lines = append(lines, styleError.Render(textfmt.CleanError(c.err)))
	}
	threaded := false
	for _, msg := range c.msgs {
		threaded = threaded || msg.ReplyToID != ""
	}
	for j := 0; j < len(c.msgs); {
		msg := c.msgs[j]
		n := 1
		for j+n < len(c.msgs) && c.msgs[j+n].Sender == msg.Sender &&
			backend.GenericMessageText(c.msgs[j+n]) == backend.GenericMessageText(msg) {
			n++
		}
		j += n
		line := ""
		if threaded {
			line = "  "
			if msg.ReplyToID != "" {
				line = m.st.dim.Render("↳ ")
			}
		}
		sender := backend.GenericMessageSender(msg)
		nick := textfmt.NickColor(sender)
		if m.bot != nil && msg.Outgoing {
			sender, nick = agentIcon+" "+m.bot.name, "" // the bot wrote it, not the user
		}
		col := truncate(textfmt.Plain(sender), unreadNickWidth)
		col += strings.Repeat(" ", unreadNickWidth-textfmt.DisplayWidth(col))
		switch {
		case c.entry.IsChannel: // a channel posts as itself
		case nick == "":
			line += m.st.agent.Render(col) + " "
		default:
			line += nick + col + "\x1b[0m "
		}
		repeat := ""
		if n > 1 {
			repeat = " " + m.st.tag.Render(fmt.Sprintf(" ×%d ", n))
		}
		room := width - textfmt.DisplayWidth(line) - textfmt.DisplayWidth(repeat)
		lines = append(lines, line+m.excerpt(msg, room)+repeat)
	}
	if i == m.selected {
		keys := []helpKey{{"enter", "open"}}
		if _, ok := m.threadReply(c); ok {
			keys = append(keys, helpKey{"t", "open thread"})
		}
		if m.canMark(c) {
			keys = append(keys, helpKey{"m", "mark read"})
		}
		var parts []string
		for _, k := range keys {
			parts = append(parts, m.st.accent.Render(k.key)+" "+m.st.subtle.Render(k.desc))
		}
		lines = append(lines, m.st.dim.Render(strings.Repeat("╌", width)),
			strings.Join(parts, "   "))
	}
	return lines
}

// excerpt is the message on one line, width cells at most: its media kind as
// a tag, emoji codes dim and, in a message mentioning the user, @names in the
// accent.
func (m unreadModel) excerpt(msg backend.Message, width int) string {
	kind, text := "", textfmt.Plain(msg.Text)
	if label := textfmt.Plain(msg.MediaLabel); label != "" {
		if rest, ok := strings.CutPrefix(label, "["); ok {
			if k, details, ok := strings.Cut(rest, "]"); ok {
				kind, label = k, strings.TrimSpace(details)
			}
		}
		switch {
		case label != "" && text != "":
			text = label + " — " + text
		case label != "":
			text = label
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && kind == "" {
		text = "message"
	}
	out := ""
	if kind != "" {
		out = m.st.tag.Render(" "+kind+" ") + " "
		width -= textfmt.DisplayWidth(kind) + 3
	}
	text = truncate(text, max(1, width))
	var b strings.Builder
	for _, word := range strings.SplitAfter(text, " ") {
		core := strings.TrimRight(word, " ")
		switch {
		case msg.Mentioned && strings.HasPrefix(core, "@") && len(core) > 1:
			b.WriteString(m.st.accent.Render(core) + word[len(core):])
		case shortcode.MatchString(core):
			b.WriteString(shortcode.ReplaceAllStringFunc(core, func(s string) string {
				return m.st.dim.Render(s)
			}) + word[len(core):])
		default:
			b.WriteString(word)
		}
	}
	return out + b.String()
}

func newestAt(msgs []backend.Message) time.Time {
	var at time.Time
	for _, msg := range msgs {
		if msg.Timestamp.After(at) {
			at = msg.Timestamp
		}
	}
	return at
}

// whenText is a message time for a card: relative today, then yesterday,
// the weekday this week, else the date.
func whenText(at, now time.Time) string {
	at = at.Local()
	day := func(t time.Time) time.Time {
		y, mo, d := t.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
	}
	days := int(day(now).Sub(day(at)).Hours() / 24)
	switch {
	case days == 0 && now.Sub(at) < time.Minute:
		return "now"
	case days == 0 && now.Sub(at) < time.Hour:
		return fmt.Sprintf("%dm ago", int(now.Sub(at).Minutes()))
	case days == 0:
		return fmt.Sprintf("%dh ago", int(now.Sub(at).Hours()))
	case days == 1:
		return "yesterday " + at.Format("15:04")
	case days < 7:
		return at.Format("Mon 15:04")
	case at.Year() == now.Year():
		return at.Format("Jan 2 15:04")
	}
	return at.Format("Jan 2 2006")
}

func (m unreadModel) helpKeys() []helpKey {
	keys := []helpKey{{"↑↓", "select"}, {"enter/click", "open chat"}}
	if m.selected < len(m.cards) {
		c := m.cards[m.selected]
		if _, ok := m.threadReply(c); ok {
			keys = append(keys, helpKey{"t", "open thread"})
		}
		if m.canMark(c) {
			keys = append(keys, helpKey{"m", "mark read"})
		}
	}
	return append(keys, helpKey{"/", "search"}, helpKey{"r", "reload"}, helpKey{"esc", "back"})
}
