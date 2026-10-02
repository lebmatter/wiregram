package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// Agent UI modes.
type agentUIMode int

const (
	agentModeList agentUIMode = iota
	agentModeEditor
	agentModeThreads
	agentModeThread
	agentModeProviders
	agentModeProviderEditor
	agentModeChatPicker
)

// Root model for the agent UI.
type agentUIModel struct {
	ctx    context.Context
	mgr    *accountManager
	mode   agentUIMode
	width  int
	height int
	quit   bool
	notice string

	list           agentListModel
	editor         agentEditorModel
	threads        agentThreadsModel
	thread         agentThreadModel
	providers      providerListModel
	providerEditor providerEditorModel
	chatPicker     chatPickerModel
}

func tuiAgentUI(ctx context.Context, mgr *accountManager) {
	w, h := termSize()
	m := agentUIModel{
		ctx:    ctx,
		mgr:    mgr,
		width:  w,
		height: h,
	}
	m.list = newAgentListModel(mgr)
	m.list.width, m.list.height = w, h
	p := tea.NewProgram(m, tea.WithContext(ctx))
	p.Run()
}

func (m agentUIModel) Init() tea.Cmd {
	return m.list.Init()
}

func (m agentUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	}

	switch m.mode {
	case agentModeList:
		return m.updateList(msg)
	case agentModeEditor:
		return m.updateEditor(msg)
	case agentModeThreads:
		return m.updateThreads(msg)
	case agentModeThread:
		return m.updateThread(msg)
	case agentModeProviders:
		return m.updateProviders(msg)
	case agentModeProviderEditor:
		return m.updateProviderEditor(msg)
	case agentModeChatPicker:
		return m.updateChatPicker(msg)
	}
	return m, nil
}

