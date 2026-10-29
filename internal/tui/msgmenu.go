package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type msgActionKind int

const (
	actReact msgActionKind = iota
	actReply
	actThread
	actQuote
	actEdit
	actForward
	actOpen
	actPlay
	actSave
	actPin
	actDeleteForMe
	actDeleteForAll
	actSummarize
	actSummarizeThread
)

type msgAction struct {
	kind  msgActionKind
	label string
}

// msgMenuModel is the actions modal of one message: when the backend has
// reactions, a search field and the matching emoji row, above the actions the
// message allows. Typing anywhere in the modal searches the reactions.
type msgMenuModel struct {
	st        *styles
	b         backend.Backend
	target    backend.Message
	title     string
	excerpt   string
	reactions bool
	search    textinput.Model
	emoji     []backend.EmojiOption
	actions   []msgAction
	row       int // 0 is the emoji row when there are reactions
	col       int // the emoji chosen on the emoji row
	prev      chatSubMode
}

// msgMenuChoice is what Enter or a click picked: an action, or a reaction
// when kind is actReact.
type msgMenuChoice struct {
	kind  msgActionKind
	emoji string
}

const msgMenuWidth = 50

// threaded is set when sel is in a thread, which can then be summarised.
func newMsgMenu(st *styles, b backend.Backend, caps backend.Capabilities, sel backend.Message,
	threaded bool, prev chatSubMode) msgMenuModel {
	var acts []msgAction
	add := func(ok bool, kind msgActionKind, label string) {
		if ok {
			acts = append(acts, msgAction{kind, label})
		}
	}
	_, saveable := b.MediaSaveable(sel)
	add(true, actReply, "Reply")
	add(true, actThread, "Thread")
	add(sel.ReplyToID != "", actQuote, "Go to quoted message")
	add(caps.Edit && b.Editable(sel) == nil, actEdit, "Edit")
	add(caps.Forward, actForward, "Forward")
	add(saveable, actOpen, "Open attachment")
	add(b.MediaPlayable(sel), actPlay, "Play in mpv")
	add(saveable, actSave, "Save to ~/Downloads")
	pin := "Pin"
	if sel.Pinned {
		pin = "Unpin"
	}
	add(caps.Pin, actPin, pin)
	revocable := caps.RevokeWindow == 0 || !sel.Outgoing ||
		time.Since(sel.Timestamp) <= caps.RevokeWindow
	add(caps.DeleteForMe, actDeleteForMe, "Delete for me")
	add(revocable, actDeleteForAll, "Delete for everyone")
	add(true, actSummarize, "Summarise chat")
	add(threaded, actSummarizeThread, "Summarise thread")
	m := msgMenuModel{
		st:      st,
		b:       b,
		target:  sel,
		title:   backend.GenericMessageSender(sel),
		excerpt: textfmt.ShortExcerpt(backend.GenericMessageText(sel)),
		actions: acts,
		prev:    prev,
	}
	if caps.Reactions {
		m.reactions = true
		m.emoji = b.ReactionChoices("")
		m.search = textinput.New()
		m.search.Prompt = ""
		m.search.Placeholder = "search reactions…"
	}
	m.row = m.firstAction()
	return m
}

func (m msgMenuModel) firstAction() int {
	if m.reactions {
		return 1
	}
	return 0
}

// key moves the selection or picks a row; any other key edits the reaction
// search. It reports a choice, or done with no choice when the menu closes.
func (m msgMenuModel) key(msg tea.KeyPressMsg) (msgMenuModel, tea.Cmd, *msgMenuChoice, bool) {
	rows := m.firstAction() + len(m.actions)
	switch k := msg.String(); k {
	case "esc", "ctrl+c":
		return m, nil, nil, true
	case "up":
		m.row = max(0, m.row-1)
	case "down":
		m.row = min(rows-1, m.row+1)
	case "left", "right":
		if len(m.emoji) == 0 {
			break
		}
		if m.row == 0 && k == "left" {
			m.col = max(0, m.col-1)
		} else if m.row == 0 {
			m.col = min(len(m.emoji)-1, m.col+1)
		}
		m.row = 0
	case "enter":
		if c := m.choice(); c != nil {
			return m, nil, c, true
		}
	default:
		return m.edited(msg)
	}
	return m, nil, nil, false
}

// edited passes msg, a key or a paste, to the reaction search and refilters
// the emoji row when the query changed.
func (m msgMenuModel) edited(msg tea.Msg) (msgMenuModel, tea.Cmd, *msgMenuChoice, bool) {
	if !m.reactions {
		return m, nil, nil, false
	}
	before := m.search.Value()
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	if q := m.search.Value(); q != before {
		m.emoji = m.b.ReactionChoices(strings.TrimSpace(q))
		m.row, m.col = 0, 0
	}
	return m, cmd, nil, false
}

