package tui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// Agent mode manages Chat Agents: AI bots bound to whole accounts that answer
// incoming DMs only. It shares the account header but uses colorAgent.

type agentScreen int

const (
	scrAgents agentScreen = iota
	scrConvs
	scrConv
	scrProviders
	scrWizard
)

type wizardPurpose int

const (
	wizNewAgent wizardPurpose = iota
	wizEditAgent
	wizNewProvider
	wizEditProvider
	wizConnect
)

const agentSidebarWidth = 40

type agentModeModel struct {
	ctx  context.Context
	mgr  *account.AccountManager
	quit bool
	// switchTo is the account to open on exit; nil returns to where we came from.
	switchTo *account.Account

	screen agentScreen
	back   agentScreen // where the wizard returns to

	agents    []agent.AgentRecord
	providers []agent.AgentProvider
	bindings  map[string]int64
	threads   []agent.AgentThread
	sel       int
	tsel      int
	psel      int

	conv      account.ConvKey
	convTitle string
	convLog   []agent.AgentLoggedMessage

	wiz       wizardModel
	purpose   wizardPurpose
	target    int64 // agent or provider being edited or connected
	thenAgent bool  // start the agent wizard once a provider is saved

	confirm     func() error
	confirmText string
	notice      string
	fullHelp    bool
	width       int
	height      int
}

type agentRuntimeChanged struct{}
type agentApproveDone struct{ err error }

func listenAgents(ctx context.Context, mgr *account.AccountManager) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-mgr.Agents.Changed():
			return agentRuntimeChanged{}
		case <-ctx.Done():
			return nil
		}
	}
}

// tuiAgentMode runs agent mode. It returns the account to switch to, or nil
// to go back to wherever it was opened from.
func tuiAgentMode(ctx context.Context, mgr *account.AccountManager) (*account.Account, error) {
	w, h := termSize()
	m := agentModeModel{ctx: ctx, mgr: mgr, width: w, height: h}
	m = m.reload()
	result, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return nil, fmt.Errorf("run agent mode: %w", err)
	}
	return result.(agentModeModel).switchTo, nil
}

func (m agentModeModel) db() *agent.AgentDB { return m.mgr.Agents.Db }

func (m agentModeModel) Init() tea.Cmd {
	return tea.Batch(listenAgents(m.ctx, m.mgr), chipsTick())
}

// reload rereads everything shown from the database.
func (m agentModeModel) reload() agentModeModel {
	var err error
	if m.agents, err = m.db().ListAgents(); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	if m.providers, err = m.db().ListProviders(); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	if m.bindings, err = m.db().AccountAgents(); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	m.sel = clampIndex(m.sel, len(m.agents))
	m.psel = clampIndex(m.psel, len(m.providers))
	m.threads = nil
	if a, ok := m.selected(); ok {
		if m.threads, err = m.db().AgentThreads(a.ID); err != nil {
			m.notice = textfmt.CleanError(err)
		}
	}
	m.tsel = clampIndex(m.tsel, len(m.threads))
	if m.screen == scrConv {
		m.convLog, err = m.db().ChatHistory(m.conv.AgentID, m.conv.Chat.Backend,
			m.conv.AccountKey, m.conv.Chat.Id, 200)
		if err != nil {
			m.notice = textfmt.CleanError(err)
		}
	}
	return m
}

func clampIndex(i, n int) int { return max(0, min(i, n-1)) }

func (m agentModeModel) selected() (agent.AgentRecord, bool) {
	if m.sel < len(m.agents) {
		return m.agents[m.sel], true
	}
	return agent.AgentRecord{}, false
}

func (m agentModeModel) agentName(id int64) string {
	for _, a := range m.agents {
		if a.ID == id {
			return a.Name
		}
	}
	return "another agent"
}

// accountTags lists the agent's accounts in header order, e.g. [TG1][WA2].
func (m agentModeModel) accountTags(id int64) string {
	var tags strings.Builder
	for i, a := range m.mgr.Accounts {
		if m.bindings[m.mgr.AccountKey(a)] == id {
			fmt.Fprintf(&tags, "[%s%d]", backendTag(a.BackendType), i+1)
		}
	}
	return tags.String()
}