func (m agentUIModel) View() tea.View {
	var content string
	switch m.mode {
	case agentModeEditor:
		content = m.editor.View()
	case agentModeThreads:
		content = m.threads.View()
	case agentModeThread:
		content = m.thread.View()
	case agentModeProviders:
		content = m.providers.View()
	case agentModeProviderEditor:
		content = m.providerEditor.View()
	case agentModeChatPicker:
		content = m.chatPicker.View()
	default:
		content = m.list.View()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// Message types for mode transitions.
type agentListOpenThreads struct{ agent agentRecord }
type agentListNewAgent struct{}
type agentListEditAgent struct{ agent agentRecord }
type agentListDeleteAgent struct{ id int64 }
type agentListOpenProviders struct{}
type agentListBack struct{}
type agentEditorSaved struct{}
type agentEditorCancelled struct{}
type agentEditorPickChats struct{}
type agentThreadsBack struct{}
type agentThreadsOpenThread struct {
	agent  agentRecord
	thread agentThread
}
type agentThreadBack struct{}
type providerListBack struct{}
type providerListNew struct{}
type providerListEdit struct{ provider agentProvider }
type providerListDelete struct{ id int64 }
type providerEditorSaved struct{}
type providerEditorCancelled struct{}
type chatPickerDone struct{ selected []agentChatAssignment }
type chatPickerCancelled struct{}

// Update dispatchers.

func (m agentUIModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case agentListBack:
		m.quit = true
		return m, tea.Quit
	case agentListOpenProviders:
		m.providers = newProviderListModel(m.mgr)
		m.providers.width, m.providers.height = m.width, m.height
		m.mode = agentModeProviders
		return m, nil
	case agentListNewAgent:
		m.editor = newAgentEditorModel(m.mgr, nil)
		m.editor.width, m.editor.height = m.width, m.height
		m.mode = agentModeEditor
		return m, m.editor.Init()
	case agentListEditAgent:
		msg := msg.(agentListEditAgent)
		m.editor = newAgentEditorModel(m.mgr, &msg.agent)
		m.editor.width, m.editor.height = m.width, m.height
		m.mode = agentModeEditor
		return m, m.editor.Init()
	case agentListOpenThreads:
		msg := msg.(agentListOpenThreads)
		m.threads = newAgentThreadsModel(m.mgr, msg.agent)
		m.threads.width, m.threads.height = m.width, m.height
		m.mode = agentModeThreads
		return m, nil
	case agentListDeleteAgent:
		msg := msg.(agentListDeleteAgent)
		m.mgr.agents.db.deleteAgent(msg.id)
		m.mgr.agents.reload()
		m.list = newAgentListModel(m.mgr)
		m.list.width, m.list.height = m.width, m.height
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case agentEditorSaved:
		m.mgr.agents.reload()
		m.list = newAgentListModel(m.mgr)
		m.list.width, m.list.height = m.width, m.height
		m.mode = agentModeList
		return m, nil
	case agentEditorCancelled:
		m.mode = agentModeList
		return m, nil
	case agentEditorPickChats:
		m.chatPicker = newChatPickerModel(m.ctx, m.mgr, m.editor.chatAssignments)
		m.chatPicker.width, m.chatPicker.height = m.width, m.height
		m.mode = agentModeChatPicker
		return m, m.chatPicker.Init()
	}

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateThreads(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case agentThreadsBack:
		m.list = newAgentListModel(m.mgr)
		m.list.width, m.list.height = m.width, m.height
		m.mode = agentModeList
		return m, nil
	case agentThreadsOpenThread:
		m.thread = newAgentThreadModel(m.mgr, msg.agent, msg.thread)
		m.thread.width, m.thread.height = m.width, m.height
		m.mode = agentModeThread
		return m, nil
	}

	var cmd tea.Cmd
	m.threads, cmd = m.threads.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateThread(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case agentThreadBack:
		m.threads = newAgentThreadsModel(m.mgr, m.thread.agent)
		m.threads.width, m.threads.height = m.width, m.height
		m.mode = agentModeThreads
		return m, nil
	}

	var cmd tea.Cmd
	m.thread, cmd = m.thread.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateProviders(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case providerListBack:
		m.list = newAgentListModel(m.mgr)
		m.list.width, m.list.height = m.width, m.height
		m.mode = agentModeList
		return m, nil
	case providerListNew:
		m.providerEditor = newProviderEditorModel(m.mgr, nil)
		m.providerEditor.width, m.providerEditor.height = m.width, m.height
		m.mode = agentModeProviderEditor
		return m, m.providerEditor.Init()
	case providerListEdit:
		msg := msg.(providerListEdit)
		m.providerEditor = newProviderEditorModel(m.mgr, &msg.provider)
		m.providerEditor.width, m.providerEditor.height = m.width, m.height
		m.mode = agentModeProviderEditor
		return m, m.providerEditor.Init()
	case providerListDelete:
		msg := msg.(providerListDelete)
		m.mgr.agents.db.deleteProvider(msg.id)
		m.providers = newProviderListModel(m.mgr)
		m.providers.width, m.providers.height = m.width, m.height
		return m, nil
	}

	var cmd tea.Cmd
	m.providers, cmd = m.providers.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateProviderEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case providerEditorSaved:
		m.providers = newProviderListModel(m.mgr)
		m.providers.width, m.providers.height = m.width, m.height
		m.mode = agentModeProviders
		return m, nil
	case providerEditorCancelled:
		m.mode = agentModeProviders
		return m, nil
	}

	var cmd tea.Cmd
	m.providerEditor, cmd = m.providerEditor.Update(msg)
	return m, cmd
}

func (m agentUIModel) updateChatPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chatPickerDone:
		m.editor.chatAssignments = msg.selected
		m.editor.updateChatsLabel()
		m.mode = agentModeEditor
		return m, nil
	case chatPickerCancelled:
		m.mode = agentModeEditor
		return m, nil
	}

	var cmd tea.Cmd
	m.chatPicker, cmd = m.chatPicker.Update(msg)
	return m, cmd
}

// --- Agent List Screen ---

type agentListModel struct {
	mgr      *accountManager
	agents   []agentRecord
	selected int
	confirm  *int64
	width    int
	height   int
}

func newAgentListModel(mgr *accountManager) agentListModel {
	agents, _ := mgr.agents.db.listAgents()
	return agentListModel{mgr: mgr, agents: agents}
}

func (m agentListModel) Init() tea.Cmd { return nil }

func (m agentListModel) maxIndex() int { return len(m.agents) + 1 }

