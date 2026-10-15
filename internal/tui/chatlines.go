package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// readReporter tracks how far the server knows the open chat is read. Only
// Update touches it; MarkRead runs as a command and reports back with
// chatMarkedRead.
type readReporter struct {
	marked   string // the newest message ID the server has been told about
	inFlight bool
}

type chatMarkedRead struct {
	chatTag
	id  string
	err error
}

// newestIncoming is the ID of the newest message from someone else. Messages
// are in chronological order; their IDs do not sort on every backend.
func newestIncoming(msgs []backend.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].Outgoing {
			return msgs[i].ID
		}
	}
	return ""
}

// Generic search result lines.
func (s *styles) genericSearchResultLines(b backend.Backend, results []backend.Message, selected int, entry backend.ChatEntry, query string, width, height int) []string {
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
				marker = s.accent.Render("> ")
			}
			text := backend.GenericMessageLine(results[i], entry, nil)
			lines = append(lines, textfmt.ClipLine(marker+text, width))
		}
	}
	return lines
}

// Generic selected lines rendering.
// genericSelectedLines also returns, for each line, the index of the message
// it belongs to (-1 for none) and whether it draws that message's attachment,
// so mouse clicks can find what they hit.
// Day and unread dividers sit above the message they introduce; firstUnread
// is the ID of the oldest message unread when the chat opened ("" for none)
// and unread holds the IDs of all of them, drawn in bold.
func (s *styles) genericSelectedLines(b backend.Backend, messages []backend.Message, entry backend.ChatEntry, selected int, changed map[string]bool, images map[string]*chatImage, quotes map[string]*backend.Message, replying *backend.Message, typing bool, firstUnread string, unread map[string]bool, flash reactionKey, width, height int) ([]string, []int, []rowKind) {
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
	messageLines := func(i int) ([]string, []rowKind) {
		msg := messages[i]
		quoted := byID[msg.ReplyToID]
		if quoted == nil {
			quoted = quotes[msg.ReplyToID]
		}
		if quoted == nil {
			quoted = msg.Quote
		}
		return s.messageBlock(b, msg, entry, quoted, replies[msg.ID],
			changed[msg.ID], unread[msg.ID], images[msg.ID], flash.on(msg.ID),
			lineWidth, imageRows(height))
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		lines, owners, kinds := []string{}, []int{}, []rowKind{}
		if replying != nil {
			lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
			owners = append(owners, -1)
			kinds = append(kinds, rowText)
		}
		return lines, owners, kinds
	}
	now := time.Now()
	dividers := make(map[int]int) // divider lines at the top of each block
	blocks := make(map[int][]string)
	blockKinds := make(map[int][]rowKind)
	block := func(i int) []string {
		if lines := blocks[i]; lines != nil {
			return lines
		}
		lines, kinds := messageLines(i)
		if bg := s.blockShade(messages[i], i == selected); bg != "" {
			for j := range lines {
				lines[j] = shadeLine(lines[j], bg, lineWidth)
			}
		}
		var top []string
		if startsDay(messages, i) {
			top = append(top, s.dividerLine(dayLabel(messages[i].Timestamp, now), s.subtle,
				lineWidth))
		}
		if firstUnread != "" && messages[i].ID == firstUnread {
			top = append(top, s.dividerLine("unread", s.accent, lineWidth))
		}
		dividers[i] = len(top)
		lines = append(top, lines...)
		blocks[i] = lines
		blockKinds[i] = append(make([]rowKind, len(top)), kinds...)
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
	kinds := make([]rowKind, 0, used+1)
	for i := start; i < end; i++ {
		visible := block(i)
		if i == selected && len(visible) > available {
			visible = selectedBlock
		}
		lines = append(lines, visible...)
		kinds = append(kinds, blockKinds[i][:len(visible)]...)
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
		kinds = append(kinds, rowText)
	}
	if typing {
		lines = append(lines, textfmt.ClipLine(textfmt.Plain(entry.Title)+" is typing...", lineWidth))
		owners = append(owners, -1)
		kinds = append(kinds, rowText)
	}
	return lines, owners, kinds
}

// dividerLine centres label in a rule across the chat, like ── yesterday ──.
func (s *styles) dividerLine(label string, style lipgloss.Style, width int) string {
	label = " " + label + " "
	side := max(2, (width-textfmt.DisplayWidth(label))/2)
	rule := strings.Repeat("─", side)
	return textfmt.ClipLine(s.dim.Render(rule)+style.Render(label)+s.dim.Render(rule), width)
}

// startsDay reports whether messages[i] is the first of its day. Messages
// without a time (a WhatsApp quote) start none and are skipped over.
func startsDay(messages []backend.Message, i int) bool {
	t := messages[i].Timestamp
	if t.IsZero() {
		return false
	}
	for j := i - 1; j >= 0; j-- {
		if prev := messages[j].Timestamp; !prev.IsZero() {
			return !sameDay(prev, t)
		}
	}
	return true
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
// An unread message has its header and text in bold. Alongside the lines it
// marks those that draw the attachment (its image card or its [tag] row) and
// the meta row. flash is the reaction drawn highlighted, if any.
func (s *styles) messageBlock(b backend.Backend, msg backend.Message, entry backend.ChatEntry,
	quoted *backend.Message, replies int, edited, unread bool, img *chatImage, flash string,
	lineWidth, maxImageRows int) ([]string, []rowKind) {
	gutter := "  "
	if edited {
		gutter = "* "
	}
	indent := s.textIndent(entry)
	body, below := s.imageText(msg, img, indent, lineWidth, maxImageRows)
	prefix := gutter + s.messageHeader(msg, entry, unread) + " "
	var lines []string
	if msg.Multiline {
		lines = s.paragraphLines(msg, body, prefix, quoted, entry, lineWidth, indent)
	} else if body = styleLinks(s.dimTag(body)); msg.ReplyToID == "" {
		lines = textfmt.WrapMessage(prefix, body, lineWidth, indent)
	} else {
		// Replies put the quoted original on the header row and the text below it.
		quote := textfmt.ClipLine(prefix+s.quote(quoted), lineWidth)
		lines = append([]string{quote},
			textfmt.WrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
	}
	lines = carryStyles(lines)
	if unread {
		for i := range lines {
			lines[i] = boldLine(lines[i])
		}
	}
	kinds := make([]rowKind, len(lines), len(lines)+len(below))
	if tag := mediaTag(msg.MediaLabel); tag != "" {
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), tag) {
				kinds[i] = rowMedia
			}
		}
	}
	if len(kinds) > 0 {
		kinds[0] |= rowHeader
	}
	lines = append(lines, below...)
	for range below {
		kinds = append(kinds, rowMedia)
	}
	lines = append(lines, s.previewCard(msg.Preview, indent, lineWidth)...)
	kinds = append(kinds, make([]rowKind, len(lines)-len(kinds))...)
	if meta := s.metaRow(b, msg, entry, replies, flash); meta != "" {
		lines = append(lines, textfmt.ClipLine(strings.Repeat(" ", indent)+meta, lineWidth))
		kinds = append(kinds, rowMeta)
	}
	return lines, kinds
}

