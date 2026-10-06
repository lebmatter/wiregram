package main

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"golang.org/x/term"
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
)

type helpKey struct {
	key  string
	desc string
}

func newHelpModel(width int) help.Model {
	h := help.New()
	h.ShortSeparator = "  "
	h.Styles.ShortKey = styleAccent
	h.Styles.ShortDesc = styleSubtle
	h.Styles.ShortSeparator = styleDim
	h.Styles.FullKey = styleAccent
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

func renderHelpBar(keys []helpKey, width int) string {
	return newHelpModel(width).ShortHelpView(helpBindings(keys))
}

// renderFooter draws the key help with the notice on the right. A long notice
// (such as a delete confirmation) takes the whole line so it is never cut.
func renderFooter(keys []helpKey, full bool, notice string, width int) string {
	notice = plain(notice)
	if full {
		bindings := helpBindings(keys)
		var groups [][]key.Binding
		for len(bindings) > 0 {
			n := min(5, len(bindings))
			groups = append(groups, bindings[:n])
			bindings = bindings[n:]
		}
		out := newHelpModel(width).FullHelpView(groups)
		if notice != "" {
			out += "\n" + styleNotice.Render(truncate(notice, width))
		}
		return out
	}
	noticeWidth := displayWidth(notice)
	if notice != "" && noticeWidth > width/2 {
		return styleNotice.Render(truncate(notice, width))
	}
	if notice == "" {
		return renderHelpBar(keys, width)
	}
	left := renderHelpBar(keys, width-noticeWidth-2)
	gap := max(1, width-displayWidth(left)-noticeWidth)
	return left + strings.Repeat(" ", gap) + styleNotice.Render(notice)
}

type accountChip struct {
	label   string
	backend string
	glyph   string
	active  bool
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

func accountGlyph(s accountState) string {
	switch {
	case s.status == "online":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case s.status == "connecting" || s.status == "reconnecting":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("○")
	case s.needsLogin || s.notice != "":
		return lipgloss.NewStyle().Foreground(colorRed).Render("✕")
	}
	return styleDim.Render("○")
}

func accountChips(mgr *accountManager, active *account) []accountChip {
	if mgr == nil {
		return nil
	}
	chips := make([]accountChip, 0, len(mgr.accounts))
	for _, a := range mgr.accounts {
		chips = append(chips, accountChip{
			label:   plain(a.label()),
			backend: backendTag(a.backendType),
			glyph:   accountGlyph(a.snapshot()),
			active:  a == active,
		})
	}
	return chips
}

// renderHeader is the one-line bar shown on every screen inside an account.
// Chips are numbered to match the Alt+N account shortcuts.
func renderHeader(chips []accountChip, width int) string {
	parts := []string{styleBrand.Render(" wiregram ")}
	for i, c := range chips {
		label := truncate(c.label, 20)
		if c.active {
			label = styleTitle.Foreground(colorAccent).Render(label)
		} else {
			label = styleSubtle.Render(label)
		}
		n := ""
		if len(chips) > 1 && i < 9 {
			n = styleDim.Render(fmt.Sprintf("%d ", i+1))
		}
		parts = append(parts, n+c.glyph+" "+label+" "+styleDim.Render(c.backend))
	}
	return clipLine(strings.Join(parts, "   "), width) + "\x1b[0m"
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
		t := " " + truncate(plain(title), max(1, width-6)) + " "
		fill := max(0, width-3-displayWidth(t))
		b.WriteString(bs.Render("╭─") + styleTitle.Render(t) + bs.Render(strings.Repeat("─", fill)+"╮"))
	}
	side := bs.Render("│")
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(body) {
			line = clipLine(body[i], inner)
		}
		pad := max(0, inner-displayWidth(line))
		b.WriteString("\n" + side + " " + line + "\x1b[0m" + strings.Repeat(" ", pad) + " " + side)
	}
	b.WriteString("\n" + bs.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return b.String()
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

func renderQuote(orig *message, entry chatEntry) string {
	bar := styleQuoteBar.Render("┃ ")
	if orig == nil {
		return bar + styleSubtle.Render("earlier message")
	}
	return bar + styleSubtle.Render(genericMessageSender(*orig)+": "+
		shortExcerpt(genericMessageText(*orig)))
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
	if displayWidth(text) <= width {
		return text
	}
	return clipLine(text, width-1) + "…"
}

func chatListLine(entry chatEntry, selected, focused bool, width int) string {
	pill := ""
	if entry.Unread > 0 {
		pill = " " + styleBadge.Render(fmt.Sprintf(" %d ", entry.Unread))
	}
	marker := "  "
	if selected {
		marker = "> "
	}
	row := marker + truncate(plain(entry.Title), max(1, width-2-displayWidth(pill)))
	row += strings.Repeat(" ", max(0, width-displayWidth(pill)-displayWidth(row)))
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