func (m agentListModel) Update(msg tea.Msg) (agentListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.confirm != nil {
			switch msg.String() {
			case "y", "Y":
				id := *m.confirm
				m.confirm = nil
				return m, func() tea.Msg { return agentListDeleteAgent{id: id} }
			default:
				m.confirm = nil
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "esc":
			return m, func() tea.Msg { return agentListBack{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < m.maxIndex() {
				m.selected++
			}
		case "enter":
			if m.selected < len(m.agents) {
				return m, func() tea.Msg { return agentListOpenThreads{agent: m.agents[m.selected]} }
			}
			if m.selected == len(m.agents) {
				return m, func() tea.Msg { return agentListOpenProviders{} }
			}
			if m.selected == len(m.agents)+1 {
				return m, func() tea.Msg { return agentListBack{} }
			}
		case "n":
			return m, func() tea.Msg { return agentListNewAgent{} }
		case "e":
			if m.selected < len(m.agents) {
				a := m.agents[m.selected]
				return m, func() tea.Msg { return agentListEditAgent{agent: a} }
			}
		case "d":
			if m.selected < len(m.agents) {
				id := m.agents[m.selected].ID
				m.confirm = &id
			}
		case "s":
			return m, func() tea.Msg { return agentListOpenProviders{} }
		}
	}
	return m, nil
}

func (m agentListModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render("Agents"))
	b.WriteByte('\n')

	if m.confirm != nil {
		b.WriteString("Delete this agent? y/n\n")
		return b.String()
	}

	limit := max(1, m.height-5)
	start := max(0, m.selected-limit+1)
	end := min(len(m.agents)+2, start+limit)

	for i := start; i < end; i++ {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		switch {
		case i < len(m.agents):
			a := m.agents[i]
			mode := "suggest"
			if a.AutoReply == 1 {
				mode = "auto"
			}
			status := "off"
			if a.Enabled {
				status = "running"
			}
			chats, _ := m.mgr.agents.db.agentChats(a.ID)
			b.WriteString(clipLine(fmt.Sprintf("%s%d  %s — %s — %d chats — %s",
				marker, i+1, a.Name, mode, len(chats), status), m.width))
			b.WriteByte('\n')
		case i == len(m.agents):
			b.WriteString(fmt.Sprintf("%s    Providers…\n", marker))
		case i == len(m.agents)+1:
			b.WriteString(fmt.Sprintf("%s    Back\n", marker))
		}
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar(agentListHelp, m.width))
	return b.String()
}

var agentListHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "threads"},
	{"n", "new"},
	{"e", "edit"},
	{"d", "delete"},
	{"s", "providers"},
	{"q", "back"},
}

// --- Agent Editor Screen ---

type agentEditorField int

const (
	agentFieldName agentEditorField = iota
	agentFieldPrompt
	agentFieldMode
	agentFieldRetention
	agentFieldKnowledge
	agentFieldChats
	agentFieldCount
)

type agentEditorModel struct {
	mgr             *accountManager
	editing         *agentRecord
	field           agentEditorField
	inputs          [5]textinput.Model // name, prompt, retention, knowledge, provider
	modeValue       int                // 0=suggest, 1=auto
	providers       []agentProvider
	providerIndex   int
	chatAssignments []agentChatAssignment
	width           int
	height          int
}

func newAgentEditorModel(mgr *accountManager, existing *agentRecord) agentEditorModel {
	m := agentEditorModel{mgr: mgr, editing: existing}
	m.providers, _ = mgr.agents.db.listProviders()

	m.inputs[0] = textinput.New()
	m.inputs[0].Placeholder = "agent name"
	m.inputs[1] = textinput.New()
	m.inputs[1].Placeholder = "system prompt"
	m.inputs[2] = textinput.New()
	m.inputs[2].Placeholder = "30"
	m.inputs[3] = textinput.New()
	m.inputs[3].Placeholder = "path1.txt, path2.txt"
	m.inputs[4] = textinput.New()
	m.inputs[4].Placeholder = "select provider"

	if existing != nil {
		m.inputs[0].SetValue(existing.Name)
		m.inputs[1].SetValue(existing.Prompt)
		m.inputs[2].SetValue(strconv.Itoa(existing.RetentionDays))
		m.modeValue = existing.AutoReply

		knowledge, _ := mgr.agents.db.agentKnowledge(existing.ID)
		m.inputs[3].SetValue(strings.Join(knowledge, ", "))

		m.chatAssignments, _ = mgr.agents.db.agentChats(existing.ID)

		for i, p := range m.providers {
			if p.ID == existing.ProviderID {
				m.providerIndex = i
				break
			}
		}
	}

	m.inputs[0].Focus()
	return m
}

