package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

const (
	unreadMaxChats  = 24
	unreadCardLines = 3
	unreadCardH     = unreadCardLines + 3 // border, kind line, messages
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

func (k chatKind) tag() (string, color.Color) {
	switch k {
	case kindDM:
		return "● DM", colorGreen
	case kindChannel:
		return "▶ Channel", colorYellow
	}
	return "# Group", colorGroup
}

type unreadCard struct {
	entry backend.ChatEntry
	msgs  []backend.Message
	count int // the pill: unread messages, or mentions in the Mentions view
	err   error
}

type unreadLoaded struct {
	gen   int
	cards []unreadCard
	err   error
}
type unreadPicked struct{ card unreadCard }
type unreadClosed struct{}

// unreadModel is a column of full-width cards, one per chat with unread
// messages or, in the Mentions view, with messages mentioning the user.
type unreadModel struct {
	st       *styles
	ctx      context.Context
	b        backend.Backend
	mentions bool
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
				msgs, err := unreadMessages(ctx, b, e)
				cards[i] = unreadCard{entry: e, msgs: msgs, count: e.Unread, err: err}
			})
		}
		wg.Wait()
		return unreadLoaded{gen, cards, nil}
	}
}

// unreadMessages uses the backend's unread API when it has one, else keeps
// the newest incoming messages among the unread tail of a History page.
func unreadMessages(ctx context.Context, b backend.Backend, e backend.ChatEntry) ([]backend.Message, error) {
	if f, ok := b.(backend.UnreadFetcher); ok {
		return f.UnreadMessages(ctx, e, min(e.Unread, unreadCardLines))
	}
	msgs, err := b.History(ctx, e)
	return backend.NewestIncoming(msgs[max(0, len(msgs)-e.Unread):], unreadCardLines), err
}

func (m unreadModel) resized(width, height int) unreadModel {
	m.width, m.height = width, height
	return m.scrolled()
}

func (m unreadModel) visibleRows() int { return max(1, (m.height-2)/unreadCardH) }

func (m unreadModel) scrolled() unreadModel {
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+m.visibleRows() {
		m.offset = m.selected - m.visibleRows() + 1
	}
	return m
}

// cardAt maps a click at pane row y to a card.
func (m unreadModel) cardAt(y int) (int, bool) {
	if y < 1 {
		return 0, false
	}
	i := m.offset + (y-1)/unreadCardH
	return i, i < len(m.cards)
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
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(max(0, len(m.cards)-1), m.selected+1)
		case "enter":
			if m.selected < len(m.cards) {
				card := m.cards[m.selected]
				return m, func() tea.Msg { return unreadPicked{card} }
			}
		}
		return m.scrolled(), nil
	}
	return m, nil
}

func (m unreadModel) name() string {
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
	case len(m.cards) == 0 && m.mentions:
		body = append(body, m.st.subtle.Render("No mentions"))
	case len(m.cards) == 0:
		body = append(body, m.st.subtle.Render("Nothing unread"))
	default:
		end := min(len(m.cards), m.offset+m.visibleRows())
		for i := m.offset; i < end; i++ {
			body = append(body, strings.Split(m.card(i, m.width-4, focused), "\n")...)
		}
		if m.loading {
			title += " · refreshing…"
		}
	}
	return renderBox(title, body, m.width, m.height, m.st.border(focused))
}

func (m unreadModel) card(i, width int, focused bool) string {
	c := m.cards[i]
	kind, title := kindOf(c.entry)
	tag, border := kind.tag()
	head := lipgloss.NewStyle().Bold(true).Foreground(border).Render(tag)
	for _, msg := range c.msgs {
		if msg.ReplyToID != "" {
			head += m.st.dim.Render(" · ") + styleQuoteBar.Render("thread")
			break
		}
	}
	pill := m.st.badge.Render(fmt.Sprintf(" %d ", c.count))
	inner := width - 4
	head += strings.Repeat(" ", max(1, inner-lipgloss.Width(head)-lipgloss.Width(pill))) + pill

	lines := []string{head}
	if c.err != nil {
		lines = append(lines, styleError.Render(textfmt.CleanError(c.err)))
	}
	for _, msg := range c.msgs {
		prefix := ""
		if msg.ReplyToID != "" {
			prefix = m.st.dim.Render("↳ ")
		}
		sender := backend.GenericMessageSender(msg)
		text := textfmt.ShortExcerpt(backend.GenericMessageText(msg))
		if msg.Subject != "" {
			text = textfmt.ShortExcerpt(textfmt.Plain(msg.Subject))
		}
		lines = append(lines, prefix+textfmt.NickColor(sender)+sender+"\x1b[0m"+
			m.st.subtle.Render(": ")+text)
	}
	if i == m.selected {
		if focused {
			border = m.st.accentColor
		}
		title = "> " + title
	}
	return renderBox(title, lines, width, unreadCardH, border)
}

func (m unreadModel) helpKeys() []helpKey { return unreadHelp }

var unreadHelp = []helpKey{
	{"↑↓", "select"},
	{"enter/click", "open chat"},
	{"r", "reload"},
	{"esc", "back"},
}
