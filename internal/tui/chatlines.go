package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

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

// Generic selected lines rendering.
// genericSelectedLines also returns, for each line, the index of the message
// it belongs to (-1 for none) and whether it draws that message's attachment,
// so mouse clicks can find what they hit.
// Day and unread dividers sit above the message they introduce; firstUnread
// is the ID of the oldest message unread when the chat opened ("" for none)
// and unread holds the IDs of all of them, drawn in bold.
// openThread is the root of the thread open beside the chat, whose replies
// chip is drawn solid.
func (s *styles) genericSelectedLines(b backend.Backend, messages []backend.Message, entry backend.ChatEntry, selected int, changed map[string]bool, images map[string]*chatImage, quotes map[string]*backend.Message, openThread string, firstUnread string, unread map[string]bool, flash reactionKey, width, height int) ([]string, []int, []rowKind) {
	lineWidth := min(width, textfmt.MaxChatLineWidth)
	available := max(1, height)
	byID := make(map[string]*backend.Message, len(messages))
	threads := make(map[string]threadInfo)
	for i := range messages {
		byID[messages[i].ID] = &messages[i]
		if id := messages[i].ReplyToID; id != "" {
			t := threads[id]
			t.count++
			if messages[i].Timestamp.After(t.last) {
				t.last = messages[i].Timestamp
			}
			threads[id] = t
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
		t := threads[msg.ID]
		t.open = msg.ID == openThread
		return s.messageBlock(b, msg, entry, blockOpts{
			quoted: quoted, thread: t, edited: changed[msg.ID], unread: unread[msg.ID],
			selected: i == selected, cont: continues(messages, i),
			samePointer: i > 0 && msg.ReplyToID != "" && messages[i-1].ReplyToID == msg.ReplyToID,
			img:         images[msg.ID], flash: flash.on(msg.ID),
		}, lineWidth, imageRows(height))
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		return []string{}, []int{}, []rowKind{}
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
			top = append(top, s.dividerLine(fmt.Sprintf("unread · %d", len(unread)), s.accent,
				lineWidth))
		}
		if len(top) == 0 && i > 0 && !continues(messages, i) {
			top = append(top, "") // a gap before each new sender
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
	return lines, owners, kinds
}

// continues reports whether messages[i] follows one from the same sender
// closely enough to leave its name out.
func continues(messages []backend.Message, i int) bool {
	if i == 0 {
		return false
	}
	a, b := messages[i-1], messages[i]
	if a.Outgoing != b.Outgoing || a.Sender != b.Sender || a.SenderID != b.SenderID ||
		a.Agent != b.Agent || a.Timestamp.IsZero() || !sameDay(a.Timestamp, b.Timestamp) {
		return false
	}
	return b.Timestamp.Sub(a.Timestamp) < 10*time.Minute
}

// threadInfo is what a message's replies chip shows.
type threadInfo struct {
	count int       // replies to the message
	last  time.Time // the newest reply
	open  bool      // its thread is open beside the chat
}

// blockOpts is how messageBlock draws a message.
type blockOpts struct {
	quoted *backend.Message // the message it replies to, if known
	thread threadInfo
	edited bool
	unread bool
	// selected draws the accent bar in the gutter and the time in the accent.
	selected bool
	cont     bool // follows a message from the same sender: no name
	// samePointer leaves out the reply pointer the message before already showed.
	samePointer bool
	img         *chatImage
	flash       string // the reaction drawn highlighted, if any
	// root draws the thread pane's first message: a header line with its day,
	// time and sender, then the text from the left edge.
	root bool
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
// a meta row. Selection only colours the gutter and the time, so columns
// never move. An unread message has its header and text in bold. Alongside
// the lines it marks those that draw the attachment (its image card or its
// [tag] row) and the meta row.
func (s *styles) messageBlock(b backend.Backend, msg backend.Message, entry backend.ChatEntry,
	o blockOpts, lineWidth, maxImageRows int) ([]string, []rowKind) {
	gutter := "  "
	if o.edited {
		gutter = "* "
	}
	indent := s.textIndent(entry)
	prefix := gutter + s.messageHeader(msg, entry, o.unread, o.selected, o.cont) + " "
	var head []string
	if o.root {
		indent, prefix = 2, gutter
		head = []string{gutter + s.rootHeader(msg, entry, o.unread)}
	}
	body, below := s.imageText(msg, o.img, indent, lineWidth, maxImageRows)
	reply := msg.ReplyToID != "" && !o.samePointer && !o.root
	var lines []string
	if msg.Multiline {
		lines = s.paragraphLines(msg, body, prefix, o.quoted, reply, lineWidth, indent)
	} else if body = s.inline(styleLinks(s.dimTag(body))); !reply {
		lines = textfmt.WrapMessage(prefix, body, lineWidth, indent)
	} else {
		// Replies put the pointer to the original on the header row and the text below it.
		quote := textfmt.ClipLine(prefix+s.quote(o.quoted), lineWidth)
		lines = append([]string{quote},
			textfmt.WrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
	}
	lines = carryStyles(append(head, lines...))
	if o.unread {
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
	if meta := s.metaRow(b, msg, entry, o.thread, o.flash); meta != "" {
		lines = append(lines, textfmt.ClipLine(strings.Repeat(" ", indent)+meta, lineWidth))
		kinds = append(kinds, rowMeta)
	}
	if o.selected {
		bar := s.accent.Render("▎")
		for i := range lines {
			lines[i] = gutterBar(lines[i], bar)
		}
	}
	return lines, kinds
}

// rootHeader is the thread root's header line: its day and time, then its
// sender.
func (s *styles) rootHeader(msg backend.Message, entry backend.ChatEntry, unread bool) string {
	stamp := dayLabel(msg.Timestamp, time.Now()) + " " + msg.Timestamp.Format("15:04")
	if !unread {
		stamp = s.dim.Render(stamp)
	}
	if kind, _ := kindOf(entry); kind == kindChannel {
		return stamp
	}
	if msg.Outgoing && msg.Agent != "" {
		return stamp + "  " + s.agent.Render(agentIcon+" "+textfmt.Plain(msg.Agent))
	}
	return stamp + "  " + backend.MessageNickColor(msg, entry) +
		textfmt.Plain(backend.GenericMessageSender(msg)) + "\x1b[0m"
}

// gutterBar puts bar in place of the first cell of line when it is blank.
func gutterBar(line, bar string) string {
	i := 0
	for strings.HasPrefix(line[i:], "\x1b[") { // skip leading SGRs such as bold
		j := i + 2
		for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
			j++
		}
		i = min(j+1, len(line))
	}
	if i < len(line) && line[i] == ' ' {
		return line[:i] + bar + line[i+1:]
	}
	return line
}

// Inline marks: @names in the quote colour, emoji codes dim. Each sets and
// resets only the foreground, so bold, links and the selection survive it.
var (
	atName    = regexp.MustCompile(`(^|\s)(@[\pL\pN_.\-]+)`)
	fgOff     = "\x1b[39m"
	atNameOn  = "\x1b[38;5;75m"
	shortcode = regexp.MustCompile(`:[a-z0-9_+-]*[a-z][a-z0-9_+-]*:`)
)

// inline colours @names and dims emoji codes in text.
func (s *styles) inline(text string) string {
	text = atName.ReplaceAllString(text, "${1}"+atNameOn+"${2}"+fgOff)
	return shortcode.ReplaceAllStringFunc(text, func(code string) string {
		return s.dimFgSGR + code + fgOff
	})
}

// chatWide are the @names that mention everyone rather than someone.
var chatWide = map[string]bool{"@all": true, "@channel": true, "@here": true, "@room": true,
	"@everyone": true}

// mentionAt is the @name drawn under a click, as inline colours it. A
// trailing full stop or dash ends the sentence rather than the name.
func mentionAt(hit msgHit) (string, bool) {
	for _, loc := range atName.FindAllStringSubmatchIndex(hit.line, -1) {
		name := strings.TrimRight(hit.line[loc[4]:loc[5]], ".-")
		start := textfmt.DisplayWidth(hit.line[:loc[4]])
		if hit.cell >= start && hit.cell < start+textfmt.DisplayWidth(name) &&
			len(name) > 1 && !chatWide[strings.ToLower(name)] {
			return name, true
		}
	}
	return "", false
}

// textAt reports whether a click fell on the row's text, from its first
// character to its last, rather than on the blank around it. The gutter is
// never text.
func textAt(hit msgHit) bool {
	first, last, x := -1, -1, 0
	for _, r := range hit.line {
		w := textfmt.DisplayWidth(string(r))
		if x >= 2 && !unicode.IsSpace(r) {
			if first < 0 {
				first = x
			}
			last = x + w
		}
		x += w
	}
	return first >= 0 && hit.cell >= first && hit.cell < last
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
// A line starting "> " is a quote, drawn dim behind a bar.
func (s *styles) paragraphLines(msg backend.Message, body, prefix string, quoted *backend.Message,
	reply bool, lineWidth, indent int) []string {
	pad := strings.Repeat(" ", indent)
	var text []string
	for i, para := range bodyParagraphs(msg, body) {
		if rest, ok := strings.CutPrefix(ansi.Strip(para), "> "); ok {
			para = s.dim.Render("▎ ") + s.subtle.Render(rest)
		} else {
			para = s.inline(styleLinks(s.dimTag(para)))
		}
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

// metaRow joins a message's reactions as chips, its replies chip, pin and
// read ticks into one row, or "" when there is nothing to show. The
// reaction flash names is drawn reversed; the replies chip is solid while
// its thread is open.
func (s *styles) metaRow(b backend.Backend, msg backend.Message, entry backend.ChatEntry,
	t threadInfo, flash string) string {
	var chips []string
	for _, r := range shownReactions(msg) {
		count := fmt.Sprint(r.Count)
		switch {
		case r.Emoji == flash:
			chips = append(chips, s.accent.Reverse(true).Render(" "+r.Emoji+" "+count+" "))
			continue
		case r.Chosen:
			count = s.accent.Bold(true).Render(count)
		default:
			count = s.subtle.Render(count)
		}
		chips = append(chips, shadeLine(" "+r.Emoji+" "+count+" ", s.selBgSGR, 0))
	}
	if t.count > 0 {
		chips = append(chips, s.repliesChip(t))
	}
	var parts []string
	if len(chips) > 0 {
		parts = append(parts, strings.Join(chips, " "))
	}
	if msg.Pinned {
		parts = append(parts, s.subtle.Render("pinned"))
	}
	if tick := strings.TrimSpace(b.ReadMarker(entry, msg)); tick != "" {
		parts = append(parts, s.subtle.Render(tick))
	}
	return strings.Join(parts, s.dim.Render(" · "))
}

// repliesChip is "↳ N replies · last HH:MM", on the accent fill while the
// thread is open.
func (s *styles) repliesChip(t threadInfo) string {
	text := repliesText(t)
	if t.open {
		return s.badge.Render(text)
	}
	return shadeLine(s.accent.Render(text), s.selBgSGR, 0)
}

func repliesText(t threadInfo) string {
	text := fmt.Sprintf(" ↳ %d %s ", t.count, plural(t.count, "reply", "replies"))
	if !t.last.IsZero() {
		text += "· last " + t.last.Format("15:04") + " "
	}
	return text
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

// reactionWidth is the cells of a reaction chip.
func reactionWidth(r backend.MessageReaction) int {
	return textfmt.DisplayWidth(r.Emoji) + 3 + len(fmt.Sprint(r.Count))
}

// reactionAt is the reaction metaRow draws at column col of the meta row,
// counted from the start of the message's text column.
func reactionAt(msg backend.Message, col int) (string, bool) {
	x := 0
	for _, r := range shownReactions(msg) {
		w := reactionWidth(r)
		if col >= x && col < x+w {
			return r.Emoji, true
		}
		x += w + 1
	}
	return "", false
}

// repliesAt reports whether column col of the meta row falls on the replies
// chip of a message with t's replies.
func repliesAt(msg backend.Message, t threadInfo, col int) bool {
	if t.count == 0 {
		return false
	}
	x := 0
	for _, r := range shownReactions(msg) {
		x += reactionWidth(r) + 1
	}
	return col >= x && col < x+textfmt.DisplayWidth(repliesText(t))
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
