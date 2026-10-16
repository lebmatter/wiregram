package tui

import (
	"fmt"
	"image/color"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

func termSize() (width, height int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 4 || h < 4 {
		return 80, 24
	}
	return w, h
}

// twoPaneWidth is the narrowest terminal that shows the chat list beside the chat.
const twoPaneWidth = 100

// sidebarWidth is the chat list width in two-pane mode, borders included.
const sidebarWidth = 30

// Colours that mean the same in every theme; the rest come from styles.
var (
	colorGreen  = lipgloss.Color("42")
	colorYellow = lipgloss.Color("220")
	colorRed    = lipgloss.Color("203")
	colorQuote  = lipgloss.Color("75")
	colorGroup  = lipgloss.Color("67")
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleNotice   = lipgloss.NewStyle().Foreground(colorYellow)
	styleError    = lipgloss.NewStyle().Foreground(colorRed)
	styleQuoteBar = lipgloss.NewStyle().Foreground(colorQuote)
)

type helpKey struct {
	key  string
	desc string
}

func (s *styles) newHelpModel(width int) help.Model {
	h := help.New()
	h.ShortSeparator = "  "
	h.Styles.ShortKey = s.accent
	h.Styles.ShortDesc = s.subtle
	h.Styles.ShortSeparator = s.dim
	h.Styles.FullKey = s.accent
	h.Styles.FullDesc = s.subtle
	h.Styles.FullSeparator = s.dim
	h.Styles.Ellipsis = s.dim
	h.SetWidth(width)
	return h
}

func helpBindings(keys []helpKey) []key.Binding {
	bindings := make([]key.Binding, len(keys))
	for i, k := range keys {
		bindings[i] = key.NewBinding(key.WithKeys(k.key), key.WithHelp(k.key, k.desc))
	}
	return bindings
}

func (s *styles) helpBar(keys []helpKey, width int) string {
	return s.newHelpModel(width).ShortHelpView(helpBindings(keys))
}

// footer draws the key help with the notice on the right. A long notice
// (such as a delete confirmation) takes the whole line so it is never cut.
func (s *styles) footer(keys []helpKey, notice string, width int) string {
	notice = textfmt.Plain(notice)
	noticeWidth := textfmt.DisplayWidth(notice)
	if notice != "" && noticeWidth > width/2 {
		return styleNotice.Render(truncate(notice, width))
	}
	if notice == "" {
		return s.helpBar(keys, width)
	}
	left := s.helpBar(keys, width-noticeWidth-2)
	gap := max(1, width-textfmt.DisplayWidth(left)-noticeWidth)
	return left + strings.Repeat(" ", gap) + styleNotice.Render(notice)
}

func backendTag(backendType string) string {
	switch backendType {
	case "telegram":
		return "TG"
	case "whatsapp":
		return "WA"
	case "matrix":
		return "MX"
	case "mattermost":
		return "MM"
	case "email":
		return "EM"
	}
	return "??"
}

func (st *styles) accountGlyph(s account.AccountState) string {
	switch {
	case s.Status == "online":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case s.Status == "connecting" || s.Status == "reconnecting":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("○")
	case s.NeedsLogin || s.Notice != "":
		return lipgloss.NewStyle().Foreground(colorRed).Render("✕")
	}
	return st.dim.Render("○")
}

// keyStrip is the account chooser's footer: bold keys on a filled strip, with
// the notice on the right, or across the strip when it is long.
func (s *styles) keyStrip(keys []helpKey, notice string, width int) string {
	notice = textfmt.Plain(notice)
	if notice != "" && textfmt.DisplayWidth(notice) > width/2 {
		return shadeLine(" "+styleNotice.Render(truncate(notice, width-2)), s.barSGR, width)
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, styleTitle.Render(k.key)+" "+s.subtle.Render(k.desc))
	}
	line := " " + strings.Join(parts, "   ")
	if notice != "" {
		room := width - textfmt.DisplayWidth(notice) - 1
		line = textfmt.ClipLine(line, max(0, room-1))
		line += strings.Repeat(" ", max(1, room-textfmt.DisplayWidth(line))) +
			styleNotice.Render(notice)
	}
	return shadeLine(textfmt.ClipLine(line, width), s.barSGR, width)
}

// header is the wordmark line on the screens outside an account.
func (s *styles) header(width int) string {
	return textfmt.ClipLine(s.brand.Render(" wiregram "), width) + "\x1b[0m"
}

