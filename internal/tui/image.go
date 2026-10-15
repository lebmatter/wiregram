package tui

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/halfblock"
	"wiregram/internal/textfmt"
)

// mediaCacheMaxAge is how long a downloaded attachment stays in the cache
// after it was last shown or opened.
const mediaCacheMaxAge = 7 * 24 * time.Hour

const (
	// imageMaxRows caps an inline image so one photo never fills the pane.
	imageMaxRows = 20
	// imageKeepWidth is the pixel width kept in memory, enough for the
	// widest chat line.
	imageKeepWidth = 2 * textfmt.MaxChatLineWidth
)

// chatImage is an inline image of one message: loading until img or failed
// is set. Rendered lines are memoised per size; View fills the shared map.
// With kitty graphics, placed holds the terminal image of each size drawn
// and want the sizes asked for since Update last placed them.
type chatImage struct {
	img    image.Image
	failed bool
	lines  map[[2]int][]string
	kitty  bool
	placed map[[2]int]uint32
	want   map[[2]int]bool
}

type chatImageLoaded struct {
	chatTag
	id  string
	img image.Image
	err error
}

func (c *chatImage) render(cols, rows int) []string {
	key := [2]int{cols, rows}
	if c.kitty {
		c.want[key] = true
		if id, ok := c.placed[key]; ok {
			return kittyLines(id, cols, rows)
		}
	}
	if lines, ok := c.lines[key]; ok {
		return lines
	}
	lines := halfblock.Render(c.img, cols, rows)
	c.lines[key] = lines
	return lines
}

// placeImages sends the terminal each image size the last layout asked for.
// An image drawn at a new size, after a resize, drops its other sizes; one
// scrolled out of view keeps them for when it comes back.
func (m chatViewModel) placeImages() tea.Cmd {
	if m.kitty == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, c := range m.images {
		if c.img == nil || len(c.want) == 0 {
			continue
		}
		for key, id := range c.placed {
			if !c.want[key] {
				m.kitty.stale = append(m.kitty.stale, id)
				delete(c.placed, key)
			}
		}
		for key := range c.want {
			if _, ok := c.placed[key]; !ok {
				id := m.kitty.newID()
				c.placed[key] = id
				cmds = append(cmds, kittyTransmit(c.img, id, key[0], key[1]))
			}
		}
		clear(c.want)
	}
	return tea.Batch(cmds...)
}

// dropImages marks every image of the chat for deletion as it closes.
func (m chatViewModel) dropImages() {
	if m.kitty == nil {
		return
	}
	for _, c := range m.images {
		for _, id := range c.placed {
			m.kitty.stale = append(m.kitty.stale, id)
		}
	}
}

// imageRows is the tallest an inline image may be in a pane height rows high.
func imageRows(height int) int {
	return max(1, min(imageMaxRows, height/2))
}

