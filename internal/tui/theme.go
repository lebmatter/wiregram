package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/store"
)

// theme is a palette of lipgloss colour specs. Colours that carry meaning,
// such as status, chat kinds, links and nicks, are the same in every theme.
type theme struct {
	name    string // shown in Settings and stored as the setting's value
	accent  string
	subtle  string
	dim     string
	selBg   string
	badgeFg string
	agent   string // replaces accent in agent mode
}

// themes is fixed; the first is the default. A new theme is one more entry.
var themes = [...]theme{
	{name: "Zest", accent: "#E8FE96", subtle: "#9a92b5", dim: "#6b6188", selBg: "#3a2d5e",
		badgeFg: "#2b1f4a", agent: "#af87ff"},
	{name: "Copper", accent: "214", subtle: "245", dim: "240", selBg: "237", badgeFg: "16",
		agent: "141"},
}

const themeSetting = "theme"

// loadTheme is the theme chosen in Settings, or the default.
func loadTheme(db *store.DB) theme {
	if name, err := db.Setting(themeSetting); err == nil {
		for _, t := range themes {
			if t.name == name {
				return t
			}
		}
	}
	return themes[0]
}

func saveTheme(db *store.DB, t theme) error { return db.SetSetting(themeSetting, t.name) }

// styles is a theme turned into the styles every screen draws with. Models
// carry one and pass it down, so a theme change needs no global state.
type styles struct {
	theme theme

	accentColor color.Color
	dimColor    color.Color

	brand       lipgloss.Style
	accent      lipgloss.Style
	subtle      lipgloss.Style
	dim         lipgloss.Style
	badge       lipgloss.Style
	selectedRow lipgloss.Style
	agent       lipgloss.Style

	// selBgSGR is the selection background as a raw SGR, re-applied after
	// resets by shadeLine.
	selBgSGR string
}

func newStyles(t theme) *styles {
	return newAccentStyles(t, t.accent)
}

func newAccentStyles(t theme, accent string) *styles {
	a, selBg := lipgloss.Color(accent), lipgloss.Color(t.selBg)
	return &styles{
		theme:       t,
		accentColor: a,
		dimColor:    lipgloss.Color(t.dim),
		brand:       lipgloss.NewStyle().Bold(true).Foreground(a),
		accent:      lipgloss.NewStyle().Foreground(a),
		subtle:      lipgloss.NewStyle().Foreground(lipgloss.Color(t.subtle)),
		dim:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.dim)),
		badge: lipgloss.NewStyle().Bold(true).Background(a).
			Foreground(lipgloss.Color(t.badgeFg)),
		selectedRow: lipgloss.NewStyle().Bold(true).Background(selBg),
		agent:       lipgloss.NewStyle().Foreground(lipgloss.Color(t.agent)),
		selBgSGR:    ansi.NewStyle().BackgroundColor(selBg).String(),
	}
}

// forAgents is the same theme with the agent colour as its accent, for agent
// mode and its wizards.
func (s *styles) forAgents() *styles { return newAccentStyles(s.theme, s.theme.agent) }
