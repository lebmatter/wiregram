package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
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
}

type pendingUpload struct {
	path    string
	size    int64
	started time.Time
}

func uploadKindLabel(kind uploadKind) string {
	switch kind {
	case uploadPhoto:
		return "photo"
	case uploadVideo:
		return "video"
	case uploadAudio:
		return "audio"
	case uploadGIF:
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
		case "enter":
			return m.pickUpload(u.input.Value())
		}
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
	kind, mime := classifyUpload(path)
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
	heading := styleTitle.Render("Upload") + styleSubtle.Render(" to "+plain(m.entry.Title))
	var lines []string
	switch u.step {
	case uploadPick:
		lines = []string{heading, styleDim.Render(truncate(u.picker.CurrentDirectory, width)), ""}
		lines = append(lines, strings.Split(u.picker.View(), "\n")...)
	case uploadTyping:
		lines = []string{heading, ""}
		lines = append(lines, composerBox("Path", nil, "file › ", u.input, width, colorQuote)...)
	case uploadCaption:
		card := []string{
			styleTitle.Render(filepath.Base(u.path)),
			styleSubtle.Render(u.kind + " · " + u.mime + " · " + formatSize(u.size)),
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
		return []helpKey{{"⏎", "continue"}, {"esc", "back to browser"}}
	case uploadCaption:
		return []helpKey{{"⏎", "send"}, {"esc", "pick another file"}}
	}
	return []helpKey{{"↑↓", "browse"}, {"→", "open folder"}, {"←", "up"}, {"⏎", "select"},
		{"/", "type a path"}, {"esc", "cancel"}}
}