func (m agentModeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tuiChipsTick:
		return m, chipsTick()
	case agentRuntimeChanged:
		return m.reload(), listenAgents(m.ctx, m.mgr)
	case agentApproveDone:
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
		} else {
			m.notice = "Reply sent"
		}
		return m.reload(), nil
	case wizardFinished:
		return m.finishWizard(msg.answers)
	case wizardCancelled:
		m.screen, m.thenAgent = m.back, false
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.screen == scrWizard {
		var cmd tea.Cmd
		m.wiz, cmd = m.wiz.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m agentModeModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.confirm != nil {
		run := m.confirm
		m.confirm, m.confirmText = nil, ""
		if k == "y" || k == "Y" {
			if err := run(); err != nil {
				m.notice = textfmt.CleanError(err)
			}
			m.mgr.Agents.Reload()
			return m.reload(), nil
		}
		return m, nil
	}
	if len(k) == 5 && strings.HasPrefix(k, "alt+") && k[4] >= '1' && k[4] <= '9' {
		if i := int(k[4] - '1'); i < len(m.mgr.Accounts) {
			m.switchTo = m.mgr.Accounts[i]
			return m, tea.Quit
		}
		return m, nil
	}
	if m.screen == scrWizard {
		var cmd tea.Cmd
		m.wiz, cmd = m.wiz.Update(msg)
		return m, cmd
	}
	m.notice = ""
	if k == "?" {
		m.fullHelp = !m.fullHelp
		return m, nil
	}
	switch m.screen {
	case scrAgents:
		return m.agentsKey(k)
	case scrConvs:
		return m.convsKey(k)
	case scrConv:
		return m.convKey(k)
	case scrProviders:
		return m.providersKey(k)
	}
	return m, nil
}

func (m agentModeModel) agentsKey(k string) (tea.Model, tea.Cmd) {
	a, ok := m.selected()
	switch k {
	case "esc", "q", "ctrl+c", "alt+a":
		return m, tea.Quit
	case "up", "k":
		m.sel = max(0, m.sel-1)
		m.tsel = 0
		return m.reload(), nil
	case "down", "j":
		m.sel = clampIndex(m.sel+1, len(m.agents))
		m.tsel = 0
		return m.reload(), nil
	case "enter", "tab":
		if ok {
			m.screen = scrConvs
		}
	case "n":
		if len(m.providers) == 0 {
			m.thenAgent = true
			m.notice = "Connect an AI provider first"
			return m.startWizard(wizNewProvider, 0)
		}
		return m.startWizard(wizNewAgent, 0)
	case "e":
		if ok {
			return m.startWizard(wizEditAgent, a.ID)
		}
	case "c":
		if ok {
			return m.startWizard(wizConnect, a.ID)
		}
	case "space":
		if ok {
			a.Enabled = !a.Enabled
			if err := m.db().UpdateAgent(a); err != nil {
				m.notice = textfmt.CleanError(err)
			}
			m.mgr.Agents.Reload()
			return m.reload(), nil
		}
	case "d":
		if ok {
			id := a.ID
			m.confirmText = "Delete agent " + a.Name + " and its history? y/n"
			m.confirm = func() error { return m.db().DeleteAgent(id) }
		}
	case "p":
		m.screen = scrProviders
	}
	return m, nil
}

func (m agentModeModel) convsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "tab", "q":
		m.screen = scrAgents
	case "up", "k":
		m.tsel = max(0, m.tsel-1)
	case "down", "j":
		m.tsel = clampIndex(m.tsel+1, len(m.threads))
	case "enter":
		if m.tsel < len(m.threads) {
			t := m.threads[m.tsel]
			m.conv = account.ConvKey{
				AgentID:    m.agents[m.sel].ID,
				AccountKey: t.AccountID,
				Chat:       backend.ChatID{Backend: t.BackendType, Id: t.ChatID},
			}
			m.convTitle = "[" + accountShort(m.mgr, t.AccountID) + "] " + threadTitle(t)
			m.screen = scrConv
			return m.reload(), nil
		}
	}
	return m, nil
}

