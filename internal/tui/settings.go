package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/mail"
	"wiregram/internal/textfmt"
)

// settingsSections are the left pane's sections; the actions follow them.
var settingsSections = []string{"Accounts", "Email"}

const sectionEmail = 1

var settingsActions = []string{"Chat Agents…", "Quit"}

type settingsPane int

const (
	paneSections settingsPane = iota
	paneAccounts
)

type settingsResult struct {
	account *account.Account // switch to it
	back    bool             // return to the active account
	add     bool
	agents  bool
}

type settingsRemoved struct {
	a   *account.Account
	err error
}

// settingsModel is the two-pane Settings screen: sections on the left, the
// selected section's rows on the right.
type settingsModel struct {
	ctx      context.Context
	mgr      *account.AccountManager
	accounts []*account.Account
	active   *account.Account

	// fromAccounts: opened from the account chooser, so Esc goes back there.
	fromAccounts bool

	focus   settingsPane
	section int // left row: a section, then the actions
	sel     int // right row: an account, then "Add account…"

	result      settingsResult
	confirm     func() tea.Cmd
	confirmText string
	busy        bool // a removal is running
	notice      string
	fullHelp    bool
	width       int
	height      int
}

func newSettingsModel(ctx context.Context, mgr *account.AccountManager, active *account.Account, fromAccounts bool) settingsModel {
	w, h := termSize()
	m := settingsModel{ctx: ctx, mgr: mgr, accounts: mgr.List(), active: active,
		fromAccounts: fromAccounts, width: w, height: h}
	for i, a := range m.accounts {
		if a == active {
			m.sel = i
		}
	}
	m.focus = paneAccounts // Accounts comes first, so open it straight away
	return m
}

func (m settingsModel) Init() tea.Cmd { return listenAccounts(m.mgr) }

func (m settingsModel) leftRows() int { return len(settingsSections) + len(settingsActions) }

func (m settingsModel) canGoBack() bool {
	return m.fromAccounts || m.active != nil && m.active.Snapshot().Status == "online"
}

func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case accountsChanged:
		m.accounts = m.mgr.List()
		m.sel = clampIndex(m.sel, len(m.accounts)+1)
		return m, listenAccounts(m.mgr)
	case settingsRemoved:
		m.busy = false
		m.notice = "Removed " + textfmt.Plain(msg.a.Label())
		if msg.err != nil {
			m.notice = textfmt.CleanError(msg.err)
		}
		if msg.a == m.active {
			m.active = nil
		}
		m.accounts = m.mgr.List()
		m.sel = clampIndex(m.sel, len(m.accounts)+1)
		if len(m.accounts) == 0 {
			m.result.add = true
			return m, tea.Quit
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg.String())
	case tea.MouseWheelMsg:
		if k, ok := wheelKey(msg); ok && msg.Y < m.bodyHeight() {
			if m.twoPane() {
				m.focus = m.paneAt(msg.X)
			}
			return m.handleKey(k.String())
		}
	case tea.MouseClickMsg:
		return m.handleClick(msg.Mouse())
	}
	return m, nil
}

func (m settingsModel) handleKey(k string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	if m.confirm != nil {
		run := m.confirm
		m.confirm, m.confirmText = nil, ""
		if k == "y" || k == "Y" {
			m.busy, m.notice = true, "Signing out…"
			return m, run()
		}
		return m, nil
	}
	m.notice = ""
	switch k {
	case "?":
		m.fullHelp = !m.fullHelp
		return m, nil
	case "q", "ctrl+d":
		return m, tea.Quit
	case "alt+s":
		if m.canGoBack() {
			m.result.back = true
			return m, tea.Quit
		}
		return m, nil
	}
	if m.focus == paneSections {
		return m.sectionsKey(k)
	}
	if m.section == sectionEmail {
		return m.emailKey(k)
	}
	return m.accountsKey(k)
}

// emailKey handles the Email section, whose one row cycles how long the
// local mail cache keeps mail.
func (m settingsModel) emailKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c":
		if m.twoPane() && m.canGoBack() {
			m.result.back = true
			return m, tea.Quit
		}
		m.focus = paneSections
	case "tab", "left", "h":
		m.focus = paneSections
	case "enter", "space", " ", "right", "l":
		days := mail.RetentionDays(m.mgr.Store)
		next := mail.RetentionChoices[0]
		for i, d := range mail.RetentionChoices {
			if d == days {
				next = mail.RetentionChoices[(i+1)%len(mail.RetentionChoices)]
			}
		}
		if err := m.mgr.Store.SetSetting(mail.RetentionSetting, strconv.Itoa(next)); err != nil {
			m.notice = textfmt.CleanError(err)
		} else {
			m.notice = "Keeping " + retentionLabel(next) + " of mail in the local cache"
		}
	}
	return m, nil
}

