package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
func genericSelectedLines(b backend.Backend, messages []backend.Message, entry backend.ChatEntry, selected int, changed map[string]bool, replying *backend.Message, typing bool, width, height int) ([]string, []int) {
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
	const msgPrefixWidth = 7 + 2 + textfmt.NickWidth + 2
	messageLines := func(i int) []string {
		msg := messages[i]
		marker := "  "
		if changed[msg.ID] {
			marker = "* "
		}
		if i == selected {
			marker = styleAccent.Render(">") + " " + b.ReadMarker(entry, msg)
			if reactions := backend.FormatMessageReactions(msg.Reactions); reactions != "" {
				marker += "[" + reactions + "] "
			}
		}
		indent := textfmt.DisplayWidth(marker) + msgPrefixWidth
		body := backend.GenericMessageText(msg)
		if msg.Pinned {
			body += " [pinned]"
		}
		if n := replies[msg.ID]; n > 0 {
			body += styleSubtle.Render(fmt.Sprintf(" · %d %s", n, plural(n, "reply", "replies")))
		}
		prefix := marker + backend.GenericMessagePrefix(msg, entry) + " "
		var lines []string
		if msg.ReplyToID == "" {
			lines = textfmt.WrapMessage(prefix, body, lineWidth, indent)
		} else {
			// Replies put the quoted original on the header row and the text below it.
			quote := textfmt.ClipLine(prefix+renderQuote(byID[msg.ReplyToID], entry), lineWidth)
			lines = append([]string{quote},
				textfmt.WrapLineIndent(strings.Repeat(" ", indent)+body, lineWidth, indent)...)
		}
		return append(lines, previewCard(msg.Preview, indent, lineWidth)...)
	}
	if len(messages) == 0 || selected < 0 || selected >= len(messages) {
		lines, owners := []string{}, []int{}
		if replying != nil {
			lines = append(lines, textfmt.ClipLine("replying to "+backend.GenericReplySummary(*replying, entry), lineWidth))
			owners = append(owners, -1)
		}
		return lines, owners
	}
	blocks := make(map[int][]string)
	block := func(i int) []string {
		if lines := blocks[i]; lines != nil {
			return lines
		}
		lines := messageLines(i)
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
		for range visible {
			owners = append(owners, i)
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
