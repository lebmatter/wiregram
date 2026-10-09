package tui

import (
	"fmt"
	"image/color"
	"os"
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

var (
	colorAccent = lipgloss.Color("214")
	colorSubtle = lipgloss.Color("245")
	colorDim    = lipgloss.Color("240")
	colorGreen  = lipgloss.Color("42")
	colorYellow = lipgloss.Color("220")
	colorRed    = lipgloss.Color("203")
	colorQuote  = lipgloss.Color("75")
	colorSelBg  = lipgloss.Color("237")
	colorBadge  = lipgloss.Color("16")
	// colorAgent replaces colorAccent throughout agent mode.
	colorAgent = lipgloss.Color("141")
)

var (
	styleTitle       = lipgloss.NewStyle().Bold(true)
	styleBrand       = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleAccent      = lipgloss.NewStyle().Foreground(colorAccent)
	styleSubtle      = lipgloss.NewStyle().Foreground(colorSubtle)
	styleDim         = lipgloss.NewStyle().Foreground(colorDim)
	styleUnread      = lipgloss.NewStyle().Foreground(colorAccent)
	styleNotice      = lipgloss.NewStyle().Foreground(colorYellow)
	styleError       = lipgloss.NewStyle().Foreground(colorRed)
	styleQuoteBar    = lipgloss.NewStyle().Foreground(colorQuote)
	styleBadge       = lipgloss.NewStyle().Bold(true).Background(colorAccent).Foreground(colorBadge)
	styleSelectedRow = lipgloss.NewStyle().Bold(true).Background(colorSelBg)
	styleAgent       = lipgloss.NewStyle().Foreground(colorAgent)
)

type helpKey struct {
	key  string
	desc string
}

func newHelpModel(width int, accent color.Color) help.Model {
	keyStyle := lipgloss.NewStyle().Foreground(accent)
	h := help.New()
	h.ShortSeparator = "  "
	h.Styles.ShortKey = keyStyle
	h.Styles.ShortDesc = styleSubtle
	h.Styles.ShortSeparator = styleDim
	h.Styles.FullKey = keyStyle
	h.Styles.FullDesc = styleSubtle
	h.Styles.FullSeparator = styleDim
	h.Styles.Ellipsis = styleDim
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

func renderHelpBar(keys []helpKey, width int, accent color.Color) string {
	return newHelpModel(width, accent).ShortHelpView(helpBindings(keys))
}

// renderFooter draws the key help with the notice on the right. A long notice
// (such as a delete confirmation) takes the whole line so it is never cut.
func renderFooter(keys []helpKey, full bool, notice string, width int) string {
	return renderThemedFooter(keys, full, notice, width, colorAccent)
}

func renderThemedFooter(keys []helpKey, full bool, notice string, width int, accent color.Color) string {
	notice = textfmt.Plain(notice)
	if full {
		bindings := helpBindings(keys)
		var groups [][]key.Binding
		for len(bindings) > 0 {
			n := min(5, len(bindings))
			groups = append(groups, bindings[:n])
			bindings = bindings[n:]
		}
		out := newHelpModel(width, accent).FullHelpView(groups)
		if notice != "" {
			out += "\n" + styleNotice.Render(truncate(notice, width))
		}
		return out
	}
	noticeWidth := textfmt.DisplayWidth(notice)
	if notice != "" && noticeWidth > width/2 {
		return styleNotice.Render(truncate(notice, width))
	}
	if notice == "" {
		return renderHelpBar(keys, width, accent)
	}
	left := renderHelpBar(keys, width-noticeWidth-2, accent)
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

func accountGlyph(s account.AccountState) string {
	switch {
	case s.Status == "online":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case s.Status == "connecting" || s.Status == "reconnecting":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("○")
	case s.NeedsLogin || s.Notice != "":
		return lipgloss.NewStyle().Foreground(colorRed).Render("✕")
	}
	return styleDim.Render("○")
}

// renderHeader is the wordmark line on the screens outside an account.
func renderHeader(width int) string {
	return textfmt.ClipLine(styleBrand.Render(" wiregram "), width) + "\x1b[0m"
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

// accountShort is the account's tag in agent mode, such as TG1: its backend
// and its Alt+N number.
func accountShort(mgr *account.AccountManager, key string) string {
	for i, a := range mgr.Accounts {
		if mgr.AccountKey(a) == key {
			return fmt.Sprintf("%s%d", backendTag(a.BackendType), i+1)
		}
	}
	return "??"
}

func boxBorder(focused bool) color.Color {
	if focused {
		return colorAccent
	}
	return colorDim
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

func renderQuote(orig *backend.Message, entry backend.ChatEntry) string {
	bar := styleQuoteBar.Render("┃ ")
	if orig == nil {
		return bar + styleSubtle.Render("earlier message")
	}
	return bar + styleSubtle.Render(backend.GenericMessageSender(*orig)+": "+
		textfmt.ShortExcerpt(backend.GenericMessageText(*orig)))
}

// textIndent is the column message text starts at: after the 2-cell gutter,
// the timestamp and, outside channels, the nick column.
func textIndent(entry backend.ChatEntry) int {
	if kind, _ := kindOf(entry); kind == kindChannel {
		return 2 + 8
	}
	return 2 + 8 + textfmt.NickWidth + 3
}

// messageHeader is "[HH:MM] <nick>", or just the time in channels, where
// every post comes from the channel itself. The time is dim unless the
// message is unread, when the whole block is bold instead.
func messageHeader(msg backend.Message, entry backend.ChatEntry, unread bool) string {
	stamp := msg.Timestamp.Format("[15:04]")
	if !unread {
		stamp = styleDim.Render(stamp)
	}
	if kind, _ := kindOf(entry); kind == kindChannel {
		return stamp
	}
	nick, _ := strings.CutPrefix(backend.GenericMessagePrefix(msg, entry),
		msg.Timestamp.Format("[15:04]"))
	return stamp + nick
}

// shadeLine paints line on the selection background, padded to width. The
// background is re-applied after every reset, since nick colours, styles and
// image rows all end with one.
func shadeLine(line string, width int) string {
	pad := strings.Repeat(" ", max(0, width-textfmt.DisplayWidth(line)))
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+selBgSGR)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+selBgSGR)
	return selBgSGR + line + pad + "\x1b[0m"
}

// boldLine sets line in bold, re-applied after every reset as in shadeLine.
// It ends with SGR 22 rather than a full reset so a selection tint survives.
func boldLine(line string) string {
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m\x1b[1m")
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m\x1b[1m")
	return "\x1b[1m" + line + "\x1b[22m"
}

// styleMediaTag dims the leading "[tag]" of a media label.
func styleMediaTag(text string) string {
	if !strings.HasPrefix(text, "[") {
		return text
	}
	end := strings.Index(text, "]")
	if end < 0 {
		return text
	}
	return styleSubtle.Render(text[:end+1]) + text[end+1:]
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

// selBgSGR is colorSelBg as a raw SGR, re-applied after resets by shadeLine.
const selBgSGR = "\x1b[48;5;237m"

// styleLinks marks the http(s) links in text before it is wrapped.
func styleLinks(text string) string {
	var out strings.Builder
	for {
		start := strings.Index(text, "http")
		if start < 0 {
			out.WriteString(text)
			return out.String()
		}
		rest := text[start:]
		if !strings.HasPrefix(rest, "https://") && !strings.HasPrefix(rest, "http://") {
			out.WriteString(text[:start+4])
			text = text[start+4:]
			continue
		}
		end := strings.IndexAny(rest, " \t\n\x1b")
		if end < 0 {
			end = len(rest)
		}
		url := rest[:end]
		out.WriteString(text[:start] + linkOn + oscLink + url + "\a" + url + oscLinkEnd + linkOff)
		text = rest[end:]
	}
}

// closeLinks ends a link style at the end of a wrapped line and reopens it
// after the next line's indent, so a long wrapped link stays styled without
// underlining the indent.
func closeLinks(lines []string) []string {
	opener := ""
	for i, line := range lines {
		if opener != "" {
			body := strings.TrimLeft(line, " ")
			line = line[:len(line)-len(body)] + opener + body
		}
		opener = ""
		if on := strings.LastIndex(line, linkOn); on > strings.LastIndex(line, linkOff) {
			opener = line[on:]
			if end := strings.IndexByte(opener, '\a'); end >= 0 {
				opener = opener[:end+1]
			}
			line += oscLinkEnd + linkOff
		}
		lines[i] = line
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

func chatListLine(entry backend.ChatEntry, selected, focused bool, width int) string {
	pill := ""
	if entry.Unread > 0 {
		pill = " " + styleBadge.Render(fmt.Sprintf(" %d ", entry.Unread))
	}
	marker := "  "
	if selected {
		marker = "> "
	}
	row := marker + truncate(textfmt.Plain(entry.Title), max(1, width-2-textfmt.DisplayWidth(pill)))
	row += strings.Repeat(" ", max(0, width-textfmt.DisplayWidth(pill)-textfmt.DisplayWidth(row)))
	if selected && focused {
		row = styleSelectedRow.Render(row)
	} else if selected {
		row = styleTitle.Render(row)
	}
	return row + pill
}

const wiregramLogo = `╦ ╦╦╦═╗╔═╗╔═╗╦═╗╔═╗╔╦╗
║║║║╠╦╝║╣ ║ ╦╠╦╝╠═╣║║║
╚╩╝╩╩╚═╚═╝╚═╝╩╚═╩ ╩╩ ╩`

// renderLogo returns the block logo, or the plain wordmark when it won't fit.
func renderLogo(width int) string {
	if width < lipgloss.Width(wiregramLogo)+4 {
		return styleBrand.Render("wiregram")
	}
	return styleAccent.Render(wiregramLogo)
}
