package tui

import (
	"strings"

	"wiregram/internal/backend"
	"wiregram/internal/game"
	"wiregram/internal/textfmt"
)

// run is the columns [a, b) from lo to hi around x where at answers as it
// does at x, or false when at x it answers "".
func run(x, lo, hi int, at func(int) string) (int, int, bool) {
	key := at(x)
	if key == "" {
		return 0, 0, false
	}
	a, b := x, x+1
	for a > lo && at(a-1) == key {
		a--
	}
	for b < hi && at(b) == key {
		b++
	}
	return a, b, true
}

// hoverSpan is the columns [a, b) of row y of the pane that a left click at
// x would act on beyond selecting a message: a name, a reaction, the replies
// chip, an attachment's tag, a game invite, or a button or key.
func (m chatViewModel) hoverSpan(x, y int) (int, int, bool) {
	switch m.subMode {
	case chatSubMenu, chatSubUpload, chatSubConfirmChatDelete, chatSubConfirmMsgDelete:
		return 0, 0, false
	}
	if a, b, ok, onMessage := m.messageSpan(x, y); onMessage {
		return a, b, ok
	}
	return run(x, 0, m.width, func(c int) string {
		switch {
		case m.threadCloseAt(c, y):
			return "close"
		case m.replyCancelAt(c, y):
			return "cancel"
		case m.gameButtonAt(c, y):
			return "game"
		}
		if hit, _ := m.uploadButtonAt(c, y); hit {
			return "upload"
		}
		if act, ok := m.cardAt(c, y); ok {
			return act
		}
		return ""
	})
}

// messageSpan is hoverSpan over a message, and whether x, y is on one.
func (m chatViewModel) messageSpan(x, y int) (a, b int, ok, onMessage bool) {
	sel, hit, found := m.threadMessageAt(x, y)
	inChat := !found
	if !found {
		i, h, ok := m.messageAt(x, y)
		if !ok {
			return 0, 0, false, false
		}
		sel, hit = m.messages[i], h
	}
	_, profiles := m.b.(backend.Profiler)
	_, saveable := m.b.MediaSaveable(sel)
	_, _, invite := game.ParseInvite(sel.Text)
	info := m.threadInfo(sel.ID)
	shift := hit.cell - hit.col
	key := func(cell int) string {
		h := hit
		h.cell, h.col = cell, cell-shift
		if h.kind&rowMeta != 0 {
			if inChat && repliesAt(sel, info, h.col) {
				return "replies"
			}
			if emoji, ok := reactionAt(sel, h.col); ok {
				return "react " + emoji
			}
			return ""
		}
		if !textAt(h) {
			return ""
		}
		if invite && gameInviteAt(h) {
			return "invite"
		}
		if profiles {
			if name, ok := mentionAt(h); ok {
				return "@" + name
			}
			if h.kind&rowHeader != 0 && m.senderAt(sel, h.col) {
				return "sender"
			}
		}
		if h.kind&rowMedia != 0 && saveable && h.col >= 0 && !imageRow(h.line) {
			return "media"
		}
		return ""
	}
	a, b, ok = run(hit.cell, 0, textfmt.DisplayWidth(hit.line), key)
	off := x - hit.cell
	return a + off, b + off, ok, true
}

// imageRow reports whether a row of an attachment draws an image or a card,
// which hover leaves alone: only a [tag] row is underlined.
func imageRow(line string) bool {
	return strings.ContainsRune(line, kittyPlaceholder) || strings.ContainsAny(line, "╭╮╰╯│▄▀")
}

// hoverSpan is the columns of the reader's action at x, y of the pane.
func (m mailListModel) hoverSpan(x, y int) (int, int, bool) {
	if m.composing || m.search != nil || m.reading == nil || y != 1 ||
		m.split() && x < m.listWidth() {
		return 0, 0, false
	}
	left := 0
	if m.beside() {
		left = m.listWidth()
	}
	label, a, b := keySpan(m.readerKeys(), x-left-2, 2, m.readerWidth()-4)
	return a + left + 2, b + left + 2, clickable(label)
}

// hoverSpan is the columns of the selected card's key at x, y of the pane.
func (m unreadModel) hoverSpan(x, y int) (int, int, bool) {
	top, ok := m.keysRow(y)
	if !ok || y != top {
		return 0, 0, false
	}
	label, a, b := keySpan(m.cardKeys(m.cards[m.selected]), x-4, 3, m.cardInner())
	return a + 4, b + 4, clickable(label)
}