func (m agentModeModel) convKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "q":
		m.screen = scrConvs
		return m.reload(), nil
	case "y":
		if _, ok := m.mgr.Agents.Pending(m.conv); ok {
			rt, key := m.mgr.Agents, m.conv
			m.notice = "Sending…"
			return m, func() tea.Msg { return agentApproveDone{rt.Approve(key)} }
		}
	case "n":
		if _, ok := m.mgr.Agents.Pending(m.conv); ok {
			m.mgr.Agents.Discard(m.conv)
			m.notice = "Suggestion discarded"
		}
	}
	return m, nil
}

func (m agentModeModel) providersKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "q", "p":
		m.screen = scrAgents
	case "up", "k":
		m.psel = max(0, m.psel-1)
	case "down", "j":
		m.psel = clampIndex(m.psel+1, len(m.providers))
	case "n":
		return m.startWizard(wizNewProvider, 0)
	case "e":
		if m.psel < len(m.providers) {
			return m.startWizard(wizEditProvider, m.providers[m.psel].ID)
		}
	case "d":
		if m.psel < len(m.providers) {
			p := m.providers[m.psel]
			for _, a := range m.agents {
				if a.ProviderID == p.ID {
					m.notice = "Agent " + a.Name + " uses this provider; change it first"
					return m, nil
				}
			}
			m.confirmText = "Delete provider " + p.Name + "? y/n"
			m.confirm = func() error { return m.db().DeleteProvider(p.ID) }
		}
	}
	return m, nil
}

func threadTitle(t agent.AgentThread) string {
	if t.ChatTitle != "" {
		return textfmt.Plain(t.ChatTitle)
	}
	return t.ChatID
}

// --- Wizards -----------------------------------------------------------------

type providerPreset struct{ value, name, url, env, model string }

var providerPresets = []providerPreset{
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "openai/gpt-4o-mini"},
	{"openai", "OpenAI", "https://api.openai.com/v1", "OPENAI_API_KEY", "gpt-4o-mini"},
	{"ollama", "Ollama (this machine)", "http://localhost:11434/v1", "", "llama3.2"},
}

func presetFor(a wizardAnswers) providerPreset {
	for _, p := range providerPresets {
		if p.value == a["preset"] {
			return p
		}
	}
	return providerPreset{value: "custom"}
}

func (m agentModeModel) startWizard(purpose wizardPurpose, target int64) (tea.Model, tea.Cmd) {
	if m.screen != scrWizard {
		m.back = m.screen
	}
	m.purpose, m.target = purpose, target
	switch purpose {
	case wizNewProvider:
		m.wiz = newWizard("Connect an AI provider", colorAgent, m.providerSteps(false), nil)
	case wizEditProvider:
		initial := wizardAnswers{"preset": "custom"}
		for _, p := range m.providers {
			if p.ID == target {
				initial["name"], initial["url"] = p.Name, p.BaseURL
				initial["env"], initial["model"] = p.APIKeyEnv, p.Model
			}
		}
		m.wiz = newWizard("Edit provider", colorAgent, m.providerSteps(true), initial)
	case wizNewAgent:
		m.wiz = newWizard("New Chat Agent", colorAgent, m.agentSteps(0), nil)
	case wizEditAgent:
		m.wiz = newWizard("Edit Chat Agent", colorAgent, m.agentSteps(target), m.agentAnswers(target))
	case wizConnect:
		initial := wizardAnswers{"accounts": m.boundKeys(target)}
		m.wiz = newWizard("Connect accounts", colorAgent, []wizardStep{m.accountsStep(target)}, initial)
	}
	m.screen = scrWizard
	return m, m.wiz.Init()
}

