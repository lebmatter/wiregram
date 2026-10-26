package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// The agent pane is one Chat Agent's settings, a bot in the UI, opened from
// its sidebar row in place of a chat. Like Settings, each row is chosen with
// the arrows and changed with Enter; Chats opens the dmPicker modal.

type agentField int

const (
	fieldStatus agentField = iota
	fieldName
	fieldHandle
	fieldProvider
	fieldModel
	fieldPrompt
	fieldKnowledge
	fieldMedia
	fieldReplies
	fieldPrefix
	fieldRetention
	fieldChats
	fieldManager    // the account's manager bot, on Telegram
	fieldBotAccount // registering on the network, where the backend can
	fieldRotate     // these two only while registered
	fieldUnregister
	fieldDelete
)

type agentRow struct {
	field agentField
}

type agentPaneModel struct {
	st        *styles
	ctx       context.Context
	b         backend.Backend // the account's, for finding people to pick
	mgr       *account.AccountManager
	acct      *account.Account
	rec       agent.AgentRecord
	knowledge []string
	providers []agent.AgentProvider
	entries   []backend.ChatEntry // the account's chats, for the picker
	chats     []agent.AgentChat   // the DMs it answers when AllChats is off
	sel       int
	edit      textinput.Model
	editing   bool
	confirm   *confirmModal
	confirmOf agentField // the row the confirm modal goes ahead with
	busy      bool       // a request to the server is running
	picker    *dmPicker
	latest    []agent.AgentLoggedMessage // its newest messages, for the side column
	notice    string
	width     int
	height    int
}

type agentPaneClosed struct{}

// agentBotAlert asks the root to show a failed request about a bot as an
// alert over the screen.
type agentBotAlert struct{ title, text string }

// agentBotDone ends a request to the server about the bot: done is the
// notice on success, and rec, when set, is the record to save after it.
type agentBotDone struct {
	agentID int64 // the bot asked about; its pane may have closed since
	err     error
	fail    string // the alert's title when err is set
	done    string
	rec     *agent.AgentRecord
	gone    bool // the bot was deleted; close the pane
}

// alert is the alert a failed request shows.
func (d agentBotDone) alert() agentBotAlert {
	text := textfmt.CleanError(d.err)
	if r, size := utf8.DecodeRuneInString(text); size > 0 {
		text = string(unicode.ToUpper(r)) + text[size:]
	}
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return agentBotAlert{title: d.fail, text: text}
}

// botTimeout bounds a request to the server about a bot.
const botTimeout = 30 * time.Second

// agentPaneHeader is how many lines sit above the rows: the status line and a gap.
const agentPaneHeader = 2

func newAgentPane(ctx context.Context, st *styles, mgr *account.AccountManager,
	acct *account.Account, b backend.Backend, id int64,
	entries []backend.ChatEntry) (agentPaneModel, error) {
	m := agentPaneModel{st: st, ctx: ctx, b: b, mgr: mgr, acct: acct, entries: entries}
	rec, ok := mgr.Agents.Agent(id)
	if !ok {
		return m, fmt.Errorf("that bot has been deleted")
	}
	db := mgr.Agents.Db
	var err error
	if m.knowledge, err = db.AgentKnowledge(rec.ID); err != nil {
		return m, err
	}
	if m.providers, err = db.ListProviders(); err != nil {
		return m, err
	}
	if m.chats, err = db.AgentChats(rec.ID); err != nil {
		return m, err
	}
	m.rec = rec
	return m.reloadLatest(), nil
}

// latestShown is how many of the bot's newest messages the side column lists.
const latestShown = 8

// reloadLatest rereads the bot's newest messages; a failure leaves the list as
// it was, since it is only a glance.
func (m agentPaneModel) reloadLatest() agentPaneModel {
	if latest, err := m.mgr.Agents.Db.LatestMessages(m.rec.ID, latestShown); err == nil {
		m.latest = latest
	}
	return m
}

func (m agentPaneModel) resized(width, height int) agentPaneModel {
	m.width, m.height = width, height
	if m.picker != nil {
		m.picker.resized(width, height)
	}
	return m
}

