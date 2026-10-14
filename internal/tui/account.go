package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/timer"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/textfmt"
)

type accountChoice struct {
	index    int
	add      bool
	agents   bool
	settings bool
	quit     bool
}

type accountListModel struct {
	st       *styles
	ctx      context.Context
	mgr      *account.AccountManager
	accounts []*account.Account
	active   *account.Account
	selected int
	notice   string
	result   *accountChoice
	digest   digestModel
	widgets  widgetsModel
	quitting *quitModal
	width    int
	height   int
}

type accountsChanged struct{}

func listenAccounts(mgr *account.AccountManager) tea.Cmd {
	changes := mgr.Changes()
	return func() tea.Msg {
		<-changes
		return accountsChanged{}
	}
}

func newAccountListModel(ctx context.Context, mgr *account.AccountManager, active *account.Account) accountListModel {
	w, h := termSize()
	selected := 0
	for i, a := range mgr.Accounts {
		if a == active {
			selected = i
		}
	}
	st := newStyles(loadTheme(mgr.Store))
	return accountListModel{
		st:       st,
		ctx:      ctx,
		mgr:      mgr,
		accounts: mgr.Accounts,
		active:   active,
		selected: selected,
		digest:   newDigest(ctx, st, mgr),
		widgets:  newWidgets(ctx, st, mgr.Plugins),
		width:    w,
		height:   h,
	}
}

func (m accountListModel) maxIndex() int { return len(m.accounts) + 3 }

func (m accountListModel) Init() tea.Cmd {
	_, widgets := m.widgets.start(m.width - 4)
	return tea.Batch(listenAccounts(m.mgr), func() tea.Msg { return digestSync{} }, widgets)
}

func (m accountListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case accountsChanged:
		m.accounts = m.mgr.Accounts
		if m.selected > m.maxIndex() {
			m.selected = m.maxIndex()
		}
		var sync tea.Cmd
		m.digest, sync = m.digest.sync()
		return m, tea.Batch(listenAccounts(m.mgr), sync)

	case digestSync:
		var sync tea.Cmd
		m.digest, sync = m.digest.sync()
		return m, sync

	case digestLoaded:
		m.digest = m.digest.loaded(msg)

	case widgetTick:
		var cmd tea.Cmd
		m.widgets, cmd = m.widgets.tick(m.width - 4)
		return m, cmd

	case widgetUpdated:
		m.widgets = m.widgets.updated(msg)

	case tea.MouseWheelMsg:
		if k, ok := wheelKey(msg); ok {
			return m.Update(k)
		}

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			break
		}
		if m.quitting != nil {
			x, y, w, h := m.quitRect()
			if msg.X < x || msg.X >= x+w || msg.Y < y || msg.Y >= y+h {
				m.quitting = nil
			} else if q, ok := m.quitting.hit(msg.Y - y); ok {
				m.quitting = &q
				return m.Update(keyPress(tea.KeyEnter))
			}
			return m, nil
		}
		if i, ok := m.itemAt(msg.Y); ok {
			m.selected = i
			return m.Update(keyPress(tea.KeyEnter))
		}
		_, shift, digest, _ := m.layout()
		top := 2 + shift + len(m.accounts) + 5 + digestGap // the cards' first screen row
		if a, ok := m.digest.cardAt(msg.X-2, msg.Y-top, m.width-4); ok && msg.Y-top < digest {
			if i := slices.Index(m.accounts, a); i >= 0 {
				m.selected = i
				return m.Update(keyPress(tea.KeyEnter))
			}
		}

	case tea.KeyPressMsg:
		if m.quitting != nil {
			q, quit, done := m.quitting.key(msg)
			m.quitting = &q
			switch {
			case quit:
				m.result = &accountChoice{quit: true}
				return m, tea.Quit
			case done:
				m.quitting = nil
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = &quitModal{st: m.st}
		case "esc":
			if m.canGoBack() {
				m.result = &accountChoice{index: -1}
				return m, tea.Quit
			}
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < m.maxIndex() {
				m.selected++
			}
		case "r":
			if m.selected < len(m.accounts) {
				m.mgr.Start(m.ctx, m.accounts[m.selected], nil)
			}
		case "enter":
			if m.selected == len(m.accounts)+3 {
				m.quitting = &quitModal{st: m.st}
				return m, nil
			}
			if m.selected == len(m.accounts)+2 {
				m.result = &accountChoice{add: true}
				return m, tea.Quit
			}
			if m.selected == len(m.accounts)+1 {
				m.result = &accountChoice{settings: true}
				return m, tea.Quit
			}
			if m.selected == len(m.accounts) {
				m.result = &accountChoice{agents: true}
				return m, tea.Quit
			}
			a := m.accounts[m.selected]
			s := a.Snapshot()
			if s.Status == "online" || s.NeedsLogin || s.Identity.ID == 0 {
				m.result = &accountChoice{index: m.selected}
				return m, tea.Quit
			}
			m.mgr.Start(m.ctx, a, nil)
		}
	}
	return m, nil
}

