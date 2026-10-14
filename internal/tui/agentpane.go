package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// The agent pane is an account's Chat Agent settings, opened from the
// sidebar's agent row in place of a chat. Like Settings, each row is chosen
// with the arrows and changed with Enter.

type agentField int

const (
	fieldStatus agentField = iota
	fieldName
	fieldProvider
	fieldModel
	fieldPrompt
	fieldKnowledge
	fieldMedia
	fieldReplies
	fieldRetention
	fieldChats
	fieldChat // one row per private chat while Chats is Selected
	fieldDelete
)

type agentRow struct {
	field agentField
	chat  agent.AgentChat
}

type agentPaneModel struct {
	st        *styles
	mgr       *account.AccountManager
	acct      *account.Account
	rec       agent.AgentRecord
	knowledge []string
	providers []agent.AgentProvider
	dms       []agent.AgentChat // the account's private chats, picked ones first
	picked    map[string]bool
	sel       int
	edit      textinput.Model
	editing   bool
	confirm   *confirmModal
	notice    string
	width     int
	height    int
}

type agentPaneClosed struct{}

// agentPaneHeader is how many lines sit above the rows: the status line and a gap.
const agentPaneHeader = 2

func newAgentPane(st *styles, mgr *account.AccountManager, acct *account.Account,
	entries []backend.ChatEntry) (agentPaneModel, error) {
	m := agentPaneModel{st: st, mgr: mgr, acct: acct, picked: make(map[string]bool)}
	rec, ok := mgr.Agents.AgentFor(acct)
	if !ok {
		return m, fmt.Errorf("this account has no agent")
	}
	db := mgr.Agents.Db
	var err error
	if m.knowledge, err = db.AgentKnowledge(rec.ID); err != nil {
		return m, err
	}
	if m.providers, err = db.ListProviders(); err != nil {
		return m, err
	}
	chosen, err := db.AgentChats(rec.ID)
	if err != nil {
		return m, err
	}
	m.rec = rec
	listed := make(map[string]bool)
	for _, c := range chosen {
		m.picked[c.ID] = true
		listed[c.ID] = true
		m.dms = append(m.dms, c)
	}
	for _, e := range entries {
		if e.IsDM && !e.IsSelf && !listed[e.ID.Id] {
			m.dms = append(m.dms, agent.AgentChat{ID: e.ID.Id, Title: textfmt.Plain(e.Title)})
		}
	}
	return m, nil
}

func (m agentPaneModel) resized(width, height int) agentPaneModel {
	m.width, m.height = width, height
	return m
}

func (m agentPaneModel) rows() []agentRow {
	var rows []agentRow
	for f := fieldStatus; f <= fieldChats; f++ {
		rows = append(rows, agentRow{field: f})
	}
	if !m.rec.AllChats {
		for _, c := range m.dms {
			rows = append(rows, agentRow{field: fieldChat, chat: c})
		}
	}
	return append(rows, agentRow{field: fieldDelete})
}