func (m agentModeModel) providerSteps(editing bool) []wizardStep {
	return []wizardStep{
		{
			key:  "preset",
			kind: wizChoice,
			ask:  say("Which AI provider should your agents use?"),
			options: func(wizardAnswers) []wizardOption {
				var opts []wizardOption
				for _, p := range providerPresets {
					opts = append(opts, wizardOption{label: p.name, value: p.value})
				}
				return append(opts, wizardOption{label: "Other OpenAI-compatible API", value: "custom"})
			},
			skip: func(wizardAnswers) bool { return editing },
			show: func(v string, a wizardAnswers) string {
				if p := presetFor(a); p.name != "" {
					return p.name
				}
				return "Other OpenAI-compatible API"
			},
		},
		{
			key:  "name",
			kind: wizText,
			ask:  say("What should I call this provider?"),
			def:  func(a wizardAnswers) string { return presetFor(a).name },
		},
		{
			key:         "url",
			kind:        wizText,
			ask:         say("What is its API base URL? It must use HTTPS unless it runs on this machine."),
			placeholder: "https://api.example.com/v1",
			skip:        func(a wizardAnswers) bool { return a["preset"] != "custom" },
			validate: func(v string, _ wizardAnswers) error {
				return agent.ValidateProviderURL(v)
			},
		},
		{
			key:  "env",
			kind: wizText,
			ask: say("Which environment variable holds the API key? The key itself is never " +
				"stored. Leave it empty if the provider needs no key."),
			def:      func(a wizardAnswers) string { return presetFor(a).env },
			optional: true,
			validate: func(v string, _ wizardAnswers) error {
				for i, r := range v {
					if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
						return fmt.Errorf("Use letters, digits and _ only, like OPENROUTER_API_KEY.")
					}
				}
				return nil
			},
		},
		{
			key:  "model",
			kind: wizText,
			ask:  say("Which model should agents use by default?"),
			def:  func(a wizardAnswers) string { return presetFor(a).model },
		},
		{
			key:  "confirm",
			kind: wizChoice,
			ask: func(a wizardAnswers) string {
				summary := fmt.Sprintf("%s\n%s\nmodel %s", a["name"], providerURL(a), a["model"])
				if env := a["env"]; env != "" {
					summary += "\nkey from $" + env
					if os.Getenv(env) == "" {
						summary += " — not set in this shell yet"
					}
				}
				return summary + "\n\nSave this provider?"
			},
			options: confirmOptions("Save"),
		},
	}
}

func providerURL(a wizardAnswers) string {
	if a["preset"] == "custom" {
		return strings.TrimRight(a["url"], "/")
	}
	return presetFor(a).url
}

func confirmOptions(save string) func(wizardAnswers) []wizardOption {
	return func(wizardAnswers) []wizardOption {
		return []wizardOption{
			{label: save, value: "save"},
			{label: "Start over", value: "restart"},
			{label: "Cancel", value: "cancel"},
		}
	}
}

