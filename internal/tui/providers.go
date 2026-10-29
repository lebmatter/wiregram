package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/ai/local"
	"wiregram/internal/textfmt"
)

// settingsNotice is a line for the footer after work done outside Update.
type settingsNotice string

// Fields of an opened provider in Settings' AI pane.
const (
	provHeader = iota - 1
	provName
	provURL
	provEnv
	provModel
	provDelete
)

// providerRow is one row of Settings' AI pane: a task and the provider that
// does it, a provider, one of the fields of the opened provider, Add
// provider… (p nil, no task or model), or a local model to download.
type providerRow struct {
	task  string
	p     *agent.AgentProvider
	field int
	model *local.Model
}

// localTick redraws the AI pane while a model downloads.
type localTick struct{}

func localTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return localTick{} })
}

// downloading reports whether any local model is downloading.
func (m settingsModel) downloading() bool {
	if m.mgr.Local == nil {
		return false
	}
	for _, lm := range local.Catalog {
		if m.mgr.Local.Progress(lm).Running {
			return true
		}
	}
	return false
}

// aiTasks are the AI pane's task rows and their labels.
var aiTasks = []struct{ task, label string }{
	{account.TaskSummarize, "Summarise with"},
	{account.TaskCatchup, "Catch up with"},
	{account.TaskRewrite, "Rewrite with"},
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
	for _, t := range aiTasks {
		rows = append(rows, providerRow{task: t.task})
	}
	for _, p := range m.providers() {
		rows = append(rows, providerRow{p: &p, field: provHeader})
		if p.ID == m.openProvider {
			for f := provName; f <= provDelete; f++ {
				// A local provider has no server to reach.
				if p.Kind == agent.ProviderLocal && (f == provURL || f == provEnv) {
					continue
				}
				rows = append(rows, providerRow{p: &p, field: f})
			}
		}
	}
	rows = append(rows, providerRow{})
	if m.mgr.Local != nil {
		for _, lm := range local.Catalog {
			rows = append(rows, providerRow{model: &lm})
		}
	}
	return rows
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
	case r.task != "":
		return m.nextTaskProvider(r.task), nil
	case r.model != nil:
		return m.applyLocalModel(*r.model)
	case r.p != nil && r.p.Kind == agent.ProviderLocal && r.field == provModel:
		return m.nextLocalModel(*r.p), nil
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

// applyLocalModel downloads lm, stops its download, or asks before removing
// it. The first download adds a Local provider for the downloaded models.
func (m settingsModel) applyLocalModel(lm local.Model) (tea.Model, tea.Cmd) {
	models := m.mgr.Local
	switch {
	case models.Installed(lm):
		m.confirm = newConfirmModal(m.st, "Remove model", "Remove "+lm.Name+" ("+
			textfmt.FormatSize(lm.Size)+")? It can be downloaded again.", "Remove", true)
		m.confirmRun = func() tea.Cmd {
			return func() tea.Msg {
				if err := models.Remove(lm); err != nil {
					return settingsNotice(textfmt.CleanError(err))
				}
				return settingsNotice("Removed " + lm.Name)
			}
		}
		return m, nil
	case models.Progress(lm).Running:
		models.CancelDownload(lm)
		m.notice = "Stopped downloading " + lm.Name + "; Enter resumes it"
		return m, nil
	}
	if err := m.ensureLocalProvider(lm); err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	running := m.downloading()
	models.Download(m.ctx, lm)
	m.notice = "Downloading " + lm.Name + " from Hugging Face"
	if running {
		return m, nil // a tick is already going
	}
	return m, localTickCmd()
}

// ensureLocalProvider adds the Local provider, answering with lm, unless
// there is one.
func (m settingsModel) ensureLocalProvider(lm local.Model) error {
	for _, p := range m.providers() {
		if p.Kind == agent.ProviderLocal {
			return nil
		}
	}
	_, err := m.mgr.Agents.Db.CreateProvider(agent.AgentProvider{Name: "Local",
		Kind: agent.ProviderLocal, Model: lm.ID})
	return err
}

// nextLocalModel moves local provider p to the next downloaded model.
func (m settingsModel) nextLocalModel(p agent.AgentProvider) settingsModel {
	var have []local.Model
	for _, lm := range local.Catalog {
		if m.mgr.Local != nil && m.mgr.Local.Installed(lm) {
			have = append(have, lm)
		}
	}
	if len(have) == 0 {
		m.notice = "Download a model under local models first"
		return m
	}
	cur := p.Model
	p.Model = have[0].ID
	for i, lm := range have {
		if lm.ID == cur {
			p.Model = have[(i+1)%len(have)].ID
		}
	}
	if err := m.mgr.Agents.Db.UpdateProvider(p); err != nil {
		m.notice = textfmt.CleanError(err)
		return m
	}
	m.mgr.Agents.Reload()
	return m
}

// nextTaskProvider gives task to the provider after the one doing it.
func (m settingsModel) nextTaskProvider(task string) settingsModel {
	all := m.providers()
	if len(all) == 0 {
		m.notice = "Add a provider first"
		return m
	}
	next := all[0]
	if cur, err := m.mgr.AI.Provider(task); err == nil {
		for i, p := range all {
			if p.ID == cur.ID {
				next = all[(i+1)%len(all)]
			}
		}
	}
	value := strconv.FormatInt(next.ID, 10)
	if err := m.mgr.Store.SetSetting(account.TaskSetting(task), value); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	return m
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
	labelW := textfmt.DisplayWidth("Summarise with")
	for _, r := range rows {
		switch {
		case r.p != nil && r.field == provHeader:
			labelW = max(labelW, textfmt.DisplayWidth(r.p.Name))
		case r.model != nil:
			labelW = max(labelW, textfmt.DisplayWidth(r.model.Name))
		}
	}
	var lines []string
	for i := window(m.sel, height); i < len(rows) && len(lines) < height; i++ {
		r := rows[i]
		selected := i == m.sel
		if r.model != nil && (i == 0 || rows[i-1].model == nil) {
			lines = append(lines, "", m.st.modalRule("local models", width))
			if len(lines) >= height {
				break
			}
		}
		var label, value string
		switch {
		case r.model != nil:
			label, value = r.model.Name, m.localModelValue(*r.model)
		case r.task != "":
			for _, t := range aiTasks {
				if t.task == r.task {
					label = t.label
				}
			}
			value = m.st.subtle.Render("no provider")
			if p, err := m.mgr.AI.Provider(r.task); err == nil {
				value = p.Name + " " + m.st.subtle.Render(providerModelName(p))
			}
		case r.p == nil:
			label = m.st.subtle.Render("+ Add provider…")
		case r.field == provHeader:
			marker := "▸ "
			if r.p.ID == m.openProvider {
				marker = "▾ "
			}
			label, value = marker+r.p.Name, m.st.subtle.Render(providerModelName(*r.p))
		case r.field == provDelete:
			label = styleError.Render("  Delete provider…")
		default:
			label, value = labels[r.field], providerField(*r.p, r.field)
			if r.field == provModel {
				value = providerModelName(*r.p)
			}
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

// providerModelName is p's model as shown: a local model by its name.
func providerModelName(p agent.AgentProvider) string {
	if lm, ok := local.Find(p.Model); ok && p.Kind == agent.ProviderLocal {
		return lm.Name
	}
	return p.Model
}

// localModelValue is a local model's row value: its download size, context,
// license and whether it is downloaded, downloading or failed to.
func (m settingsModel) localModelValue(lm local.Model) string {
	info := m.st.subtle.Render(fmt.Sprintf("%s · %dk context · %s ·", textfmt.FormatSize(lm.Size),
		lm.Context/1024, lm.License))
	pr := m.mgr.Local.Progress(lm)
	switch {
	case m.mgr.Local.Installed(lm):
		return info + " " + m.st.accent.Render("downloaded")
	case pr.Running:
		return info + " " + fmt.Sprintf("%d%% of %s", pr.Done*100/max(1, pr.Total),
			textfmt.FormatSize(pr.Total))
	case pr.Err != nil:
		return info + " " + styleError.Render(textfmt.CleanError(pr.Err))
	}
	return info + " " + m.st.accent.Render("get")
}
