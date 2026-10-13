package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
)

// Threads are built from loaded messages only: a thread is a root plus every
// message whose reply chain leads back to it. Mattermost stores the root in
// ReplyToID; the other backends form chains of ordinary replies. A root older
// than the loaded history comes from the originals fetched for quotes.

// threadOriginals maps every message the thread can start from by ID: the
// loaded ones, then the fetched quotes and the copies replies carry.
func (m chatViewModel) threadOriginals() map[string]backend.Message {
	byID := make(map[string]backend.Message, len(m.messages))
	for _, q := range m.quotes {
		if q != nil {
			byID[q.ID] = *q
		}
	}
	for _, msg := range m.messages {
		if msg.Quote != nil {
			byID[msg.Quote.ID] = *msg.Quote
		}
	}
	for _, msg := range m.messages {
		byID[msg.ID] = msg
	}
	return byID
}

// threadRootOf walks up the reply chain to the oldest known ancestor.
func (m chatViewModel) threadRootOf(msg backend.Message) backend.Message {
	byID := m.threadOriginals()
	for depth := 0; msg.ReplyToID != "" && depth < 100; depth++ {
		parent, ok := byID[msg.ReplyToID]
		if !ok {
			break
		}
		msg = parent
	}
	return msg
}

// threadMessages returns the root followed by its replies in order: the
// thread fetched from the server, if any, with the loaded copies of its
// messages and any loaded replies it lacks.
func (m chatViewModel) threadMessages(rootID string) []backend.Message {
	parent := make(map[string]string, len(m.messages))
	for _, msg := range m.messages {
		parent[msg.ID] = msg.ReplyToID
	}
	var loaded []backend.Message
	for _, msg := range m.messages {
		id := msg.ID
		for depth := 0; id != "" && depth < 100; depth++ {
			if id == rootID {
				loaded = append(loaded, msg)
				break
			}
			id = parent[id]
		}
	}
	full := m.threads[rootID]
	if len(full) == 0 {
		if _, ok := parent[rootID]; ok {
			return loaded
		}
		root, ok := m.threadOriginals()[rootID]
		if !ok {
			return nil
		}
		return append([]backend.Message{root}, loaded...)
	}
	fresh := make(map[string]backend.Message, len(loaded))
	for _, msg := range loaded {
		fresh[msg.ID] = msg
	}
	out := make([]backend.Message, 0, len(full)+len(loaded))
	for _, msg := range full {
		if l, ok := fresh[msg.ID]; ok {
			msg = l
			delete(fresh, msg.ID)
		}
		out = append(out, msg)
	}
	for _, msg := range loaded {
		if _, ok := fresh[msg.ID]; ok {
			out = append(out, msg)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out
}

// fetchThread loads the whole thread from the server, once per root.
func (m chatViewModel) fetchThread(rootID string) tea.Cmd {
	tf, ok := m.b.(backend.ThreadFetcher)
	if _, asked := m.threads[rootID]; !ok || asked || rootID == "" {
		return nil
	}
	m.threads[rootID] = nil
	ctx, entry := m.ctx, m.entry
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		found, err := tf.Thread(ctx, entry, rootID)
		if err != nil {
			found = nil // the loaded part of the thread stays; not worth a notice
		}
		return threadLoaded{entry.ID, rootID, found}
	}
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
	// Most mails are replies; their threads open only on Alt+T.
	if m.width < threadPaneMinWidth || m.privateMode || m.caps.MultilineCompose {
		return m, nil
	}
	sel, ok := m.selectedMsg()
	if !ok {
		return m, nil
	}
	root := m.threadRootOf(sel)
	switch {
	case len(m.threadMessages(root.ID)) < 2:
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
	root := m.threadRootOf(sel)
	m.threadRoot = root.ID
	m.subMode = chatSubThread
	m.threadScroll = 0
	m.threadInput = textinput.New()
	m.threadInput.Prompt = ""
	m.threadInput.Placeholder = "Reply in thread…"
	return m, tea.Batch(m.threadInput.Focus(), m.fetchThread(root.ID))
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
			return m.openMenu()
		}
		m.threadInput.SetValue("")
		m.threadScroll = 0
		return m, m.sendMessage(chatOutgoing{text: text, replyTo: m.threadRoot})
	}
	if msg.Text == "@" && m.mentionAllowed() {
		return m, func() tea.Msg { return chatMentionReq{} }
	}
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	return m, cmd
}

