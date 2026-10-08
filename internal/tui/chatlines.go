package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// Generic read reporter for any backend.
type genericReadReporter struct {
	b      backend.Backend
	entry  backend.ChatEntry
	target string
	timer  *time.Timer
	done   <-chan error
	cancel context.CancelFunc
}

func newGenericReadReporter(b backend.Backend, entry backend.ChatEntry) *genericReadReporter {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &genericReadReporter{b: b, entry: entry, target: b.InboxPosition(entry), timer: timer}
}

func (r *genericReadReporter) observe(id string) {
	if id != "" && id > r.target {
		r.target = id
		r.timer.Reset(3 * time.Second)
	}
}

func (r *genericReadReporter) Start(ctx context.Context) {
	if r.done != nil {
		return
	}
	current := r.b.InboxPosition(r.entry)
	if r.target == "" || r.target <= current {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	done := make(chan error, 1)
	r.done = done
	id := r.target
	go func() { done <- r.b.MarkRead(ctx, r.entry, id) }()
}

func (r *genericReadReporter) complete() {
	r.cancel()
	r.done, r.cancel = nil, nil
	current := r.b.InboxPosition(r.entry)
	if r.target != "" && r.target > current {
		r.timer.Reset(3 * time.Second)
	}
}

func (r *genericReadReporter) Close(ctx context.Context) error {
	r.timer.Stop()
	if r.done != nil {
		r.cancel()
		<-r.done
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return r.b.MarkRead(ctx, r.entry, r.target)
}

// Generic search result lines.
func genericSearchResultLines(b backend.Backend, results []backend.Message, selected int, entry backend.ChatEntry, query string, width, height int) []string {
	width = min(width, textfmt.MaxChatLineWidth)
	available := max(1, height-2)
	var lines []string
	heading := fmt.Sprintf("Search: %s (%d results)", textfmt.Plain(query), len(results))
	lines = append(lines, textfmt.ClipLine(heading, width))
	lines = append(lines, "")
	if len(results) == 0 {
		if query != "" {
			lines = append(lines, "  No matching messages")
		}
	} else {
		start := max(0, selected-available+1)
		for i := start; i < min(len(results), start+available); i++ {
			marker := "  "
			if i == selected {
				marker = styleAccent.Render("> ")
			}
			text := backend.GenericMessageLine(results[i], entry, nil)
			lines = append(lines, textfmt.ClipLine(marker+text, width))
		}
	}
	return lines
}

// Generic selected lines rendering.
// genericSelectedLines also returns, for each line, the index of the message
// it belongs to (-1 for none) so mouse clicks can find their message.
// Day and unread dividers sit above the message they introduce; firstUnread
// is the ID of the oldest message unread when the chat opened ("" for none)
// and unread holds the IDs of all of them, drawn in bold.
func genericSelectedLines(b backend.Backend, messages []backend.Message, entry backend.ChatEntry, selected int, changed map[string]bool, images map[string]*chatImage, replying *backend.Message, typing bool, firstUnread string, unread map[string]bool, width, height int) ([]string, []int) {
	lineWidth := min(width, textfmt.MaxChatLineWidth)
	reserved := 0
	if replying != nil {
		reserved++
	}
	if typing {
		reserved++
	}
	available := max(1, height-reserved)
	byID := make(map[string]*backend.Message, len(messages))
	replies := make(map[string]int)
	for i := range messages {
		byID[messages[i].ID] = &messages[i]
		if messages[i].ReplyToID != "" {
			replies[messages[i].ReplyToID]++
		}
	}
	messageLines := func(i int) []string {
		msg := messages[i]
		return messageBlock(b, msg, entry, byID[msg.ReplyToID], replies[msg.ID],
			changed[msg.ID], unread[msg.ID], images[msg.ID],
			lineWidth, imageRows(height))
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		lines, owners := []string{}, []int{}
		if replying != nil {
			lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
			owners = append(owners, -1)
		}
		return lines, owners
	}
	now := time.Now()
	dividers := make(map[int]int) // divider lines at the top of each block
	blocks := make(map[int][]string)
	block := func(i int) []string {
		if lines := blocks[i]; lines != nil {
			return lines
		}
		lines := messageLines(i)
		if i == selected {
			for j := range lines {
				lines[j] = shadeLine(lines[j], lineWidth)
			}
		}
		var top []string
		day := messages[i].Timestamp
		if i == 0 || !sameDay(messages[i-1].Timestamp, day) {
			top = append(top, dividerLine(dayLabel(day, now), styleSubtle, lineWidth))
		}
		if firstUnread != "" && messages[i].ID == firstUnread {
			top = append(top, dividerLine("unread", styleAccent, lineWidth))
		}
		dividers[i] = len(top)
		lines = append(top, lines...)
		blocks[i] = lines
		return lines
	}
	selectedBlock := block(selected)
	if len(selectedBlock) > available {
		selectedBlock = selectedBlock[:available]
	}
	start, end, used := selected, selected+1, len(selectedBlock)
	for start > 0 {
		previous := block(start - 1)
		if used+len(previous) > available {
			break
		}
		start--
		used += len(previous)
	}
	for end < len(messages) {
		next := block(end)
		if used+len(next) > available {
			break
		}
		end++
		used += len(next)
	}
	lines := make([]string, 0, used+1)
	owners := make([]int, 0, used+1)
	for i := start; i < end; i++ {
		visible := block(i)
		if i == selected && len(visible) > available {
			visible = selectedBlock
		}
		lines = append(lines, visible...)
		for j := range visible {
			if j < dividers[i] {
				owners = append(owners, -1)
			} else {
				owners = append(owners, i)
			}
		}
	}
	if replying != nil {
		lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
		owners = append(owners, -1)
	}
	if typing {
		lines = append(lines, textfmt.ClipLine(textfmt.Plain(entry.Title)+" is typing...", lineWidth))
		owners = append(owners, -1)
	}
	return lines, owners
}

// dividerLine centres label in a rule across the chat, like ── yesterday ──.
func dividerLine(label string, style lipgloss.Style, width int) string {
	label = " " + label + " "
	side := max(2, (width-textfmt.DisplayWidth(label))/2)
	rule := strings.Repeat("─", side)
	return textfmt.ClipLine(styleDim.Render(rule)+style.Render(label)+styleDim.Render(rule), width)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

// dayLabel names t's day relative to now: today, yesterday, N days ago within
// a week, then the date.
func dayLabel(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	}
	days := int(day(now).Sub(day(t)).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 7:
		return fmt.Sprintf("%d days ago", days)
	case t.Year() == now.Year():
		return t.Format("Mon 2 Jan")
	default:
		return t.Format("2 Jan 2006")
	}
}

// messageBlock lays out one message in the shared block format: a fixed
// gutter and header, the text at textIndent, any image or preview card, and
// a meta row. Nothing in it depends on selection, so columns never move.
// An unread message has its header and text in bold.
func messageBlock(b backend.Backend, msg backend.Message, entry backend.ChatEntry,
	quoted *backend.Message, replies int, edited, unread bool, img *chatImage,
	lineWidth, maxImageRows int) []string {
	gutter := "  "
	if edited {
		gutter = "* "
	}
	indent := textIndent(entry)
	body, below := imageText(msg, img, indent, lineWidth, maxImageRows)
	body = styleLinks(styleMediaTag(body))
	prefix := gutter + messageHeader(msg, entry, unread) + " "
	var lines []string
	if msg.ReplyToID == "" {
		lines = textfmt.WrapMessage(prefix, body, lineWidth, indent)
	} else {
		// Replies put the quoted original on the header row and the text below it.
		quote := textfmt.ClipLine(prefix+renderQuote(quoted, entry), lineWidth)
		lines = append([]string{quote},
			textfmt.WrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
	}
	lines = closeLinks(lines)
	if unread {
		for i := range lines {
			lines[i] = boldLine(lines[i])
		}
	}
	lines = append(lines, below...)
	lines = append(lines, previewCard(msg.Preview, indent, lineWidth)...)
	if meta := metaRow(b, msg, entry, replies); meta != "" {
		lines = append(lines, textfmt.ClipLine(strings.Repeat(" ", indent)+meta, lineWidth))
	}
	return lines
}

// metaRow joins a message's reactions, reply count, pin and read ticks into
// one dim row, or "" when there is nothing to show.
func metaRow(b backend.Backend, msg backend.Message, entry backend.ChatEntry, replies int) string {
	var reactions []string
	for _, r := range msg.Reactions {
		if r.Emoji == "" || r.Count < 1 {
			continue
		}
		count := fmt.Sprint(r.Count)
		if r.Chosen {
			count = styleAccent.Render(count)
		} else {
			count = styleSubtle.Render(count)
		}
		reactions = append(reactions, r.Emoji+" "+count)
	}
	var parts []string
	if len(reactions) > 0 {
		parts = append(parts, strings.Join(reactions, "  "))
	}
	if replies > 0 {
		parts = append(parts, styleSubtle.Render(fmt.Sprintf("%d %s", replies,
			plural(replies, "reply", "replies"))))
	}
	if msg.Pinned {
		parts = append(parts, styleSubtle.Render("pinned"))
	}
	if tick := strings.TrimSpace(b.ReadMarker(entry, msg)); tick != "" {
		parts = append(parts, styleSubtle.Render(tick))
	}
	return strings.Join(parts, styleDim.Render(" · "))
}

// previewCard draws a link preview as a small box under the message text,
// or nothing when there is no preview or too little room.
func previewCard(p *backend.LinkPreview, indent, lineWidth int) []string {
	width := min(60, lineWidth-indent)
	if p == nil || width < 20 {
		return nil
	}
	inner := width - 4
	var body []string
	if p.Title != "" {
		body = append(body, styleTitle.Render(truncate(textfmt.Plain(p.Title), inner)))
	}
	if p.Description != "" {
		desc := textfmt.WrapLineIndent(textfmt.Plain(p.Description), inner, 0)
		for i, line := range desc[:min(2, len(desc))] {
			line = strings.TrimLeft(line, " ")
			if i == 1 && len(desc) > 2 {
				line = textfmt.ClipLine(line, inner-1) + "…"
			}
			body = append(body, styleSubtle.Render(line))
		}
	}
	site := p.Site
	if site == "" {
		site = p.URL
	}
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(renderBox(site, body, width, len(body)+2, colorDim), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return lines
}