// renderBox draws a rounded box exactly width×height cells with the title in
// the top edge. Body lines are clipped and padded to fit.
func renderBox(title string, body []string, width, height int, border color.Color) string {
	if width < 4 || height < 2 {
		return ""
	}
	bs := lipgloss.NewStyle().Foreground(border)
	inner := width - 4
	var b strings.Builder
	if title == "" {
		b.WriteString(bs.Render("╭" + strings.Repeat("─", width-2) + "╮"))
	} else {
		t := " " + truncate(textfmt.Plain(title), max(1, width-6)) + " "
		fill := max(0, width-3-textfmt.DisplayWidth(t))
		b.WriteString(bs.Render("╭─") + styleTitle.Render(t) + bs.Render(strings.Repeat("─", fill)+"╮"))
	}
	side := bs.Render("│")
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(body) {
			line = textfmt.ClipLine(body[i], inner)
		}
		pad := max(0, inner-textfmt.DisplayWidth(line))
		b.WriteString("\n" + side + " " + line + "\x1b[0m" + strings.Repeat(" ", pad) + " " + side)
	}
	b.WriteString("\n" + bs.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return b.String()
}

// fadeUnder draws box over bg with its top-left corner at column x and row
// y, with bg reduced to plain dim text so the box is all that stands out.
func (s *styles) fadeUnder(bg, box string, x, y int) string {
	lines := strings.Split(bg, "\n")
	rows := strings.Split(box, "\n")
	for i, line := range lines {
		line = blankPlaceholders(ansi.Strip(line))
		r := i - y
		if r < 0 || r >= len(rows) {
			lines[i] = s.dim.Render(line)
			continue
		}
		left := ansi.TruncateWc(line, x, "")
		left += strings.Repeat(" ", max(0, x-textfmt.DisplayWidth(left)))
		end := x + textfmt.DisplayWidth(rows[r])
		right := ansi.TruncateLeftWc(line, end, "")
		// A wide character cut by the box's right edge leaves a gap.
		right = strings.Repeat(" ",
			max(0, textfmt.DisplayWidth(line)-end-textfmt.DisplayWidth(right))) + right
		lines[i] = s.dim.Render(left) + rows[r] + "\x1b[0m" + s.dim.Render(right)
	}
	return strings.Join(lines, "\n")
}

// formModalWidth is the widest the Add account and sign-in modals get.
const formModalWidth = 60

// modalRect is the top-left corner of a w×h box centred over a body
// width×height cells.
func modalRect(width, height, w, h int) (x, y int) {
	return max(0, (width-w)/2), max(0, (height-h)/2)
}

// overlay draws box centred over backdrop, the screen the modal was opened
// from, cut or padded to width×height and faded. With no backdrop it fades
// over the wordmark alone.
func (s *styles) overlay(backdrop, box string, width, height int) string {
	if backdrop == "" {
		backdrop = s.header(width)
	}
	lines := strings.Split(backdrop, "\n")
	lines = append(lines[:min(len(lines), height)], make([]string, max(0, height-len(lines)))...)
	for i, line := range lines {
		lines[i] = ansi.TruncateWc(line, width, "")
	}
	x, y := modalRect(width, height, lipgloss.Width(box), lipgloss.Height(box))
	return s.fadeUnder(strings.Join(lines, "\n"), box, x, y)
}

// formRow is a label with its value in a column labelW cells in, close
// enough to read together.
func (s *styles) formRow(label, value string, labelW int, selected, focused bool, width int) string {
	marker := "  "
	if selected {
		marker = s.accent.Render(">") + " "
	}
	row := marker + label
	if value != "" {
		row += strings.Repeat(" ", max(1, labelW+2-textfmt.DisplayWidth(label))) + value
	}
	row = textfmt.ClipLine(row, width)
	switch {
	case selected && focused:
		return s.selectedRow.Render(row)
	case selected:
		return styleTitle.Render(row)
	}
	return row
}

// keyPress is a synthetic key press, so mouse input can reuse key handlers.
func keyPress(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// wheelKey turns a vertical wheel step into the up or down key: every list
// scrolls with its selection, so the wheel moves the selection.
func wheelKey(msg tea.MouseWheelMsg) (tea.KeyPressMsg, bool) {
	switch msg.Button {
	case tea.MouseWheelUp:
		return keyPress(tea.KeyUp), true
	case tea.MouseWheelDown:
		return keyPress(tea.KeyDown), true
	}
	return tea.KeyPressMsg{}, false
}

// cursorRow is the first line of view whose plain text starts with marker. It
// finds the selection inside bubbles components that keep theirs private.
func cursorRow(view, marker string) (int, bool) {
	for i, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(ansi.Strip(line), marker) {
			return i, true
		}
	}
	return 0, false
}