func (m chatViewModel) threadBox(width, height int, focused bool) string {
	inner := width - 4
	composer := m.threadComposer(inner, focused)
	rows := height - 2 - len(composer)
	lines, _, _ := m.threadLines(inner, rows)
	body := append(screenRows(scrolled(lines, rows, m.threadScroll), rows, ""), composer...)
	return renderBox("Thread", body, width, height, m.st.border(focused))
}

func (m chatViewModel) threadComposer(inner int, focused bool) []string {
	return m.st.composerBox("", nil, "› ", m.threadInput, inner, m.st.border(focused))
}

// threadLines lays out the thread inner columns wide and returns, for each
// line, the index into threadMessages it belongs to (-1 for none) and
// whether it draws that message's attachment.
func (m chatViewModel) threadLines(inner, height int) ([]string, []int, []bool) {
	thread := m.threadMessages(m.threadRoot)
	var lines []string
	var owners []int
	var media []bool
	add := func(owner int, more ...string) {
		lines = append(lines, more...)
		for range more {
			owners = append(owners, owner)
			media = append(media, false)
		}
	}
	if len(thread) == 0 {
		add(-1, m.st.subtle.Render("The thread's first message is no longer loaded."))
	}
	replies := make(map[string]int)
	for _, msg := range thread {
		replies[msg.ReplyToID]++
	}
	// The root's count is already on the divider below it.
	delete(replies, m.threadRoot)
	for i, msg := range thread {
		// The thread is the chain itself, so replies are drawn without a quote.
		msg.ReplyToID = ""
		block, blockMedia := m.st.messageBlock(m.b, msg, m.entry, nil, replies[msg.ID],
			m.changedMessages[msg.ID], m.unread[msg.ID], m.images[msg.ID], inner,
			imageRows(height))
		if bg := m.st.blockShade(msg, false); bg != "" {
			for j := range block {
				block[j] = shadeLine(block[j], bg, inner)
			}
		}
		add(i, block...)
		copy(media[len(media)-len(block):], blockMedia)
		if i == 0 {
			n := len(thread) - 1
			label := fmt.Sprintf(" %d %s ", n, plural(n, "reply", "replies"))
			add(-1, m.st.dim.Render("──"+label+strings.Repeat("─", max(0, inner-2-len(label)))))
		}
	}
	return lines, owners, media
}

// threadOwners maps each row inside a thread pane width columns wide, below
// its top border, to the threadMessages index drawn there (-1 for none) and
// whether the row draws its attachment.
func (m chatViewModel) threadOwners(width int) ([]int, []bool) {
	inner := width - 4
	height := m.height - 2 - len(m.threadComposer(inner, false))
	_, owners, media := m.threadLines(inner, height)
	return screenRows(scrolled(owners, height, m.threadScroll), height, -1),
		screenRows(scrolled(media, height, m.threadScroll), height, false)
}

// scrolled drops the rows the thread pane is scrolled past, so the last of
// the rest sits at the bottom of a pane height rows tall.
func scrolled[T any](rows []T, height, scroll int) []T {
	return rows[:len(rows)-min(scroll, max(0, len(rows)-height))]
}

// overThread reports whether column x of the chat pane is in the thread pane.
func (m chatViewModel) overThread(x int) bool {
	if m.subMode != chatSubThread {
		return false
	}
	left, split := m.threadSplit()
	return !split || x >= left
}

// scrollThread moves the thread pane by delta lines, up when positive.
func (m chatViewModel) scrollThread(delta int) chatViewModel {
	width := m.width
	if left, split := m.threadSplit(); split {
		width -= left
	}
	inner := width - 4
	height := m.height - 2 - len(m.threadComposer(inner, false))
	lines, _, _ := m.threadLines(inner, height)
	m.threadScroll = min(max(0, m.threadScroll+delta), max(0, len(lines)-height))
	return m
}

// threadMessageAt returns the thread message drawn at column x and row y
// of the chat pane, when the thread pane is there, and whether the row
// draws its attachment.
func (m chatViewModel) threadMessageAt(x, y int) (backend.Message, bool, bool) {
	if m.subMode != chatSubThread || m.privateMode {
		return backend.Message{}, false, false
	}
	left, split := m.threadSplit()
	if !split {
		left = 0
	}
	if x < left {
		return backend.Message{}, false, false
	}
	owners, media := m.threadOwners(m.width - left)
	row := y - 1
	if row < 0 || row >= len(owners) || owners[row] < 0 {
		return backend.Message{}, false, false
	}
	return m.threadMessages(m.threadRoot)[owners[row]], media[row], true
}
