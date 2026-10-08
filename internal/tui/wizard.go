package tui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The wizard is a scripted conversation: wiregram asks one question per
// bubble and the only replies accepted are the current step's typed value or
// fixed options. There is no free chat.

type wizardKind int

const (
	wizText wizardKind = iota
	wizChoice
	wizMulti
)

type wizardOption struct {
	label   string
	value   string
	blocked string // why the option can't be picked; "" when it can
}

// wizardAnswers maps step keys to values. Multi-select values are joined by
// commas.
type wizardAnswers map[string]string

type wizardStep struct {
	key         string
	kind        wizardKind
	ask         func(wizardAnswers) string
	options     func(wizardAnswers) []wizardOption
	placeholder string
	def         func(wizardAnswers) string // text prefilled when there is no answer yet
	optional    bool
	validate    func(string, wizardAnswers) error
	skip        func(wizardAnswers) bool
	// show turns a stored answer into the reply bubble's text.
	show func(string, wizardAnswers) string
}

type wizardModel struct {
	title   string
	accent  color.Color
	steps   []wizardStep
	initial wizardAnswers
	answers wizardAnswers
	done    []int // answered steps, oldest first, for Esc to walk back
	cur     int
	input   textinput.Model
	cursor  int
	picked  map[string]bool
	err     string
}

// wizardFinished carries the answers once the last step is answered.
type wizardFinished struct{ answers wizardAnswers }
type wizardCancelled struct{}

func say(text string) func(wizardAnswers) string {
	return func(wizardAnswers) string { return text }
}

func newWizard(
	title string, accent color.Color, steps []wizardStep, initial wizardAnswers,
) wizardModel {
	m := wizardModel{title: title, accent: accent, steps: steps, initial: initial}
	return m.restart()
}

func (m wizardModel) restart() wizardModel {
	m.answers = wizardAnswers{}
	for k, v := range m.initial {
		m.answers[k] = v
	}
	m.done = nil
	return m.enter(m.next(-1))
}

// next finds the first step after i that applies to the current answers.
func (m wizardModel) next(i int) int {
	for i++; i < len(m.steps); i++ {
		if s := m.steps[i]; s.skip == nil || !s.skip(m.answers) {
			return i
		}
	}
	return i
}

// enter prepares step i, starting from its stored answer when there is one.
func (m wizardModel) enter(i int) wizardModel {
	m.cur, m.err = i, ""
	if i >= len(m.steps) {
		return m
	}
	step := m.steps[i]
	value, answered := m.answers[step.key]
	if !answered && step.def != nil {
		value = step.def(m.answers)
	}
	switch step.kind {
	case wizText:
		m.input = textinput.New()
		m.input.Placeholder = step.placeholder
		m.input.Prompt = ""
		m.input.SetValue(value)
		m.input.CursorEnd()
		m.input.Focus()
	case wizChoice:
		m.cursor = 0
		for j, o := range step.options(m.answers) {
			if o.value == value {
				m.cursor = j
			}
		}
	case wizMulti:
		m.cursor = 0
		m.picked = make(map[string]bool)
		for _, v := range strings.Split(value, ",") {
			if v != "" {
				m.picked[v] = true
			}
		}
	}
	return m
}

func (m wizardModel) Init() tea.Cmd { return textinput.Blink }

func (m wizardModel) Update(msg tea.Msg) (wizardModel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok || m.cur >= len(m.steps) {
		var cmd tea.Cmd
		if m.cur < len(m.steps) && m.steps[m.cur].kind == wizText {
			m.input, cmd = m.input.Update(msg)
		}
		return m, cmd
	}
	step := m.steps[m.cur]
	switch key.String() {
	case "ctrl+c":
		return m, func() tea.Msg { return wizardCancelled{} }
	case "esc":
		if len(m.done) == 0 {
			return m, func() tea.Msg { return wizardCancelled{} }
		}
		prev := m.done[len(m.done)-1]
		m.done = m.done[:len(m.done)-1]
		return m.enter(prev), nil
	case "enter":
		return m.submit()
	}
	if step.kind == wizText {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.err = ""
		return m, cmd
	}
	options := step.options(m.answers)
	switch key.String() {
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(options)-1, m.cursor+1)
	case "space":
		if step.kind == wizMulti && m.cursor < len(options) {
			o := options[m.cursor]
			if o.blocked != "" {
				m.err = o.blocked
				return m, nil
			}
			m.picked[o.value] = !m.picked[o.value]
			m.err = ""
		}
	}
	return m, nil
}

// click picks the option at row y of a wizard drawn height rows tall, y
// counted from its top edge: a choice is answered, a multi-select toggled.
func (m wizardModel) click(y, height int) (wizardModel, tea.Cmd) {
	if m.cur >= len(m.steps) || m.steps[m.cur].kind == wizText {
		return m, nil
	}
	options := m.steps[m.cur].options(m.answers)
	below := 0 // the error line under the options
	if m.err != "" {
		below = 1
	}
	// The transcript is bottom-anchored, so the options end above the error.
	j := y - 1 - (height - 2 - len(options) - below)
	if y < 1 || j < 0 || j >= len(options) {
		return m, nil
	}
	m.cursor = j
	if m.steps[m.cur].kind == wizMulti {
		return m.Update(keyPress(tea.KeySpace))
	}
	return m.submit()
}

