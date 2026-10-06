package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type uploadStep int

const (
	uploadPick uploadStep = iota
	uploadTyping
	uploadCaption
)

// uploadPanel is the Ctrl+U flow: browse or type a path, preview, caption.
type uploadPanel struct {
	step    uploadStep
	picker  filepicker.Model
	input   textinput.Model // the typed path, then the caption
	path    string
	size    int64
	kind    string
	mime    string
	lastDir string
	// Tab completion: candidate names under the path, the text they follow,
	// and the highlighted one (↑↓ to move, Tab or Enter to accept).
	matches     []string
	matchPrefix string
	matchIdx    int
}

type pendingUpload struct {
	path    string
	size    int64
	started time.Time
}

func uploadKindLabel(kind backend.UploadKind) string {
	switch kind {
	case backend.UploadPhoto:
		return "photo"
	case backend.UploadVideo:
		return "video"
	case backend.UploadAudio:
		return "audio"
	case backend.UploadGIF:
		return "GIF"
	}
	return "file"
}

func (m chatViewModel) startUpload() (chatViewModel, tea.Cmd) {
	dir := m.upload.lastDir
	if dir == "" {
		dir = "."
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		}
	}
	p := filepicker.New()
	p.CurrentDirectory = dir
	p.ShowPermissions = false
	p.AutoHeight = false
	// Esc belongs to the panel (back/cancel), not to directory navigation.
	p.KeyMap.Back = key.NewBinding(key.WithKeys("h", "backspace", "left"))
	p.Styles.Cursor = styleAccent
	p.Styles.Selected = styleAccent.Bold(true)
	p.Styles.Directory = styleQuoteBar
	p.Styles.EmptyDirectory = styleDim.SetString("  This folder is empty")
	p.SetHeight(max(3, m.height-5))
	m.upload = uploadPanel{step: uploadPick, picker: p, lastDir: dir}
	m.subMode = chatSubUpload
	return m, p.Init()
}

func (m chatViewModel) handleUploadKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	u := &m.upload
	var cmd tea.Cmd
	switch u.step {
	case uploadPick:
		switch msg.String() {
		case "esc", "ctrl+c":
			m.subMode = chatSubNone
			return m, nil
		case "/", "~":
			start := u.picker.CurrentDirectory + string(filepath.Separator)
			if msg.String() == "~" {
				start = "~/"
			}
			u.input = textinput.New()
			u.input.Prompt = ""
			u.input.SetValue(start)
			u.input.CursorEnd()
			u.step = uploadTyping
			return m, u.input.Focus()
		}
		u.picker, cmd = u.picker.Update(msg)
		u.lastDir = u.picker.CurrentDirectory
		if ok, path := u.picker.DidSelectFile(msg); ok {
			return m.pickUpload(path)
		}
		return m, cmd
	case uploadTyping:
		switch msg.String() {
		case "esc", "ctrl+c":
			u.step = uploadPick
			return m, nil
		case "up", "down":
			if n := len(u.matches); n > 0 {
				step := 1
				if msg.String() == "up" {
					step = n - 1
				}
				u.matchIdx = (u.matchIdx + step) % n
			}
			return m, nil
		case "enter":
			if len(u.matches) == 0 {
				return m.pickUpload(u.input.Value())
			}
			if !u.acceptMatch() {
				return m.pickUpload(u.input.Value())
			}
			u.completePath()
			return m, nil
		case "tab":
			if len(u.matches) > 0 {
				u.acceptMatch()
			}
			u.completePath()
			return m, nil
		}
		u.matches = nil
		u.input, cmd = u.input.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		u.step = uploadPick
		return m, nil
	case "enter":
		caption, path, size := u.input.Value(), u.path, u.size
		replyTo := ""
		if m.replying != nil {
			replyTo = m.replying.ID
			m.replying = nil
		}
		m.subMode = chatSubNone
		m.uploads = append(m.uploads, pendingUpload{path, size, time.Now()})
		return m, tea.Batch(
			m.sendMessage(chatOutgoing{text: caption, path: path, replyTo: replyTo}),
			m.spin.Tick)
	}
	u.input, cmd = u.input.Update(msg)
	return m, cmd
}

// completePath extends the typed path by the longest common prefix of the
// matching entries and lists them when more than one remains.
// acceptMatch puts the highlighted candidate into the path. It reports
// whether the candidate is a folder, so completion can carry on inside it.
func (u *uploadPanel) acceptMatch() bool {
	match := u.matches[u.matchIdx]
	u.input.SetValue(u.matchPrefix + match)
	u.input.CursorEnd()
	u.matches = nil
	return strings.HasSuffix(match, string(filepath.Separator))
}