func (m agentEditorModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m agentEditorModel) Update(msg tea.Msg) (agentEditorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return agentEditorCancelled{} }
		case "tab":
			m.inputs[m.field].Blur()
			m.field = (m.field + 1) % agentFieldCount
			if m.field < 5 && m.field != agentFieldMode && m.field != agentFieldChats {
				m.inputs[m.fieldInputIndex()].Focus()
			}
			return m, nil
		case "shift+tab":
			m.inputs[m.field].Blur()
			m.field = (m.field - 1 + agentFieldCount) % agentFieldCount
			if m.field < 5 && m.field != agentFieldMode && m.field != agentFieldChats {
				m.inputs[m.fieldInputIndex()].Focus()
			}
			return m, nil
		case "enter":
			if m.field == agentFieldChats {
				return m, func() tea.Msg { return agentEditorPickChats{} }
			}
			return m, func() tea.Msg { return m.save() }
		case "left", "right":
			if m.field == agentFieldMode {
				m.modeValue = 1 - m.modeValue
				return m, nil
			}
			if m.field == agentFieldName {
				// Provider cycling with arrow keys when on name is confusing; skip
			}
		case " ":
			if m.field == agentFieldMode {
				m.modeValue = 1 - m.modeValue
				return m, nil
			}
		}
	}

	idx := m.fieldInputIndex()
	if idx >= 0 {
		var cmd tea.Cmd
		m.inputs[idx], cmd = m.inputs[idx].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m agentEditorModel) fieldInputIndex() int {
	switch m.field {
	case agentFieldName:
		return 0
	case agentFieldPrompt:
		return 1
	case agentFieldRetention:
		return 2
	case agentFieldKnowledge:
		return 3
	default:
		return -1
	}
}

func (m *agentEditorModel) updateChatsLabel() {}

func (m agentEditorModel) save() tea.Msg {
	name := strings.TrimSpace(m.inputs[0].Value())
	if name == "" {
		return agentEditorCancelled{}
	}
	if len(m.providers) == 0 {
		return agentEditorCancelled{}
	}

	retention, _ := strconv.Atoi(m.inputs[2].Value())
	if retention <= 0 {
		retention = 30
	}

	rec := agentRecord{
		Name:          name,
		Prompt:        m.inputs[1].Value(),
		AutoReply:     m.modeValue,
		ProviderID:    m.providers[m.providerIndex].ID,
		RetentionDays: retention,
		Enabled:       true,
	}

	db := m.mgr.agents.db
	if m.editing != nil {
		rec.ID = m.editing.ID
		rec.Enabled = m.editing.Enabled
		db.updateAgent(rec)
	} else {
		id, err := db.createAgent(rec)
		if err != nil {
			return agentEditorCancelled{}
		}
		rec.ID = id
	}

	// Knowledge paths.
	raw := strings.TrimSpace(m.inputs[3].Value())
	var paths []string
	if raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				paths = append(paths, p)
			}
		}
	}
	db.setAgentKnowledge(rec.ID, paths)

	// Chat assignments.
	db.setAgentChats(rec.ID, m.chatAssignments)

	return agentEditorSaved{}
}