func (m agentPaneModel) rows() []agentRow {
	_, registered := m.mgr.Agents.Registration(m.rec.ID)
	var rows []agentRow
	for f := fieldStatus; f <= fieldDelete; f++ {
		switch {
		case f == fieldManager && !m.mgr.Agents.NeedsManager(m.acct),
			f == fieldBotAccount && !registered && !m.mgr.Agents.CanRegister(m.acct),
			(f == fieldRotate || f == fieldUnregister) && !registered:
			continue
		}
		rows = append(rows, agentRow{field: f})
	}
	return rows
}

// botOp runs a request to the server about the bot in the background; a
// failure is shown as an alert titled fail.
func (m agentPaneModel) botOp(notice, fail string, op func(ctx context.Context) agentBotDone) (
	agentPaneModel, tea.Cmd) {
	m.busy, m.notice = true, notice
	id := m.rec.ID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), botTimeout)
		defer cancel()
		done := op(ctx)
		done.agentID, done.fail = id, fail
		return done
	}
}

// botDone shows how a request to the server ended.
func (m agentPaneModel) botDone(msg agentBotDone) (agentPaneModel, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.notice = ""
		alert := msg.alert()
		return m, func() tea.Msg { return alert }
	}
	if msg.gone {
		return m, func() tea.Msg { return agentPaneClosed{} }
	}
	if msg.rec != nil {
		m.rec = *msg.rec
		m = m.save()
		if strings.HasPrefix(m.notice, "Saved") {
			m.notice = msg.done
		}
		return m, nil
	}
	m.notice = msg.done
	return m, nil
}

func (m agentPaneModel) Update(msg tea.Msg) (agentPaneModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.picker != nil {
			return m, m.picker.paste(msg)
		}
		if m.editing {
			var cmd tea.Cmd
			m.edit, cmd = m.edit.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	case agentBotDone:
		return m.botDone(msg)
	case dmSearchDebounce, dmSearchResults, dmResolved:
		if m.picker != nil {
			cmd := m.picker.update(msg)
			return m.saveChats(), cmd
		}
	}
	return m, nil
}