func (u *uploadPanel) completePath() {
	value := u.input.Value()
	line := []rune(value)
	candidates, _ := backend.PathCompleter{}.Do(line, len(line))
	u.matches, u.matchIdx = nil, 0
	if len(candidates) == 0 {
		return
	}
	common := string(candidates[0])
	for _, c := range candidates[1:] {
		s := string(c)
		for !strings.HasPrefix(s, common) {
			_, size := utf8.DecodeLastRuneInString(common)
			common = common[:len(common)-size]
		}
	}
	if common != "" {
		u.input.SetValue(value + common)
		u.input.CursorEnd()
	}
	if len(candidates) > 1 {
		typedBase := filepath.Base(backend.ExpandHome(value))
		if strings.HasSuffix(value, string(filepath.Separator)) || value == "" {
			typedBase = ""
		}
		u.matchPrefix = value[:len(value)-len(typedBase)]
		for _, c := range candidates {
			u.matches = append(u.matches, typedBase+string(c))
		}
	}
}

// pickUpload validates the chosen file and moves on to the preview and caption.
func (m chatViewModel) pickUpload(value string) (chatViewModel, tea.Cmd) {
	path, err := uploadPath(value)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	kind, mime := backend.ClassifyUpload(path)
	u := &m.upload
	u.path, u.size, u.kind, u.mime = path, info.Size(), uploadKindLabel(kind), mime
	u.lastDir = filepath.Dir(path)
	u.step = uploadCaption
	u.input = textinput.New()
	u.input.Prompt = ""
	u.input.Placeholder = "Add a caption (optional)"
	return m, u.input.Focus()
}

func (m chatViewModel) uploadBody(width, height int) []string {
	u := m.upload
	heading := styleTitle.Render("Upload") + styleSubtle.Render(" to "+textfmt.Plain(m.entry.Title))
	var lines []string
	switch u.step {
	case uploadPick:
		lines = []string{heading, styleDim.Render(truncate(u.picker.CurrentDirectory, width)), ""}
		lines = append(lines, strings.Split(u.picker.View(), "\n")...)
	case uploadTyping:
		lines = []string{heading, ""}
		lines = append(lines, composerBox("Path", nil, "file › ", u.input, width, colorQuote)...)
		limit := max(1, height-len(lines))
		start := max(0, u.matchIdx-limit+1)
		for i := start; i < min(len(u.matches), start+limit); i++ {
			if i == u.matchIdx {
				lines = append(lines, styleSelectedRow.Render(styleAccent.Render("> ")+u.matches[i]))
			} else {
				lines = append(lines, "  "+styleSubtle.Render(u.matches[i]))
			}
		}
	case uploadCaption:
		card := []string{
			styleTitle.Render(filepath.Base(u.path)),
			styleSubtle.Render(u.kind + " · " + u.mime + " · " + textfmt.FormatSize(u.size)),
			styleDim.Render(truncate(filepath.Dir(u.path), width-4)),
		}
		lines = []string{heading, ""}
		lines = append(lines, strings.Split(renderBox("Ready to send", card, width, len(card)+2, colorQuote), "\n")...)
		lines = append(lines, "")
		lines = append(lines, composerBox("Caption", m.replyBar(), "› ", u.input, width, colorAccent)...)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

func (m chatViewModel) uploadHelp() []helpKey {
	switch m.upload.step {
	case uploadTyping:
		if len(m.upload.matches) > 0 {
			return []helpKey{{"↑↓", "choose"}, {"tab/⏎", "accept"}, {"esc", "back to browser"}}
		}
		return []helpKey{{"tab", "complete"}, {"⏎", "continue"}, {"esc", "back to browser"}}
	case uploadCaption:
		return []helpKey{{"⏎", "send"}, {"esc", "pick another file"}}
	}
	return []helpKey{{"↑↓", "browse"}, {"→", "open folder"}, {"←", "up"}, {"⏎", "select"},
		{"/", "type a path"}, {"esc", "cancel"}}
}

func uploadPath(value string) (string, error) {
	path := backend.ExpandHome(strings.TrimSpace(value))
	if path == "" {
		return "", fmt.Errorf("enter a file path or press Esc to cancel")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve upload path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("open upload file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upload path must be a regular file")
	}
	return path, nil
}