func (m msgMenuModel) choice() *msgMenuChoice {
	if m.row < m.firstAction() {
		if m.col >= len(m.emoji) {
			return nil
		}
		return &msgMenuChoice{kind: actReact, emoji: m.emoji[m.col].Value}
	}
	if i := m.row - m.firstAction(); i < len(m.actions) {
		return &msgMenuChoice{kind: m.actions[i].kind}
	}
	return nil
}

// emojiCells is the emoji row's visible window, the first emoji shown and
// each one's padded cell.
func (m msgMenuModel) emojiCells(inner int) (int, []string) {
	cell := func(i int) string { return " " + m.emoji[i].Value + " " }
	fits := func(start int) int { // one past the last emoji that fits after start
		used, end := 2, start // room for the ‹ › arrows
		for end < len(m.emoji) && used+textfmt.DisplayWidth(cell(end)) <= inner {
			used += textfmt.DisplayWidth(cell(end))
			end++
		}
		return end
	}
	start := 0
	for start < m.col && fits(start) <= m.col {
		start++
	}
	var cells []string
	for i := start; i < fits(start); i++ {
		cells = append(cells, cell(i))
	}
	return start, cells
}

// msgMenuTop is the rows above the reaction field or the actions: the top
// border and the excerpt.
const msgMenuTop = 2

// size is the box's width and height on a screen width columns wide.
func (m msgMenuModel) size(width int) (int, int) {
	h := msgMenuTop + 1 + len(m.actions) + 3 // the rule, the keys and the bottom border
	if m.reactions {
		h += 4 // the field and the emoji row
	}
	return min(msgMenuWidth, max(8, width-2)), h
}

func (m msgMenuModel) View(width int) string {
	w, h := m.size(width)
	inner := w - 4
	body := []string{m.st.subtle.Render(textfmt.ClipLine(m.excerpt, inner))}
	if m.reactions {
		m.search.SetWidth(max(1, inner-6))
		body = append(body, m.st.modalField("+", m.search.View(), inner)...)
	}
	if m.reactions && len(m.emoji) == 0 {
		body = append(body, m.st.dim.Render("  no matching reaction"))
	} else if m.reactions {
		start, cells := m.emojiCells(inner)
		var b strings.Builder
		if start > 0 {
			b.WriteString(m.st.dim.Render("‹"))
		} else {
			b.WriteString(" ")
		}
		for i, c := range cells {
			if m.row == 0 && start+i == m.col {
				b.WriteString(shadeLine(m.st.brand.Render(c), m.st.selBgSGR, 0))
			} else {
				b.WriteString(c)
			}
		}
		if start+len(cells) < len(m.emoji) {
			b.WriteString(m.st.dim.Render("›"))
		}
		body = append(body, b.String())
	}
	body = append(body, m.st.modalRule("actions", inner))
	for i, a := range m.actions {
		label := a.label
		selected := m.row == m.firstAction()+i
		switch {
		case a.kind == actDeleteForMe || a.kind == actDeleteForAll:
			label = styleError.Render(label)
		case selected:
			label = m.st.brand.Render(label)
		}
		body = append(body, m.st.modalRow("", label, selected, inner))
	}
	keys := []helpKey{{"↑↓", "select"}}
	if m.reactions {
		keys = append(keys, helpKey{"←→", "react"})
	}
	keys = append(keys, helpKey{"enter", "choose"}, helpKey{"esc", "close"})
	body = append(body, m.st.modalKeys(keys, "", inner)...)
	return m.st.modalBox(textfmt.Plain(m.title), body, w, h)
}

// hit maps column x and row y of the box, counted from its top-left corner,
// to the row and emoji there.
func (m msgMenuModel) hit(x, y, width int) (msgMenuModel, bool) {
	w, _ := m.size(width)
	row := y - msgMenuTop
	if m.reactions {
		if row == 3 { // under the field
			start, cells := m.emojiCells(w - 4)
			at := 3 // the border, its padding and the ‹ column
			for i, c := range cells {
				cw := textfmt.DisplayWidth(c)
				if x >= at && x < at+cw {
					m.row, m.col = 0, start+i
					return m, true
				}
				at += cw
			}
			return m, false
		}
		row -= 4
	}
	row-- // the actions rule
	if row < 0 || row >= len(m.actions) {
		return m, false
	}
	m.row = m.firstAction() + row
	return m, true
}

var msgMenuHelp = []helpKey{
	{"↑↓", "actions"},
	{"type", "search reactions"},
	{"←→", "reactions"},
	{"enter", "choose"},
	{"esc", "close"},
}