func (m agentEditorModel) View() string {
	var b strings.Builder
	title := "New Agent"
	if m.editing != nil {
		title = "Edit Agent: " + m.editing.Name
	}
	b.WriteString(styleTitle.Width(m.width).Render(title))
	b.WriteString("\n\n")

	fields := []struct {
		label string
		value string
	}{
		{"Name", m.inputs[0].View()},
		{"Prompt", m.inputs[1].View()},
		{"Mode", m.modeLabel()},
		{"Retention", m.inputs[2].View() + " days"},
		{"Knowledge", m.inputs[3].View()},
		{"Chats", m.chatsLabel()},
	}

	for i, f := range fields {
		cursor := "  "
		if agentEditorField(i) == m.field {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s%-10s %s\n", cursor, f.label+":", f.value))
	}

	if len(m.providers) > 0 {
		p := m.providers[m.providerIndex]
		b.WriteString(fmt.Sprintf("\n  Provider:  %s (%s)\n", p.Name, p.Model))
		if len(m.providers) > 1 {
			b.WriteString(styleSubtle.Render("  (use ←/→ on Provider field to cycle)"))
			b.WriteByte('\n')
		}
	} else {
		b.WriteString(styleSubtle.Render("\n  No providers configured. Add one in Provider settings first."))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar(agentEditorHelp, m.width))
	return b.String()
}

func (m agentEditorModel) modeLabel() string {
	if m.modeValue == 1 {
		return "[auto]"
	}
	return "[suggest]"
}

func (m agentEditorModel) chatsLabel() string {
	n := len(m.chatAssignments)
	if n == 0 {
		return styleSubtle.Render("(none — press Enter to select)")
	}
	return fmt.Sprintf("%d selected — press Enter to change", n)
}

var agentEditorHelp = []helpKey{
	{"Tab", "next"},
	{"S-Tab", "prev"},
	{"⏎", "save"},
	{"Esc", "cancel"},
}

// --- Agent Threads Screen ---

type agentThreadsModel struct {
	mgr      *accountManager
	agent    agentRecord
	threads  []agentThread
	selected int
	width    int
	height   int
}

func newAgentThreadsModel(mgr *accountManager, agent agentRecord) agentThreadsModel {
	threads, _ := mgr.agents.db.agentThreads(agent.ID)
	return agentThreadsModel{mgr: mgr, agent: agent, threads: threads}
}

func (m agentThreadsModel) Init() tea.Cmd { return nil }

func (m agentThreadsModel) Update(msg tea.Msg) (agentThreadsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			return m, func() tea.Msg { return agentThreadsBack{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < len(m.threads)-1 {
				m.selected++
			}
		case "enter":
			if m.selected < len(m.threads) {
				t := m.threads[m.selected]
				a := m.agent
				return m, func() tea.Msg { return agentThreadsOpenThread{agent: a, thread: t} }
			}
		}
	}
	return m, nil
}

func (m agentThreadsModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render(m.agent.Name + " — Threads"))
	b.WriteByte('\n')

	if len(m.threads) == 0 {
		b.WriteString(styleSubtle.Render("  No threads yet."))
		b.WriteString("\n\n")
		b.WriteString(renderHelpBar([]helpKey{{"q", "back"}}, m.width))
		return b.String()
	}

	limit := max(1, m.height-5)
	start := max(0, m.selected-limit+1)
	end := min(len(m.threads), start+limit)

	for i := start; i < end; i++ {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		t := m.threads[i]

		// Check for pending suggestion.
		pendingLabel := ""
		m.mgr.agents.mu.Lock()
		key := agentChatKey{agentID: m.agent.ID, backendType: t.BackendType, accountID: t.AccountID, chatID: t.ChatID}
		if _, ok := m.mgr.agents.pending[key]; ok {
			pendingLabel = " [pending]"
		}
		m.mgr.agents.mu.Unlock()

		backendTag := backendShortTag(t.BackendType)
		timeStr := t.LastAt.Format("15:04")
		b.WriteString(clipLine(fmt.Sprintf("%s%s %s  last: %s%s",
			marker, backendTag, t.ChatID, timeStr, pendingLabel), m.width))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar([]helpKey{{"↑↓", "select"}, {"⏎", "open"}, {"q", "back"}}, m.width))
	return b.String()
}

// --- Agent Thread Detail Screen ---

type agentThreadModel struct {
	mgr      *accountManager
	agent    agentRecord
	thread   agentThread
	messages []agentLoggedMessage
	selected int
	width    int
	height   int
}

func newAgentThreadModel(mgr *accountManager, agent agentRecord, thread agentThread) agentThreadModel {
	msgs, _ := mgr.agents.db.chatHistory(agent.ID, thread.BackendType, thread.AccountID, thread.ChatID, 100)
	return agentThreadModel{
		mgr:      mgr,
		agent:    agent,
		thread:   thread,
		messages: msgs,
		selected: max(0, len(msgs)-1),
	}
}

