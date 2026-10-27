package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type uploadStep int

const (
	uploadTyping uploadStep = iota
	uploadCaption
)

// uploadPanel is the Ctrl+U flow: type a path over the entries it matches,
// then a caption.
type uploadPanel struct {
	step    uploadStep
	prev    chatSubMode     // the chat or its thread, which Esc returns to
	thread  bool            // the file is a reply in the open thread
	input   textinput.Model // the typed path, then the caption
	path    string
	size    int64
	kind    string
	mime    string
	lastDir string
	// The entries under the typed path, the text they follow and the
	// highlighted one (↑↓ to move, Tab or Enter to accept).
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

// startUpload opens the upload modal, for the open thread when thread is set.
func (m chatViewModel) startUpload(thread bool) (chatViewModel, tea.Cmd) {
	sep := string(filepath.Separator)
	dir := m.upload.lastDir
	home, err := os.UserHomeDir()
	if dir == "" {
		dir = "."
		if err == nil {
			dir = home
		}
	}
	start := strings.TrimSuffix(dir, sep) + sep
	if err == nil && strings.HasPrefix(start, home+sep) {
		start = "~" + sep + strings.TrimPrefix(start, home+sep)
	}
	m.upload = uploadPanel{prev: m.subMode, thread: thread, lastDir: dir}
	return m.typeUploadPath(start)
}

// typeUploadPath puts the path input up with value in it.
func (m chatViewModel) typeUploadPath(value string) (chatViewModel, tea.Cmd) {
	u := &m.upload
	u.step = uploadTyping
	u.input = textinput.New()
	u.input.Prompt = ""
	u.input.SetValue(value)
	u.input.CursorEnd()
	u.listMatches()
	m.subMode = chatSubUpload
	return m, u.input.Focus()
}

func (m chatViewModel) handleUploadKey(msg tea.KeyPressMsg) (chatViewModel, tea.Cmd) {
	u := &m.upload
	var cmd tea.Cmd
	if u.step == uploadTyping {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.subMode = u.prev
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
		case "tab":
			if len(u.matches) > 0 {
				u.acceptMatch()
				u.listMatches()
			}
			return m, nil
		case "enter":
			if len(u.matches) == 0 || !u.acceptMatch() {
				return m.pickUpload(u.input.Value())
			}
			u.listMatches()
			return m, nil
		}
		u.input, cmd = u.input.Update(msg)
		u.listMatches()
		return m, cmd
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		return m.typeUploadPath(u.matchPrefix)
	case "enter":
		caption, path, size := u.input.Value(), u.path, u.size
		replyTo := ""
		if u.thread {
			replyTo = m.threadRoot
		} else if m.replying != nil {
			replyTo = m.replying.ID
			m.replying = nil
		}
		m.subMode = u.prev
		m.uploads = append(m.uploads, pendingUpload{path, size, time.Now()})
		return m, tea.Batch(
			m.sendMessage(chatOutgoing{text: caption, path: path, replyTo: replyTo}),
			m.spin.Tick)
	}
	u.input, cmd = u.input.Update(msg)
	return m, cmd
}

// acceptMatch puts the highlighted entry into the path. It reports whether
// the entry is a folder, so the list can carry on inside it.
func (u *uploadPanel) acceptMatch() bool {
	match := u.matches[u.matchIdx]
	u.input.SetValue(u.matchPrefix + match)
	u.input.CursorEnd()
	return strings.HasSuffix(match, string(filepath.Separator))
}