// rowKind is what a line of a message block draws, so a click knows what it
// hit. The header row is a flag, since it can draw the attachment's tag too.
type rowKind uint8

const (
	rowText   rowKind = 0
	rowMedia  rowKind = 1 << iota // the attachment: its image card or [tag] row
	rowMeta                       // reactions, reply count, pin and ticks
	rowHeader                     // the first row: time, sender and the text's start
)

// reactionKey names one reaction on one message; the zero value names none.
type reactionKey struct{ msgID, emoji string }

// on is the emoji to highlight on message id, or "".
func (k reactionKey) on(id string) string {
	if k.msgID != id {
		return ""
	}
	return k.emoji
}

// mediaTag is the first bracket tag of a media label, like "[photo]".
func mediaTag(label string) string {
	start := strings.Index(label, "[")
	end := strings.Index(label, "]")
	if start < 0 || end < start {
		return ""
	}
	return label[start : end+1]
}

// paragraphLines lays out a message that keeps its line breaks, each line
// of the text wrapped on its own.
func (s *styles) paragraphLines(msg backend.Message, body, prefix string, quoted *backend.Message,
	entry backend.ChatEntry, lineWidth, indent int) []string {
	pad := strings.Repeat(" ", indent)
	reply := msg.ReplyToID != ""
	var text []string
	for i, para := range bodyParagraphs(msg, body) {
		para = styleLinks(s.dimTag(para))
		if i == 0 && !reply {
			text = textfmt.WrapMessage(prefix, para, lineWidth, indent)
			continue
		}
		text = append(text, textfmt.WrapLineIndent(pad+para, lineWidth, indent)...)
	}
	if reply {
		quote := textfmt.ClipLine(prefix+s.quote(quoted), lineWidth)
		return append([]string{quote}, text...)
	}
	return text
}

