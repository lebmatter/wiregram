package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// textDrag is a left button pressed in the chat or thread pane: a click
// when it is let go where it went down, else a selection of the pane's text
// from the press to the pointer, copied when it is let go. Points are in the
// chat's columns and rows.
type textDrag struct {
	x0, y0, x1, y1 int
	left, right    int  // the columns of the pane's text, past its border and gutter
	bottom         int  // the last row inside the pane's border; the first is 1
	moved          bool // the pointer left the cell it was pressed in
	done           bool // let go: the selection stays drawn until the next key or click
}

const kittyPlaceholder = '\U0010EEEE'

// press starts a drag at column x and row y of the chat, in the pane there.
func (m chatViewModel) press(x, y int) chatViewModel {
	left, right := 0, m.width
	if split, ok := m.threadSplit(); ok {
		if x >= split {
			left = split
		} else {
			right = split
		}
	}
	m.drag = &textDrag{x0: x, y0: y, x1: x, y1: y, left: left + 4, right: right - 2,
		bottom: m.height - 2}
	return m
}

// dragTo moves the drag's end to column x and row y, kept inside its pane.
func (m chatViewModel) dragTo(x, y int) chatViewModel {
	if m.drag == nil || m.drag.done {
		return m
	}
	d := *m.drag
	d.x1, d.y1 = x, y
	d.moved = d.moved || d.x1 != d.x0 || d.y1 != d.y0
	m.drag = &d
	return m
}

// release ends the drag: a click where it was pressed, or the selection
// copied to the clipboard.
func (m chatViewModel) release() (chatViewModel, tea.Cmd) {
	d := m.drag
	if d == nil || d.done {
		return m, nil
	}
	if !d.moved {
		m.drag = nil
		return m.handleClick(chatClick{d.x0, d.y0, false})
	}
	done := *d
	done.done = true
	m.drag = &done
	return m.copySelection()
}

// copySelection puts the selected text on the clipboard.
func (m chatViewModel) copySelection() (chatViewModel, tea.Cmd) {
	text := m.selectedText()
	if text == "" {
		return m, nil
	}
	m.notice = fmt.Sprintf("Copied %d characters", len([]rune(text)))
	m.copiedAt = time.Now()
	return m, tea.Batch(tea.SetClipboard(text), systemCopy(text))
}

// clipboardFailed says the system's clipboard tool could not take a copy.
type clipboardFailed struct{ err error }

// systemCopy also hands text to the desktop's clipboard tool, since many
// terminals (GNOME Terminal and the other VTE ones among them) ignore the
// OSC 52 that tea.SetClipboard writes. Over SSH the terminal's copy is the
// only one that reaches the user.
func systemCopy(text string) tea.Cmd {
	if os.Getenv("SSH_TTY") != "" {
		return nil
	}
	var tool []string
	switch {
	case runtime.GOOS == "darwin":
		tool = []string{"pbcopy"}
	case runtime.GOOS == "windows":
		tool = []string{"clip"}
	case os.Getenv("WAYLAND_DISPLAY") != "":
		tool = []string{"wl-copy"}
	case os.Getenv("DISPLAY") != "":
		tool = []string{"xclip", "-selection", "clipboard"}
		if _, err := exec.LookPath("xclip"); err != nil {
			tool = []string{"xsel", "--clipboard", "--input"}
		}
	default:
		return nil
	}
	return func() tea.Msg {
		if _, err := exec.LookPath(tool[0]); err != nil {
			return nil // the terminal's copy may still have worked
		}
		cmd := exec.Command(tool[0], tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return clipboardFailed{fmt.Errorf("copy with %s: %w", tool[0], err)}
		}
		return nil
	}
}

// selecting reports whether a finished selection is drawn.
func (m chatViewModel) selecting() bool {
	return m.drag != nil && m.drag.done
}

// copyKey reports whether key copies a drawn selection again.
func copyKey(key string) bool {
	switch key {
	case "ctrl+c", "ctrl+shift+c", "super+c":
		return true
	}
	return false
}

// clearSelection drops a finished selection's highlight.
func (m chatViewModel) clearSelection() chatViewModel {
	if m.drag != nil && m.drag.done {
		m.drag = nil
	}
	return m
}

// span is the columns selected on row y, from the start to the end of the
// drag read as running text, or false when none are.
func (d *textDrag) span(y int) (int, int, bool) {
	x0, y0, x1, y1 := d.x0, d.y0, d.x1, d.y1
	if y1 < y0 || y1 == y0 && x1 < x0 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	if !d.moved || y < max(y0, 1) || y > min(y1, d.bottom) {
		return 0, 0, false
	}
	a, b := d.left, d.right
	if y == y0 {
		a = max(a, x0)
	}
	if y == y1 {
		b = min(b, x1+1)
	}
	return a, b, a < b
}

// highlight draws the selection over view, the chat's lines, in reverse.
func (m chatViewModel) highlight(view string) string {
	if m.drag == nil || !m.drag.moved {
		return view
	}
	lines := strings.Split(view, "\n")
	for y, line := range lines {
		if a, b, ok := m.drag.span(y); ok {
			lines[y] = cutCells(line, 0, a) + "\x1b[0;7m" + ansi.Strip(cutCells(line, a, b)) +
				"\x1b[0m" + cutCells(line, b, m.width)
		}
	}
	return strings.Join(lines, "\n")
}

// selectedText is the text under the selection, each row's trailing blanks
// and inline images left out.
func (m chatViewModel) selectedText() string {
	lines := strings.Split(m.view(), "\n")
	var out []string
	for y, line := range lines {
		if a, b, ok := m.drag.span(y); ok {
			out = append(out, strings.TrimRightFunc(plainCells(cutCells(line, a, b)),
				unicode.IsSpace))
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// cutCells is columns [left, right) of s, styles kept. ansi.CutWc cannot do
// it: it truncates on the right for both edges.
func cutCells(s string, left, right int) string {
	if right <= left {
		return ""
	}
	return ansi.TruncateLeftWc(ansi.TruncateWc(s, right, ""), left, "")
}

// plainCells is s without styles and without kitty's image placeholders,
// whose diacritics follow them.
func plainCells(s string) string {
	var b strings.Builder
	inImage := false
	for _, r := range ansi.Strip(s) {
		switch {
		case r == kittyPlaceholder:
			inImage = true
		case inImage && unicode.Is(unicode.Mn, r):
		default:
			inImage = false
			b.WriteRune(r)
		}
	}
	return b.String()
}
