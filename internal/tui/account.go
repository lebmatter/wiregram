package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/textfmt"
)

type accountChoice struct {
	index  int
	add    bool
	agents bool
	quit   bool
}

type accountListModel struct {
	ctx      context.Context
	mgr      *account.AccountManager
	accounts []*account.Account
	active   *account.Account
	selected int
	notice   string
	result   *accountChoice
	width    int
	height   int
}

type accountsChanged struct{}

func listenAccounts(mgr *account.AccountManager) tea.Cmd {
	return func() tea.Msg {
		<-mgr.Changed
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
	return accountListModel{
		ctx:      ctx,
		mgr:      mgr,
		accounts: mgr.Accounts,
		active:   active,
		selected: selected,
		width:    w,
		height:   h,
	}
}

func (m accountListModel) maxIndex() int { return len(m.accounts) + 2 }

func (m accountListModel) Init() tea.Cmd {
	return listenAccounts(m.mgr)
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
		return m, listenAccounts(m.mgr)

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+d":
			m.result = &accountChoice{quit: true}
			return m, tea.Quit
		case "esc", "ctrl+c":
			if m.active != nil && m.active.Snapshot().Status == "online" {
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
			if m.selected == len(m.accounts)+2 {
				m.result = &accountChoice{quit: true}
				return m, tea.Quit
			}
			if m.selected == len(m.accounts)+1 {
				m.result = &accountChoice{add: true}
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

func (m accountListModel) View() tea.View {
	bodyHeight := max(4, m.height-2)
	inner := m.width - 4
	var rows []string
	for i, a := range m.accounts {
		s := a.Snapshot()
		marker := "  "
		if i == m.selected {
			marker = styleAccent.Render("> ")
		}
		label := truncate(textfmt.Plain(a.Label()), max(8, inner/2))
		if a == m.active {
			label = styleTitle.Render(label)
		}
		status := s.Status
		if a == m.active {
			status += " · current"
		}
		row := fmt.Sprintf("%s%d %s %s  %s  %s", marker, i+1, accountGlyph(s), label,
			styleDim.Render(backendTag(a.BackendType)), styleSubtle.Render(status))
		if i == m.selected {
			row = styleSelectedRow.Render(row)
		}
		rows = append(rows, row)
	}
	rows = append(rows, styleDim.Render(strings.Repeat("─", inner)))
	for i, label := range []string{"Agents…", "Add account…", "Quit"} {
		index := len(m.accounts) + i
		if index == m.selected {
			rows = append(rows, styleSelectedRow.Render(styleAccent.Render("> ")+label))
		} else {
			rows = append(rows, styleSubtle.Render("  "+label))
		}
	}
	selRow := m.selected
	if m.selected >= len(m.accounts) {
		selRow++
	}
	limit := bodyHeight - 2
	if logo := strings.Split(renderLogo(inner), "\n"); len(rows)+len(logo)+2 <= limit {
		var top []string
		for _, line := range logo {
			top = append(top, lipgloss.PlaceHorizontal(inner, lipgloss.Center, line))
		}
		rows = append(append(top, "", ""), rows...)
	} else if start := max(0, selRow-limit+1); start > 0 {
		rows = rows[start:]
	}

	notice := ""
	if m.selected < len(m.accounts) {
		notice = m.accounts[m.selected].Snapshot().Notice
	}
	content := renderHeader(nil, m.width) + "\n" +
		renderBox("Accounts", rows, m.width, bodyHeight, colorAccent) + "\n" +
		renderFooter(accountListHelp, false, notice, m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

var accountListHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "switch"},
	{"r", "reconnect"},
	{"esc", "back"},
	{"q", "quit"},
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
	if final.result.add {
		return tuiPickAndAdd(ctx, mgr)
	}
	if final.result.index < 0 {
		return active, nil
	}
	return mgr.Accounts[final.result.index], nil
}

type addAccountModel struct {
	selected int
	prompt   *textinput.Model
	result   string
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
}

func newAddAccountModel() addAccountModel {
	w, h := termSize()
	return addAccountModel{width: w, height: h}
}

func (m addAccountModel) Init() tea.Cmd { return nil }

func (m addAccountModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyPressMsg:
		if m.prompt != nil {
			return m.updatePrompt(msg)
		}
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+d":
			return m, tea.Quit
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < len(addAccountOptions)-1 {
				m.selected++
			}
		case "enter":
			opt := addAccountOptions[m.selected]
			if opt.key == "mattermost" || opt.key == "matrix" {
				ti := textinput.New()
				ti.Focus()
				m.prompt = &ti
				return m, ti.Focus()
			}
			m.result = opt.key
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m addAccountModel) updatePrompt(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+d":
		m.prompt = nil
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.prompt.Value())
		if value == "" {
			m.prompt = nil
			return m, nil
		}
		opt := addAccountOptions[m.selected]
		if opt.key == "mattermost" {
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				value = "https://" + value
			}
		} else if opt.key == "matrix" {
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				value = "https://" + value
			}
		}
		m.result = opt.key + ":" + value
		return m, tea.Quit
	default:
		ti, cmd := m.prompt.Update(msg)
		m.prompt = &ti
		return m, cmd
	}
}