func (m agentPaneModel) Update(msg tea.Msg) (agentPaneModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.editing {
			var cmd tea.Cmd
			m.edit, cmd = m.edit.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m agentPaneModel) key(msg tea.KeyPressMsg) (agentPaneModel, tea.Cmd) {
	k := msg.String()
	if m.confirm != nil {
		yes, done := m.confirm.key(k)
		if !done {
			return m, nil
		}
		m.confirm = nil
		if yes {
			return m.deleteAgent()
		}
		return m, nil
	}
	if m.editing {
		switch k {
		case "esc":
			m.editing = false
		case "enter":
			m.editing = false
			return m.saveText(strings.TrimSpace(textfmt.Plain(m.edit.Value()))), nil
		default:
			var cmd tea.Cmd
			m.edit, cmd = m.edit.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	m.notice = ""
	rows := m.rows()
	switch k {
	case "esc", "left", "h":
		return m, func() tea.Msg { return agentPaneClosed{} }
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(len(rows)-1, m.sel+1)
	case "enter", "space", " ", "right", "l":
		if m.sel < len(rows) {
			return m.apply(rows[m.sel])
		}
	}
	return m, nil
}

// apply changes the row's setting: a switch flips, a choice moves to the
// next option, text opens for typing.
func (m agentPaneModel) apply(r agentRow) (agentPaneModel, tea.Cmd) {
	switch r.field {
	case fieldStatus:
		m.rec.Enabled = !m.rec.Enabled
	case fieldProvider:
		if len(m.providers) == 0 {
			m.notice = "Add an AI provider in Settings first"
			return m, nil
		}
		old, next := m.provider(), m.providers[0]
		for i, p := range m.providers {
			if p.ID == m.rec.ProviderID {
				next = m.providers[(i+1)%len(m.providers)]
			}
		}
		m.rec.ProviderID = next.ID
		if m.rec.Model == old.Model {
			m.rec.Model = next.Model // follow the default unless it was changed
		}
	case fieldMedia:
		m.rec.Media = nextOption(mediaOptions, m.rec.Media)
	case fieldReplies:
		m.rec.Auto = !m.rec.Auto
	case fieldRetention:
		days, _ := strconv.Atoi(nextOption(retentionOptions, strconv.Itoa(m.rec.RetentionDays)))
		m.rec.RetentionDays = days
	case fieldChats:
		m.rec.AllChats = !m.rec.AllChats
		if !m.rec.AllChats && len(m.picked) == 0 {
			m.notice = "Pick the chats it answers below"
		}
	case fieldChat:
		m.picked[r.chat.ID] = !m.picked[r.chat.ID]
		var chats []agent.AgentChat
		for _, c := range m.dms {
			if m.picked[c.ID] {
				chats = append(chats, c)
			}
		}
		if err := m.mgr.Agents.Db.SetAgentChats(m.rec.ID, chats); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.mgr.Agents.Reload()
		return m, nil
	case fieldDelete:
		m.confirm = newConfirmModal(m.st, "Delete agent",
			"Delete agent "+textfmt.Plain(m.rec.Name)+" and its history?", "Delete", true)
		return m, nil
	default:
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 2000
		in.SetValue(m.text(r.field))
		in.CursorEnd()
		m.edit, m.editing = in, true
		return m, m.edit.Focus()
	}
	return m.save(), nil
}

func nextOption(opts []wizardOption, v string) string {
	for i, o := range opts {
		if o.value == v {
			return opts[(i+1)%len(opts)].value
		}
	}
	return opts[0].value
}

func optionLabel(opts []wizardOption, v string) string {
	for _, o := range opts {
		if o.value == v {
			return o.label
		}
	}
	return v
}

func (m agentPaneModel) text(f agentField) string {
	switch f {
	case fieldName:
		return m.rec.Name
	case fieldModel:
		return m.rec.Model
	case fieldPrompt:
		return m.rec.Prompt
	case fieldKnowledge:
		return strings.Join(m.knowledge, ", ")
	}
	return ""
}

func (m agentPaneModel) saveText(v string) agentPaneModel {
	rows := m.rows()
	if m.sel >= len(rows) {
		return m
	}
	switch rows[m.sel].field {
	case fieldName, fieldModel:
		if v == "" {
			m.notice = "That field needs a value"
			return m
		}
		if rows[m.sel].field == fieldName {
			m.rec.Name = v
		} else {
			m.rec.Model = v
		}
	case fieldPrompt:
		m.rec.Prompt = v
	case fieldKnowledge:
		if err := checkKnowledge(m.mgr.Dir, v); err != nil {
			m.notice = err.Error()
			return m
		}
		if err := m.mgr.Agents.Db.SetAgentKnowledge(m.rec.ID, splitList(v)); err != nil {
			m.notice = textfmt.CleanError(err)
			return m
		}
		m.knowledge = splitList(v)
		m.mgr.Agents.Reload()
		m.notice = "Saved"
		return m
	}
	return m.save()
}

func (m agentPaneModel) save() agentPaneModel {
	if err := m.mgr.Agents.Db.UpdateAgent(m.rec); err != nil {
		m.notice = textfmt.CleanError(err)
		return m
	}
	m.mgr.Agents.Reload()
	m.notice = "Saved"
	return m
}

func (m agentPaneModel) deleteAgent() (agentPaneModel, tea.Cmd) {
	if err := m.mgr.Agents.Db.DeleteAgent(m.rec.ID); err != nil {
		m.notice = textfmt.CleanError(err)
		return m, nil
	}
	m.mgr.Agents.Reload()
	return m, func() tea.Msg { return agentPaneClosed{} }
}

func (m agentPaneModel) provider() agent.AgentProvider {
	for _, p := range m.providers {
		if p.ID == m.rec.ProviderID {
			return p
		}
	}
	return agent.AgentProvider{Name: "missing"}
}

func (m agentPaneModel) label(r agentRow) (string, string) {
	on := func(b bool, yes, no string) string {
		if b {
			return yes
		}
		return no
	}
	switch r.field {
	case fieldStatus:
		return "Status", on(m.rec.Enabled, "on", "paused")
	case fieldName:
		return "Name", m.rec.Name
	case fieldProvider:
		return "Provider", m.provider().Name
	case fieldModel:
		return "Model", m.rec.Model
	case fieldPrompt:
		return "Prompt", on(m.rec.Prompt == "", "brief and helpful (default)", m.rec.Prompt)
	case fieldKnowledge:
		return "Knowledge", on(len(m.knowledge) == 0, "none", strings.Join(m.knowledge, ", "))
	case fieldMedia:
		return "Understands", optionLabel(mediaOptions, m.rec.Media)
	case fieldReplies:
		return "Replies", on(m.rec.Auto, "automatically", "ask me first")
	case fieldRetention:
		return "History", fmt.Sprintf("%d days", m.rec.RetentionDays)
	case fieldChats:
		if m.rec.AllChats {
			return "Chats", "all private chats"
		}
		return "Chats", fmt.Sprintf("selected (%d)", len(m.picked))
	case fieldChat:
		return "  " + on(m.picked[r.chat.ID], "[x] ", "[ ] ") + r.chat.Title, ""
	}
	return styleError.Render("Delete agent…"), ""
}

func (m agentPaneModel) view(focused bool) string {
	inner, height := m.width-4, m.height-2
	status := "Answers incoming private messages; never groups or channels."
	if problem := m.mgr.Agents.Problem(m.rec.ID); problem != "" {
		status = styleError.Render(textfmt.ClipLine("Last reply failed: "+problem, inner))
	} else {
		status = m.st.subtle.Render(textfmt.ClipLine(status, inner))
	}
	lines := []string{status, ""}
	rows := m.rows()
	labelW := 0
	for _, r := range rows {
		if r.field != fieldChat {
			l, _ := m.label(r)
			labelW = max(labelW, textfmt.DisplayWidth(l))
		}
	}
	for i := window(m.sel, height-agentPaneHeader); i < len(rows) && len(lines) < height; i++ {
		label, value := m.label(rows[i])
		value = m.st.subtle.Render(textfmt.Plain(value))
		if i == m.sel && m.editing {
			in := m.edit
			in.SetWidth(max(8, inner-labelW-5))
			value = in.View()
		}
		lines = append(lines, m.st.formRow(label, value, labelW, i == m.sel, focused, inner))
	}
	return renderBox(agentIcon+" "+m.rec.Name, lines, m.width, m.height, m.st.border(focused))
}

// rowAt maps a row y of the pane, counted from its top edge, to a row index.
func (m agentPaneModel) rowAt(y int) (int, bool) {
	i := window(m.sel, m.height-2-agentPaneHeader) + y - 1 - agentPaneHeader
	return i, y-1 >= agentPaneHeader && i >= 0 && i < len(m.rows())
}

func (m agentPaneModel) helpKeys() []helpKey {
	switch {
	case m.confirm != nil:
		return m.confirm.helpKeys()
	case m.editing:
		return []helpKey{{"enter", "save"}, {"esc", "cancel"}}
	}
	return []helpKey{{"↑↓", "select"}, {"enter", "change"}, {"esc", "back"}}
}
