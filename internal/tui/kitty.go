package tui

import (
	"fmt"
	"image"
	"os"
	"strings"
	"unicode"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"golang.org/x/image/draw"
)

const (
	// kittyKeepWidth is the pixel width kept in memory when images are drawn
	// with kitty graphics, enough for a sharp card at any chat width.
	kittyKeepWidth = 1280
	// kittyCellPixels is the most pixels sent per cell column; cells are
	// assumed twice as tall as wide, as halfblock.Size does.
	kittyCellPixels = 20
	// kittyMaxID keeps image IDs within the 24-bit colour that carries them.
	kittyMaxID = 1<<24 - 1
)

// kittyGraphics reports whether the terminal draws kitty images through
// Unicode placeholders: kitty and Ghostty, outside tmux, which swallows them.
func kittyGraphics() bool {
	if os.Getenv("TMUX") != "" {
		return false
	}
	term := os.Getenv("TERM")
	return term == "xterm-kitty" || term == "xterm-ghostty" ||
		os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("KITTY_WINDOW_ID") != ""
}

// kittyImages hands out the image IDs of one screen and collects the ones no
// longer drawn, which the root model deletes from the terminal.
type kittyImages struct {
	next  uint32
	stale []uint32
}

func (k *kittyImages) newID() uint32 {
	k.next = k.next%kittyMaxID + 1
	return k.next
}

// flush deletes the stale images from the terminal.
func (k *kittyImages) flush() tea.Cmd {
	if k == nil || len(k.stale) == 0 {
		return nil
	}
	var b strings.Builder
	for _, id := range k.stale {
		b.WriteString(ansi.KittyGraphics(nil, "a=d", "d=I", fmt.Sprintf("i=%d", id), "q=2"))
	}
	k.stale = nil
	return tea.Raw(b.String())
}

// deleteAll deletes every image this screen sent, before it quits.
func (k *kittyImages) deleteAll() tea.Cmd {
	if k == nil || k.next == 0 {
		return nil
	}
	return tea.Raw(ansi.KittyGraphics(nil, "a=d", "d=R", "x=1",
		fmt.Sprintf("y=%d", k.next), "q=2"))
}

// kittyTransmit sends img to the terminal as image id with a virtual
// placement cols×rows cells, which kittyLines then draws.
func kittyTransmit(img image.Image, id uint32, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		b := img.Bounds()
		w := min(b.Dx(), cols*kittyCellPixels)
		h := max(1, b.Dy()*w/b.Dx())
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		var out strings.Builder
		err := kitty.EncodeGraphics(&out, dst, &kitty.Options{
			Action:           kitty.TransmitAndPut,
			Format:           kitty.PNG,
			Transmission:     kitty.Direct,
			ID:               int(id),
			Quiet:            2,
			VirtualPlacement: true,
			Columns:          cols,
			Rows:             rows,
			Chunk:            true,
		})
		if err != nil {
			return nil
		}
		return tea.RawMsg{Msg: out.String()}
	}
}

// kittyLines draws image id as rows lines of cols placeholder cells, each
// naming its row and column; the foreground colour carries the ID.
func kittyLines(id uint32, cols, rows int) []string {
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	lines := make([]string, rows)
	for y := range rows {
		var line strings.Builder
		line.WriteString(fg)
		for x := range cols {
			line.WriteRune(kitty.Placeholder)
			line.WriteRune(kitty.Diacritic(y))
			line.WriteRune(kitty.Diacritic(x))
		}
		line.WriteString("\x1b[0m")
		lines[y] = line.String()
	}
	return lines
}

// blankPlaceholders turns the image cells of a line stripped of its colours,
// which no longer name their image, into spaces.
func blankPlaceholders(line string) string {
	if !strings.ContainsRune(line, kitty.Placeholder) {
		return line
	}
	var b strings.Builder
	cell := false
	for _, r := range line {
		switch {
		case r == kitty.Placeholder:
			b.WriteByte(' ')
			cell = true
		case cell && unicode.Is(unicode.Mn, r):
		default:
			b.WriteRune(r)
			cell = false
		}
	}
	return b.String()
}
