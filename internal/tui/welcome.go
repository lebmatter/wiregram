package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// welcomeModal greets an account the first time it opens after being added,
// saying the mouse gets around. Any key or click closes it.
type welcomeModal struct {
	st    *styles
	label string
}

const welcomeModalWidth = 48

// welcomeMouse is what the mouse does, as the card lists it.
var welcomeMouse = []helpKey{
	{"click", "open a chat, card or menu row"},
	{"right click", "see what you can do with it"},
	{"wheel", "scroll the chat"},
	{"hover", "see what a click will act on"},
}

func newWelcomeModal(st *styles, b backend.Backend) welcomeModal {
	return welcomeModal{st: st, label: textfmt.Plain(b.AccountLabel())}
}

func (m welcomeModal) size() (int, int) {
	// Borders, account, blank, rule, the gestures, blank, the keys' note, key rule and keys.
	return welcomeModalWidth, 9 + len(welcomeMouse)
}

func (m welcomeModal) View() string {
	w, h := m.size()
	glyph := lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	inner := w - 4
	body := []string{glyph + " " + m.label, "",
		m.st.modalRule("wiregram works with the mouse", inner)}
	for _, g := range welcomeMouse {
		pad := strings.Repeat(" ", max(1, 13-textfmt.DisplayWidth(g.key)))
		body = append(body, " "+m.st.accent.Render(g.key)+pad+g.desc)
	}
	body = append(body, "", m.st.dim.Render(" keys work too: wiregram -h lists them"))
	body = append(body, m.st.modalKeys([]helpKey{{"click", "start"}}, "", inner)...)
	return m.st.modalBox("account connected", body, w, h)
}

var welcomeModalHelp = []helpKey{{"enter", "start"}}
