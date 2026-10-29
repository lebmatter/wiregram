package tui

import (
	"charm.land/bubbletea/v2"

	"wiregram/internal/account"
	"wiregram/internal/textfmt"
)

// chatRewriteReq asks the root for the rewrite style picker, for the
// composer's draft.
type chatRewriteReq struct{}

// rewriteDraft is the composer's draft as a model rewrites it, shown in the
// card over the composer.
type rewriteDraft struct {
	id     int
	stream *account.Stream
	text   string
	done   bool
	err    error
}

// shown is the card's text: the rewrite so far, or why it failed.
func (r *rewriteDraft) shown(st *styles) string {
	switch {
	case r.err != nil:
		return styleError.Render(sentence(textfmt.CleanError(r.err)))
	case r.text == "" && !r.done:
		return st.subtle.Render("Rewriting…")
	case !r.done:
		return aiText(r.text) + "▍"
	}
	return aiText(r.text)
}

// buttons are the card's: Use only once the rewrite is complete.
func (r *rewriteDraft) buttons() []cardButton {
	if !r.done || r.err != nil {
		return []cardButton{{"[Discard ^X]", "discard"}}
	}
	return []cardButton{{"[Use ^Y]", "use"}, {"[Discard ^X]", "discard"}}
}

// rewriteAct uses the rewrite in place of the draft or drops it.
func (m chatViewModel) rewriteAct(act string) chatViewModel {
	r := m.rewrite
	switch act {
	case "use":
		if !r.done || r.err != nil {
			return m
		}
		m.input.SetValue(aiText(r.text))
		m.input.MoveToEnd()
		m.fitInputs()
	case "discard":
		r.stream.Cancel()
	default:
		return m
	}
	m.rewrite = nil
	return m
}

// rewritePicker chooses how to rewrite the draft, over the chat.
type rewritePicker struct {
	st  *styles
	row int
}

const rewritePickerWidth = 44

var rewritePickerKeys = []helpKey{{"↑↓", "choose"}, {"enter", "rewrite"}, {"esc", "cancel"}}

func (p *rewritePicker) size() (w, h int) {
	return rewritePickerWidth, len(account.RewriteStyles) + 5
}

// key moves the selection; it reports the style chosen, or done when the
// picker closes either way.
func (p *rewritePicker) key(k string) (style int, done bool) {
	switch k {
	case "up", "k":
		p.row = max(0, p.row-1)
	case "down", "j":
		p.row = min(len(account.RewriteStyles)-1, p.row+1)
	case "enter":
		return p.row, true
	case "esc", "q":
		return -1, true
	}
	return -1, false
}

// click picks the style on row y of the picker, counted from its top edge.
func (p *rewritePicker) click(y int) (style int, ok bool) {
	if i := y - 1; i >= 0 && i < len(account.RewriteStyles) {
		return i, true
	}
	return -1, false
}

func (p *rewritePicker) View() string {
	w, h := p.size()
	var lines []string
	for i, s := range account.RewriteStyles {
		lines = append(lines, p.st.modalRow("", s.Label, i == p.row, w-2))
	}
	lines = append(lines, "")
	lines = append(lines, p.st.modalKeys(rewritePickerKeys, "", w-4)...)
	return renderTitledBox(p.st.brand.Render("rewrite"), "", lines, w, h, p.st.accentColor)
}

// rewritePickerRect centres the picker over the chat.
func (m tuiModel) rewritePickerRect() (x, y, w, h int) {
	left := 0
	if m.twoPane() {
		left = sidebarWidth
	}
	w, h = m.rewritePicker.size()
	x, y = modalRect(m.chat.width, m.bodyHeight(), w, h)
	return left + x, y, w, h
}

// startRewrite rewrites the composer's draft in style, into the card over
// the composer.
func (m tuiModel) startRewrite(style int) (tuiModel, tea.Cmd) {
	draft := m.chat.input.Value()
	if !m.chatOpen || draft == "" {
		return m, nil
	}
	if m.mgr.AI == nil {
		m.alert = newAlertModal(m.st, "AI unavailable", "Bots and AI are off: wiregram "+
			"could not open their database.")
		return m, nil
	}
	if r := m.chat.rewrite; r != nil {
		r.stream.Cancel()
	}
	m.aiSeq++
	stream := m.mgr.AI.Rewrite(m.ctx, draft, style)
	m.chat.rewrite = &rewriteDraft{id: m.aiSeq, stream: stream}
	m.focus = focusChat
	return m, listenAI(m.aiSeq, stream)
}

// aiArrived passes the next piece of an AI task to the summary or rewrite
// it belongs to. One that has gone, with its chat or modal, is cancelled.
func (m tuiModel) aiArrived(c aiChunk) (tuiModel, tea.Cmd) {
	switch {
	case m.summary != nil && m.summary.id == c.id:
		m.summary.add(c, m.paneWidth(), m.bodyHeight())
		if !c.done {
			return m, listenAI(c.id, m.summary.stream)
		}
	case m.chatOpen && m.chat.rewrite != nil && m.chat.rewrite.id == c.id:
		r := *m.chat.rewrite
		r.text += c.text
		r.done, r.err = c.done, c.err
		m.chat.rewrite = &r
		if !c.done {
			return m, listenAI(c.id, r.stream)
		}
	default:
		c.stream.Cancel()
	}
	return m, nil
}

// paneWidth is the width of the pane right of the sidebar, or the screen's.
func (m tuiModel) paneWidth() int {
	if m.twoPane() && m.paneOpen() {
		return m.width - sidebarWidth
	}
	return m.width
}