// imageCard draws a loaded image in a rounded card titled with its media
// label, the caption wrapped below it, or nothing when it does not fit.
func (s *styles) imageCard(msg backend.Message, c *chatImage, indent, lineWidth, maxRows int) []string {
	avail := lineWidth - indent
	if c == nil || c.img == nil || avail < 12 {
		return nil
	}
	cols, rows := halfblock.Size(c.img, avail-4, maxRows)
	if cols == 0 {
		return nil
	}
	width := min(avail, max(cols+4, 24))
	body := c.render(cols, rows)
	for _, para := range strings.Split(strings.TrimSpace(textfmt.Plain(msg.Text)), "\n") {
		if para == "" {
			continue
		}
		for _, line := range textfmt.WrapLineIndent(para, width-4, 0) {
			body = append(body, strings.TrimLeft(line, " "))
		}
	}
	title := mediaTitle(msg.MediaLabel)
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(renderBox(title, body, width, len(body)+2, s.dimColor), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return lines
}

// imageText returns the message text to print after the prefix and the
// lines drawn below it: a loaded image moves the caption into its card.
func (s *styles) imageText(msg backend.Message, c *chatImage, indent, lineWidth,
	maxRows int) (string, []string) {
	text := messageText(msg)
	if !msg.Image {
		if card := s.videoCard(msg, indent, lineWidth); card != nil {
			return "", card
		}
		return text, nil
	}
	if c == nil || c.failed {
		return text, nil
	}
	if c.img == nil {
		return text, []string{strings.Repeat(" ", indent) + s.subtle.Render("loading image…")}
	}
	card := s.imageCard(msg, c, indent, lineWidth, maxRows)
	if card == nil {
		return text, nil
	}
	return "", card
}

// videoIcon is a play button drawn in half-blocks, made from
// assets/video-icon.png (Noto Emoji, Apache 2.0) at 10x5 cells.
//
//go:embed video-icon.ans
var videoIcon string

const videoIconWidth = 10

// videoCard draws a video as a small card with a play-button icon beside its
// details and the caption below, so videos stand out while scrolling; nil
// for any other message or when it does not fit.
func (s *styles) videoCard(msg backend.Message, indent, lineWidth int) []string {
	switch mediaTag(msg.MediaLabel) {
	case "[video]", "[video note]", "[gif]":
	default:
		return nil
	}
	width := min(lineWidth-indent, 48)
	if width < videoIconWidth+16 {
		return nil
	}
	videoArt := strings.Split(strings.TrimSuffix(videoIcon, "\n"), "\n")
	label := textfmt.Plain(msg.MediaLabel)
	detail := strings.TrimSpace(label[strings.Index(label, "]")+1:])
	var details []string
	for _, part := range strings.Split(detail, ", ") {
		details = append(details, textfmt.WrapLineIndent(part, width-7-videoIconWidth, 0)...)
	}
	var body []string
	for i := range max(len(videoArt), len(details)) {
		art := strings.Repeat(" ", videoIconWidth)
		if i < len(videoArt) {
			art = videoArt[i]
		}
		line := " " + art
		if i < len(details) {
			line += "  " + s.subtle.Render(strings.TrimLeft(details[i], " "))
		}
		body = append(body, line)
	}
	for _, para := range strings.Split(strings.TrimSpace(textfmt.Plain(msg.Text)), "\n") {
		if para == "" {
			continue
		}
		for _, line := range textfmt.WrapLineIndent(para, width-4, 0) {
			body = append(body, strings.TrimLeft(line, " "))
		}
	}
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(renderBox(mediaTitle(msg.MediaLabel), body, width, len(body)+2,
		s.dimColor), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return lines
}

// mediaTitle shortens a media label to its first kind for a card title, with
// a count of any further attachments: "[photo] 800x792, 587 KB, [photo] …"
// becomes "photo +1".
func mediaTitle(label string) string {
	start := strings.Index(label, "[")
	end := strings.Index(label, "]")
	if start < 0 || end < start {
		return label
	}
	title := label[start+1 : end]
	if more := strings.Count(label, "[") - 1; more > 0 {
		title += fmt.Sprintf(" +%d", more)
	}
	return title
}

// loadVisibleImages starts downloading the images of the messages on screen
// that have not been asked for yet, so images load only as they scroll in.
func (m chatViewModel) loadVisibleImages() tea.Cmd {
	if m.messages == nil || m.privateMode || m.images == nil {
		return nil
	}
	var visible []backend.Message
	add := func(msgs []backend.Message, owners []int) {
		for _, i := range owners {
			if i >= 0 && (len(visible) == 0 || visible[len(visible)-1].ID != msgs[i].ID) {
				visible = append(visible, msgs[i])
			}
		}
	}
	width, split := m.threadSplit()
	if m.subMode == chatSubNone || split {
		owners, _ := m.chatOwners(width)
		add(m.messages, owners)
	}
	if m.subMode == chatSubThread {
		if !split {
			width = 0
		}
		owners, _ := m.threadOwners(m.width - width)
		add(m.threadMessages(m.threadRoot), owners)
	}
	var cmds []tea.Cmd
	for _, msg := range visible {
		if !msg.Image || m.images[msg.ID] != nil {
			continue
		}
		filename, ok := m.b.MediaSaveable(msg)
		if !ok {
			continue
		}
		m.images[msg.ID] = &chatImage{lines: make(map[[2]int][]string), kitty: m.kitty != nil,
			placed: make(map[[2]int]uint32), want: make(map[[2]int]bool)}
		dir, err := mediaDir(m.b, m.entry, msg)
		if err != nil {
			continue
		}
		ctx, b, keep, tag := m.ctx, m.b, imageKeepWidth, chatTag{m.entry.ID}
		if m.kitty != nil {
			keep = kittyKeepWidth
		}
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			path, err := fetchMedia(ctx, b, msg, dir, filename)
			if err != nil {
				return chatImageLoaded{chatTag: tag, id: msg.ID, err: err}
			}
			img, err := halfblock.Load(path, keep)
			return chatImageLoaded{chatTag: tag, id: msg.ID, img: img, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// mediaDir is the cache directory of one message's attachment.
func mediaDir(b backend.Backend, entry backend.ChatEntry, msg backend.Message) (string, error) {
	root, err := mediaCacheRoot()
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s\x00%v\x00%s", b.AccountLabel(), entry.ID, msg.ID)
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(root, hex.EncodeToString(sum[:12])), nil
}

// fetchMedia returns the cached attachment of msg, downloading it first
// unless an earlier fetch already did.
func fetchMedia(ctx context.Context, b backend.Backend, msg backend.Message, dir,
	filename string) (string, error) {
	// The name comes from the sender, so it must not reach outside dir.
	if !filepath.IsLocal(filename) || filepath.Base(filename) != filename {
		return "", fmt.Errorf("attachment has an unsafe file name")
	}
	if err := ensureMediaCache(filepath.Dir(dir)); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(dir, now, now) // keeps it from being pruned
		return path, nil
	}
	// Download beside the final directory and rename it into place, so an
	// interrupted download is never mistaken for a cached one.
	part := dir + ".part"
	if err := os.RemoveAll(part); err != nil {
		return "", fmt.Errorf("clear partial download: %w", err)
	}
	if err := os.Mkdir(part, 0o700); err != nil {
		return "", fmt.Errorf("create download directory: %w", err)
	}
	if err := b.SaveMedia(ctx, msg, part); err != nil {
		_ = os.RemoveAll(part)
		return "", err
	}
	if err := os.Rename(part, dir); err != nil {
		// A concurrent fetch of the same message may have won the rename.
		_ = os.RemoveAll(part)
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil
		}
		return "", fmt.Errorf("store download: %w", err)
	}
	return path, nil
}

// mediaCacheRoot is the private directory that holds downloaded attachments.
func mediaCacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache directory: %w", err)
	}
	return filepath.Join(base, "wiregram", "media"), nil
}

// ensureMediaCache creates the cache directory, refusing one another user
// could have planted.
func ensureMediaCache(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create media cache: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check media cache: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("media cache %s is not a private directory", dir)
	}
	return nil
}

// pruneMediaCache removes attachments not shown for mediaCacheMaxAge. It is
// best effort: a cache it cannot read is left alone.
func pruneMediaCache() tea.Msg {
	root, err := mediaCacheRoot()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-mediaCacheMaxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
	return nil
}