// accountLabel is the account's label without its [TG] prefix, for rows
// that show the tag themselves.
func accountLabel(a *account.Account) string {
	label := textfmt.Plain(a.Label())
	if _, rest, ok := strings.Cut(label, "] "); ok && strings.HasPrefix(label, "[") {
		return rest
	}
	return label
}

// accountShort is the account's tag, such as TG1: its backend and its
// position in the account list.
func accountShort(mgr *account.AccountManager, key string) string {
	for i, a := range mgr.Accounts {
		if mgr.AccountKey(a) == key {
			return fmt.Sprintf("%s%d", backendTag(a.BackendType), i+1)
		}
	}
	return "??"
}

// border is a box's border colour: the accent when it has focus.
func (s *styles) border(focused bool) color.Color {
	if focused {
		return s.accentColor
	}
	return s.dimColor
}

// bottomLines keeps the last height lines, padding at the top so content
// sits against the composer like a chat log.
func bottomLines(lines []string, height int) []string {
	height = max(0, height)
	if len(lines) > height {
		return lines[len(lines)-height:]
	}
	out := make([]string, height-len(lines), height)
	return append(out, lines...)
}

func (s *styles) quote(orig *backend.Message) string {
	bar := styleQuoteBar.Render("┃ ")
	if orig == nil {
		return bar + s.subtle.Render("earlier message")
	}
	return bar + s.subtle.Render(backend.GenericMessageSender(*orig)+": "+
		textfmt.ShortExcerpt(backend.GenericMessageText(*orig)))
}

// textIndent is the column message text starts at: after the 2-cell gutter,
// the timestamp and, with the <nick> style outside channels, the nick column.
// With the nick: style only a message's first line follows its nick.
func (s *styles) textIndent(entry backend.ChatEntry) int {
	if kind, _ := kindOf(entry); kind == kindChannel || !s.nickColumn {
		return 2 + 8
	}
	return 2 + 8 + textfmt.NickWidth + 3
}

// messageHeader is "[HH:MM] <nick>", or just the time in channels, where
// every post comes from the channel itself. The time is dim unless the
// message is unread, when the whole block is bold instead.
func (s *styles) messageHeader(msg backend.Message, entry backend.ChatEntry, unread bool) string {
	stamp := msg.Timestamp.Format("[15:04]")
	if msg.Timestamp.IsZero() {
		stamp = "[--:--]" // a WhatsApp quote carries no time
	}
	if !unread {
		stamp = s.dim.Render(stamp)
	}
	if kind, _ := kindOf(entry); kind == kindChannel {
		return stamp
	}
	if !s.nickColumn {
		if msg.Outgoing && msg.Agent != "" {
			return stamp + " " + s.agent.Render(agentIcon+" "+textfmt.Plain(msg.Agent)+":")
		}
		return stamp + " " + backend.MessageNickColor(msg, entry) +
			backend.GenericMessageSender(msg) + ":\x1b[0m"
	}
	if msg.Outgoing && msg.Agent != "" {
		return stamp + " " + s.agent.Render("<"+textfmt.FormatNick(agentIcon+" "+
			textfmt.Plain(msg.Agent))+">")
	}
	nick, _ := strings.CutPrefix(backend.GenericMessagePrefix(msg, entry),
		msg.Timestamp.Format("[15:04]"))
	return stamp + nick
}

// shadeLine paints line on the background bg (an SGR), padded to width. The
// background is re-applied after every reset, since nick colours, styles and
// image rows all end with one.
func shadeLine(line, bg string, width int) string {
	pad := strings.Repeat(" ", max(0, width-textfmt.DisplayWidth(line)))
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bg)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bg)
	return bg + line + pad + "\x1b[0m"
}

// blockShade is the background of a message block: selection wins over a
// mention, and neither leaves it unshaded.
func (s *styles) blockShade(msg backend.Message, selected bool) string {
	switch {
	case selected:
		return s.selBgSGR
	case msg.Mentioned:
		return s.mentionBgSGR
	}
	return ""
}

