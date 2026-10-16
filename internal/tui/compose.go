package tui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// composeField is one field of the mail composer, in Tab order.
type composeField int

const (
	fieldTo composeField = iota
	fieldCc
	fieldSubject
	fieldAttach
	fieldBody
	composeFields
)

var composeLabels = [...]string{"To", "Cc", "Subject", "Attach"}

// composeLabelWidth is the label column, as wide as the longest label.
const composeLabelWidth = 7

// composeAct is what a key asks of the composer's owner.
type composeAct int

const (
	composeNone composeAct = iota
	composeSend
	composeKeep    // close, keeping the draft
	composeDiscard // close, dropping it
)

// mailComposer writes a mail: To, Cc, Subject and Attach fields above the
// text. Address fields complete from the addresses the account knows, and
// Attach from the file system; Tab accepts a completion, else moves on.
type mailComposer struct {
	st     *styles
	title  string
	inputs [fieldBody]textinput.Model
	body   textarea.Model
	focus  composeField
	attach []string
	// forwarded describes the attachments a forward carries along.
	forwarded string
	draft     backend.MailDraft
	addresses []string
	sending   bool
	notice    string
}

func newMailComposer(st *styles, title string, d backend.MailDraft, addresses []string,
	forwarded string) *mailComposer {
	c := &mailComposer{st: st, title: title, draft: d, addresses: addresses, forwarded: forwarded}
	for i := range c.inputs {
		in := textinput.New()
		in.Prompt = ""
		in.ShowSuggestions = i != int(fieldSubject)
		c.inputs[i] = in
	}
	c.inputs[fieldTo].SetValue(d.To)
	c.inputs[fieldCc].SetValue(d.Cc)
	c.inputs[fieldSubject].SetValue(d.Subject)
	c.inputs[fieldAttach].Placeholder = "a file path, then enter"
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.SetValue(d.Body)
	ta.MoveToBegin()
	c.body = ta
	switch {
	case d.To == "":
		c.focus = fieldTo
	case d.Subject == "":
		c.focus = fieldSubject
	default:
		c.focus = fieldBody
	}
	return c
}

// focusCmd focuses the current field, blurring the others.
func (c *mailComposer) focusCmd() tea.Cmd {
	for i := range c.inputs {
		c.inputs[i].Blur()
	}
	c.body.Blur()
	if c.focus == fieldBody {
		return c.body.Focus()
	}
	return c.inputs[c.focus].Focus()
}

// value is the draft as the fields hold it now.
func (c *mailComposer) value() backend.MailDraft {
	d := c.draft
	d.To = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c.inputs[fieldTo].Value()), ","))
	d.Cc = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c.inputs[fieldCc].Value()), ","))
	d.Subject = c.inputs[fieldSubject].Value()
	d.Body = c.body.Value()
	d.Attach = c.attach
	return d
}

func (c *mailComposer) key(msg tea.KeyPressMsg) (composeAct, tea.Cmd) {
	c.notice = ""
	if c.sending {
		return composeNone, nil
	}
	switch msg.String() {
	case "ctrl+s":
		return composeSend, nil
	case "esc":
		return composeKeep, nil
	case "ctrl+x":
		return composeDiscard, nil
	case "tab":
		if c.focus != fieldBody && c.focus != fieldSubject && c.suggesting() {
			break // the field takes it as accepting the completion
		}
		c.focus = (c.focus + 1) % composeFields
		return composeNone, c.focusCmd()
	case "shift+tab":
		c.focus = (c.focus + composeFields - 1) % composeFields
		return composeNone, c.focusCmd()
	case "enter":
		switch c.focus {
		case fieldAttach:
			c.addAttachment()
			return composeNone, nil
		case fieldBody:
		default:
			c.focus++
			return composeNone, c.focusCmd()
		}
	case "backspace":
		if c.focus == fieldAttach && c.inputs[fieldAttach].Value() == "" && len(c.attach) > 0 {
			c.attach = c.attach[:len(c.attach)-1]
			return composeNone, nil
		}
	}
	var cmd tea.Cmd
	if c.focus == fieldBody {
		c.body, cmd = c.body.Update(msg)
		return composeNone, cmd
	}
	c.inputs[c.focus], cmd = c.inputs[c.focus].Update(msg)
	c.complete()
	return composeNone, cmd
}

// pass hands the focused field anything else, such as its cursor's blink.
func (c *mailComposer) pass(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if c.focus == fieldBody {
		c.body, cmd = c.body.Update(msg)
		return cmd
	}
	c.inputs[c.focus], cmd = c.inputs[c.focus].Update(msg)
	return cmd
}

