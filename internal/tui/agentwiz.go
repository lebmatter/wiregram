package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/agent"
)

// The provider and agent wizards are form modals: Settings' AI providers adds
// providers, and an account's New bot… row creates a bot (a Chat Agent).

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

// validEnvName accepts an environment variable name, or "" for none.
func validEnvName(v string) error {
	for i, r := range v {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return fmt.Errorf("Use letters, digits and _ only, like OPENROUTER_API_KEY.")
		}
	}
	return nil
}

func providerSteps() []wizardStep {
	return []wizardStep{
		{
			key:   "preset",
			label: "Provider",
			kind:  wizChoice,
			ask:   say("Which AI provider should bots use?"),
			options: func(wizardAnswers) []wizardOption {
				var opts []wizardOption
				for _, p := range providerPresets {
					opts = append(opts, wizardOption{label: p.name, value: p.value})
				}
				return append(opts, wizardOption{label: "Other OpenAI-compatible API", value: "custom"})
			},
			show: func(v string, a wizardAnswers) string {
				if p := presetFor(a); p.name != "" {
					return p.name
				}
				return "Other OpenAI-compatible API"
			},
		},
		{
			key:   "name",
			label: "Name",
			kind:  wizText,
			ask:   say("What should I call this provider?"),
			def:   func(a wizardAnswers) string { return presetFor(a).name },
		},
		{
			key:         "url",
			label:       "URL",
			kind:        wizText,
			ask:         say("What is its API base URL? It must use HTTPS unless it runs on this machine."),
			placeholder: "https://api.example.com/v1",
			skip:        func(a wizardAnswers) bool { return a["preset"] != "custom" },
			validate: func(v string, _ wizardAnswers) error {
				return agent.ValidateProviderURL(v)
			},
		},
		{
			key:   "env",
			label: "Key from",
			kind:  wizText,
			ask: say("Which environment variable holds the API key? The key itself is never " +
				"stored. Leave it empty if the provider needs no key."),
			def:      func(a wizardAnswers) string { return presetFor(a).env },
			optional: true,
			validate: func(v string, _ wizardAnswers) error { return validEnvName(v) },
		},
		{
			key:   "model",
			label: "Model",
			kind:  wizText,
			ask:   say("Which model should bots use by default?"),
			def:   func(a wizardAnswers) string { return presetFor(a).model },
		},
		{
			key:  "confirm",
			kind: wizChoice,
			ask: func(a wizardAnswers) string {
				summary := providerURL(a)
				if env := a["env"]; env != "" && os.Getenv(env) == "" {
					summary += "\n$" + env + " is not set in this shell yet."
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

func providerFromAnswers(a wizardAnswers) agent.AgentProvider {
	return agent.AgentProvider{Name: a["name"], BaseURL: providerURL(a),
		APIKeyEnv: a["env"], Model: a["model"]}
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

// choiceStep is a wizard step picking one of opts.
func choiceStep(key, label, ask string, opts ...wizardOption) wizardStep {
	return wizardStep{
		key: key, label: label, kind: wizChoice, ask: say(ask),
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

func withDefault(s wizardStep, v string) wizardStep {
	s.def = func(wizardAnswers) string { return v }
	return s
}

var (
	mediaOptions = []wizardOption{
		{label: "Text only", value: agent.MediaText},
		{label: "Text + media descriptions", value: agent.MediaTags},
		{label: "Text + vision (photos go to the model)", value: agent.MediaVision},
	}
	replyOptions = []wizardOption{
		{label: "Ask me first", value: "ask"},
		{label: "Automatically", value: "auto"},
	}
	retentionOptions = []wizardOption{
		{label: "7 days", value: "7"},
		{label: "30 days", value: "30"},
		{label: "90 days", value: "90"},
	}
)

// agentSteps create a bot for an account whose other bots hold the handles in
// taken (handle → name); Chats and the rest are changed later in its pane.
func agentSteps(providers []agent.AgentProvider, dir string, taken map[string]string) []wizardStep {
	providerModel := func(a wizardAnswers) string {
		for _, p := range providers {
			if strconv.FormatInt(p.ID, 10) == a["provider"] {
				return p.Model
			}
		}
		return ""
	}
	var provOpts []wizardOption
	for _, p := range providers {
		provOpts = append(provOpts, wizardOption{label: p.Name, value: strconv.FormatInt(p.ID, 10)})
	}
	return []wizardStep{
		{key: "name", label: "Name", kind: wizText,
			ask: say("What is the bot's name?"), placeholder: "Support Bot"},
		{
			key:   "handle",
			label: "Handle",
			kind:  wizText,
			ask: say("Its @handle? It names the bot if you register it on the network " +
				"later, and can be changed until then."),
			def: func(a wizardAnswers) string { return agent.SuggestHandle(a["name"]) },
			validate: func(v string, _ wizardAnswers) error {
				v = strings.TrimPrefix(strings.ToLower(v), "@")
				if err := agent.ValidHandle(v); err != nil {
					return err
				}
				if name, ok := taken[v]; ok {
					return fmt.Errorf("@%s is used by bot %s.", v, name)
				}
				return nil
			},
		},
		choiceStep("provider", "Provider", "Which provider should it use?", provOpts...),
		{key: "model", label: "Model", kind: wizText,
			ask: say("Which model? Keep the provider's default or type another."),
			def: providerModel},
		{key: "prompt", label: "Prompt", kind: wizText, optional: true,
			ask:         say("How should it reply? Give it instructions, or leave empty for a brief, helpful default."),
			placeholder: "You answer questions about my bakery's hours and menu."},
		{
			key:      "knowledge",
			label:    "Knowledge",
			kind:     wizText,
			optional: true,
			ask: say("Any knowledge files it should read? Comma-separated paths; relative " +
				"paths are inside the wiregram config folder."),
			placeholder: "~/notes/faq.md",
			validate:    func(v string, _ wizardAnswers) error { return checkKnowledge(dir, v) },
		},
		withDefault(choiceStep("media", "Understands", "What can it understand?",
			mediaOptions...), agent.MediaTags),
		choiceStep("mode", "Replies", "How should it reply?", replyOptions...),
		withDefault(choiceStep("retention", "History",
			"How long should it keep conversation history?", retentionOptions...), "30"),
		{
			key:  "confirm",
			kind: wizChoice,
			ask: say("It answers private chats as you: every one when no other bot here " +
				"answers any, else the ones you pick next. It never starts a chat or " +
				"answers groups."),
			options: confirmOptions("Create bot"),
		},
	}
}

func checkKnowledge(dir, v string) error {
	for _, name := range splitList(v) {
		if _, err := os.Stat(agent.KnowledgePath(dir, name)); err != nil {
			return fmt.Errorf("Can't read %s.", name)
		}
	}
	return nil
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

// createAgent saves the bot the wizard described for the account.
func createAgent(db *agent.AgentDB, accountKey string, a wizardAnswers) (agent.AgentRecord, error) {
	providerID, err := strconv.ParseInt(a["provider"], 10, 64)
	if err != nil {
		return agent.AgentRecord{}, fmt.Errorf("pick a provider: %w", err)
	}
	retention, err := strconv.Atoi(a["retention"])
	if err != nil {
		return agent.AgentRecord{}, fmt.Errorf("pick a retention period: %w", err)
	}
	rec := agent.AgentRecord{
		AccountKey: accountKey, Name: a["name"],
		Handle: strings.TrimPrefix(strings.ToLower(a["handle"]), "@"), Prompt: a["prompt"],
		Auto: a["mode"] == "auto", Media: a["media"], ProviderID: providerID, Model: a["model"],
		RetentionDays: retention, Enabled: true,
	}
	if rec, err = db.CreateAgent(rec); err != nil {
		return rec, err
	}
	return rec, db.SetAgentKnowledge(rec.ID, splitList(a["knowledge"]))
}

// modal draws the wizard as a form box for a screen width wide.
func (m wizardModel) modal(width, bodyHeight int) (box string, top int) {
	w := min(formModalWidth, width-2)
	lines, _ := m.formView(w - 4)
	return formBox(m.title, lines, m.helpKeys(), w, bodyHeight, m.st)
}

// modalClick answers a click at screen x, y on the modal, centred in an area
// left..left+width of the body.
func (m wizardModel) modalClick(x, y, left, width, bodyHeight int) (wizardModel, tea.Cmd) {
	box, top := m.modal(width, bodyHeight)
	bx, by := modalRect(width, bodyHeight, lipgloss.Width(box), lipgloss.Height(box))
	if x < left+bx || x >= left+bx+lipgloss.Width(box) {
		return m, nil
	}
	return m.formClick(y-by-1+top, lipgloss.Width(box)-4) // under the top edge
}
