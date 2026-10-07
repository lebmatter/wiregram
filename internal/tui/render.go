package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
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
	// colorGame replaces colorAccent throughout game mode.
	colorGame = lipgloss.Color("114")
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
	styleGame        = lipgloss.NewStyle().Foreground(colorGame)
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

// renderModal draws a titled box with centred body lines over the middle of
// screen, a width×height frame. A terminal too small for it gets screen as is.
func renderModal(screen, title string, body []string, width, height int,
	accent color.Color) string {
	widest := 0
	for _, l := range body {
		widest = max(widest, lipgloss.Width(l))
	}
	w := min(width-4, max(32, widest+6))
	h := len(body) + 4
	if w < 20 || h > height {
		return screen
	}
	lines := []string{""}
	for _, l := range body {
		lines = append(lines, lipgloss.PlaceHorizontal(w-4, lipgloss.Center, l))
	}
	box := lipgloss.NewLayer(renderBox(title, lines, w, h, accent)).
		X((width - w) / 2).Y((height - h) / 2).Z(1)
	return lipgloss.NewCompositor(lipgloss.NewLayer(screen), box).Render()
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
