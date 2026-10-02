package main

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type accountChoice struct {
	index  int
	add    bool
	agents bool
	quit   bool
}

type accountListModel struct {
	ctx      context.Context
	mgr      *accountManager
	accounts []*account
	active   *account
	selected int
	notice   string
	result   *accountChoice
	width    int
	height   int
}

type accountsChanged struct{}

func listenAccounts(mgr *accountManager) tea.Cmd {
	return func() tea.Msg {
		<-mgr.changed
		return accountsChanged{}
	}
}

func newAccountListModel(ctx context.Context, mgr *accountManager, active *account) accountListModel {
	w, h := termSize()
	selected := 0
	for i, a := range mgr.accounts {
		if a == active {
			selected = i
		}
	}
	return accountListModel{
		ctx:      ctx,
		mgr:      mgr,
		accounts: mgr.accounts,
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
		m.accounts = m.mgr.accounts
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
			if m.active != nil && m.active.snapshot().status == "online" {
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
				m.mgr.start(m.ctx, m.accounts[m.selected], nil)
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
			s := a.snapshot()
			if s.status == "online" || s.needsLogin || s.identity.ID == 0 {
				m.result = &accountChoice{index: m.selected}
				return m, tea.Quit
			}
			m.mgr.start(m.ctx, a, nil)
		}
	}
	return m, nil
}