func (m agentModeModel) agentSteps(editing int64) []wizardStep {
	choice := func(key, ask string, opts ...wizardOption) wizardStep {
		return wizardStep{
			key: key, kind: wizChoice, ask: say(ask),
			options: func(wizardAnswers) []wizardOption { return opts },
			show: func(v string, _ wizardAnswers) string {
				for _, o := range opts {
					if o.value == v {
						return o.label
					}
				}
				return v
			},
		}
	}
	providerModel := func(a wizardAnswers) string {
		for _, p := range m.providers {
			if strconv.FormatInt(p.ID, 10) == a["provider"] {
				return p.Model
			}
		}
		return ""
	}
	saveLabel := "Create agent"
	if editing != 0 {
		saveLabel = "Save"
	}
	return []wizardStep{
		{key: "name", kind: wizText, ask: say("Let's set up a Chat Agent. What is its name?"),
			placeholder: "Support Bot"},
		{
			key:  "provider",
			kind: wizChoice,
			ask:  say("Which provider should it use?"),
			options: func(wizardAnswers) []wizardOption {
				var opts []wizardOption
				for _, p := range m.providers {
					opts = append(opts, wizardOption{label: p.Name, value: strconv.FormatInt(p.ID, 10)})
				}
				return opts
			},
			show: func(v string, _ wizardAnswers) string {
				for _, p := range m.providers {
					if strconv.FormatInt(p.ID, 10) == v {
						return p.Name
					}
				}
				return v
			},
		},
		{key: "model", kind: wizText, ask: say("Which model? Keep the provider's default or type another."),
			def: providerModel},
		{key: "prompt", kind: wizText, optional: true,
			ask:         say("How should it reply? Give it instructions, or leave empty for a brief, helpful default."),
			placeholder: "You answer questions about my bakery's hours and menu."},
		{
			key:      "knowledge",
			kind:     wizText,
			optional: true,
			ask: say("Any knowledge files it should read? Comma-separated paths; relative " +
				"paths are inside the wiregram config folder."),
			placeholder: "~/notes/faq.md",
			validate: func(v string, _ wizardAnswers) error {
				for _, name := range splitList(v) {
					if _, err := os.Stat(agent.KnowledgePath(m.mgr.Dir, name)); err != nil {
						return fmt.Errorf("Can't read %s.", name)
					}
				}
				return nil
			},
		},
		choice("media", "What can it understand?",
			wizardOption{label: "Text only (media messages never reach it)", value: agent.MediaText},
			wizardOption{label: "Text + media descriptions ([photo], [file]…)", value: agent.MediaTags},
			wizardOption{label: "Text + vision (incoming photos go to the model)", value: agent.MediaVision}),
		choice("mode", "How should it reply?",
			wizardOption{label: "Suggest replies for me to approve", value: "suggest"},
			wizardOption{label: "Reply automatically", value: "auto"}),
		choice("retention", "How long should it keep conversation history?",
			wizardOption{label: "7 days", value: "7"},
			wizardOption{label: "30 days", value: "30"},
			wizardOption{label: "90 days", value: "90"}),
		m.accountsStep(editing),
		{
			key:  "confirm",
			kind: wizChoice,
			ask: func(a wizardAnswers) string {
				tags := m.keyTags(a["accounts"])
				if tags == "" {
					tags = "no accounts yet"
				}
				return fmt.Sprintf("%s · %s\n%s · %s · %s days\n%s\n\nIt only ever answers "+
					"incoming private messages; it never starts a chat or joins groups.",
					a["name"], a["model"], mediaLabel(a["media"]), a["mode"], a["retention"], tags)
			},
			options: confirmOptions(saveLabel),
		},
	}
}

func mediaLabel(v string) string {
	switch v {
	case agent.MediaVision:
		return "text + vision"
	case agent.MediaTags:
		return "text + media"
	}
	return "text only"
}

func (m agentModeModel) accountsStep(agentID int64) wizardStep {
	return wizardStep{
		key:      "accounts",
		kind:     wizMulti,
		optional: true,
		ask: say("Which accounts should it answer DMs on? Each account can have " +
			"one agent. You can change this later with c."),
		options: func(wizardAnswers) []wizardOption {
			var opts []wizardOption
			for i, a := range m.mgr.Accounts {
				key := m.mgr.AccountKey(a)
				o := wizardOption{
					label: fmt.Sprintf("[%s%d] %s", backendTag(a.BackendType), i+1, textfmt.Plain(a.Label())),
					value: key,
				}
				if owner, ok := m.bindings[key]; ok && owner != agentID {
					o.blocked = "taken by " + m.agentName(owner)
				}
				opts = append(opts, o)
			}
			return opts
		},
		show: func(v string, _ wizardAnswers) string { return m.keyTags(v) },
	}
}

