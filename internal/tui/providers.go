package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/agent"
	"wiregram/internal/textfmt"
)

// settingsNotice is a line for the footer after work done outside Update.
type settingsNotice string

// Fields of an opened provider in Settings' AI providers pane.
const (
	provHeader = iota - 1
	provName
	provURL
	provEnv
	provModel
	provDelete
)

// providerRow is one row of the AI providers pane: a provider, one of the
// fields of the opened provider, or Add provider… (p nil).
type providerRow struct {
	p     *agent.AgentProvider
	field int
}

func (m settingsModel) providers() []agent.AgentProvider {
	if m.mgr.Agents == nil {
		return nil
	}
	list, err := m.mgr.Agents.Db.ListProviders()
	if err != nil {
		return nil
	}
	return list
}

func (m settingsModel) providerRows() []providerRow {
	if m.mgr.Agents == nil {
		return nil
	}
	var rows []providerRow
	for _, p := range m.providers() {
		rows = append(rows, providerRow{p: &p, field: provHeader})
		if p.ID == m.openProvider {
			for f := provName; f <= provDelete; f++ {
				rows = append(rows, providerRow{p: &p, field: f})
			}
		}
	}
	return append(rows, providerRow{})
}

func (m settingsModel) providersKey(k string) (tea.Model, tea.Cmd) {
	rows := m.providerRows()
	switch k {
	case "esc", "ctrl+c", "tab", "left", "h":
		m.focus = paneAccounts
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(len(rows)-1, m.sel+1)
	case "enter", "space", " ", "right", "l":
		if m.sel < len(rows) {
			return m.applyProvider(rows[m.sel])
		}
	}
	return m, nil
}

// applyProvider opens or closes a provider, starts typing one of its fields,
// asks before deleting it, or adds a provider through the wizard.
func (m settingsModel) applyProvider(r providerRow) (tea.Model, tea.Cmd) {
	switch {
	case r.p == nil:
		wiz := newWizard(m.st, "add AI provider", providerSteps(), nil)
		m.wiz = &wiz
		return m, wiz.Init()
	case r.field == provHeader:
		if r.p.ID == m.openProvider {
			m.openProvider = 0
		} else {
			m.openProvider = r.p.ID
			for i, row := range m.providerRows() {
				if row.p != nil && row.p.ID == r.p.ID && row.field == provHeader {
					m.sel = i // opening one closes another above it
				}
			}
		}
		return m, nil
	case r.field == provDelete:
		return m.askDeleteProvider(*r.p), nil
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 200
	in.SetValue(providerField(*r.p, r.field))
	m.edit, m.editing = in, true
	return m, m.edit.Focus()
}

func (m settingsModel) askDeleteProvider(p agent.AgentProvider) settingsModel {
	db := m.mgr.Agents.Db
	agents, err := db.ListAgents()
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m
	}
	for _, a := range agents {
		if a.ProviderID == p.ID {
			m.notice = "Bot " + textfmt.Plain(a.Name) + " uses " + p.Name +
				"; pick another provider for it first"
			return m
		}
	}
	m.confirm = newConfirmModal(m.st, "Delete provider", "Delete provider "+p.Name+"?",
		"Delete", true)
	m.confirmRun = func() tea.Cmd {
		return func() tea.Msg {
			if err := db.DeleteProvider(p.ID); err != nil {
				return settingsNotice(textfmt.CleanError(err))
			}
			return settingsNotice("Deleted provider " + p.Name)
		}
	}
	return m
}

func providerField(p agent.AgentProvider, field int) string {
	switch field {
	case provName:
		return p.Name
	case provURL:
		return p.BaseURL
	case provEnv:
		return p.APIKeyEnv
	case provModel:
		return p.Model
	}
	return ""
}

// saveProviderField stores the edited field of the selected row's provider.
func (m settingsModel) saveProviderField(v string) (tea.Model, tea.Cmd) {
	rows := m.providerRows()
	if m.sel >= len(rows) || rows[m.sel].p == nil {
		return m, nil
	}
	p, field := *rows[m.sel].p, rows[m.sel].field
	var err error
	switch field {
	case provName:
		p.Name = v
	case provURL:
		v = strings.TrimRight(v, "/")
		err = agent.ValidateProviderURL(v)
		p.BaseURL = v
	case provEnv:
		err = validEnvName(v)
		p.APIKeyEnv = v
	case provModel:
		p.Model = v
	}
	if err == nil && v == "" && field != provEnv {
		m.notice = "That field needs a value"
		return m, nil
	}
	if err == nil {
		err = m.mgr.Agents.Db.UpdateProvider(p)
	}
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m.mgr.Agents.Reload()
	m.notice = "Saved provider " + p.Name
	return m, nil
}

// providerAdded saves the provider the wizard described.
func (m settingsModel) providerAdded(a wizardAnswers) (tea.Model, tea.Cmd) {
	switch a["confirm"] {
	case "restart":
		wiz := m.wiz.restart()
		m.wiz = &wiz
		return m, wiz.Init()
	case "cancel":
		m.wiz = nil
		return m, nil
	}
	m.wiz = nil
	p := providerFromAnswers(a)
	id, err := m.mgr.Agents.Db.CreateProvider(p)
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m.openProvider, m.notice = id, "Added provider "+p.Name
	for i, r := range m.providerRows() {
		if r.p != nil && r.p.ID == id && r.field == provHeader {
			m.sel = i
		}
	}
	return m, nil
}

func (m settingsModel) providerLines(width, height int) []string {
	if m.mgr.Agents == nil {
		return []string{m.st.dim.Render("Bots are unavailable: their tables could not be opened.")}
	}
	rows := m.providerRows()
	focused := m.focus == paneItems
	labels := [...]string{"  Name", "  URL", "  Key from", "  Model"}
	labelW := textfmt.DisplayWidth("  Key from")
	for _, r := range rows {
		if r.p != nil && r.field == provHeader {
			labelW = max(labelW, textfmt.DisplayWidth(r.p.Name))
		}
	}
	var lines []string
	for i := window(m.sel, height); i < len(rows) && len(lines) < height; i++ {
		r := rows[i]
		selected := i == m.sel
		var label, value string
		switch {
		case r.p == nil:
			label = m.st.subtle.Render("+ Add provider…")
		case r.field == provHeader:
			marker := "▸ "
			if r.p.ID == m.openProvider {
				marker = "▾ "
			}
			label, value = marker+r.p.Name, m.st.subtle.Render(r.p.Model)
		case r.field == provDelete:
			label = styleError.Render("  Delete provider…")
		default:
			label, value = labels[r.field], providerField(*r.p, r.field)
			if r.field == provEnv && value == "" {
				value = "no key"
			} else if r.field == provEnv {
				value = "$" + value
			}
			value = m.st.subtle.Render(value)
			if selected && m.editing {
				in := m.edit
				in.SetWidth(max(8, width-labelW-5))
				value = in.View()
			}
		}
		lines = append(lines, m.st.formRow(label, value, labelW, selected, focused, width))
	}
	return lines
}
