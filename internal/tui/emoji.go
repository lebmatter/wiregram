package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type emojiPickerModel struct {
	query        []rune
	choices      []backend.EmojiOption
	selected     int
	reactionMode bool
	target       *backend.Message
	b            backend.Backend
}

func newEmojiPicker(b backend.Backend, reactionMode bool, target *backend.Message) emojiPickerModel {
	var choices []backend.EmojiOption
	if reactionMode {
		choices = b.ReactionChoices("")
	} else {
		choices = b.EmojiChoices("")
	}
	return emojiPickerModel{
		choices:      choices,
		reactionMode: reactionMode,
		target:       target,
		b:            b,
	}
}

type emojiPicked struct {
	value        string
	reactionMode bool
	target       *backend.Message
}

type emojiCancelled struct{}

func (m emojiPickerModel) Update(msg tea.Msg) (emojiPickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+d":
			return m, func() tea.Msg { return emojiCancelled{} }
		case "up":
			if m.selected > 0 {
				m.selected--
			}
		case "down":
			if m.selected+1 < len(m.choices) {
				m.selected++
			}
		case "enter":
			if m.selected < len(m.choices) {
				value := m.choices[m.selected].Value
				return m, func() tea.Msg {
					return emojiPicked{value: value, reactionMode: m.reactionMode, target: m.target}
				}
			}
		case "tab":
			if m.reactionMode {
				m.reactionMode = false
				m.target = nil
			} else {
				m.reactionMode = true
			}
			m.selected = 0
			m.refreshChoices()
		case "backspace":
			if len(m.query) > 0 {
				m.query = m.query[:len(m.query)-1]
				m.refreshChoices()
			}
		case "ctrl+u":
			m.query = nil
			m.refreshChoices()
		default:
			key := msg.String()
			if !strings.HasPrefix(key, "alt+") && len(key) > 0 && key[0] != 0x1b {
				m.query = textfmt.AppendQuery(m.query, key)
				m.refreshChoices()
			}
		}
	}
	return m, nil
}

func (m *emojiPickerModel) refreshChoices() {
	q := string(m.query)
	if m.reactionMode {
		m.choices = m.b.ReactionChoices(q)
	} else {
		m.choices = m.b.EmojiChoices(q)
	}
	m.selected = min(m.selected, max(0, len(m.choices)-1))
}

func (m emojiPickerModel) View(width, height int) string {
	limit := max(1, height-4)
	start := 0
	if m.selected >= limit {
		start = m.selected - limit + 1
	}
	end := min(len(m.choices), start+limit)
	heading := "Emoji for draft — type to filter; Tab: react"
	if m.reactionMode {
		heading = "React — type to filter; Tab: insert into draft"
	}
	if len(m.query) == 0 && len(m.b.RecentEmoji()) > 0 {
		heading += "; recent first"
	}
	var b strings.Builder
	b.WriteString(textfmt.ClipLine(heading, width))
	b.WriteByte('\n')
	if len(m.query) > 0 {
		b.WriteString(fmt.Sprintf("Filter: %s", string(m.query)))
	}
	b.WriteByte('\n')
	for index := start; index < end; index++ {
		marker := "  "
		if index == m.selected {
			marker = styleAccent.Render("> ")
		}
		b.WriteString(textfmt.ClipLine(fmt.Sprintf("%s%s  %s", marker, m.choices[index].Value, m.choices[index].Name), width-1))
		b.WriteByte('\n')
	}
	if len(m.choices) == 0 {
		b.WriteString("  No matching emoji\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var emojiHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "confirm"},
	{"Tab", "mode"},
	{"Esc", "cancel"},
}