func retentionLabel(days int) string {
	switch {
	case days == 0:
		return "all"
	case days%365 == 0:
		return fmt.Sprintf("%d %s", days/365, plural(days/365, "year", "years"))
	}
	return fmt.Sprintf("%d days", days)
}

func (m settingsModel) emailLines(width int) []string {
	focused := m.focus == paneAccounts
	value := styleSubtle.Render(retentionLabel(mail.RetentionDays(m.mgr.Store)))
	return []string{
		themedListRow("Keep mail", value, true, focused, width, colorAccent),
		"",
		styleSubtle.Render(textfmt.ClipLine("How long email stays cached in wiregram.db.", width)),
		styleSubtle.Render(textfmt.ClipLine("Mail on the server is never deleted.", width)),
	}
}

func (m settingsModel) sectionsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c":
		if m.canGoBack() {
			m.result.back = true
			return m, tea.Quit
		}
	case "up", "k":
		m.section = max(0, m.section-1)
	case "down", "j":
		m.section = min(m.leftRows()-1, m.section+1)
	case "enter", "tab", "right", "l":
		switch m.section - len(settingsSections) {
		case 0:
			if k == "enter" {
				m.result.agents = true
				return m, tea.Quit
			}
		case 1:
			if k == "enter" {
				return m, tea.Quit
			}
		default:
			m.focus = paneAccounts
		}
	}
	return m, nil
}

func (m settingsModel) accountsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c":
		if m.twoPane() && m.canGoBack() {
			m.result.back = true
			return m, tea.Quit
		}
		m.focus = paneSections
	case "tab", "left", "h":
		m.focus = paneSections
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(len(m.accounts), m.sel+1)
	case "n":
		m.result.add = true
		return m, tea.Quit
	case "r":
		if m.sel < len(m.accounts) {
			m.mgr.Start(m.ctx, m.accounts[m.sel], nil)
		}
	case "d":
		if m.sel < len(m.accounts) {
			a := m.accounts[m.sel]
			skip := !digestSkipped(m.mgr, a)
			m.notice = accountShort(m.mgr, m.mgr.AccountKey(a)) + " shown in the unread digest"
			if skip {
				m.notice = accountShort(m.mgr, m.mgr.AccountKey(a)) + " hidden from the unread digest"
			}
			if err := setDigestSkipped(m.mgr, a, skip); err != nil {
				m.notice = textfmt.CleanError(err)
			}
		}
	case "x", "delete":
		if m.sel < len(m.accounts) {
			a := m.accounts[m.sel]
			m.confirmText = fmt.Sprintf("Remove %s %s? It signs out and deletes its local data. y/n",
				accountShort(m.mgr, m.mgr.AccountKey(a)), textfmt.Plain(a.Label()))
			m.confirm = func() tea.Cmd {
				return func() tea.Msg { return settingsRemoved{a: a, err: m.mgr.Remove(m.ctx, a)} }
			}
		}
	case "enter":
		if m.sel == len(m.accounts) {
			m.result.add = true
			return m, tea.Quit
		}
		a := m.accounts[m.sel]
		s := a.Snapshot()
		if s.Status == "online" || s.NeedsLogin || s.Identity.ID == 0 {
			m.result.account = a
			return m, tea.Quit
		}
		m.mgr.Start(m.ctx, a, nil)
	}
	return m, nil
}

func (m settingsModel) twoPane() bool { return m.width >= twoPaneWidth }

func (m settingsModel) paneAt(x int) settingsPane {
	if !m.twoPane() {
		return m.focus
	}
	if x < sidebarWidth {
		return paneSections
	}
	return paneAccounts
}

// handleClick selects the clicked row and acts on it as Enter would.
func (m settingsModel) handleClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	h := m.bodyHeight() - 2
	row := mouse.Y - 1 // under the box's top edge
	if mouse.Button != tea.MouseLeft || m.busy || row < 0 || row >= h {
		return m, nil
	}
	if m.confirm != nil {
		return m.handleKey("esc") // anything but y declines
	}
	if m.focus = m.paneAt(mouse.X); m.focus == paneSections {
		i := row
		if i > len(settingsSections) {
			i-- // the separator
		} else if i == len(settingsSections) {
			return m, nil
		}
		if i >= m.leftRows() {
			return m, nil
		}
		m.section = i
		return m.sectionsKey("enter")
	}
	i := window(m.sel, h) + row
	if i > len(m.accounts) {
		return m, nil
	}
	if m.section == sectionEmail {
		if row == 0 {
			return m.emailKey("enter")
		}
		return m, nil
	}
	m.sel = i
	return m.accountsKey("enter")
}

func (m settingsModel) footer() string {
	notice := m.notice
	switch {
	case m.confirm != nil:
		notice = m.confirmText
	case notice == "" && m.focus == paneAccounts && m.section != sectionEmail &&
		m.sel < len(m.accounts):
		notice = m.accounts[m.sel].Snapshot().Notice
	}
	return renderFooter(m.helpKeys(), m.fullHelp, notice, m.width)
}