func (m wizardModel) submit() (wizardModel, tea.Cmd) {
	step := m.steps[m.cur]
	var value string
	switch step.kind {
	case wizText:
		value = strings.TrimSpace(m.input.Value())
		if value == "" && !step.optional {
			m.err = "This one needs an answer."
			return m, nil
		}
	case wizChoice:
		options := step.options(m.answers)
		if m.cursor >= len(options) {
			return m, nil
		}
		if o := options[m.cursor]; o.blocked != "" {
			m.err = o.blocked
			return m, nil
		}
		value = options[m.cursor].value
	case wizMulti:
		var vals []string
		for _, o := range step.options(m.answers) {
			if m.picked[o.value] {
				vals = append(vals, o.value)
			}
		}
		value = strings.Join(vals, ",")
		if value == "" && !step.optional {
			m.err = "Pick at least one with Space."
			return m, nil
		}
	}
	if step.validate != nil {
		if err := step.validate(value, m.answers); err != nil {
			m.err = err.Error()
			return m, nil
		}
	}
	m.answers[step.key] = value
	m.done = append(m.done, m.cur)
	m = m.enter(m.next(m.cur))
	if m.cur >= len(m.steps) {
		answers := m.answers
		return m, func() tea.Msg { return wizardFinished{answers} }
	}
	return m, nil
}

func (m wizardModel) helpKeys() []helpKey {
	if m.cur >= len(m.steps) {
		return nil
	}
	keys := []helpKey{{"⏎", "answer"}}
	switch m.steps[m.cur].kind {
	case wizChoice:
		keys = []helpKey{{"↑↓", "choose"}, {"⏎", "pick"}}
	case wizMulti:
		keys = []helpKey{{"↑↓", "choose"}, {"space", "toggle"}, {"⏎", "done"}}
	}
	if len(m.done) > 0 {
		return append(keys, helpKey{"esc", "back"}, helpKey{"ctrl+c", "cancel"})
	}
	return append(keys, helpKey{"esc", "cancel"})
}

// view renders the transcript as bubbles, newest at the bottom.
func (m wizardModel) view(width, height int) string {
	inner := width - 4
	bubbleWidth := wizardBubbleWidth(inner)
	accent := lipgloss.NewStyle().Foreground(m.accent)
	var lines []string
	for _, i := range m.done {
		step := m.steps[i]
		reply := m.answers[step.key]
		if step.show != nil {
			reply = step.show(reply, m.answers)
		}
		if reply == "" {
			reply = "(skip)"
		}
		lines = wizardAsk(lines, step.ask(m.answers), bubbleWidth, m.accent)
		lines = wizardReply(lines, reply, inner, bubbleWidth)
	}
	if m.cur < len(m.steps) {
		step := m.steps[m.cur]
		lines = wizardAsk(lines, step.ask(m.answers), bubbleWidth, m.accent)
		switch step.kind {
		case wizText:
			m.input.SetWidth(max(10, inner-4))
			lines = append(lines, accent.Render("› ")+m.input.View())
		default:
			for j, o := range step.options(m.answers) {
				marker := "  "
				if j == m.cursor {
					marker = accent.Render("> ")
				}
				box := ""
				if step.kind == wizMulti {
					box = "[ ] "
					if m.picked[o.value] {
						box = accent.Render("[x] ")
					}
				}
				row := marker + box + o.label
				if o.blocked != "" {
					row = marker + box + styleDim.Render(o.label+" — "+o.blocked)
				} else if j == m.cursor {
					row = marker + box + styleTitle.Render(o.label)
				}
				lines = append(lines, row)
			}
		}
		if m.err != "" {
			lines = append(lines, styleError.Render(m.err))
		}
	}
	return renderBox(m.title, bottomLines(lines, height-2), width, height, m.accent)
}

func wizardBubbleWidth(inner int) int { return min(64, max(20, inner*3/4)) }

// wizardAsk appends wiregram's question bubble to lines.
func wizardAsk(lines []string, text string, width int, accent color.Color) []string {
	lines = append(lines, lipgloss.NewStyle().Foreground(accent).Bold(true).Render("wiregram"))
	return append(lines, strings.Split(wizardBubble(text, width, accent), "\n")...)
}

// wizardReply appends the user's right-aligned answer bubble and a gap to lines.
func wizardReply(lines []string, text string, inner, width int) []string {
	lines = append(lines, lipgloss.PlaceHorizontal(inner, lipgloss.Right, styleSubtle.Render("you")))
	bubble := lipgloss.PlaceHorizontal(inner, lipgloss.Right, wizardBubble(text, width, colorDim))
	return append(append(lines, strings.Split(bubble, "\n")...), "")
}

func wizardBubble(text string, width int, border color.Color) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(min(width, lipgloss.Width(text)+4)).
		Render(text)
}