func (m addAccountModel) View() tea.View {
	bodyHeight := max(4, m.height-2)
	inner := m.width - 4
	rows := []string{styleSubtle.Render("Which network is the new account on?"), ""}
	for i, opt := range addAccountOptions {
		row := fmt.Sprintf("  %d  %s  %s", i+1, opt.label, styleDim.Render(backendTag(opt.key)))
		if i == m.selected {
			row = styleSelectedRow.Render(styleAccent.Render("> ") + row[2:])
		}
		rows = append(rows, row)
	}
	if m.prompt != nil {
		title := "Server URL"
		if addAccountOptions[m.selected].key == "matrix" {
			title = "Homeserver URL"
		}
		rows = append(rows, "")
		rows = append(rows, composerBox(title, nil, "› ", *m.prompt, min(inner, 60), colorAccent)...)
	}
	help := []helpKey{{"↑↓", "select"}, {"⏎", "confirm"}, {"esc", "cancel"}}
	content := renderHeader(nil, m.width) + "\n" +
		renderBox("Add account", rows, m.width, bodyHeight, colorAccent) + "\n" +
		renderFooter(help, false, "", m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func tuiPickAndAdd(ctx context.Context, mgr *account.AccountManager) (*account.Account, error) {
	m := newAddAccountModel()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("run add account: %w", err)
	}
	final := result.(addAccountModel)
	if final.result == "" {
		return nil, context.Canceled
	}
	parts := strings.SplitN(final.result, ":", 2)
	switch parts[0] {
	case "telegram":
		return mgr.AddTelegram()
	case "mattermost":
		return mgr.AddMattermost(parts[1])
	case "matrix":
		return mgr.AddMatrix(parts[1])
	case "whatsapp":
		return mgr.AddWhatsApp()
	}
	return nil, context.Canceled
}

type loginModel struct {
	ctx      context.Context
	mgr      *account.AccountManager
	a        *account.Account
	requests chan account.LoginPrompt
	label    string
	input    textinput.Model
	current  *account.LoginPrompt
	notice   string
	done     bool
	width    int
	height   int
}

type loginPromptMsg struct {
	req account.LoginPrompt
}

type loginDoneMsg struct{}

func newLoginModel(ctx context.Context, mgr *account.AccountManager, a *account.Account, requests chan account.LoginPrompt) loginModel {
	w, h := termSize()
	return loginModel{
		ctx:      ctx,
		mgr:      mgr,
		a:        a,
		requests: requests,
		label:    a.Label(),
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
		return m, listenAccounts(m.mgr)

	case loginPromptMsg:
		m.current = &msg.req
		ti := textinput.New()
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
			if msg.String() == "esc" || msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+d":
			m.current.Answer <- account.LoginAnswer{Err: context.Canceled}
			m.current = nil
			return m, tea.Quit
		case "enter":
			value := m.input.Value()
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
	return m, nil
}

func (m loginModel) View() tea.View {
	bodyHeight := max(4, m.height-2)
	inner := m.width - 4
	var rows []string
	if m.current != nil {
		label := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m.current.Label), ":"))
		rows = append(rows, composerBox(label, nil, "› ", m.input, min(inner, 60), colorAccent)...)
	} else {
		rows = append(rows, styleSubtle.Render("Waiting for server…"))
	}
	if m.notice != "" {
		rows = append(rows, "", styleError.Render(textfmt.Plain(m.notice)))
	}
	content := renderHeader(nil, m.width) + "\n" +
		renderBox("Sign in — "+m.label, rows, m.width, bodyHeight, colorAccent) + "\n" +
		renderFooter([]helpKey{{"⏎", "submit"}, {"esc", "cancel"}}, false, "", m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func tuiLogin(ctx context.Context, mgr *account.AccountManager, a *account.Account) error {
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

	m := newLoginModel(loginCtx, mgr, a, requests)
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
	spin := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(styleAccent))
	return connectingModel{ctx: ctx, mgr: mgr, a: a, spin: spin, width: w, height: h}
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
		if msg.String() == "esc" || msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m connectingModel) View() tea.View {
	content := lipgloss.JoinVertical(lipgloss.Center,
		renderLogo(m.width),
		"",
		m.spin.View()+"Connecting to "+textfmt.Plain(m.a.Label())+"…",
		"",
		styleDim.Render("esc to go back"),
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
