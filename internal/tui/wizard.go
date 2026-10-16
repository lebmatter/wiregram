package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/textfmt"
)

// The wizard is a scripted form: one question at a time, and the only replies
// accepted are the current step's typed value or fixed options. There is no
// free chat.

type wizardKind int

const (
	wizText wizardKind = iota
	wizChoice
)

type wizardOption struct {
	label string
	value string
}

// wizardAnswers maps step keys to values.
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
	// show turns a stored answer into the text the form shows.
	show func(string, wizardAnswers) string
	// label names an answered step in the form view.
	label  string
	masked bool // the form view hides the answer
}

type wizardModel struct {
	st      *styles // its accent colours the wizard
	title   string
	steps   []wizardStep
	initial wizardAnswers
	answers wizardAnswers
	done    []int // answered steps, oldest first, for Esc to walk back
	cur     int
	input   textinput.Model
	cursor  int
	err     string
}

// wizardFinished carries the answers once the last step is answered.
type wizardFinished struct{ answers wizardAnswers }
type wizardCancelled struct{}

func say(text string) func(wizardAnswers) string {
	return func(wizardAnswers) string { return text }
}

func newWizard(
	st *styles, title string, steps []wizardStep, initial wizardAnswers,
) wizardModel {
	m := wizardModel{st: st, title: title, steps: steps, initial: initial}
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
	}
	return m, nil
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
		value = options[m.cursor].value
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
	keys := []helpKey{{"enter", "answer"}}
	if m.steps[m.cur].kind == wizChoice {
		keys = []helpKey{{"↑↓", "choose"}, {"enter", "pick"}}
	}
	if len(m.done) > 0 {
		return append(keys, helpKey{"esc", "back"}, helpKey{"ctrl+c", "cancel"})
	}
	return append(keys, helpKey{"esc", "cancel"})
}

// formView renders the wizard as a form, top-anchored: answered steps as
// label and value rows, then the current question and its input or options.
// first is the line of the first option.
func (m wizardModel) formView(width int) (lines []string, first int) {
	labelW := 0
	for _, i := range m.done {
		labelW = max(labelW, textfmt.DisplayWidth(m.steps[i].label))
	}
	for _, i := range m.done {
		step := m.steps[i]
		value := m.answers[step.key]
		switch {
		case value == "":
			value = "(skip)"
		case step.masked:
			value = "••••••"
		case step.show != nil:
			value = step.show(value, m.answers)
		}
		pad := strings.Repeat(" ", labelW+2-textfmt.DisplayWidth(step.label))
		lines = append(lines, m.st.modalRow(m.st.dim.Render("✓"),
			textfmt.ClipLine(step.label+pad+m.st.subtle.Render(value), width-modalMark),
			false, width))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	if m.cur >= len(m.steps) {
		return lines, len(lines)
	}
	step := m.steps[m.cur]
	for _, l := range strings.Split(lipgloss.NewStyle().Width(width).Render(step.ask(m.answers)), "\n") {
		lines = append(lines, styleTitle.Render(l))
	}
	lines = append(lines, "")
	first = len(lines)
	if step.kind == wizText {
		m.input.SetWidth(max(10, width-6))
		lines = append(lines, m.st.modalField("›", m.input.View(), width)...)
	} else {
		for j, o := range step.options(m.answers) {
			label := o.label
			if j == m.cursor {
				label = m.st.brand.Render(label)
			}
			lines = append(lines, m.st.modalRow("", label, j == m.cursor, width))
		}
	}
	if m.err != "" {
		lines = append(lines, styleError.Render(m.err))
	}
	return lines, first
}

// formClick picks the option at line y of the form view, as click does for
// the bubbles.
func (m wizardModel) formClick(y, width int) (wizardModel, tea.Cmd) {
	if m.cur >= len(m.steps) || m.steps[m.cur].kind == wizText {
		return m, nil
	}
	_, first := m.formView(width)
	j := y - first
	if j < 0 || j >= len(m.steps[m.cur].options(m.answers)) {
		return m, nil
	}
	m.cursor = j
	return m.submit()
}