func (m agentPaneModel) key(msg tea.KeyPressMsg) (agentPaneModel, tea.Cmd) {
	k := msg.String()
	if m.picker != nil {
		done, cmd := m.picker.key(msg)
		if done {
			m.picker = nil
			return m, nil
		}
		return m.saveChats(), cmd
	}
	if m.confirm != nil {
		yes, done := m.confirm.key(k)
		if !done {
			return m, nil
		}
		m.confirm = nil
		if !yes {
			return m, nil
		}
		switch m.confirmOf {
		case fieldBotAccount:
			return m.register()
		case fieldRotate:
			return m.rotate()
		case fieldUnregister:
			return m.unregister()
		}
		return m.deleteAgent()
	}
	if m.editing {
		switch k {
		case "esc":
			m.editing = false
		case "enter":
			m.editing = false
			return m.saveText(strings.TrimSpace(textfmt.Plain(m.edit.Value())))
		default:
			var cmd tea.Cmd
			m.edit, cmd = m.edit.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	if m.busy {
		return m, nil // wait for the server
	}
	m.notice = ""
	rows := m.rows()
	switch k {
	case "esc", "left", "h":
		return m, func() tea.Msg { return agentPaneClosed{} }
	case "p":
		return m.apply(agentRow{field: fieldStatus})
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
			alert := agentBotAlert{title: "No AI provider",
				text: "Add an AI provider in Settings > AI providers first."}
			return m, func() tea.Msg { return alert }
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
	case fieldPrefix:
		m.rec.Prefix = nextOption(prefixOptions, m.rec.ReplyPrefix())
	case fieldRetention:
		days, _ := strconv.Atoi(nextOption(retentionOptions, strconv.Itoa(m.rec.RetentionDays)))
		m.rec.RetentionDays = days
	case fieldChats:
		return m.openPicker(), nil
	case fieldManager:
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = "paste the manager bot's token"
		in.EchoMode = textinput.EchoPassword
		in.CharLimit = 200
		m.edit, m.editing = in, true
		m.notice = "A bot with Bot Management on in @BotFather; it creates this account's bots"
		return m, m.edit.Focus()
	case fieldBotAccount:
		if _, ok := m.mgr.Agents.Registration(m.rec.ID); ok {
			return m, nil
		}
		name := "@" + m.rec.Handle
		if m.mgr.Agents.NeedsManager(m.acct) {
			name += " (Telegram adds _bot unless it ends in bot)"
		}
		m.confirm = newConfirmModal(m.st, "Register bot", "Create "+name+" on "+
			accountLabel(m.acct)+"? People can then message it, and "+
			textfmt.Plain(m.rec.Name)+" answers them as the bot.", "Register", false)
	case fieldRotate:
		m.confirm = newConfirmModal(m.st, "Rotate token",
			"Give @"+m.rec.Handle+" a new token and revoke the old one?", "Rotate", false)
	case fieldUnregister:
		m.confirm = newConfirmModal(m.st, "Unregister bot", "Disable @"+m.rec.Handle+
			" on the server? It stops answering its messages; you can register it again.",
			"Unregister", true)
	case fieldDelete:
		question := "Delete bot " + textfmt.Plain(m.rec.Name) + " and its history?"
		if _, ok := m.mgr.Agents.Registration(m.rec.ID); ok {
			question += " @" + m.rec.Handle + " is disabled on the server too."
		}
		m.confirm = newConfirmModal(m.st, "Delete bot", question, "Delete", true)
	default:
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 2000
		in.SetValue(m.text(r.field))
		in.CursorEnd()
		m.edit, m.editing = in, true
		return m, m.edit.Focus()
	}
	if r.field >= fieldBotAccount && r.field <= fieldDelete {
		m.confirmOf = r.field
		return m, nil
	}
	return m.save(), nil
}

// prefixOptions are the prefixes a bot's replies can start with.
var prefixOptions = []wizardOption{
	{label: "🤖 robot", value: agent.PrefixRobot},
	{label: "💻 computer", value: agent.PrefixComputer},
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
	case fieldHandle:
		return m.rec.Handle
	case fieldModel:
		return m.rec.Model
	case fieldPrompt:
		return m.rec.Prompt
	case fieldKnowledge:
		return strings.Join(m.knowledge, ", ")
	}
	return ""
}

func (m agentPaneModel) saveText(v string) (agentPaneModel, tea.Cmd) {
	rows := m.rows()
	if m.sel >= len(rows) {
		return m, nil
	}
	switch rows[m.sel].field {
	case fieldName, fieldModel:
		if v == "" {
			m.notice = "That field needs a value"
			return m, nil
		}
		if rows[m.sel].field == fieldModel {
			m.rec.Model = v
			break
		}
		next := m.rec
		next.Name = v
		return m.rename(next)
	case fieldHandle:
		v = strings.TrimPrefix(strings.ToLower(v), "@")
		if err := agent.ValidHandle(v); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		next := m.rec
		next.Handle = v
		return m.rename(next)
	case fieldManager:
		if v == "" {
			return m, nil
		}
		rt, acct := m.mgr.Agents, m.acct
		return m.botOp("Checking the manager bot…", "Manager bot not set", func(ctx context.Context) agentBotDone {
			mgr, err := rt.SetManager(ctx, acct, v)
			return agentBotDone{err: err, done: "Manager bot @" + mgr.Username + " set"}
		})
	case fieldPrompt:
		m.rec.Prompt = v
	case fieldKnowledge:
		if err := checkKnowledge(m.mgr.Dir, v); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		if err := m.mgr.Agents.Db.SetAgentKnowledge(m.rec.ID, splitList(v)); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.knowledge = splitList(v)
		m.mgr.Agents.Reload()
		m.notice = "Saved"
		return m, nil
	}
	return m.save(), nil
}

// rename saves a new name or handle; a registered bot is renamed on the
// server first, so the two never disagree.
func (m agentPaneModel) rename(next agent.AgentRecord) (agentPaneModel, tea.Cmd) {
	if _, ok := m.mgr.Agents.Registration(m.rec.ID); !ok {
		m.rec = next
		return m.save(), nil
	}
	for _, other := range m.mgr.Agents.AgentsFor(m.acct) {
		if other.ID != next.ID && other.Handle == next.Handle {
			m.notice = "@" + next.Handle + " is used by bot " + textfmt.Plain(other.Name)
			return m, nil
		}
	}
	rt, acct := m.mgr.Agents, m.acct
	return m.botOp("Renaming on the server…", "Bot not renamed", func(ctx context.Context) agentBotDone {
		err := rt.Rename(ctx, acct, next.ID, next.Handle, next.Name)
		return agentBotDone{err: err, done: "Renamed", rec: &next}
	})
}

func (m agentPaneModel) register() (agentPaneModel, tea.Cmd) {
	rt, acct, id, handle := m.mgr.Agents, m.acct, m.rec.ID, m.rec.Handle
	return m.botOp("Registering @"+handle+"…", "Bot not registered", func(ctx context.Context) agentBotDone {
		err := rt.Register(ctx, acct, id)
		return agentBotDone{err: err, done: "Registered @" + handle}
	})
}

func (m agentPaneModel) rotate() (agentPaneModel, tea.Cmd) {
	rt, acct, id := m.mgr.Agents, m.acct, m.rec.ID
	return m.botOp("Rotating the token…", "Token not rotated", func(ctx context.Context) agentBotDone {
		return agentBotDone{err: rt.RotateToken(ctx, acct, id), done: "New token in use"}
	})
}

func (m agentPaneModel) unregister() (agentPaneModel, tea.Cmd) {
	rt, acct, id, handle := m.mgr.Agents, m.acct, m.rec.ID, m.rec.Handle
	return m.botOp("Unregistering @"+handle+"…", "Bot not unregistered", func(ctx context.Context) agentBotDone {
		return agentBotDone{err: rt.Unregister(ctx, acct, id), done: "@" + handle + " disabled"}
	})
}

func (m agentPaneModel) save() agentPaneModel {
	if err := m.mgr.Agents.Db.UpdateAgent(m.rec); err != nil {
		m.notice = textfmt.CleanError(err)
		if rec, ok := m.mgr.Agents.Agent(m.rec.ID); ok {
			m.rec = rec // back to what is stored
		}
		return m
	}
	m.mgr.Agents.Reload()
	m.notice = "Saved"
	return m
}

// openPicker opens the modal choosing the bot's chats, with the DMs other bots
// of the account answer marked as theirs.
func (m agentPaneModel) openPicker() agentPaneModel {
	claims, err := m.mgr.Agents.Db.ChatClaims(m.mgr.AccountKey(m.acct))
	if err != nil {
		m.notice = textfmt.CleanError(err)
		return m
	}
	names := make(map[int64]string)
	for _, rec := range m.mgr.Agents.AgentsFor(m.acct) {
		names[rec.ID] = textfmt.Plain(rec.Name)
	}
	allBy := ""
	if claims.All != 0 && claims.All != m.rec.ID {
		allBy = names[claims.All]
	}
	owners := make(map[string]string)
	for id, owner := range claims.Chats {
		if owner != m.rec.ID {
			owners[id] = names[owner]
		}
	}
	picked := make(map[string]bool)
	listed := make(map[string]bool)
	var dms []agent.AgentChat
	for _, c := range m.chats {
		picked[c.ID], listed[c.ID] = true, true
		dms = append(dms, c)
	}
	for _, e := range m.entries {
		if e.IsDM && !e.IsSelf && !listed[e.ID.Id] {
			dms = append(dms, agent.AgentChat{ID: e.ID.Id, Title: textfmt.Plain(e.Title)})
		}
	}
	selfBy := ""
	if claims.Self != 0 && claims.Self != m.rec.ID {
		selfBy = names[claims.Self]
	}
	m.picker = newDMPicker(m.ctx, m.st, m.b, m.rec.Name, m.rec.AllChats, m.rec.SelfChat, dms,
		picked, owners, allBy, selfBy)
	m.picker.resized(m.width, m.height)
	return m
}

// saveChats stores the picker's choice as soon as it changes; on failure the
// picker goes back to what was stored and shows the reason.
func (m agentPaneModel) saveChats() agentPaneModel {
	if m.picker == nil || !m.picker.dirty {
		return m
	}
	m.picker.dirty = false
	all, self, chats := m.picker.chosen()
	if err := m.mgr.Agents.Db.SetAgentChats(m.rec.ID, all, self, chats); err != nil {
		m.picker.all, m.picker.self = m.rec.AllChats, m.rec.SelfChat
		clear(m.picker.picked)
		for _, c := range m.chats {
			m.picker.picked[c.ID] = true
		}
		m.picker.notice = textfmt.CleanError(err)
		return m
	}
	m.rec.AllChats, m.rec.SelfChat, m.chats = all, self, chats
	m.mgr.Agents.Reload()
	m.notice = "Saved"
	return m
}

func (m agentPaneModel) deleteAgent() (agentPaneModel, tea.Cmd) {
	rt, acct, id := m.mgr.Agents, m.acct, m.rec.ID
	del := func() error {
		if err := rt.Db.DeleteAgent(id); err != nil {
			return err
		}
		rt.Reload()
		return nil
	}
	if _, ok := rt.Registration(id); !ok {
		if err := del(); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		return m, func() tea.Msg { return agentPaneClosed{} }
	}
	// The server goes first: a deleted bot would leave no way to disable it.
	return m.botOp("Disabling the bot on the server…", "Bot not deleted", func(ctx context.Context) agentBotDone {
		if err := rt.Unregister(ctx, acct, id); err != nil {
			return agentBotDone{err: fmt.Errorf("the server said: %w", err)}
		}
		return agentBotDone{err: del(), gone: true}
	})
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
		return "Status", on(m.rec.Enabled, "live", "paused")
	case fieldName:
		return "Name", m.rec.Name
	case fieldHandle:
		return "Handle", "@" + m.rec.Handle
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
	case fieldPrefix:
		return "Prefix", m.rec.Prefixed("…")
	case fieldRetention:
		return "History", fmt.Sprintf("%d days", m.rec.RetentionDays)
	case fieldManager:
		if mgr, ok := m.mgr.Agents.Manager(m.acct); ok {
			return "Manager bot", "@" + mgr.Username + ", for all of " + accountLabel(m.acct)
		}
		return "Manager bot", "none: set one to register bots"
	case fieldBotAccount:
		if bot, ok := m.mgr.Agents.Registration(m.rec.ID); ok {
			return "Bot account", "@" + bot.Username + " on " + accountLabel(m.acct)
		}
		return "Bot account", "not registered · Register…"
	case fieldRotate:
		return "Rotate token…", ""
	case fieldUnregister:
		return styleError.Render("Unregister…"), ""
	case fieldChats:
		you := ""
		if m.rec.SelfChat {
			you = " + you"
		}
		switch {
		case m.rec.AllChats:
			return "Chats", "all private chats" + you
		case len(m.chats) == 0 && m.rec.SelfChat:
			return "Chats", "you"
		case len(m.chats) == 0:
			return "Chats", "none yet: pick some"
		}
		return "Chats", fmt.Sprintf("%d selected", len(m.chats)) + you
	}
	return styleError.Render("Delete bot…"), ""
}