func (c *mailComposer) paste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	if c.focus == fieldBody {
		c.body, cmd = c.body.Update(msg)
		return cmd
	}
	c.inputs[c.focus], cmd = c.inputs[c.focus].Update(msg)
	c.complete()
	return cmd
}

// suggesting reports whether the focused field shows a completion.
func (c *mailComposer) suggesting() bool {
	in := &c.inputs[c.focus]
	s := in.CurrentSuggestion()
	return s != "" && s != in.Value()
}

// complete offers the focused field its completions: known addresses for
// the word being typed in To or Cc, or the names in the folder typed so
// far in Attach.
func (c *mailComposer) complete() {
	in := &c.inputs[c.focus]
	value := in.Value()
	var out []string
	switch c.focus {
	case fieldTo, fieldCc:
		head, word := "", strings.TrimLeft(value, " ")
		if i := strings.LastIndex(value, ","); i >= 0 {
			head, word = value[:i+1]+" ", strings.TrimLeft(value[i+1:], " ")
		}
		if word == "" {
			break
		}
		for _, a := range c.addresses {
			if strings.HasPrefix(a, strings.ToLower(word)) && len(out) < 20 {
				out = append(out, head+a)
			}
		}
	case fieldAttach:
		line := []rune(value)
		candidates, _ := backend.PathCompleter{}.Do(line, len(line))
		for _, s := range candidates {
			out = append(out, value+string(s))
		}
	}
	in.SetSuggestions(out)
}

func (c *mailComposer) addAttachment() {
	path, err := uploadPath(c.inputs[fieldAttach].Value())
	if err != nil {
		c.notice = textfmt.CleanError(err)
		return
	}
	c.attach = append(c.attach, path)
	c.inputs[fieldAttach].SetValue("")
	c.inputs[fieldAttach].SetSuggestions(nil)
}

// attachLine lists the files going with the mail.
func (c *mailComposer) attachLine() string {
	var names []string
	for _, path := range c.attach {
		size := ""
		if info, err := os.Stat(path); err == nil {
			size = ", " + textfmt.FormatBytes(info.Size())
		}
		names = append(names, "[file] "+filepath.Base(path)+size)
	}
	if c.forwarded != "" {
		names = append(names, c.forwarded)
	}
	return strings.Join(names, " · ")
}

// composeModalWidth is the widest the composer modal gets.
const composeModalWidth = 110

// composeRect is the composer modal's place, centred over a body
// width×height cells with a margin around it.
func composeRect(width, height int) (x, y, w, h int) {
	w, h = min(composeModalWidth, max(20, width-4)), max(8, height-2)
	x, y = modalRect(width, height, w, h)
	return x, y, w, h
}

// view draws the composer in a box width by height.
func (c *mailComposer) view(width, height int) string {
	inner := width - 4
	room := inner - modalMark - composeLabelWidth - 2
	field := func(f composeField) string {
		label := composeLabels[f]
		style := c.st.subtle
		if c.focus == f {
			style = c.st.brand
		}
		c.inputs[f].SetWidth(max(1, room))
		return c.st.modalRow("", style.Render(label)+
			strings.Repeat(" ", composeLabelWidth+2-len(label))+c.inputs[f].View(),
			c.focus == f, inner)
	}
	lines := []string{field(fieldTo), field(fieldCc), field(fieldSubject), field(fieldAttach)}
	if files := c.attachLine(); files != "" {
		lines = append(lines, strings.Repeat(" ", modalMark+composeLabelWidth+2)+
			c.st.dim.Render(textfmt.ClipLine(textfmt.Plain(files), room)))
	}
	lines = append(lines, c.st.modalRule("message", inner))
	c.body.SetWidth(max(1, inner))
	c.body.SetHeight(max(1, height-2-len(lines)-2))
	lines = append(lines, strings.Split(c.body.View(), "\n")...)
	status := ""
	switch {
	case c.sending:
		status = c.st.subtle.Render("sending…")
	case c.notice != "":
		status = styleError.Render(textfmt.ClipLine(c.notice, inner/2))
	}
	keys := []helpKey{{"^S", "send"}, {"tab", "next"}, {"esc", "keep draft"}, {"^X", "discard"}}
	lines = append(lines, c.st.modalKeys(keys, status, inner)...)
	return c.st.modalBox(strings.ToLower(c.title), lines, width, height)
}

func (c *mailComposer) helpKeys() []helpKey {
	keys := []helpKey{{"^S", "send"}, {"tab", "next field"}}
	if c.focus == fieldAttach {
		keys = append(keys, helpKey{"enter", "attach"}, helpKey{"⌫", "remove last"})
	}
	return append(keys, helpKey{"esc", "keep draft"}, helpKey{"^X", "discard"})
}