func (m agentModeModel) keyTags(csv string) string {
	var tags []string
	for _, key := range splitList(csv) {
		tags = append(tags, "["+accountShort(m.mgr, key)+"]")
	}
	return strings.Join(tags, "")
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (m agentModeModel) boundKeys(agentID int64) string {
	var keys []string
	for _, a := range m.mgr.Accounts {
		if key := m.mgr.AccountKey(a); m.bindings[key] == agentID {
			keys = append(keys, key)
		}
	}
	return strings.Join(keys, ",")
}

func (m agentModeModel) agentAnswers(id int64) wizardAnswers {
	for _, a := range m.agents {
		if a.ID != id {
			continue
		}
		knowledge, _ := m.db().AgentKnowledge(id)
		media, mode := a.Media, "suggest"
		if a.Auto {
			mode = "auto"
		}
		retention := strconv.Itoa(a.RetentionDays)
		if retention != "7" && retention != "90" {
			retention = "30"
		}
		return wizardAnswers{
			"name": a.Name, "provider": strconv.FormatInt(a.ProviderID, 10), "model": a.Model,
			"prompt": a.Prompt, "knowledge": strings.Join(knowledge, ", "), "media": media,
			"mode": mode, "retention": retention, "accounts": m.boundKeys(id),
		}
	}
	return nil
}

func (m agentModeModel) finishWizard(a wizardAnswers) (tea.Model, tea.Cmd) {
	switch a["confirm"] {
	case "restart":
		m.wiz = m.wiz.restart()
		return m, m.wiz.Init()
	case "cancel":
		m.screen, m.thenAgent = m.back, false
		return m, nil
	}
	var err error
	switch m.purpose {
	case wizNewProvider, wizEditProvider:
		p := agent.AgentProvider{ID: m.target, Name: a["name"], BaseURL: providerURL(a),
			APIKeyEnv: a["env"], Model: a["model"]}
		if m.purpose == wizEditProvider {
			err = m.db().UpdateProvider(p)
		} else {
			_, err = m.db().CreateProvider(p)
		}
		if err == nil {
			m.notice = "Provider " + p.Name + " saved"
		}
	case wizNewAgent, wizEditAgent:
		err = m.saveAgent(a)
	case wizConnect:
		err = m.db().SetAgentAccounts(m.target, splitList(a["accounts"]))
		if err == nil {
			m.notice = "Accounts updated"
		}
	}
	m.mgr.Agents.Reload()
	m = m.reload()
	if err != nil {
		m.notice = textfmt.CleanError(err)
	}
	m.screen = m.back
	if m.thenAgent && err == nil && m.purpose == wizNewProvider {
		m.thenAgent = false
		return m.startWizard(wizNewAgent, 0)
	}
	m.thenAgent = false
	return m, nil
}

func (m *agentModeModel) saveAgent(a wizardAnswers) error {
	providerID, err := strconv.ParseInt(a["provider"], 10, 64)
	if err != nil {
		return fmt.Errorf("pick a provider: %w", err)
	}
	retention, err := strconv.Atoi(a["retention"])
	if err != nil {
		return fmt.Errorf("pick a retention period: %w", err)
	}
	rec := agent.AgentRecord{
		ID: m.target, Name: a["name"], Prompt: a["prompt"], Auto: a["mode"] == "auto",
		Media: a["media"], ProviderID: providerID, Model: a["model"],
		RetentionDays: retention, Enabled: true,
	}
	if m.purpose == wizEditAgent {
		for _, old := range m.agents {
			if old.ID == rec.ID {
				rec.Enabled = old.Enabled
			}
		}
		err = m.db().UpdateAgent(rec)
	} else {
		rec.ID, err = m.db().CreateAgent(rec)
	}
	if err != nil {
		return err
	}
	if err := m.db().SetAgentKnowledge(rec.ID, splitList(a["knowledge"])); err != nil {
		return err
	}
	if err := m.db().SetAgentAccounts(rec.ID, splitList(a["accounts"])); err != nil {
		return err
	}
	m.notice = "Chat Agent " + rec.Name + " saved"
	return nil
}

// --- Rendering ---------------------------------------------------------------

func (m agentModeModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m agentModeModel) render() string {
	notice := m.notice
	if m.confirm != nil {
		notice = m.confirmText
	}
	footer := renderThemedFooter(m.helpKeys(), m.fullHelp, notice, m.width, colorAgent)
	bodyHeight := max(4, m.height-lipgloss.Height(footer))
	var body string
	switch {
	case m.screen == scrWizard:
		body = m.wiz.view(m.width, bodyHeight)
	case m.screen == scrProviders:
		body = renderBox("AI providers", m.providerLines(m.width-4, bodyHeight-2),
			m.width, bodyHeight, colorAgent)
	case m.width >= twoPaneWidth:
		left := renderBox("Chat Agents", m.agentLines(agentSidebarWidth-4, bodyHeight-2),
			agentSidebarWidth, bodyHeight, agentBorderColor(m.screen == scrAgents))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left,
			m.rightPane(m.width-agentSidebarWidth, bodyHeight))
	case m.screen == scrAgents:
		body = renderBox("Chat Agents", m.agentLines(m.width-4, bodyHeight-2),
			m.width, bodyHeight, colorAgent)
	default:
		body = m.rightPane(m.width, bodyHeight)
	}
	return body + "\n" + footer
}