// boldLine sets line in bold, re-applied after every reset as in shadeLine.
// It ends with SGR 22 rather than a full reset so a selection tint survives.
func boldLine(line string) string {
	line = strings.ReplaceAll(line, boldOff, boldOff+boldOn)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m\x1b[1m")
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m\x1b[1m")
	return "\x1b[1m" + line + "\x1b[22m"
}

// dimTag dims the leading "[tag]" of a media label.
func (s *styles) dimTag(text string) string {
	if !strings.HasPrefix(text, "[") {
		return text
	}
	end := strings.Index(text, "]")
	if end < 0 {
		return text
	}
	return s.subtle.Render(text[:end+1]) + text[end+1:]
}

// Links are underlined in the quote colour. linkOff resets only underline
// and foreground, so a selection background survives it. Each link is also an
// OSC 8 hyperlink to the full URL, so a click on any wrapped line of it opens
// the whole link rather than the fragment the terminal sees on that row.
const (
	linkOn     = "\x1b[4;38;5;75m"
	linkOff    = "\x1b[24;39m"
	oscLink    = "\x1b]8;;"
	oscLinkEnd = "\x1b]8;;\a"
)

// Message text styles. Each ends with its own SGR rather than a full reset,
// so a selection tint or an unread bold survives it.
const (
	boldOn    = "\x1b[1m"
	boldOff   = "\x1b[22m"
	italicOn  = "\x1b[3m"
	italicOff = "\x1b[23m"
	strikeOn  = "\x1b[9m"
	strikeOff = "\x1b[29m"
	codeOn    = "\x1b[38;5;180m"
	codeOff   = "\x1b[39m"
)

// textStyles pairs each span style with its on and off sequences, links last
// so their OSC 8 target is reopened after the rest.
func textStyles() []struct {
	style   backend.Style
	on, off string
} {
	return []struct {
		style   backend.Style
		on, off string
	}{
		{backend.StyleBold, boldOn, boldOff},
		{backend.StyleItalic, italicOn, italicOff},
		{backend.StyleStrike, strikeOn, strikeOff},
		{backend.StyleCode, codeOn, codeOff},
		{backend.StyleLink, linkOn, oscLinkEnd + linkOff},
	}
}

// styleSpans renders text with its spans as SGR sequences, dropping control
// characters but keeping line breaks. At every span edge it closes what was
// open and reopens what still applies, so spans may nest and overlap.
func styleSpans(text string, spans []backend.Span) string {
	edges := []int{0, len(text)}
	for _, sp := range spans {
		if sp.Start >= 0 && sp.Start < sp.End && sp.End <= len(text) {
			edges = append(edges, sp.Start, sp.End)
		}
	}
	sort.Ints(edges)
	var out strings.Builder
	for i := 0; i+1 < len(edges); i++ {
		from, to := edges[i], edges[i+1]
		if from == to {
			continue
		}
		var style backend.Style
		url := ""
		for _, sp := range spans {
			if sp.Start <= from && to <= sp.End {
				style |= sp.Style
				if sp.URL != "" {
					url = sp.URL
				}
			}
		}
		styles := textStyles()
		for _, ts := range styles {
			if style&ts.style == 0 {
				continue
			}
			out.WriteString(ts.on)
			if ts.style == backend.StyleLink {
				out.WriteString(oscLink + textfmt.Plain(url) + "\a")
			}
		}
		for _, r := range text[from:to] {
			switch {
			case r == '\n':
				out.WriteRune(r)
			case r == '\t':
				out.WriteByte(' ')
			case r < 32 || (r >= 127 && r <= 159):
			default:
				out.WriteRune(r)
			}
		}
		for j := len(styles) - 1; j >= 0; j-- {
			if style&styles[j].style != 0 {
				out.WriteString(styles[j].off)
			}
		}
	}
	return out.String()
}

// styleLinks marks the bare http(s) links in text before it is wrapped,
// leaving links already styled, such as Markdown labels, as they are.
func styleLinks(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		rest := text[i:]
		switch {
		case strings.HasPrefix(rest, linkOn):
			end := strings.Index(rest, linkOff)
			if end < 0 {
				end = len(rest)
			} else {
				end += len(linkOff)
			}
			out.WriteString(rest[:end])
			i += end
		case rest[0] == '\x1b':
			end := strings.IndexByte(rest, 'm') + 1
			if end == 0 {
				end = len(rest)
			}
			out.WriteString(rest[:end])
			i += end
		case strings.HasPrefix(rest, "https://") || strings.HasPrefix(rest, "http://"):
			end := strings.IndexAny(rest, " \t\n\x1b")
			if end < 0 {
				end = len(rest)
			}
			url := rest[:end]
			out.WriteString(linkOn + oscLink + url + "\a" + url + oscLinkEnd + linkOff)
			i += end
		default:
			out.WriteByte(rest[0])
			i++
		}
	}
	return out.String()
}

// carryStyles ends the text styles still open at the end of a wrapped line
// and reopens them after the next line's indent, so a long styled run stays
// styled without styling the indent. A full reset ends every style.
func carryStyles(lines []string) []string {
	var openers []string
	for i, line := range lines {
		if len(openers) > 0 {
			body := strings.TrimLeft(line, " ")
			line = line[:len(line)-len(body)] + strings.Join(openers, "") + body
		}
		openers = openers[:0]
		reset := max(strings.LastIndex(line, "\x1b[0m"), strings.LastIndex(line, "\x1b[m"))
		var closers []string
		for _, ts := range textStyles() {
			on := strings.LastIndex(line, ts.on)
			if on <= max(reset, strings.LastIndex(line, ts.off)) {
				continue
			}
			opener := ts.on
			if ts.style == backend.StyleLink {
				opener = line[on:]
				if end := strings.IndexByte(opener, '\a'); end >= 0 {
					opener = opener[:end+1]
				}
			}
			openers = append(openers, opener)
			closers = append([]string{ts.off}, closers...)
		}
		lines[i] = line + strings.Join(closers, "")
	}
	return lines
}

// joinColumns puts two boxes side by side line by line. Both are already
// padded to their width, and lipgloss.JoinHorizontal would re-measure them
// by grapheme cluster, which disagrees with the terminal on scripts like
// Malayalam and pushes the right box off its column.
func joinColumns(left, right string) string {
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	pad := strings.Repeat(" ", textfmt.DisplayWidth(l[0]))
	lines := make([]string, max(len(l), len(r)))
	for i := range lines {
		a, b := pad, ""
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		lines[i] = a + b
	}
	return strings.Join(lines, "\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncate(text string, width int) string {
	if width < 1 {
		return ""
	}
	if textfmt.DisplayWidth(text) <= width {
		return text
	}
	return textfmt.ClipLine(text, width-1) + "…"
}

// chatListLine is a chat row: the title, led by the agent icon when agent is
// set (an agent answers the chat or has a suggestion waiting), then mark and
// the unread pill at the right edge.
func (s *styles) chatListLine(entry backend.ChatEntry, agent bool, mark string, selected,
	focused bool, width int) string {
	pill := ""
	if entry.Unread > 0 {
		pill = " " + s.badge.Render(fmt.Sprintf(" %d ", entry.Unread))
	}
	if mark != "" {
		pill = " " + mark + pill
	}
	marker := "  "
	if selected {
		marker = "> "
	}
	lead := ""
	if agent {
		lead = agentIcon + " "
	}
	room := max(1, width-2-textfmt.DisplayWidth(lead)-textfmt.DisplayWidth(pill))
	rest := truncate(textfmt.Plain(entry.Title), room)
	rest += strings.Repeat(" ", max(0, room-textfmt.DisplayWidth(rest)))
	plain := lipgloss.NewStyle()
	switch {
	case selected && focused:
		plain = s.selectedRow
	case selected:
		plain = styleTitle
	}
	if lead != "" {
		lead = s.agent.Inherit(plain).Render(lead)
	}
	return plain.Render(marker) + lead + plain.Render(rest) + pill
}

// agentIcon marks Chat Agents: an account that has one, a DM it answers or
// with a suggestion waiting, and the messages an agent sent.
const agentIcon = "✦"

// agentMark is the agent icon, dim while the agent is paused.
func (s *styles) agentMark(enabled bool) string {
	if !enabled {
		return s.dim.Render(agentIcon)
	}
	return s.agent.Render(agentIcon)
}

func clampIndex(i, n int) int { return max(0, min(i, n-1)) }

// window returns the first visible item so that sel stays on screen.
func window(sel, rows int) int {
	if rows < 1 || sel < rows {
		return 0
	}
	return sel - rows + 1
}

func (s *styles) listRow(text, right string, selected, focused bool, width int) string {
	marker := "  "
	if selected {
		marker = s.accent.Render(">") + " "
	}
	room := max(1, width-2-textfmt.DisplayWidth(right)-1)
	row := marker + truncate(text, room)
	gap := max(1, width-textfmt.DisplayWidth(row)-textfmt.DisplayWidth(right))
	row += strings.Repeat(" ", gap) + right
	switch {
	case selected && focused:
		return s.selectedRow.Render(row)
	case selected:
		return styleTitle.Render(row)
	}
	return row
}

const wiregramLogo = `╦ ╦╦╦═╗╔═╗╔═╗╦═╗╔═╗╔╦╗
║║║║╠╦╝║╣ ║ ╦╠╦╝╠═╣║║║
╚╩╝╩╩╚═╚═╝╚═╝╩╚═╩ ╩╩ ╩`

// logo returns the block logo, or the plain wordmark when it won't fit.
func (s *styles) logo(width int) string {
	if width < lipgloss.Width(wiregramLogo)+4 {
		return s.brand.Render("wiregram")
	}
	return s.accent.Render(wiregramLogo)
}
