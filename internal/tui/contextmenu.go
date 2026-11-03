package tui

import (
	tea "charm.land/bubbletea/v2"

	"wiregram/internal/textfmt"
)

// contextMenu is what a right click on a row opens at the pointer: the
// actions that row takes, each the key press that does it, so choosing one
// does what pressing its key on the selected row would.
type contextMenu struct {
	st    *styles
	title string
	items []contextItem
	row   int
	x, y  int // its top-left corner on the screen
}

// contextItem is a menu row: its label and the key press it stands for.
type contextItem struct {
	label  string
	key    tea.KeyPressMsg
	danger bool
}

const (
	contextMenuMinWidth = 24
	contextMenuMaxTitle = 40
)

var contextMenuKeys = []helpKey{{"enter", "choose"}, {"esc", "close"}}

// newContextMenu places the menu with its corner at column x and row y of a
// screen width×height cells, moved in to fit.
func newContextMenu(st *styles, title string, items []contextItem,
	x, y, width, height int) *contextMenu {
	title = truncate(textfmt.Plain(title), contextMenuMaxTitle)
	if title == "" {
		title = "(no subject)" // only mail goes untitled
	}
	c := &contextMenu{st: st, title: title, items: items}
	w, h := c.size()
	c.x, c.y = max(0, min(x, width-w)), max(0, min(y, height-h))
	return c
}

// item is the key press a menu row sends, from a help label such as enter,
// space, r or M-d.
func item(label, key string) contextItem {
	k, _ := labelKey(key)
	return contextItem{label: label, key: k}
}

func (c *contextMenu) size() (int, int) {
	w := textfmt.DisplayWidth(c.title) + 6
	for _, it := range c.items {
		w = max(w, textfmt.DisplayWidth(it.label)+modalMark+4)
	}
	// The borders, the rows, the key rule and the keys.
	return max(contextMenuMinWidth, w), len(c.items) + 4
}

func (c *contextMenu) View() string {
	w, h := c.size()
	inner := w - 4
	var body []string
	for i, it := range c.items {
		label := it.label
		switch {
		case it.danger:
			label = styleError.Render(label)
		case i == c.row:
			label = c.st.brand.Render(label)
		}
		body = append(body, c.st.modalRow("", label, i == c.row, inner))
	}
	body = append(body, c.st.modalKeys(contextMenuKeys, "", inner)...)
	return c.st.modalBox(c.title, body, w, h)
}

// key moves the selection; it reports the item chosen, or done when the
// menu closes either way.
func (c *contextMenu) key(k string) (chosen *contextItem, done bool) {
	switch k {
	case "up", "k":
		c.row = max(0, c.row-1)
	case "down", "j":
		c.row = min(len(c.items)-1, c.row+1)
	case "enter":
		return &c.items[c.row], true
	case "esc", "q":
		return nil, true
	}
	return nil, false
}

// click answers a click at column x and row y of the screen: a row chooses
// its item, a key under them acts, and a click outside closes the menu.
func (c *contextMenu) click(x, y int) (chosen *contextItem, done bool) {
	w, h := c.size()
	if x < c.x || x >= c.x+w || y < c.y || y >= c.y+h {
		return nil, true
	}
	if i := y - c.y - 1; i >= 0 && i < len(c.items) {
		return &c.items[i], true
	}
	if y == c.y+h-2 {
		if k, ok := labelKey(keyAt(contextMenuKeys, x-c.x-2, 2, w-4)); ok {
			return c.key(k.String())
		}
	}
	return nil, false
}