func (m agentModeModel) rightPane(width, height int) string {
	if m.screen == scrConv {
		return renderBox(m.convTitle, m.convLines(width-4, height-2), width, height, colorAgent)
	}
	title := "Conversations"
	if a, ok := m.selected(); ok {
		title = a.Name + " — conversations"
	}
	return renderBox(title, m.threadLines(width-4, height-2), width, height,
		agentBorderColor(m.screen == scrConvs))
}

func agentBorderColor(focused bool) color.Color {
	if focused {
		return colorAgent
	}
	return colorDim
}

// window returns the first visible item so that sel stays on screen.
func window(sel, rows int) int {
	if rows < 1 || sel < rows {
		return 0
	}
	return sel - rows + 1
}

func listRow(text, right string, selected, focused bool, width int) string {
	marker := "  "
	if selected {
		marker = styleAgent.Render(">") + " "
	}
	room := max(1, width-2-textfmt.DisplayWidth(right)-1)
	row := marker + truncate(text, room)
	gap := max(1, width-textfmt.DisplayWidth(row)-textfmt.DisplayWidth(right))
	row += strings.Repeat(" ", gap) + right
	switch {
	case selected && focused:
		return styleSelectedRow.Render(row)
	case selected:
		return styleTitle.Render(row)
	}
	return row
}

func (m agentModeModel) agentLines(width, height int) []string {
	if len(m.agents) == 0 {
		hint := "Press n to create your first Chat Agent."
		if len(m.providers) == 0 {
			hint = "Chat Agents answer incoming\nDMs with AI.\n\n" +
				"Press n to connect a provider\nand create your first agent."
		}
		return centerLines(width, height, styleSubtle.Render(hint))
	}
	var lines []string
	for i := window(m.sel, height/2); i < len(m.agents); i++ {
		a := m.agents[i]
		tags := m.accountTags(a.ID)
		status := lipgloss.NewStyle().Foreground(colorGreen).Render("● on")
		problem := m.mgr.Agents.Problem(a.ID)
		switch {
		case !a.Enabled:
			status = styleDim.Render("paused")
		case problem != "":
			status = styleError.Render("✕ error")
		case tags == "":
			status = styleDim.Render("no accounts")
		}
		mode := "suggest"
		if a.Auto {
			mode = "auto"
		}
		detail := styleDim.Render(fmt.Sprintf("    %s · %s %s", mode, mediaLabel(a.Media), tags))
		if problem != "" && a.Enabled {
			detail = "    " + styleError.Render(truncate(problem, width-4))
		}
		lines = append(lines,
			listRow(textfmt.Plain(a.Name), status, i == m.sel, m.screen == scrAgents, width),
			textfmt.ClipLine(detail, width))
	}
	return lines
}