func (m accountListModel) bodyHeight() int { return max(4, m.height-2) }

// digestGap is the blank line and the divider above the digest's cards.
const digestGap = 2

// layout returns the logo when it fits above the rows, the digest and the
// widgets, and how far the rows are moved down to make room for it, or up to
// keep the selection in view; digest and widgets are the heights left for
// their cards under the rows. The widgets give way to the digest.
func (m accountListModel) layout() (logo []string, shift, digest, widgets int) {
	rows := len(m.accounts) + 5 // the accounts, a separator and four actions
	limit := m.bodyHeight() - 2
	cards := m.digest.height(m.width - 4)
	wants := m.widgets.height(m.width - 4)
	logo = strings.Split(m.st.logo(m.width-4), "\n")
	section := 0
	if cards > 0 {
		section = digestGap + cards
	}
	if wants > 0 {
		section += digestGap + wants
	}
	if rows+len(logo)+2+section <= limit {
		return logo, len(logo) + 2, cards, wants
	}
	selRow := m.selected
	if m.selected >= len(m.accounts) {
		selRow++
	}
	if shift = -max(0, selRow-limit+1); shift < 0 {
		return nil, shift, 0, 0
	}
	left := limit - rows
	if cards > 0 && left-digestGap >= digestCardH {
		digest = min(cards, left-digestGap)
		left -= digestGap + digest
	}
	if wants > 0 && left-digestGap >= wants {
		widgets = wants
	}
	return nil, 0, digest, widgets
}

// itemAt maps a screen row to an account or action; the separator is none.
func (m accountListModel) itemAt(y int) (int, bool) {
	_, shift, _, _ := m.layout()
	r := y - 2 - shift // under the header and the box's top edge
	switch {
	case r < 0 || y >= m.bodyHeight():
		return 0, false
	case r < len(m.accounts):
		return r, true
	case r == len(m.accounts):
		return 0, false
	}
	return r - 1, r-1 <= m.maxIndex()
}