func (m agentThreadModel) Init() tea.Cmd { return nil }

func (m agentThreadModel) Update(msg tea.Msg) (agentThreadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		key := agentChatKey{
			agentID:     m.agent.ID,
			backendType: m.thread.BackendType,
			accountID:   m.thread.AccountID,
			chatID:      m.thread.ChatID,
		}

		// Handle pending suggestion y/n.
		m.mgr.agents.mu.Lock()
		_, hasPending := m.mgr.agents.pending[key]
		m.mgr.agents.mu.Unlock()

		if hasPending {
			switch msg.String() {
			case "y":
				m.mgr.agents.approveSuggestion(key)
				m.messages, _ = m.mgr.agents.db.chatHistory(m.agent.ID, m.thread.BackendType, m.thread.AccountID, m.thread.ChatID, 100)
				m.selected = max(0, len(m.messages)-1)
				return m, nil
			case "n":
				m.mgr.agents.discardSuggestion(key)
				return m, nil
			}
		}

		switch msg.String() {
		case "q", "esc":
			return m, func() tea.Msg { return agentThreadBack{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < len(m.messages)-1 {
				m.selected++
			}
		}
	}
	return m, nil
}

func (m agentThreadModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render(m.agent.Name + " — " + m.thread.ChatID))
	b.WriteByte('\n')

	if len(m.messages) == 0 {
		b.WriteString(styleSubtle.Render("  No messages."))
		b.WriteByte('\n')
	} else {
		limit := max(1, m.height-8)
		start := max(0, m.selected-limit+1)
		end := min(len(m.messages), start+limit)

		for i := start; i < end; i++ {
			msg := m.messages[i]
			marker := "  "
			if i == m.selected {
				marker = "> "
			}
			sender := msg.Sender
			if msg.IsAgentReply {
				sender = "agent"
			} else if msg.Outgoing {
				sender = "you"
			}
			ts := msg.Timestamp.Local().Format("15:04")
			b.WriteString(clipLine(fmt.Sprintf("%s[%s] <%s> %s", marker, ts, sender, msg.Text), m.width))
			b.WriteByte('\n')
		}
	}

	// Pending suggestion.
	key := agentChatKey{
		agentID:     m.agent.ID,
		backendType: m.thread.BackendType,
		accountID:   m.thread.AccountID,
		chatID:      m.thread.ChatID,
	}
	m.mgr.agents.mu.Lock()
	suggestion, hasPending := m.mgr.agents.pending[key]
	m.mgr.agents.mu.Unlock()

	if hasPending {
		b.WriteByte('\n')
		b.WriteString(styleUnread.Render("[AI suggest — y send · n discard]"))
		b.WriteByte('\n')
		b.WriteString("  " + suggestion.Text)
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	help := []helpKey{{"↑↓", "scroll"}, {"q", "back"}}
	if hasPending {
		help = append([]helpKey{{"y", "approve"}, {"n", "discard"}}, help...)
	}
	b.WriteString(renderHelpBar(help, m.width))
	return b.String()
}

// --- Provider List Screen ---

type providerListModel struct {
	mgr       *accountManager
	providers []agentProvider
	selected  int
	confirm   *int64
	width     int
	height    int
}

func newProviderListModel(mgr *accountManager) providerListModel {
	providers, _ := mgr.agents.db.listProviders()
	return providerListModel{mgr: mgr, providers: providers}
}

func (m providerListModel) Init() tea.Cmd { return nil }

func (m providerListModel) Update(msg tea.Msg) (providerListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.confirm != nil {
			switch msg.String() {
			case "y", "Y":
				id := *m.confirm
				m.confirm = nil
				return m, func() tea.Msg { return providerListDelete{id: id} }
			default:
				m.confirm = nil
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "esc":
			return m, func() tea.Msg { return providerListBack{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < len(m.providers) {
				m.selected++
			}
		case "enter":
			if m.selected < len(m.providers) {
				p := m.providers[m.selected]
				return m, func() tea.Msg { return providerListEdit{provider: p} }
			}
		case "n":
			return m, func() tea.Msg { return providerListNew{} }
		case "e":
			if m.selected < len(m.providers) {
				p := m.providers[m.selected]
				return m, func() tea.Msg { return providerListEdit{provider: p} }
			}
		case "d":
			if m.selected < len(m.providers) {
				id := m.providers[m.selected].ID
				m.confirm = &id
			}
		}
	}
	return m, nil
}

func (m providerListModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render("Providers"))
	b.WriteByte('\n')

	if m.confirm != nil {
		b.WriteString("Delete this provider? y/n\n")
		return b.String()
	}

	if len(m.providers) == 0 {
		b.WriteString(styleSubtle.Render("  No providers. Press n to add one."))
		b.WriteByte('\n')
	}

	limit := max(1, m.height-5)
	start := max(0, m.selected-limit+1)
	end := min(len(m.providers)+1, start+limit)

	for i := start; i < end; i++ {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		if i < len(m.providers) {
			p := m.providers[i]
			envLabel := p.APIKeyEnv
			if envLabel == "" {
				envLabel = "(no key)"
			}
			b.WriteString(clipLine(fmt.Sprintf("%s%d  %s — %s — %s",
				marker, i+1, p.Name, p.BaseURL, envLabel), m.width))
			b.WriteByte('\n')
		} else {
			b.WriteString(fmt.Sprintf("%s    Back\n", marker))
		}
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar([]helpKey{{"↑↓", "select"}, {"n", "new"}, {"e", "edit"}, {"d", "delete"}, {"q", "back"}}, m.width))
	return b.String()
}

// --- Provider Editor Screen ---

type providerEditorModel struct {
	mgr     *accountManager
	editing *agentProvider
	inputs  [4]textinput.Model // name, base_url, api_key_env, model
	field   int
	width   int
	height  int
}

func newProviderEditorModel(mgr *accountManager, existing *agentProvider) providerEditorModel {
	m := providerEditorModel{mgr: mgr, editing: existing}

	m.inputs[0] = textinput.New()
	m.inputs[0].Placeholder = "OpenRouter"
	m.inputs[1] = textinput.New()
	m.inputs[1].Placeholder = "https://openrouter.ai/api/v1"
	m.inputs[2] = textinput.New()
	m.inputs[2].Placeholder = "OPENROUTER_API_KEY"
	m.inputs[3] = textinput.New()
	m.inputs[3].Placeholder = "model-id"

	if existing != nil {
		m.inputs[0].SetValue(existing.Name)
		m.inputs[1].SetValue(existing.BaseURL)
		m.inputs[2].SetValue(existing.APIKeyEnv)
		m.inputs[3].SetValue(existing.Model)
	}

	m.inputs[0].Focus()
	return m
}

func (m providerEditorModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m providerEditorModel) Update(msg tea.Msg) (providerEditorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return providerEditorCancelled{} }
		case "tab":
			m.inputs[m.field].Blur()
			m.field = (m.field + 1) % 4
			m.inputs[m.field].Focus()
			return m, nil
		case "shift+tab":
			m.inputs[m.field].Blur()
			m.field = (m.field + 3) % 4
			m.inputs[m.field].Focus()
			return m, nil
		case "enter":
			return m, func() tea.Msg { return m.save() }
		}
	}

	var cmd tea.Cmd
	m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
	return m, cmd
}

