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
	name   string // shown in Settings and stored as the setting's value
	accent string
	subtle string
	dim    string
	selBg  string
	// mentionBg tints messages that mention the user.
	mentionBg string
	badgeFg   string
	agent     string // marks Chat Agents: their icon, suggestions and messages
	bar       string // the account chooser's key strip
}

// themes is fixed; the first is the default. A new theme is one more entry.
var themes = [...]theme{
	{name: "Zest", accent: "#E8FE96", subtle: "#9a92b5", dim: "#6b6188", selBg: "#3a2d5e",
		mentionBg: "#33401f", badgeFg: "#2b1f4a", agent: "#af87ff", bar: "#1f1636"},
	{name: "Copper", accent: "214", subtle: "245", dim: "240", selBg: "237", mentionBg: "58",
		badgeFg: "16", agent: "141", bar: "235"},
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

// nickStyleSetting picks how a message names its sender: "nick:" (the
// default) puts the text right after the name, "<nick>" pads every name to
// one column so the text lines up.
const (
	nickStyleSetting = "nick_style"
	nickStyleColon   = "nick:"
	nickStyleColumn  = "<nick>"
)

func loadNickStyle(db *store.DB) string {
	if v, err := db.Setting(nickStyleSetting); err == nil && v == nickStyleColumn {
		return v
	}
	return nickStyleColon
}

// chatCacheSetting keeps the messages of chats opened this session in memory,
// so reopening one draws at once; "off" turns it off.
const chatCacheSetting = "chat_cache"

func loadChatCache(db *store.DB) bool {
	v, err := db.Setting(chatCacheSetting)
	return err != nil || v != "off"
}

// loadStyles is the chosen theme with the chosen nick style.
func loadStyles(db *store.DB) *styles {
	st := newStyles(loadTheme(db))
	st.nickColumn = loadNickStyle(db) == nickStyleColumn
	return st
}

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
	tag         lipgloss.Style // a service tag such as TG, on the subtle fill
	selectedRow lipgloss.Style
	agent       lipgloss.Style

	// selBgSGR and mentionBgSGR are the selection and mention backgrounds as
	// raw SGRs, re-applied after resets by shadeLine.
	selBgSGR     string
	mentionBgSGR string
	barSGR       string
	dimFgSGR     string // the dim colour as a foreground SGR

	// nickColumn draws senders as a fixed-width <nick> column.
	nickColumn bool
}

func newStyles(t theme) *styles {
	a, selBg := lipgloss.Color(t.accent), lipgloss.Color(t.selBg)
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
		tag: lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(t.subtle)).
			Foreground(lipgloss.Color(t.badgeFg)),
		selectedRow: lipgloss.NewStyle().Bold(true).Background(selBg),
		agent:       lipgloss.NewStyle().Foreground(lipgloss.Color(t.agent)),
		selBgSGR:    ansi.NewStyle().BackgroundColor(selBg).String(),
		mentionBgSGR: ansi.NewStyle().
			BackgroundColor(lipgloss.Color(t.mentionBg)).String(),
		barSGR:   ansi.NewStyle().BackgroundColor(lipgloss.Color(t.bar)).String(),
		dimFgSGR: ansi.NewStyle().ForegroundColor(lipgloss.Color(t.dim)).String(),
	}
}