// agentSideWidth is the side column of accounts and latest messages, shown
// when the pane has room for it beside the rows.
const agentSideWidth = 40

func (m agentPaneModel) view(focused bool) string {
	inner, height := m.width-4, m.height-2
	status := "Answers its private chats as you; never groups or channels."
	if bot, ok := m.mgr.Agents.Registration(m.rec.ID); ok {
		status = "Answers its private chats as you, and messages to @" + bot.Username +
			" as the bot; never groups or channels."
		if n := m.mgr.Agents.InboxWaiting(m.rec.ID); n > 0 {
			status = fmt.Sprintf("%d %s to @%s waiting for approval in its Inbox.", n,
				plural(n, "reply", "replies"), bot.Username)
		}
	}
	chip := styleError.Bold(true).Render("● paused")
	if m.rec.Enabled {
		chip = m.st.accent.Bold(true).Render("● live")
	}
	room := inner - textfmt.DisplayWidth(chip) - 2
	if problem := m.mgr.Agents.Problem(m.rec.ID); problem != "" {
		status = styleError.Render(textfmt.ClipLine("Last reply failed: "+problem, room))
	} else {
		status = m.st.subtle.Render(textfmt.ClipLine(status, room))
	}
	left, side := inner, 0
	if inner >= 2*agentSideWidth+4 {
		side = agentSideWidth
		left = inner - side - 4
	}
	lines := m.rowLines(left, height-agentPaneHeader, focused)
	if side > 0 {
		right := m.sideLines(side)
		for i := range lines {
			pad := strings.Repeat(" ", max(0, left-textfmt.DisplayWidth(lines[i])))
			if i < len(right) {
				lines[i] += pad + "    " + right[i]
			}
		}
	}
	lines = append([]string{chip + "  " + status, ""}, lines...)
	return renderBox(agentIcon+" "+m.rec.Name, lines, m.width, m.height, m.st.border(focused))
}