// listMatches lists the entries the typed path could go on to: the folder's
// when it ends in a separator, else those starting with its last part.
func (u *uploadPanel) listMatches() {
	value := u.input.Value()
	u.matches, u.matchIdx = nil, 0
	if value == "~" {
		u.matchPrefix, u.matches = "", []string{"~" + string(filepath.Separator)}
		return
	}
	line := []rune(value)
	candidates, _ := backend.PathCompleter{}.Do(line, len(line))
	typedBase := filepath.Base(backend.ExpandHome(value))
	if value == "" || strings.HasSuffix(value, string(filepath.Separator)) ||
		!strings.HasSuffix(value, typedBase) {
		typedBase = ""
	}
	u.matchPrefix = value[:len(value)-len(typedBase)]
	for _, c := range candidates {
		u.matches = append(u.matches, typedBase+string(c))
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
	// Esc from the caption goes back to the folder as it was typed.
	typed := u.input.Value()
	u.matchPrefix = typed[:strings.LastIndex(typed, string(filepath.Separator))+1]
	u.step = uploadCaption
	u.input = textinput.New()
	u.input.Prompt = ""
	u.input.Placeholder = "Add a caption (optional)"
	return m, u.input.Focus()
}

// uploadView is the upload modal: the typed path over the entries it
// matches, or the chosen file over its caption.
func (m chatViewModel) uploadView() string {
	w, h := chatModalSize(m.width, m.height)
	inner := w - 4
	u := m.upload
	input := u.input
	var body []string
	switch u.step {
	case uploadTyping:
		input.SetWidth(max(1, inner-9))
		body = m.st.modalField("file", input.View(), inner)
		body = append(body, m.st.modalRule(fmt.Sprintf("%d matches", len(u.matches)), inner))
		rows := chatModalRows(h)
		first := max(0, u.matchIdx-rows+1)
		for i := first; i < min(len(u.matches), first+rows); i++ {
			body = append(body, m.st.modalRow("", truncate(u.matches[i], inner-modalMark-1),
				i == u.matchIdx, inner))
		}
		if len(u.matches) == 0 {
			body = append(body, m.st.dim.Render("  nothing here"))
		}
	case uploadCaption:
		input.SetWidth(max(1, inner-6))
		body = []string{
			" " + styleTitle.Render(truncate(filepath.Base(u.path), inner-2)),
			" " + m.st.subtle.Render(truncate(u.kind+" · "+u.mime+" · "+
				textfmt.FormatSize(u.size), inner-2)),
			" " + m.st.dim.Render(truncate(filepath.Dir(u.path), inner-2)),
			"",
			m.st.modalRule("caption", inner),
		}
		if m.replying != nil {
			body = append(body, " "+m.st.subtle.Render(truncate("↪ replying to "+
				textfmt.Plain(backend.GenericMessageSender(*m.replying)), inner-2)))
		}
		body = append(body, m.st.modalField("›", input.View(), inner)...)
	}
	body = fillLines(body, h-4)[:h-4]
	body = append(body, m.st.modalKeys(m.uploadHelp(), "", inner)...)
	return m.st.modalBox("upload to "+chatName(m.entry), body, w, h)
}

// uploadClick opens the folder or picks the file at row y of the upload
// modal, as Enter would.
func (m chatViewModel) uploadClick(y int) (chatViewModel, tea.Cmd) {
	u := &m.upload
	if u.step != uploadTyping {
		return m, nil
	}
	_, h := chatModalSize(m.width, m.height)
	rows := chatModalRows(h)
	first := max(0, u.matchIdx-rows+1)
	i := first + y - chatModalTop
	if y < chatModalTop || i >= min(len(u.matches), first+rows) {
		return m, nil
	}
	u.matchIdx = i
	return m.handleUploadKey(keyPress(tea.KeyEnter))
}

func (m chatViewModel) uploadHelp() []helpKey {
	if m.upload.step == uploadCaption {
		return []helpKey{{"enter", "send"}, {"esc", "back"}}
	}
	if len(m.upload.matches) == 0 {
		return []helpKey{{"enter", "pick"}, {"esc", "close"}}
	}
	return []helpKey{{"↑↓", "choose"}, {"tab", "complete"}, {"enter", "open · pick"},
		{"esc", "close"}}
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