func (m providerEditorModel) save() tea.Msg {
	name := strings.TrimSpace(m.inputs[0].Value())
	baseURL := strings.TrimSpace(m.inputs[1].Value())
	model := strings.TrimSpace(m.inputs[3].Value())
	if name == "" || baseURL == "" || model == "" {
		return providerEditorCancelled{}
	}

	p := agentProvider{
		Name:      name,
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKeyEnv: strings.TrimSpace(m.inputs[2].Value()),
		Model:     model,
	}

	db := m.mgr.agents.db
	if m.editing != nil {
		p.ID = m.editing.ID
		db.updateProvider(p)
	} else {
		db.createProvider(p)
	}
	return providerEditorSaved{}
}

func (m providerEditorModel) View() string {
	var b strings.Builder
	title := "New Provider"
	if m.editing != nil {
		title = "Edit Provider: " + m.editing.Name
	}
	b.WriteString(styleTitle.Width(m.width).Render(title))
	b.WriteString("\n\n")

	labels := []string{"Name", "Base URL", "API Key Env", "Model"}
	for i, label := range labels {
		cursor := "  "
		if i == m.field {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s%-12s %s\n", cursor, label+":", m.inputs[i].View()))
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar([]helpKey{{"Tab", "next"}, {"S-Tab", "prev"}, {"⏎", "save"}, {"Esc", "cancel"}}, m.width))
	return b.String()
}

// --- Chat Picker Screen ---

type chatPickerEntry struct {
	assignment agentChatAssignment
	title      string
	selected   bool
}

type chatPickerModel struct {
	ctx     context.Context
	mgr     *accountManager
	entries []chatPickerEntry
	cursor  int
	loaded  bool
	width   int
	height  int
}

type chatPickerLoaded struct {
	entries []chatPickerEntry
}

func newChatPickerModel(ctx context.Context, mgr *accountManager, current []agentChatAssignment) chatPickerModel {
	return chatPickerModel{ctx: ctx, mgr: mgr}
}

func (m chatPickerModel) Init() tea.Cmd {
	return m.loadChats()
}

func (m chatPickerModel) loadChats() tea.Cmd {
	return func() tea.Msg {
		var entries []chatPickerEntry
		for _, acct := range m.mgr.accounts {
			s := acct.snapshot()
			if s.backend == nil {
				continue
			}
			dialogs, _, err := s.backend.Dialogs(m.ctx, false)
			if err != nil {
				continue
			}
			acctID := filepath.Base(acct.dir)
			tag := backendShortTag(acct.backendType)
			for _, d := range dialogs {
				if !d.IsDM {
					continue
				}
				entries = append(entries, chatPickerEntry{
					assignment: agentChatAssignment{
						BackendType: acct.backendType,
						AccountID:   acctID,
						ChatID:      d.ID.id,
					},
					title: tag + " " + d.Title,
				})
			}
		}
		return chatPickerLoaded{entries: entries}
	}
}

func (m chatPickerModel) Update(msg tea.Msg) (chatPickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case chatPickerLoaded:
		m.entries = msg.entries
		m.loaded = true
		return m, nil

	case tea.KeyPressMsg:
		if !m.loaded {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return chatPickerCancelled{} }
		case "enter":
			var selected []agentChatAssignment
			for _, e := range m.entries {
				if e.selected {
					selected = append(selected, e.assignment)
				}
			}
			return m, func() tea.Msg { return chatPickerDone{selected: selected} }
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "j", "down":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case " ":
			if m.cursor < len(m.entries) {
				m.entries[m.cursor].selected = !m.entries[m.cursor].selected
			}
		}
	}
	return m, nil
}

