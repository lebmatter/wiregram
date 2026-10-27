package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// welcomeModal greets an account the first time it opens after being added,
// with the keys to get around. Any key or click closes it.
type welcomeModal struct {
	st    *styles
	label string
	keys  []helpKey
}

const welcomeModalWidth = 44

func newWelcomeModal(st *styles, b backend.Backend, twoPane bool) welcomeModal {
	keys := []helpKey{
		{"↑↓ k j", "select"},
		{"enter", "open"},
		{"esc", "back"},
	}
	if twoPane {
		keys = append(keys, helpKey{"tab", "list ↔ chat"})
	}
	keys = append(keys, helpKey{"u", "unread"})
	if _, ok := b.(backend.MentionFetcher); ok {
		keys = append(keys, helpKey{"m", "mentions"})
	}
	keys = append(keys, helpKey{"a", "accounts"},
		helpKey{"", ""},
		helpKey{"^F", "search"},
		helpKey{"ctrl+f", "search in chat"})
	return welcomeModal{st: st, label: textfmt.Plain(b.AccountLabel()), keys: keys}
}

func (m welcomeModal) size() (int, int) {
	return welcomeModalWidth, 8 + len(m.keys) // borders, account, blanks, rules, keys
}

func (m welcomeModal) View() string {
	w, h := m.size()
	glyph := lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	inner := w - 4
	body := []string{glyph + " " + m.label, "", m.st.modalRule("navigate", inner)}
	for _, k := range m.keys {
		if k.key == "" {
			body = append(body, "", m.st.modalRule("search", inner))
			continue
		}
		pad := strings.Repeat(" ", max(1, 9-textfmt.DisplayWidth(k.key)))
		body = append(body, "   "+m.st.accent.Render(k.key)+pad+k.desc)
	}
	body = append(body, m.st.modalKeys([]helpKey{{"any key", "start"}}, "", inner)...)
	return m.st.modalBox("account connected", body, w, h)
}

var welcomeModalHelp = []helpKey{{"enter", "start"}}