func (m accountListModel) View() tea.View {
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render("Accounts"))
	b.WriteByte('\n')

	limit := max(1, m.height-5)
	start := max(0, m.selected-limit+1)
	end := min(len(m.accounts)+3, start+limit)

	for i := start; i < end; i++ {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		switch {
		case i == len(m.accounts):
			b.WriteString(fmt.Sprintf("%s    Agents…\n", marker))
		case i == len(m.accounts)+1:
			b.WriteString(fmt.Sprintf("%s    Add account…\n", marker))
		case i == len(m.accounts)+2:
			b.WriteString(fmt.Sprintf("%s    Quit\n", marker))
		default:
			a := m.accounts[i]
			s := a.snapshot()
			current := ""
			if a == m.active {
				current = " [current]"
			}
			b.WriteString(clipLine(fmt.Sprintf("%s%s — %s%s", marker, a.label(), s.status, current), m.width))
			b.WriteByte('\n')
		}
	}

	if m.selected < len(m.accounts) {
		if notice := m.accounts[m.selected].snapshot().notice; notice != "" {
			b.WriteString(plain(notice))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar(accountListHelp, m.width))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

var accountListHelp = []helpKey{
	{"↑↓", "select"},
	{"⏎", "switch"},
	{"r", "reconnect"},
	{"Esc", "back"},
	{"q", "quit"},
}

func tuiChooseAccount(ctx context.Context, mgr *accountManager, active *account) (*account, error) {
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
	return mgr.accounts[final.result.index], nil
}

type addAccountModel struct {
	selected int
	prompt   *textinput.Model
	result   string
	width    int
	height   int
}

type addAccountResult struct {
	backendType string
	serverURL   string
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
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render("Add account"))
	b.WriteByte('\n')
	b.WriteByte('\n')

	for i, opt := range addAccountOptions {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		b.WriteString(fmt.Sprintf("%s%d  %s\n", marker, i+1, opt.label))
	}

	if m.prompt != nil {
		b.WriteByte('\n')
		opt := addAccountOptions[m.selected]
		label := "Server URL: "
		if opt.key == "matrix" {
			label = "Homeserver URL: "
		}
		b.WriteString(label)
		b.WriteString(m.prompt.View())
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	help := []helpKey{{"↑↓", "select"}, {"⏎", "confirm"}, {"Esc", "cancel"}}
	b.WriteString(renderHelpBar(help, m.width))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func tuiPickAndAdd(ctx context.Context, mgr *accountManager) (*account, error) {
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
		return mgr.addTelegram()
	case "mattermost":
		return mgr.addMattermost(parts[1])
	case "matrix":
		return mgr.addMatrix(parts[1])
	case "whatsapp":
		return mgr.addWhatsApp()
	}
	return nil, context.Canceled
}

type loginModel struct {
	ctx      context.Context
	mgr      *accountManager
	a        *account
	requests chan loginPrompt
	label    string
	input    textinput.Model
	current  *loginPrompt
	notice   string
	done     bool
	width    int
	height   int
}

type loginPromptMsg struct {
	req loginPrompt
}

type loginDoneMsg struct{}

func newLoginModel(ctx context.Context, mgr *accountManager, a *account, requests chan loginPrompt) loginModel {
	w, h := termSize()
	return loginModel{
		ctx:      ctx,
		mgr:      mgr,
		a:        a,
		requests: requests,
		label:    a.label(),
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

func listenLoginPrompt(requests chan loginPrompt) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-requests
		if !ok {
			return loginDoneMsg{}
		}
		return loginPromptMsg{req}
	}
}

func listenLoginDone(a *account) tea.Cmd {
	return func() tea.Msg {
		if a.done != nil {
			<-a.done
		}
		return loginDoneMsg{}
	}
}

func (m loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case accountsChanged:
		s := m.a.snapshot()
		if s.status == "online" {
			m.done = true
			return m, tea.Quit
		}
		if notice := s.notice; notice != "" {
			m.notice = notice
		}
		return m, listenAccounts(m.mgr)

	case loginPromptMsg:
		m.current = &msg.req
		ti := textinput.New()
		if msg.req.masked {
			ti.EchoMode = textinput.EchoPassword
		}
		ti.Focus()
		m.input = ti
		return m, tea.Batch(ti.Focus(), listenLoginPrompt(m.requests))

	case loginDoneMsg:
		s := m.a.snapshot()
		if s.status == "online" {
			m.done = true
			return m, tea.Quit
		}
		m.notice = s.notice
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
			m.current.answer <- lineResult{err: context.Canceled}
			m.current = nil
			return m, tea.Quit
		case "enter":
			value := m.input.Value()
			m.current.answer <- lineResult{text: value}
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
	var b strings.Builder
	b.WriteString(styleTitle.Width(m.width).Render(fmt.Sprintf("Sign in — %s", m.label)))
	b.WriteByte('\n')
	b.WriteByte('\n')

	if m.notice != "" {
		b.WriteString(m.notice)
		b.WriteByte('\n')
	}

	if m.current != nil {
		b.WriteString(m.current.label)
		b.WriteString(m.input.View())
		b.WriteByte('\n')
	} else {
		b.WriteString("Waiting for server…\n")
	}

	b.WriteByte('\n')
	b.WriteString(renderHelpBar([]helpKey{{"⏎", "submit"}, {"Esc", "cancel"}}, m.width))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func tuiLogin(ctx context.Context, mgr *accountManager, a *account) error {
	if a.cancel != nil {
		a.cancel()
		<-a.done
	}
	requests := make(chan loginPrompt)
	loginCtx, cancel := context.WithCancel(ctx)
	prompt := func(label string, masked bool) (string, error) {
		if label == "Phone (international format): " {
			if phone := a.snapshot().identity.Phone; phone != "" {
				return "+" + strings.TrimPrefix(phone, "+"), nil
			}
		}
		r := loginPrompt{label, masked, make(chan lineResult, 1)}
		select {
		case requests <- r:
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
		select {
		case result := <-r.answer:
			return result.text, result.err
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
	}
	mgr.start(loginCtx, a, prompt)
	stopAccount := a.cancel
	a.cancel = func() { cancel(); stopAccount() }

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
		<-a.done
		if notice := a.snapshot().notice; notice != "" {
			return fmt.Errorf("account sign-in: %s", notice)
		}
		return context.Canceled
	}
	return nil
}

type connectingModel struct {
	ctx    context.Context
	mgr    *accountManager
	a      *account
	done   bool
	width  int
	height int
}

type connectingDone struct{}

func newConnectingModel(ctx context.Context, mgr *accountManager, a *account) connectingModel {
	w, h := termSize()
	return connectingModel{ctx: ctx, mgr: mgr, a: a, width: w, height: h}
}

func (m connectingModel) Init() tea.Cmd {
	return tea.Batch(listenAccounts(m.mgr), listenLoginDone(m.a))
}

func (m connectingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case accountsChanged:
		s := m.a.snapshot()
		if (s.backend != nil && s.status == "online") || s.needsLogin {
			m.done = true
			return m, tea.Quit
		}
		return m, listenAccounts(m.mgr)

	case connectingDone:
		m.done = true
		return m, tea.Quit

	case tea.KeyPressMsg:
		if msg.String() == "esc" || msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m connectingModel) View() tea.View {
	label := m.a.label()
	content := fmt.Sprintf("Connecting to %s…\n\nPress Esc to go back.", label)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func tuiWaitConnecting(ctx context.Context, mgr *accountManager, a *account) bool {
	m := newConnectingModel(ctx, mgr, a)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return false
	}
	return result.(connectingModel).done
}
