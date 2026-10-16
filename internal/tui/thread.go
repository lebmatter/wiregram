package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
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
		if err != nil || found == nil {
			// The loaded part of the thread stays; an error is not worth a notice.
			// Non-nil, so the thread no longer reads as loading.
			found = []backend.Message{}
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
	return m.width * 5 / 9, true
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

// openRoot is the root of the thread open beside the chat, or "".
func (m chatViewModel) openRoot() string {
	if m.subMode != chatSubThread {
		return ""
	}
	return m.threadRoot
}

// threadInfo is the replies chip of the loaded message id.
func (m chatViewModel) threadInfo(id string) threadInfo {
	t := threadInfo{open: id == m.openRoot()}
	for _, msg := range m.messages {
		if msg.ReplyToID == id {
			t.count++
			if msg.Timestamp.After(t.last) {
				t.last = msg.Timestamp
			}
		}
	}
	return t
}

// inThread reports whether msg belongs to a thread of two or more messages.
func (m chatViewModel) inThread(msg backend.Message) bool {
	return len(m.threadMessages(m.threadRootOf(msg).ID)) > 1
}

func (m chatViewModel) openThread(sel backend.Message) (chatViewModel, tea.Cmd) {
	root := m.threadRootOf(sel)
	m.threadRoot = root.ID
	m.subMode = chatSubThread
	m.threadScroll = 0
	m.threadInput = newComposerArea(m.st, "reply…")
	cmds := []tea.Cmd{m.threadInput.Focus(), m.fetchThread(root.ID)}
	if m.threadLoading() {
		cmds = append(cmds, m.spin.Tick)
	}
	return m, tea.Batch(cmds...)
}

// threadLoading reports whether the open thread is still being fetched.
func (m chatViewModel) threadLoading() bool {
	full, asked := m.threads[m.threadRoot]
	return m.threadRoot != "" && asked && full == nil
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
	m.fitInputs()
	m.threadInput, cmd = m.threadInput.Update(msg)
	return m, cmd
}

func (m chatViewModel) threadBox(width, height int, focused bool) string {
	inner := width - 4
	composer := m.threadComposer(inner, focused)
	rows := height - 2 - len(composer)
	lines, _, _ := m.threadLines(inner, rows)
	body := append(topRows(scrolled(lines, rows, m.threadScroll), rows, ""), composer...)
	left := styleTitle.Render("thread") + m.st.dim.Render(" · ") + m.st.subtle.Render(chatName(m.entry))
	right := m.st.accent.Render("esc") + m.st.subtle.Render(" close")
	return renderTitledBox(left, right, body, width, height, m.st.border(focused))
}

func (m chatViewModel) threadComposer(inner int, focused bool) []string {
	return m.st.areaBox("reply in thread", nil, m.threadInput, inner, m.st.border(focused))
}

// threadLines lays out the thread inner columns wide and returns, for each
// line, the index into threadMessages it belongs to (-1 for none) and what
// it draws. The root comes first with its own header line, then a rule with
// the reply count, then the replies in the chat's columns.
func (m chatViewModel) threadLines(inner, height int) ([]string, []int, []rowKind) {
	thread := m.threadMessages(m.threadRoot)
	var lines []string
	var owners []int
	var kinds []rowKind
	add := func(owner int, more ...string) {
		lines = append(lines, more...)
		for range more {
			owners = append(owners, owner)
			kinds = append(kinds, rowText)
		}
	}
	if len(thread) == 0 {
		add(-1, m.st.subtle.Render("The thread's first message is no longer loaded."))
	}
	now := time.Now()
	for i, msg := range thread {
		// The first reply's day goes on the reply count's rule instead.
		switch {
		case i > 1 && startsDay(thread, i):
			add(-1, m.st.dividerLine(dayLabel(msg.Timestamp, now), m.st.subtle, inner))
		case i > 1 && !continues(thread, i):
			add(-1, "") // a gap before each new sender
		}
		// The thread is the chain itself, so replies are drawn without a pointer.
		msg.ReplyToID = ""
		block, blockKinds := m.st.messageBlock(m.b, msg, m.entry, blockOpts{
			edited: m.changedMessages[msg.ID], unread: m.unread[msg.ID],
			cont: i > 1 && continues(thread, i), img: m.images[msg.ID],
			flash: m.flashing().on(msg.ID), root: i == 0,
		}, inner, imageRows(height))
		if bg := m.st.blockShade(msg, false); bg != "" {
			for j := range block {
				block[j] = shadeLine(block[j], bg, inner)
			}
		}
		add(i, block...)
		copy(kinds[len(kinds)-len(block):], blockKinds)
		if i == 0 {
			n := len(thread) - 1
			label := fmt.Sprintf("%d %s ", n, plural(n, "reply", "replies"))
			if n > 0 && startsDay(thread, 1) {
				label += "· " + dayLabel(thread[1].Timestamp, now) + " "
			}
			add(-1, m.st.dim.Render(strings.Repeat("─", inner)),
				m.st.subtle.Render(label)+m.st.dim.Render(strings.Repeat("─",
					max(0, inner-textfmt.DisplayWidth(label)))))
			if m.threadLoading() {
				add(-1, "", centerLines(inner, 1, m.spin.View()+m.st.subtle.Render("loading thread…"))[0], "")
			}
		}
	}
	return lines, owners, kinds
}

// threadOwners maps each row inside a thread pane width columns wide, below
// its top border, to the threadMessages index drawn there (-1 for none) and
// what the row draws.
func (m chatViewModel) threadOwners(width int) ([]int, []rowKind) {
	inner := width - 4
	height := m.height - 2 - len(m.threadComposer(inner, false))
	_, owners, kinds := m.threadLines(inner, height)
	return topRows(scrolled(owners, height, m.threadScroll), height, -1),
		topRows(scrolled(kinds, height, m.threadScroll), height, rowText)
}

// topRows keeps the last height rows, padding at the bottom so a short
// thread starts at the top of its pane.
func topRows[T any](rows []T, height int, blank T) []T {
	height = max(0, height)
	if len(rows) > height {
		return rows[len(rows)-height:]
	}
	out := append(make([]T, 0, height), rows...)
	for len(out) < height {
		out = append(out, blank)
	}
	return out
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
// of the chat pane, when the thread pane is there, and where it was hit.
func (m chatViewModel) threadMessageAt(x, y int) (backend.Message, msgHit, bool) {
	if m.subMode != chatSubThread || m.privateMode {
		return backend.Message{}, msgHit{}, false
	}
	left, split := m.threadSplit()
	if !split {
		left = 0
	}
	if x < left {
		return backend.Message{}, msgHit{}, false
	}
	owners, kinds := m.threadOwners(m.width - left)
	row := y - 1
	if row < 0 || row >= len(owners) || owners[row] < 0 {
		return backend.Message{}, msgHit{}, false
	}
	indent := m.st.textIndent(m.entry)
	if owners[row] == 0 {
		indent = 2 // the root's text starts at the left edge
	}
	return m.threadMessages(m.threadRoot)[owners[row]],
		msgHit{kinds[row], x - left - 2 - indent}, true
}