// paneItems is the rows in display order with -1 for the gap that sets the
// destructive rows apart.
func (m agentPaneModel) paneItems() []int {
	var items []int
	for i, r := range m.rows() {
		if r.field == fieldUnregister || (r.field == fieldDelete && (i == 0 ||
			m.rows()[i-1].field != fieldUnregister)) {
			items = append(items, -1)
		}
		items = append(items, i)
	}
	return items
}

// rowLines draws the rows width cells wide: a caret, the label column and the
// value, dim until selected; destructive rows in red.
func (m agentPaneModel) rowLines(width, height int, focused bool) []string {
	rows := m.rows()
	labelW := 0
	for _, r := range rows {
		l, _ := m.label(r)
		labelW = max(labelW, textfmt.DisplayWidth(ansi.Strip(l)))
	}
	items := m.paneItems()
	var lines []string
	for _, i := range items[window(m.itemOf(m.sel), height):] {
		if len(lines) == height {
			break
		}
		if i < 0 {
			lines = append(lines, "")
			continue
		}
		selected := i == m.sel
		label, value := m.label(rows[i])
		plainLabel := ansi.Strip(label)
		labelStyle := lipgloss.NewStyle()
		if label != plainLabel {
			labelStyle = styleError // Unregister… and Delete bot…
		}
		valueStyle := m.st.subtle
		if selected {
			valueStyle = lipgloss.NewStyle()
		}
		caret := "  "
		if selected {
			caret = m.st.accent.Render("›") + " "
		}
		value = textfmt.Plain(value)
		var shown string
		if before, ok := strings.CutSuffix(value, "Register…"); ok {
			shown = valueStyle.Render(before) + m.st.accent.Render("Register…")
		} else {
			shown = valueStyle.Render(value)
		}
		if selected && m.editing {
			in := m.edit
			in.SetWidth(max(8, width-labelW-5))
			shown = in.View()
		}
		row := caret + labelStyle.Render(plainLabel)
		if value != "" || (selected && m.editing) {
			row += strings.Repeat(" ", labelW+2-textfmt.DisplayWidth(plainLabel)) + shown
		}
		row = textfmt.ClipLine(row, width)
		if selected && focused {
			row = shadeLine(row, m.st.selBgSGR, width)
		}
		lines = append(lines, row)
	}
	return lines
}