func (m accountListModel) View() tea.View {
	notice := ""
	if m.selected < len(m.accounts) {
		notice = m.accounts[m.selected].Snapshot().Notice
	}
	if p := m.mgr.Plugins; notice == "" && p != nil && len(p.Errors) > 0 {
		notice = textfmt.CleanError(p.Errors[0])
	}
	body := m.frame()
	keys := m.helpKeys()
	if m.quitting != nil {
		x, y, _, _ := m.quitRect()
		body = m.st.fadeUnder(body, m.quitting.View(), x, y)
		keys, notice = quitModalHelp, ""
	}
	content := body + "\n" + m.st.footer(keys, notice, m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// frame is the screen above the footer, also the backdrop of the modals
// opened from it.
func (m accountListModel) frame() string {
	inner := m.width - 4
	var rows []string
	for i, a := range m.accounts {
		s := a.Snapshot()
		marker := "  "
		if i == m.selected {
			marker = m.st.accent.Render("> ")
		}
		label := truncate(textfmt.Plain(a.Label()), max(8, inner/2))
		if a == m.active {
			label = styleTitle.Render(label)
		}
		status := s.Status
		if a == m.active {
			status += " · current"
		}
		row := fmt.Sprintf("%s%d %s %s  %s  %s", marker, i+1, m.st.accountGlyph(s), label,
			m.st.dim.Render(backendTag(a.BackendType)), m.st.subtle.Render(status))
		if m.mgr.Agents != nil {
			if name := m.mgr.Agents.AgentName(a); name != "" {
				row += "  " + m.st.agent.Render("agent: "+textfmt.Plain(name))
			}
		}
		if i == m.selected {
			row = m.st.selectedRow.Render(row)
		}
		rows = append(rows, row)
	}
	rows = append(rows, m.st.dim.Render(strings.Repeat("─", inner)))
	for i, label := range []string{"Chat Agents…", "Settings…", "Add account…", "Quit"} {
		index := len(m.accounts) + i
		if index == m.selected {
			rows = append(rows, m.st.selectedRow.Render(m.st.accent.Render("> ")+label))
		} else {
			rows = append(rows, m.st.subtle.Render("  "+label))
		}
	}
	logo, shift, digest, widgets := m.layout()
	if digest > 0 {
		rows = append(rows, "", m.st.dim.Render(sectionRule("── Unread ", inner)))
		rows = append(rows, strings.Split(m.digest.view(inner, digest), "\n")...)
	}
	if widgets > 0 {
		rows = append(rows, "", m.st.dim.Render(sectionRule("── Widgets ", inner)))
		rows = append(rows, strings.Split(m.widgets.view(inner, widgets), "\n")...)
	}
	if shift > 0 {
		var top []string
		for _, line := range logo {
			top = append(top, lipgloss.PlaceHorizontal(inner, lipgloss.Center, line))
		}
		rows = append(append(top, "", ""), rows...)
	} else {
		rows = rows[-shift:]
	}
	return m.st.header(m.width) + "\n" +
		renderBox("Accounts", rows, m.width, m.bodyHeight(), m.st.accentColor)
}

// sectionRule is a divider that names the section under it.
func sectionRule(label string, width int) string {
	return label + strings.Repeat("─", max(0, width-textfmt.DisplayWidth(label)))
}

// quitRect is where the quit modal sits: centred over the screen.
func (m accountListModel) quitRect() (x, y, w, h int) {
	w, h = m.quitting.size()
	return max(0, (m.width-w)/2), max(0, (m.bodyHeight()+1-h)/2), w, h
}

// canGoBack reports whether Esc can return to the account it came from.
func (m accountListModel) canGoBack() bool {
	return m.active != nil && m.active.Snapshot().Status == "online"
}

func (m accountListModel) helpKeys() []helpKey {
	keys := []helpKey{{"↑↓", "select"}, {"enter", "switch"}, {"r", "reconnect"}}
	if m.canGoBack() {
		keys = append(keys, helpKey{"esc", "back"})
	}
	return append(keys, helpKey{"q", "quit"})
}

func tuiChooseAccount(ctx context.Context, mgr *account.AccountManager, active *account.Account) (*account.Account, error) {
	m := newAccountListModel(ctx, mgr, active)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("run account chooser: %w", err)
	}
	final := result.(accountListModel)
	if final.result == nil {
		return nil, nil
	}
	if final.result.quit {
		return nil, nil
	}
	if final.result.agents {
		return nil, agentsRequested{}
	}
	if final.result.settings {
		to, err := tuiSettings(ctx, mgr, active, true)
		if _, back := err.(accountsRequested); back {
			if !slices.Contains(mgr.List(), active) {
				active = nil // removed in Settings
			}
			return tuiChooseAccount(ctx, mgr, active)
		}
		return to, err
	}
	if final.result.add {
		return tuiPickAndAdd(ctx, mgr, final.frame())
	}
	if final.result.index < 0 {
		return active, nil
	}
	return mgr.Accounts[final.result.index], nil
}

type addAccountModel struct {
	st       *styles
	wiz      wizardModel
	answers  wizardAnswers // nil when cancelled
	backdrop string        // the screen it was opened from, faded behind it
	width    int
	height   int
}

var addAccountOptions = []struct {
	key   string
	label string
}{
	{"telegram", "Telegram"},
	{"mattermost", "Mattermost"},
	{"matrix", "Matrix"},
	{"whatsapp", "WhatsApp"},
	{"email", "Email (Gmail / IMAP)"},
}

// addAccountSteps asks for Telegram API credentials only when none are configured.
func addAccountSteps(st *styles, needsAPI bool) []wizardStep {
	needsServer := func(a wizardAnswers) bool {
		return a["network"] == "mattermost" || a["network"] == "matrix" || a["network"] == "email"
	}
	skipAPI := func(a wizardAnswers) bool { return !needsAPI || a["network"] != "telegram" }
	return []wizardStep{
		{
			key:   "network",
			label: "Network",
			kind:  wizChoice,
			ask:   say("Which network is the new account on?"),
			options: func(wizardAnswers) []wizardOption {
				labelW := 0
				for _, o := range addAccountOptions {
					labelW = max(labelW, len(o.label))
				}
				var opts []wizardOption
				for _, o := range addAccountOptions {
					label := fmt.Sprintf("%-*s  %s", labelW, o.label, st.dim.Render(backendTag(o.key)))
					opts = append(opts, wizardOption{label: label, value: o.key})
				}
				return opts
			},
			show: func(v string, _ wizardAnswers) string {
				for _, o := range addAccountOptions {
					if o.key == v {
						return o.label
					}
				}
				return v
			},
		},
		{
			key:   "server",
			label: "Server",
			kind:  wizText,
			ask: func(a wizardAnswers) string {
				switch a["network"] {
				case "matrix":
					return "Which Matrix homeserver?"
				case "email":
					return "Which IMAP server? Gmail and Google Workspace use imap.gmail.com."
				}
				return "What's your Mattermost server URL?"
			},
			def: func(a wizardAnswers) string {
				if a["network"] == "email" {
					return "imap.gmail.com"
				}
				return ""
			},
			skip: func(a wizardAnswers) bool { return !needsServer(a) },
			validate: func(v string, a wizardAnswers) error {
				if a["network"] == "email" {
					if v == "" || strings.ContainsAny(v, "/: ") {
						return errors.New("Give just the host name, like imap.gmail.com.")
					}
					return nil
				}
				if u, err := url.Parse(withScheme(v)); err != nil || u.Host == "" {
					return errors.New("That doesn't look like a server address.")
				}
				return nil
			},
		},
		{
			key:   "api_id",
			label: "API ID",
			kind:  wizText,
			ask: say("Telegram needs an API ID and hash from https://my.telegram.org/apps. " +
				"What's your API ID?"),
			skip: skipAPI,
			validate: func(v string, _ wizardAnswers) error {
				if id, err := strconv.Atoi(v); err != nil || id <= 0 {
					return errors.New("The API ID is a positive number.")
				}
				return nil
			},
		},
		{
			key:    "api_hash",
			label:  "API hash",
			masked: true,
			kind:   wizText,
			ask:    say("And the API hash?"),
			skip:   skipAPI,
		},
	}
}

// withScheme defaults a bare server address to https.
func withScheme(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://" + s
}

func newAddAccountModel(st *styles, needsAPI bool, backdrop string) addAccountModel {
	w, h := termSize()
	return addAccountModel{
		st:       st,
		wiz:      newWizard(st, "Add account", addAccountSteps(st, needsAPI), nil),
		backdrop: backdrop,
		width:    w,
		height:   h,
	}
}

func (m addAccountModel) Init() tea.Cmd { return m.wiz.Init() }

func (m addAccountModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case wizardFinished:
		m.answers = msg.answers
		return m, tea.Quit
	case wizardCancelled:
		return m, tea.Quit
	case tea.MouseWheelMsg:
		if k, ok := wheelKey(msg); ok {
			return m.Update(k)
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		box, top := m.form()
		x, y := modalRect(m.width, m.bodyHeight(), lipgloss.Width(box), lipgloss.Height(box))
		if msg.X < x || msg.X >= x+lipgloss.Width(box) {
			return m, nil
		}
		var cmd tea.Cmd
		m.wiz, cmd = m.wiz.formClick(msg.Y-y-1+top, lipgloss.Width(box)-4) // under the top edge
		return m, cmd
	}
	var cmd tea.Cmd
	m.wiz, cmd = m.wiz.Update(msg)
	return m, cmd
}

func (m addAccountModel) bodyHeight() int { return max(4, m.height-1) }

// form is the wizard's modal and how many of its lines scrolled off the top.
func (m addAccountModel) form() (string, int) {
	w := min(formModalWidth, m.width-2)
	lines, _ := m.wiz.formView(w - 4)
	return formBox(m.wiz.title, lines, w, m.bodyHeight(), m.st)
}

// formBox draws a form modal tall enough for lines, keeping the last ones
// when it doesn't fit, and returns how many lines were cut from the top.
func formBox(title string, lines []string, width, bodyHeight int, st *styles) (string, int) {
	h := min(bodyHeight, max(8, len(lines)+2))
	top := max(0, len(lines)-(h-2))
	return renderBox(title, lines[top:], width, h, st.accentColor), top
}

func (m addAccountModel) View() tea.View {
	box, _ := m.form()
	content := m.st.overlay(m.backdrop, box, m.width, m.bodyHeight()) + "\n" +
		m.st.footer(m.wiz.helpKeys(), "", m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func tuiPickAndAdd(ctx context.Context, mgr *account.AccountManager, backdrop string) (*account.Account, error) {
	m := newAddAccountModel(newStyles(loadTheme(mgr.Store)), !mgr.HasTelegramCredentials(), backdrop)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("run add account: %w", err)
	}
	answers := result.(addAccountModel).answers
	switch answers["network"] {
	case "telegram":
		if v := answers["api_id"]; v != "" {
			id, _ := strconv.Atoi(strings.TrimSpace(v)) // validated by the wizard
			if err := mgr.SetTelegramCredentials(id, strings.TrimSpace(answers["api_hash"])); err != nil {
				return nil, err
			}
		}
		return mgr.AddTelegram()
	case "mattermost":
		return mgr.AddMattermost(withScheme(answers["server"]))
	case "matrix":
		return mgr.AddMatrix(withScheme(answers["server"]))
	case "whatsapp":
		return mgr.AddWhatsApp()
	case "email":
		return mgr.AddEmail(strings.TrimSpace(answers["server"]))
	}
	return nil, context.Canceled
}

type loginModel struct {
	st       *styles
	ctx      context.Context
	mgr      *account.AccountManager
	a        *account.Account
	requests chan account.LoginPrompt
	label    string
	input    textinput.Model
	current  *account.LoginPrompt
	turns    []loginTurn
	notice   string
	// pair counts down the time WhatsApp leaves for pairing, from pairFull.
	pair     *timer.Model
	pairFull time.Duration
	backdrop string // the screen sign-in was opened from, faded behind it
	done     bool
	width    int
	height   int
}

// loginTurn is one answered sign-in prompt, kept for the transcript.
type loginTurn struct{ ask, reply string }

type loginPromptMsg struct {
	req account.LoginPrompt
}

type loginDoneMsg struct{}

func newLoginModel(ctx context.Context, mgr *account.AccountManager, a *account.Account,
	requests chan account.LoginPrompt, backdrop string) loginModel {
	w, h := termSize()
	return loginModel{
		st:       newStyles(loadTheme(mgr.Store)),
		ctx:      ctx,
		mgr:      mgr,
		a:        a,
		requests: requests,
		label:    a.Label(),
		backdrop: backdrop,
		width:    w,
		height:   h,
	}
}

func (m loginModel) Init() tea.Cmd {
	return tea.Batch(
		listenLoginPrompt(m.requests),
		listenAccounts(m.mgr),
		listenLoginDone(m.a),
	)
}

func listenLoginPrompt(requests chan account.LoginPrompt) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-requests
		if !ok {
			return loginDoneMsg{}
		}
		return loginPromptMsg{req}
	}
}

