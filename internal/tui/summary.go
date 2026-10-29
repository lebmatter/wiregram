package tui

import (
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// aiChunk is what has arrived of the AI task numbered id since the last one.
type aiChunk struct {
	id     int
	stream *account.Stream
	text   string
	done   bool
	err    error
}

// listenAI waits for the next piece of s and takes whatever else has
// arrived with it, so a fast stream redraws once per batch.
func listenAI(id int, s *account.Stream) tea.Cmd {
	return func() tea.Msg {
		c, ok := <-s.C
		if !ok {
			return aiChunk{id: id, stream: s, done: true}
		}
		text := c.Text
		for !c.Done {
			select {
			case next, ok := <-s.C:
				if !ok {
					return aiChunk{id: id, stream: s, text: text, done: true}
				}
				c = next
				text += c.Text
			default:
				return aiChunk{id: id, stream: s, text: text}
			}
		}
		return aiChunk{id: id, stream: s, text: text, done: true, err: c.Err}
	}
}

// aiText is a model's reply made safe to draw: no control characters, so no
// escape codes, with its line breaks kept.
func aiText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = textfmt.Plain(line)
	}
	return strings.Join(lines, "\n")
}

// summaryRequest asks the root to summarise msgs, oldest first, of entry.
type summaryRequest struct {
	entry  backend.ChatEntry
	msgs   []backend.Message
	thread bool
}

// summaryChatMessages is how many of the newest loaded messages a chat's
// summary covers when it has no unread ones.
const summaryChatMessages = 100

// summaryMessages are what Summarise chat covers: the messages unread when
// the chat opened, else the newest loaded ones.
func (m chatViewModel) summaryMessages() []backend.Message {
	for i, msg := range m.messages {
		if msg.ID == m.firstUnread {
			return m.messages[i:]
		}
	}
	return m.messages[max(0, len(m.messages)-summaryChatMessages):]
}

// summaryModal shows a summary as it streams in, over the pane it was asked
// from.
type summaryModal struct {
	st     *styles
	req    summaryRequest
	id     int
	stream *account.Stream
	text   string
	done   bool
	err    error
	offset int  // the first line shown
	follow bool // keep the newest line in view while it streams
}

var summaryKeys = []helpKey{{"↑↓", "scroll"}, {"c", "copy"}, {"r", "again"}, {"esc", "close"}}

const summaryWidth = 72

// size is the modal's width and height over a pane width×height cells.
func (s *summaryModal) size(width, height int) (w, h int) {
	w = min(summaryWidth, width-4)
	return w, max(8, min(height-2, len(s.lines(w))+5))
}

// lines is the summary wrapped inside a modal w cells wide.
func (s *summaryModal) lines(w int) []string {
	var out []string
	switch {
	case s.text == "" && s.err == nil:
		out = []string{s.st.subtle.Render("Summarising…")}
	case s.text != "":
		text := aiText(s.text)
		if !s.done {
			text += "▍"
		}
		out = strings.Split(lipgloss.NewStyle().Width(max(1, w-4)).Render(text), "\n")
	}
	if s.err != nil {
		out = append(out, styleError.Render(sentence(textfmt.CleanError(s.err))))
	}
	return out
}

// rows is how many lines of the summary fit in a modal h tall: all of it
// but the border, a blank row and the key strip.
func (s *summaryModal) rows(h int) int { return max(1, h-5) }

func (s *summaryModal) scroll(delta, width, height int) {
	w, h := s.size(width, height)
	limit := max(0, len(s.lines(w))-s.rows(h))
	s.offset = max(0, min(s.offset+delta, limit))
	s.follow = s.offset == limit
}

// add takes the next piece of the stream, keeping the newest line in view
// unless the user scrolled up.
func (s *summaryModal) add(c aiChunk, width, height int) {
	s.text += c.text
	s.done, s.err = c.done, c.err
	if s.follow {
		s.scroll(maxModalScroll, width, height)
	}
}

func (s *summaryModal) View(width, height int) string {
	w, h := s.size(width, height)
	lines := s.lines(w)
	top := max(0, min(s.offset, len(lines)-s.rows(h)))
	lines = lines[top:min(len(lines), top+s.rows(h))]
	lines = fillLines(append([]string(nil), lines...), s.rows(h)+1)
	keys := summaryKeys
	if !s.done {
		keys = []helpKey{summaryKeys[0], summaryKeys[3]}
	}
	lines = append(lines, s.st.modalKeys(keys, "", w-4)...)
	title := "summary · " + chatName(s.req.entry)
	if s.req.thread {
		title = "thread summary · " + chatName(s.req.entry)
	}
	return renderTitledBox(s.st.brand.Render(title), s.st.dim.Render(s.stream.Via), lines, w, h,
		s.st.accentColor)
}

// summaryRect is where the summary modal goes: centred over the pane right
// of the sidebar, or the screen.
func (m tuiModel) summaryRect() (x, y, w, h int) {
	left, width := 0, m.width
	if m.twoPane() && m.paneOpen() {
		left, width = sidebarWidth, m.width-sidebarWidth
	}
	w, h = m.summary.size(width, m.bodyHeight())
	x, y = modalRect(width, m.bodyHeight(), w, h)
	return left + x, y, w, h
}

// startSummary opens the summary modal on req and starts summarising.
func (m tuiModel) startSummary(req summaryRequest) (tuiModel, tea.Cmd) {
	if m.mgr.AI == nil {
		m.alert = newAlertModal(m.st, "AI unavailable", "Bots and AI are off: wiregram "+
			"could not open their database.")
		return m, nil
	}
	m.aiSeq++
	stream := m.mgr.AI.Summarize(m.ctx, req.entry, req.msgs)
	m.summary = &summaryModal{st: m.st, req: req, id: m.aiSeq, stream: stream, follow: true}
	return m, listenAI(m.aiSeq, stream)
}

func (m tuiModel) closeSummary() tuiModel {
	if m.summary != nil {
		m.summary.stream.Cancel()
		m.summary = nil
	}
	return m
}

// summaryKey handles a key while the summary modal is up.
func (m tuiModel) summaryKey(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	s := m.summary
	width := m.paneWidth()
	switch msg.String() {
	case "esc", "q":
		return m.closeSummary(), nil
	case "up", "k":
		s.scroll(-1, width, m.bodyHeight())
	case "down", "j":
		s.scroll(1, width, m.bodyHeight())
	case "pgup":
		s.scroll(-10, width, m.bodyHeight())
	case "pgdown", "space":
		s.scroll(10, width, m.bodyHeight())
	case "c":
		if s.done && s.text != "" {
			text := aiText(s.text)
			m.notice = "summary copied"
			return m, tea.Batch(tea.SetClipboard(text), systemCopy(text))
		}
	case "r":
		if s.done {
			return m.startSummary(s.req)
		}
	}
	return m, nil
}
