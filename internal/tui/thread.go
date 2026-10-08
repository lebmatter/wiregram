package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// Threads are built from loaded messages only: a thread is a root plus every
// message whose reply chain leads back to it. Mattermost stores the root in
// ReplyToID; the other backends form chains of ordinary replies.

// threadRootOf walks up the reply chain to the oldest loaded ancestor.
func threadRootOf(messages []backend.Message, msg backend.Message) backend.Message {
	byID := make(map[string]backend.Message, len(messages))
	for _, m := range messages {
		byID[m.ID] = m
	}
	for depth := 0; msg.ReplyToID != "" && depth < 100; depth++ {
		parent, ok := byID[msg.ReplyToID]
		if !ok {
			break
		}
		msg = parent
	}
	return msg
}

// threadMessages returns the root followed by its replies in order.
func threadMessages(messages []backend.Message, rootID string) []backend.Message {
	parent := make(map[string]string, len(messages))
	for _, m := range messages {
		parent[m.ID] = m.ReplyToID
	}
	var out []backend.Message
	for _, m := range messages {
		id := m.ID
		for depth := 0; id != "" && depth < 100; depth++ {
			if id == rootID {
				out = append(out, m)
				break
			}
			id = parent[id]
		}
	}
	return out
}

// threadPaneMinWidth is the chat pane width from which the thread shows beside
// the chat instead of replacing it.
const threadPaneMinWidth = 90

// threadSplit returns the chat box width while the thread shows beside it.
func (m chatViewModel) threadSplit() (int, bool) {
	if m.subMode != chatSubThread || m.width < threadPaneMinWidth {
		return m.width, false
	}
	return m.width * 3 / 5, true
}

// followThread opens the selected message's thread beside the chat while
// browsing, and closes it once the selection leaves every thread.
func (m chatViewModel) followThread() (chatViewModel, tea.Cmd) {
	if m.width < threadPaneMinWidth || m.privateMode {
		return m, nil
	}
	sel, ok := m.selectedMsg()
	if !ok {
		return m, nil
	}
	root := threadRootOf(m.messages, sel)
	switch {
	case len(threadMessages(m.messages, root.ID)) < 2:
		m.subMode = chatSubNone
		m.threadRoot = ""
		return m, nil
	case root.ID == m.threadRoot:
		m.subMode = chatSubThread // keeps the half-typed reply
		return m, nil
	}
	return m.openThread(sel)
}

func (m chatViewModel) openThread(sel backend.Message) (chatViewModel, tea.Cmd) {
	root := threadRootOf(m.messages, sel)
	m.threadRoot = root.ID
	m.subMode = chatSubThread
	m.threadInput = textinput.New()
	m.threadInput.Prompt = ""
	m.threadInput.Placeholder = "Reply in thread…"
	return m, m.threadInput.Focus()
}

func (m chatViewModel) handleThreadKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.subMode = chatSubNone
		m.threadRoot = ""
		return m, nil
	case "up", "down":
		if m.width >= threadPaneMinWidth {
			// The chat beside the thread keeps moving its selection.
			m.subMode = chatSubNone
			return m.handleKey(msg)
		}
	case "enter":
		text := strings.TrimSpace(m.threadInput.Value())
		if text == "" {
			return m, nil
		}
		m.threadInput.SetValue("")
		return m, m.sendMessage(chatOutgoing{text: text, replyTo: m.threadRoot})
	}
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	return m, cmd
}

func (m chatViewModel) threadBox(width, height int, focused bool) string {
	inner := width - 4
	composer := m.threadComposer(inner, focused)
	lines, _ := m.threadLines(inner, height-2-len(composer))
	body := append(bottomLines(lines, height-2-len(composer)), composer...)
	return renderBox("Thread", body, width, height, boxBorder(focused))
}

func (m chatViewModel) threadComposer(inner int, focused bool) []string {
	return composerBox("", nil, "› ", m.threadInput, inner, boxBorder(focused))
}

// threadLines lays out the thread inner columns wide and returns, for each
// line, the index into threadMessages it belongs to (-1 for none).
func (m chatViewModel) threadLines(inner, height int) ([]string, []int) {
	thread := threadMessages(m.messages, m.threadRoot)
	var lines []string
	var owners []int
	add := func(owner int, more ...string) {
		lines = append(lines, more...)
		for range more {
			owners = append(owners, owner)
		}
	}
	if len(thread) == 0 {
		add(-1, styleSubtle.Render("The thread's first message is no longer loaded."))
	}
	const indent = 7 + 2 + textfmt.NickWidth + 2
	for i, msg := range thread {
		text, below := imageText(msg, m.images[msg.ID], indent, inner, imageRows(height))
		add(i, textfmt.WrapMessage(backend.GenericMessagePrefix(msg, m.entry)+" ",
			text, inner, indent)...)
		add(i, below...)
		add(i, previewCard(msg.Preview, indent, inner)...)
		if i == 0 {
			n := len(thread) - 1
			label := fmt.Sprintf(" %d %s ", n, plural(n, "reply", "replies"))
			add(-1, styleDim.Render("──"+label+strings.Repeat("─", max(0, inner-2-len(label)))))
		}
	}
	return lines, owners
}

// threadOwners maps each row inside a thread pane width columns wide, below
// its top border, to the threadMessages index drawn there (-1 for none).
func (m chatViewModel) threadOwners(width int) []int {
	inner := width - 4
	height := m.height - 2 - len(m.threadComposer(inner, false))
	_, owners := m.threadLines(inner, height)
	return screenOwners(owners, height)
}

// threadMessageAt returns the thread message drawn at column x and row y
// of the chat pane, when the thread pane is there.
func (m chatViewModel) threadMessageAt(x, y int) (backend.Message, bool) {
	if m.subMode != chatSubThread || m.privateMode {
		return backend.Message{}, false
	}
	left, split := m.threadSplit()
	if !split {
		left = 0
	}
	if x < left {
		return backend.Message{}, false
	}
	owners := m.threadOwners(m.width - left)
	row := y - 1
	if row < 0 || row >= len(owners) || owners[row] < 0 {
		return backend.Message{}, false
	}
	return threadMessages(m.messages, m.threadRoot)[owners[row]], true
}