func (m agentModeModel) threadLines(width, height int) []string {
	a, ok := m.selected()
	if !ok {
		return nil
	}
	if len(m.threads) == 0 {
		hint := "No conversations yet."
		if tags := m.accountTags(a.ID); tags == "" {
			hint += "\nPress c to connect accounts."
		} else {
			hint += "\nIt answers incoming DMs on " + tags + "."
		}
		return centerLines(width, height, styleSubtle.Render(hint))
	}
	var lines []string
	for i := window(m.tsel, height); i < len(m.threads); i++ {
		t := m.threads[i]
		right := styleDim.Render(agentTime(t.LastAt))
		key := account.ConvKey{AgentID: a.ID, AccountKey: t.AccountID,
			Chat: backend.ChatID{Backend: t.BackendType, Id: t.ChatID}}
		if _, ok := m.mgr.Agents.Pending(key); ok {
			right = styleAgent.Render("pending ") + right
		}
		text := styleAgent.Render("["+accountShort(m.mgr, t.AccountID)+"]") + " " + threadTitle(t)
		lines = append(lines, listRow(text, right, i == m.tsel, m.screen == scrConvs, width))
	}
	return lines
}

func agentTime(t time.Time) string {
	t = t.Local()
	if y, mo, d := time.Now().Date(); t.Year() == y && t.Month() == mo && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}

func (m agentModeModel) convLines(width, height int) []string {
	indent := 8 + textfmt.NickWidth + 1
	var lines []string
	for _, msg := range m.convLog {
		nick := textfmt.Plain(msg.Sender)
		style := lipgloss.NewStyle()
		switch {
		case msg.IsAgentReply:
			nick, style = "agent", styleAgent
		case msg.Outgoing:
			nick = "you"
		}
		prefix := styleDim.Render(msg.Timestamp.Local().Format("[15:04]")) + " " +
			style.Render(textfmt.FormatNick(nick)) + " "
		lines = append(lines, textfmt.WrapMessage(prefix, textfmt.Plain(msg.Text), width, indent)...)
	}
	if s, ok := m.mgr.Agents.Pending(m.conv); ok {
		lines = append(lines, "", styleAgent.Bold(true).Render("Suggested reply"))
		lines = append(lines, strings.Split(wizardBubble(s.Text, min(width, 72), colorAgent), "\n")...)
		lines = append(lines, styleSubtle.Render("y send · n discard"))
	}
	return bottomLines(lines, height)
}

func (m agentModeModel) providerLines(width, height int) []string {
	if len(m.providers) == 0 {
		return centerLines(width, height, styleSubtle.Render("No providers yet. Press n to connect one."))
	}
	var lines []string
	for i := window(m.psel, height); i < len(m.providers); i++ {
		p := m.providers[i]
		right := styleDim.Render(p.Model)
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
			right = styleError.Render("$"+p.APIKeyEnv+" not set") + " " + right
		}
		lines = append(lines, listRow(textfmt.Plain(p.Name)+"  "+styleDim.Render(p.BaseURL),
			right, i == m.psel, true, width))
	}
	return lines
}

func (m agentModeModel) helpKeys() []helpKey {
	var keys []helpKey
	switch m.screen {
	case scrWizard:
		return m.wiz.helpKeys()
	case scrAgents:
		keys = []helpKey{{"↑↓", "select"}, {"⏎", "chats"}, {"n", "new"}, {"e", "edit"},
			{"c", "accounts"}, {"space", "pause"}, {"d", "delete"}, {"p", "providers"}, {"esc", "back"}}
	case scrConvs:
		keys = []helpKey{{"↑↓", "select"}, {"⏎", "open"}, {"esc", "agents"}}
	case scrConv:
		keys = []helpKey{{"y", "send suggestion"}, {"n", "discard"}, {"esc", "back"}}
	case scrProviders:
		keys = []helpKey{{"↑↓", "select"}, {"n", "new"}, {"e", "edit"}, {"d", "delete"}, {"esc", "agents"}}
	}
	if len(m.mgr.Accounts) > 1 {
		keys = append(keys, helpKey{"M-1…9", "account"})
	}
	return append(keys, helpKey{"?", "more"})
}
