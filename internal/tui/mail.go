package tui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// Email chats (caps.MultilineCompose) write in a multiline draft that Ctrl+S
// sends, and read a whole mail in the reader that Enter opens.

const draftRows = 6

func newDraft() textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = "Write here. A new mail's first line is its subject; " +
		"start a line with Subject: for a new conversation."
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.SetHeight(draftRows)
	return ta
}

// startCompose focuses the draft, typing key into it when one is given.
func (m chatViewModel) startCompose(key *tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	m.subMode = chatSubCompose
	cmd := m.draft.Focus()
	if key != nil {
		var typed tea.Cmd
		m.draft, typed = m.draft.Update(*key)
		cmd = tea.Batch(cmd, typed)
	}
	return m, cmd
}

func (m chatViewModel) handleComposeKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// The draft stays; typing again picks it up.
		m.subMode = chatSubNone
		m.draft.Blur()
		return m, nil
	case "ctrl+s":
		text := strings.TrimSpace(m.draft.Value())
		if text == "" || m.sendingDraft {
			return m, nil
		}
		replyTo := ""
		if m.replying != nil {
			replyTo = m.replying.ID
		}
		m.sendingDraft = true
		m.notice = "sending…"
		return m, m.sendMessage(chatOutgoing{text: text, replyTo: replyTo})
	}
	var cmd tea.Cmd
	m.draft, cmd = m.draft.Update(msg)
	return m, cmd
}

// draftSent clears the draft once the mail is out; a failed send keeps it.
func (m chatViewModel) draftSent(err error) chatViewModel {
	m.sendingDraft = false
	if err == nil {
		m.notice = "sent"
		m.draft.Reset()
		m.replying = nil
		m.subMode = chatSubNone
		m.draft.Blur()
	}
	return m
}

// draftBox is the composer of an email chat: the draft while writing, or a
// one-line hint until then.
func (m chatViewModel) draftBox(width int, border color.Color) []string {
	title := "Mail"
	if m.replying != nil {
		title = "Reply"
	}
	m.draft.SetWidth(max(1, width-4))
	var lines []string
	switch {
	case m.subMode == chatSubCompose:
		lines = strings.Split(m.draft.View(), "\n")
	case m.draft.Value() != "":
		m.draft.SetHeight(min(3, draftRows))
		lines = strings.Split(m.draft.View(), "\n")
		lines = append(lines, styleSubtle.Render("draft · type to continue · ^S sends"))
	default:
		lines = []string{styleSubtle.Render("Type to write a mail · ⏎ reads the selected one")}
	}
	lines = append(m.replyBar(), lines...)
	return strings.Split(renderBox(title, lines, width, len(lines)+2, border), "\n")
}

// openReader shows the selected message in full.
func (m chatViewModel) openReader() (chatViewModel, tea.Cmd) {
	sel, ok := m.selectedMsg()
	if !ok {
		return m, nil
	}
	m.reading, m.readerTop = sel, 0
	m.subMode = chatSubReader
	return m, nil
}

func (m chatViewModel) handleReaderKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	page := max(1, m.height-2)
	last := max(0, len(m.readerLines(m.width-4))-page)
	switch msg.String() {
	case "esc", "q":
		m.subMode = chatSubNone
	case "up", "k":
		m.readerTop--
	case "down", "j":
		m.readerTop++
	case "pgup", "b":
		m.readerTop -= page
	case "pgdown", "space", " ":
		m.readerTop += page
	case "home", "g":
		m.readerTop = 0
	case "end", "G":
		m.readerTop = last
	case "alt+r", "alt+R":
		m.subMode = chatSubNone
		sel := m.reading
		m.replying = &sel
		return m.startCompose(nil)
	case "alt+s", "alt+S", "delete", "shift+delete":
		m.subMode = chatSubNone
		return m.handleKey(msg)
	}
	m.readerTop = max(0, min(m.readerTop, last))
	return m, nil
}

// readerLines lays out the whole mail: subject, headers, then the full text
// with its line breaks.
func (m chatViewModel) readerLines(width int) []string {
	width = min(width, textfmt.MaxChatLineWidth)
	msg := m.reading
	headers, body := []string(nil), msg.Text
	if fm, ok := m.b.(backend.FullMessager); ok {
		headers, body = fm.FullMessage(msg)
	}
	var lines []string
	if msg.Subject != "" {
		for _, line := range textfmt.WrapLineIndent(textfmt.Plain(msg.Subject), width, 0) {
			lines = append(lines, styleTitle.Render(line))
		}
	}
	for _, h := range headers {
		lines = append(lines, styleSubtle.Render(textfmt.ClipLine(textfmt.Plain(h), width)))
	}
	if len(lines) > 0 {
		lines = append(lines, styleDim.Render(strings.Repeat("─", width)))
	}
	var text []string
	for _, para := range strings.Split(body, "\n") {
		para = styleLinks(textfmt.Plain(para))
		text = append(text, textfmt.WrapLineIndent(para, width, 0)...)
	}
	return append(lines, closeLinks(text)...)
}

func (m chatViewModel) readerBody(width, height int) []string {
	lines := m.readerLines(width)
	top := min(m.readerTop, max(0, len(lines)-height))
	return lines[top:min(len(lines), top+height)]
}

var chatComposeHelp = []helpKey{
	{"^S", "send"},
	{"⏎", "new line"},
	{"esc", "keep draft"},
}

var chatReaderHelp = []helpKey{
	{"↑↓", "scroll"},
	{"PgUp/PgDn", "page"},
	{"M-r", "reply"},
	{"M-s", "save attachment"},
	{"del", "trash"},
	{"esc", "close"},
}