// bodyParagraphs splits a message's text at its line breaks, with any media
// label on a line of its own. A caption moved into an image card leaves only
// what imageText returned.
func bodyParagraphs(msg backend.Message, body string) []string {
	if body != messageText(msg) {
		return []string{body}
	}
	paras := strings.Split(strings.TrimRight(styleSpans(msg.Text, msg.Spans), "\n"), "\n")
	if label := textfmt.Plain(msg.MediaLabel); label != "" {
		if paras[0] == "" {
			paras[0] = label
		} else {
			paras = append([]string{label}, paras...)
		}
	}
	return paras
}

// messageText is backend.GenericMessageText on one line with the text's
// spans styled.
func messageText(msg backend.Message) string {
	text := strings.ReplaceAll(styleSpans(msg.Text, msg.Spans), "\n", " ")
	label := textfmt.Plain(msg.MediaLabel)
	switch {
	case label == "":
		return text
	case text == "":
		return label
	}
	return label + " — " + text
}

// metaRow joins a message's reactions, reply count, pin and read ticks into
// one dim row, or "" when there is nothing to show. The reaction flash names
// is drawn reversed.
func (s *styles) metaRow(b backend.Backend, msg backend.Message, entry backend.ChatEntry,
	replies int, flash string) string {
	var reactions []string
	for _, r := range shownReactions(msg) {
		count := fmt.Sprint(r.Count)
		switch {
		case r.Emoji == flash:
			reactions = append(reactions, s.accent.Reverse(true).Render(r.Emoji+" "+count))
			continue
		case r.Chosen:
			count = s.accent.Render(count)
		default:
			count = s.subtle.Render(count)
		}
		reactions = append(reactions, r.Emoji+" "+count)
	}
	var parts []string
	if len(reactions) > 0 {
		parts = append(parts, strings.Join(reactions, "  "))
	}
	if replies > 0 {
		parts = append(parts, s.subtle.Render(fmt.Sprintf("%d %s", replies,
			plural(replies, "reply", "replies"))))
	}
	if msg.Pinned {
		parts = append(parts, s.subtle.Render("pinned"))
	}
	if tick := strings.TrimSpace(b.ReadMarker(entry, msg)); tick != "" {
		parts = append(parts, s.subtle.Render(tick))
	}
	return strings.Join(parts, s.dim.Render(" · "))
}

// shownReactions are the reactions metaRow draws, in order.
func shownReactions(msg backend.Message) []backend.MessageReaction {
	var shown []backend.MessageReaction
	for _, r := range msg.Reactions {
		if r.Emoji != "" && r.Count > 0 {
			shown = append(shown, r)
		}
	}
	return shown
}

// reactionAt is the reaction metaRow draws at column col of the meta row,
// counted from the start of the message's text column.
func reactionAt(msg backend.Message, col int) (string, bool) {
	x := 0
	for _, r := range shownReactions(msg) {
		w := textfmt.DisplayWidth(r.Emoji) + 1 + len(fmt.Sprint(r.Count))
		if col >= x && col < x+w {
			return r.Emoji, true
		}
		x += w + 2
	}
	return "", false
}

// previewCard draws a link preview as a small box under the message text,
// or nothing when there is no preview or too little room.
func (s *styles) previewCard(p *backend.LinkPreview, indent, lineWidth int) []string {
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
			body = append(body, s.subtle.Render(line))
		}
	}
	site := p.Site
	if site == "" {
		site = p.URL
	}
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(renderBox(site, body, width, len(body)+2, s.dimColor), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return lines
}