// itemOf is the display item of row i.
func (m agentPaneModel) itemOf(i int) int {
	for j, it := range m.paneItems() {
		if it == i {
			return j
		}
	}
	return 0
}

// sideLines is the side column: the account the bot is on with the chats it
// answers, then its newest messages.
func (m agentPaneModel) sideLines(width int) []string {
	head := func(text string) string { return m.st.dim.Bold(true).Render(text) }
	lines := []string{head("connected"),
		m.st.badge.Render(" "+backendTag(m.acct.BackendType)+" ") + " " +
			truncate(accountLabel(m.acct), width-5)}
	chat := func(text string) {
		lines = append(lines, "    "+m.st.subtle.Render(truncate(text, width-4)))
	}
	if bot, ok := m.mgr.Agents.Registration(m.rec.ID); ok {
		lines = append(lines, "    "+m.st.agent.Render(truncate(agentIcon+" @"+bot.Username, width-4)))
	}
	switch {
	case m.rec.AllChats:
		chat("@ all private chats")
	case len(m.chats) == 0 && !m.rec.SelfChat:
		chat("no chats yet")
	}
	if !m.rec.AllChats {
		for _, c := range m.chats {
			chat("@ " + textfmt.Plain(c.Title))
		}
	}
	if m.rec.SelfChat {
		chat("@ you (self)")
	}
	lines = append(lines, "", head("latest"))
	if len(m.latest) == 0 {
		lines = append(lines, m.st.subtle.Render("nothing yet"))
	}
	const timeW = 11
	for _, msg := range m.latest {
		who := textfmt.Plain(msg.Sender)
		style := lipgloss.NewStyle()
		switch {
		case msg.IsAgentReply:
			who, style = agentIcon+" "+textfmt.Plain(m.rec.Name), m.st.agent
		case msg.Outgoing:
			who = "you"
		}
		when := shortWhen(msg.Timestamp, time.Now())
		text := strings.Join(strings.Fields(textfmt.Plain(msg.Text)), " ")
		rest := truncate(who+" "+text, width-timeW-1)
		name := rest
		if len(rest) > len(who) {
			name = rest[:len(who)]
		}
		lines = append(lines, m.st.dim.Render(when)+strings.Repeat(" ",
			timeW+1-textfmt.DisplayWidth(when))+style.Render(name)+
			m.st.subtle.Render(rest[len(name):]))
	}
	return lines
}