func (m settingsModel) bodyHeight() int { return max(4, m.height-lipgloss.Height(m.footer())) }

func (m settingsModel) View() tea.View {
	footer := m.footer()
	h := max(4, m.height-lipgloss.Height(footer))
	title, lines := "Accounts", m.accountLines
	if m.section == sectionEmail {
		title, lines = "Email", func(width, _ int) []string { return m.emailLines(width) }
	}
	var body string
	switch {
	case m.twoPane():
		left := renderBox("Settings", m.sectionLines(sidebarWidth-4), sidebarWidth, h,
			boxBorder(m.focus == paneSections))
		right := renderBox(title, lines(m.width-sidebarWidth-4, h-2),
			m.width-sidebarWidth, h, boxBorder(m.focus == paneAccounts))
		body = joinColumns(left, right)
	case m.focus == paneSections:
		body = renderBox("Settings", m.sectionLines(m.width-4), m.width, h, colorAccent)
	default:
		body = renderBox(title, lines(m.width-4, h-2), m.width, h, colorAccent)
	}
	v := tea.NewView(body + "\n" + footer)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m settingsModel) sectionLines(width int) []string {
	focused := m.focus == paneSections
	var lines []string
	for i, title := range settingsSections {
		lines = append(lines, themedListRow(title, "", i == m.section, focused, width, colorAccent))
	}
	lines = append(lines, styleDim.Render(strings.Repeat("─", width)))
	for i, title := range settingsActions {
		lines = append(lines, themedListRow(styleSubtle.Render(title), "",
			len(settingsSections)+i == m.section, focused, width, colorAccent))
	}
	return lines
}

func (m settingsModel) accountLines(width, height int) []string {
	focused := m.focus == paneAccounts
	var lines []string
	for i := window(m.sel, height); i <= len(m.accounts) && len(lines) < height; i++ {
		if i == len(m.accounts) {
			lines = append(lines, themedListRow(styleSubtle.Render("+ Add account…"), "",
				i == m.sel, focused, width, colorAccent))
			break
		}
		a := m.accounts[i]
		s := a.Snapshot()
		label := textfmt.Plain(a.Label())
		if a == m.active {
			label = styleTitle.Render(label)
		}
		text := fmt.Sprintf("%s %s %s", accountGlyph(s),
			styleDim.Render(fmt.Sprintf("%s%d", backendTag(a.BackendType), i+1)), label)
		if m.mgr.Agents != nil {
			if name := m.mgr.Agents.AgentName(a); name != "" {
				text += "  " + styleAgent.Render("agent: "+textfmt.Plain(name))
			}
		}
		status := s.Status
		if a == m.active {
			status += " · current"
		}
		if digestSkipped(m.mgr, a) {
			status += " · no digest"
		}
		lines = append(lines, themedListRow(text, styleSubtle.Render(status), i == m.sel,
			focused, width, colorAccent))
	}
	return lines
}

func (m settingsModel) helpKeys() []helpKey {
	back := helpKey{"esc", "back"}
	if !m.canGoBack() {
		back = helpKey{"q", "quit"}
	}
	if m.focus == paneSections {
		return []helpKey{{"↑↓", "select"}, {"⏎", "open"}, back, {"?", "more"}}
	}
	if m.section == sectionEmail {
		return []helpKey{{"⏎", "change"}, {"tab", "sections"}, back, {"?", "more"}}
	}
	return []helpKey{{"↑↓", "select"}, {"⏎", "switch"}, {"r", "reconnect"}, {"n", "add"},
		{"x", "remove"}, {"d", "digest"}, {"tab", "sections"}, back, {"?", "more"}}
}

// tuiSettings runs the Settings screen. It returns the account to open, nil
// to quit, agentsRequested for Chat Agents, or accountsRequested when Esc
// leaves a Settings opened from the account chooser.
func tuiSettings(ctx context.Context, mgr *account.AccountManager, active *account.Account,
	fromAccounts bool) (*account.Account, error) {
	for {
		m := newSettingsModel(ctx, mgr, active, fromAccounts)
		result, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
		if err != nil {
			return nil, fmt.Errorf("run settings: %w", err)
		}
		final := result.(settingsModel)
		active = final.active
		r := final.result
		switch {
		case r.agents:
			return nil, agentsRequested{}
		case r.add:
			a, err := tuiPickAndAdd(ctx, mgr)
			if errors.Is(err, context.Canceled) && ctx.Err() == nil && len(mgr.List()) > 0 {
				continue
			}
			return a, err
		case r.back && fromAccounts:
			return nil, accountsRequested{}
		case r.back:
			return active, nil
		}
		return r.account, nil
	}
}
