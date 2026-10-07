package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"

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
	return "# Group", colorQuote
}

type unreadCard struct {
	entry backend.ChatEntry
	msgs  []backend.Message
	err   error
}

type unreadLoaded struct {
	gen   int
	cards []unreadCard
}
type unreadPicked struct{ entry backend.ChatEntry }
type unreadClosed struct{}

// unreadModel is a column of full-width cards, one per chat with unread
// messages.
type unreadModel struct {
	ctx      context.Context
	b        backend.Backend
	entries  []backend.ChatEntry
	cards    []unreadCard
	loading  bool
	gen      int
	selected int
	offset   int // first visible card
	width    int
	height   int
}

func newUnread(ctx context.Context, b backend.Backend, entries []backend.ChatEntry) (unreadModel, tea.Cmd) {
	var unread []backend.ChatEntry
	for _, e := range entries {
		if e.Unread > 0 && len(unread) < unreadMaxChats {
			unread = append(unread, e)
		}
	}
	m := unreadModel{ctx: ctx, b: b, entries: unread}
	return m.load()
}

func (m unreadModel) load() (unreadModel, tea.Cmd) {
	m.gen++
	m.loading = true
	ctx, b, entries, gen := m.ctx, m.b, m.entries, m.gen
	return m, func() tea.Msg {
		cards := make([]unreadCard, 0, len(entries))
		for _, e := range entries {
			msgs, err := b.History(ctx, e)
			cards = append(cards, unreadCard{entry: e, msgs: lastUnread(msgs, e.Unread), err: err})
		}
		return unreadLoaded{gen, cards}
	}
}

// lastUnread keeps the newest incoming messages among the unread tail.
func lastUnread(msgs []backend.Message, unread int) []backend.Message {
	msgs = msgs[max(0, len(msgs)-unread):]
	var in []backend.Message
	for _, msg := range msgs {
		if !msg.Outgoing {
			in = append(in, msg)
		}
	}
	return in[max(0, len(in)-unreadCardLines):]
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
		m.cards = msg.cards
		m.selected = min(m.selected, max(0, len(m.cards)-1))
		return m.scrolled(), nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+d":
			return m, func() tea.Msg { return unreadClosed{} }
		case "r":
			return m.load()
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(max(0, len(m.cards)-1), m.selected+1)
		case "enter":
			if m.selected < len(m.cards) {
				entry := m.cards[m.selected].entry
				return m, func() tea.Msg { return unreadPicked{entry} }
			}
		}
		return m.scrolled(), nil
	}
	return m, nil
}

func (m unreadModel) view(focused bool) string {
	title := "Unread"
	var body []string
	switch {
	case m.loading && len(m.cards) == 0:
		body = []string{styleSubtle.Render("Loading…")}
	case len(m.cards) == 0:
		body = []string{styleSubtle.Render("Nothing unread")}
	default:
		end := min(len(m.cards), m.offset+m.visibleRows())
		for i := m.offset; i < end; i++ {
			body = append(body, strings.Split(m.card(i, m.width-4, focused), "\n")...)
		}
		if m.loading {
			title = "Unread · refreshing…"
		}
	}
	return renderBox(title, body, m.width, m.height, boxBorder(focused))
}

func (m unreadModel) card(i, width int, focused bool) string {
	c := m.cards[i]
	kind, title := kindOf(c.entry)
	tag, border := kind.tag()
	head := lipgloss.NewStyle().Bold(true).Foreground(border).Render(tag)
	for _, msg := range c.msgs {
		if msg.ReplyToID != "" {
			head += styleDim.Render(" · ") + styleQuoteBar.Render("thread")
			break
		}
	}
	pill := styleBadge.Render(fmt.Sprintf(" %d ", c.entry.Unread))
	inner := width - 4
	head += strings.Repeat(" ", max(1, inner-lipgloss.Width(head)-lipgloss.Width(pill))) + pill

	lines := []string{head}
	if c.err != nil {
		lines = append(lines, styleError.Render(textfmt.CleanError(c.err)))
	}
	for _, msg := range c.msgs {
		prefix := ""
		if msg.ReplyToID != "" {
			prefix = styleDim.Render("↳ ")
		}
		sender := backend.GenericMessageSender(msg)
		text := textfmt.ShortExcerpt(backend.GenericMessageText(msg))
		lines = append(lines, prefix+textfmt.NickColor(sender)+sender+"\x1b[0m"+
			styleSubtle.Render(": ")+text)
	}
	if i == m.selected {
		if focused {
			border = colorAccent
		}
		title = "> " + title
	}
	return renderBox(title, lines, width, unreadCardH, border)
}

func (m unreadModel) helpKeys() []helpKey { return unreadHelp }

var unreadHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎/click", "open chat"},
	{"r", "reload"},
	{"esc", "back"},
}