func listenLoginDone(a *account.Account) tea.Cmd {
	return func() tea.Msg {
		if a.Done != nil {
			<-a.Done
		}
		return loginDoneMsg{}
	}
}

func (m loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case accountsChanged:
		s := m.a.Snapshot()
		if s.Status == "online" {
			m.done = true
			return m, tea.Quit
		}
		if notice := s.Notice; notice != "" {
			m.notice = notice
		}
		var start tea.Cmd
		if m.pair == nil && !s.PairDeadline.IsZero() {
			m.pairFull = time.Until(s.PairDeadline).Round(time.Second)
			t := timer.New(m.pairFull, timer.WithInterval(time.Second))
			m.pair, start = &t, t.Init()
		}
		return m, tea.Batch(listenAccounts(m.mgr), start)

	case timer.TickMsg, timer.StartStopMsg:
		if m.pair == nil {
			return m, nil
		}
		t, cmd := m.pair.Update(msg)
		m.pair = &t
		return m, cmd

	case loginPromptMsg:
		m.current = &msg.req
		ti := textinput.New()
		ti.Prompt = ""
		if msg.req.Masked {
			ti.EchoMode = textinput.EchoPassword
		}
		ti.Focus()
		m.input = ti
		return m, tea.Batch(ti.Focus(), listenLoginPrompt(m.requests))

	case loginDoneMsg:
		s := m.a.Snapshot()
		if s.Status == "online" {
			m.done = true
			return m, tea.Quit
		}
		m.notice = s.Notice
		if m.notice == "" {
			m.notice = "sign-in failed"
		}
		return m, tea.Quit

	case tea.KeyPressMsg:
		if m.current == nil {
			if msg.String() == "esc" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			m.current.Answer <- account.LoginAnswer{Err: context.Canceled}
			m.current = nil
			return m, tea.Quit
		case "enter":
			value := m.input.Value()
			reply := value
			if m.current.Masked {
				reply = "••••••"
			}
			m.turns = append(m.turns, loginTurn{loginLabel(m.current.Label), reply})
			m.current.Answer <- account.LoginAnswer{Text: value}
			m.notice = ""
			m.current = nil
			return m, nil
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}
	// Pastes, whether bracketed or Ctrl+V, arrive as their own messages.
	if m.current != nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m loginModel) View() tea.View {
	bodyHeight := max(4, m.height-1)
	w := min(formModalWidth, m.width-2)
	inner := w - 4
	labelW := 0
	for _, t := range m.turns {
		labelW = max(labelW, textfmt.DisplayWidth(t.ask))
	}
	var lines []string
	for _, t := range m.turns {
		// Long prompts leave no room beside them, so the reply goes below.
		if 2+labelW+2+textfmt.DisplayWidth(t.reply) > inner {
			lines = append(lines, m.st.formRow(t.ask, "", labelW, false, false, inner),
				textfmt.ClipLine("    "+m.st.subtle.Render(t.reply), inner))
			continue
		}
		lines = append(lines, m.st.formRow(t.ask, m.st.subtle.Render(t.reply), labelW,
			false, false, inner))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	if m.current != nil {
		lines = append(lines, styleTitle.Render(textfmt.ClipLine(loginLabel(m.current.Label), inner)), "")
		m.input.SetWidth(max(10, inner-4))
		lines = append(lines, m.st.accent.Render("› ")+m.input.View())
	} else {
		lines = append(lines, m.st.subtle.Render("Waiting for server…"))
	}
	if m.pair != nil {
		left := m.pair.Timeout
		width := max(10, inner-12)
		full := int(float64(width) * float64(left) / float64(max(time.Second, m.pairFull)))
		bar := m.st.accent.Render(strings.Repeat("━", full)) +
			m.st.dim.Render(strings.Repeat("━", width-full))
		lines = append(lines, "", bar+m.st.subtle.Render(
			fmt.Sprintf(" %d:%02d left", int(left.Minutes()), int(left.Seconds())%60)))
	}
	if m.notice != "" {
		lines = append(lines, styleError.Render(textfmt.ClipLine(textfmt.Plain(m.notice), inner)))
	}
	box, _ := formBox("Sign in — "+m.label, lines, w, bodyHeight, m.st)
	content := m.st.overlay(m.backdrop, box, m.width, bodyHeight) + "\n" +
		m.st.footer([]helpKey{{"enter", "answer"}, {"esc", "cancel"}}, "", m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// loginLabel turns a backend prompt like "Phone (international format): " into
// bubble text.
func loginLabel(label string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(label), ":"))
}

func tuiLogin(ctx context.Context, mgr *account.AccountManager, a *account.Account,
	backdrop string) error {
	if a.Cancel != nil {
		a.Cancel()
		<-a.Done
	}
	requests := make(chan account.LoginPrompt)
	loginCtx, cancel := context.WithCancel(ctx)
	prompt := func(label string, masked bool) (string, error) {
		if label == "Phone (international format): " {
			if phone := a.Snapshot().Identity.Phone; phone != "" {
				return "+" + strings.TrimPrefix(phone, "+"), nil
			}
		}
		r := account.LoginPrompt{Label: label, Masked: masked, Answer: make(chan account.LoginAnswer, 1)}
		select {
		case requests <- r:
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
		select {
		case result := <-r.Answer:
			return result.Text, result.Err
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
	}
	mgr.Start(loginCtx, a, prompt)
	stopAccount := a.Cancel
	a.Cancel = func() { cancel(); stopAccount() }

	m := newLoginModel(loginCtx, mgr, a, requests, backdrop)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		cancel()
		return fmt.Errorf("run login: %w", err)
	}
	final := result.(loginModel)
	if !final.done {
		cancel()
		<-a.Done
		if notice := a.Snapshot().Notice; notice != "" {
			return fmt.Errorf("account sign-in: %s", notice)
		}
		return context.Canceled
	}
	return nil
}

type connectingModel struct {
	st     *styles
	ctx    context.Context
	mgr    *account.AccountManager
	a      *account.Account
	spin   spinner.Model
	done   bool
	width  int
	height int
}

type connectingDone struct{}

func newConnectingModel(ctx context.Context, mgr *account.AccountManager, a *account.Account) connectingModel {
	w, h := termSize()
	st := newStyles(loadTheme(mgr.Store))
	spin := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(st.accent))
	return connectingModel{st: st, ctx: ctx, mgr: mgr, a: a, spin: spin, width: w, height: h}
}

func (m connectingModel) Init() tea.Cmd {
	return tea.Batch(listenAccounts(m.mgr), listenLoginDone(m.a), m.spin.Tick)
}

func (m connectingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case accountsChanged:
		s := m.a.Snapshot()
		if (s.Backend != nil && s.Status == "online") || s.NeedsLogin {
			m.done = true
			return m, tea.Quit
		}
		return m, listenAccounts(m.mgr)

	case connectingDone:
		m.done = true
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m connectingModel) View() tea.View {
	content := lipgloss.JoinVertical(lipgloss.Center,
		m.st.logo(m.width),
		"",
		m.spin.View()+"Connecting to "+textfmt.Plain(m.a.Label())+"…",
		"",
		m.st.dim.Render("esc to go back"),
	)
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content))
	v.AltScreen = true
	return v
}

func tuiWaitConnecting(ctx context.Context, mgr *account.AccountManager, a *account.Account) bool {
	m := newConnectingModel(ctx, mgr, a)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return false
	}
	return result.(connectingModel).done
}