func (m chatPickerModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render("Select Chats"))
	b.WriteByte('\n')

	if !m.loaded {
		b.WriteString(styleSubtle.Render("  Loading chats…"))
		b.WriteByte('\n')
		return b.String()
	}

	if len(m.entries) == 0 {
		b.WriteString(styleSubtle.Render("  No private chats found."))
		b.WriteString("\n\n")
		b.WriteString(renderHelpBar([]helpKey{{"Esc", "back"}}, m.width))
		return b.String()
	}

	limit := max(1, m.height-5)
	start := max(0, m.cursor-limit+1)
	end := min(len(m.entries), start+limit)

	for i := start; i < end; i++ {
		e := m.entries[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		check := "[ ]"
		if e.selected {
			check = "[x]"
		}
		b.WriteString(clipLine(fmt.Sprintf("%s%s %s", cursor, check, e.title), m.width))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar([]helpKey{{"↑↓", "select"}, {"Space", "toggle"}, {"⏎", "confirm"}, {"Esc", "cancel"}}, m.width))
	return b.String()
}

// Helpers.

func backendShortTag(backendType string) string {
	switch backendType {
	case "telegram":
		return "[TG]"
	case "mattermost":
		return "[MM]"
	case "matrix":
		return "[MX]"
	case "whatsapp":
		return "[WA]"
	default:
		return "[??]"
	}
}