// shortWhen is a compact message time: the time today, yday, the weekday this
// week, else the date.
func shortWhen(at, now time.Time) string {
	at = at.Local()
	day := func(t time.Time) time.Time {
		y, mo, d := t.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
	}
	switch days := int(day(now).Sub(day(at)).Hours() / 24); {
	case days == 0:
		return at.Format("15:04")
	case days == 1:
		return "yday " + at.Format("15:04")
	case days < 7:
		return at.Format("Mon 15:04")
	}
	return at.Format("Jan 2")
}

// rowAt maps a row y of the pane, counted from its top edge, to a row index.
func (m agentPaneModel) rowAt(y int) (int, bool) {
	height := m.height - 2 - agentPaneHeader
	items := m.paneItems()
	j := window(m.itemOf(m.sel), height) + y - 1 - agentPaneHeader
	if y-1 < agentPaneHeader || j < 0 || j >= len(items) || items[j] < 0 {
		return 0, false
	}
	return items[j], true
}

func (m agentPaneModel) helpKeys() []helpKey {
	switch {
	case m.picker != nil:
		return m.picker.helpKeys()
	case m.confirm != nil:
		return m.confirm.helpKeys()
	case m.editing:
		return []helpKey{{"enter", "save"}, {"esc", "cancel"}}
	}
	return []helpKey{{"↑↓", "select"}, {"enter", "edit"}, {"p", "pause/resume"}, {"esc", "back"}}
}

// anyEnabled reports whether one of the bots is on.
func anyEnabled(bots []agent.AgentRecord) bool {
	for _, rec := range bots {
		if rec.Enabled {
			return true
		}
	}
	return false
}

// botNames joins the bots' names for a summary line.
func botNames(bots []agent.AgentRecord) string {
	var names []string
	for _, rec := range bots {
		names = append(names, textfmt.Plain(rec.Name))
	}
	return strings.Join(names, ", ")
}
