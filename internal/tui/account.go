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
	settings bool
	plugin   string // the plugin whose settings to open, from its widget's modal
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
	widgets  widgetsModel
	online   string // the keys of the online accounts, to update widgets when it changes
	quitting *confirmModal
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
		widgets:  newWidgets(ctx, st, mgr.Plugins),
		width:    w,
		height:   h,
	}
}

func (m accountListModel) maxIndex() int { return len(m.accounts) + 2 }

func (m accountListModel) Init() tea.Cmd {
	_, widgets := m.widgets.start(m.contentWidth(), m.width, m.bodyHeight()+1)
	return tea.Batch(listenAccounts(m.mgr), widgets)
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
		if online := onlineKeys(m.mgr); online != m.online {
			m.online = online
			m.widgets = m.widgets.poke()
		}
		return m, listenAccounts(m.mgr)

	case widgetTick:
		var cmd tea.Cmd
		m.widgets, cmd = m.widgets.tick(m.contentWidth(), m.width, m.bodyHeight()+1)
		return m, cmd

	case widgetUpdated:
		m.widgets = m.widgets.updated(msg)

	case widgetClicked:
		m.widgets = m.widgets.clicked(msg)

	case tea.MouseWheelMsg:
		if k, ok := wheelKey(msg); ok {
			return m.Update(k)
		}

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			break
		}
		if m.quitting != nil {
			x, y, _, _ := m.quitRect()
			return m.quitAnswered(m.quitting.click(msg.X, msg.Y, x, y))
		}
		if md := m.widgets.modal; md != nil {
			x, y, w, h := m.widgetModalRect()
			if msg.X < x || msg.X >= x+w || msg.Y < y || msg.Y >= y+h {
				m.widgets.modal = nil
			} else if k, ok := widgetModalKeyAt(msg.X - x - 2); ok && msg.Y == y+h-2 {
				return m.widgetModalKey(md, k)
			}
			return m, nil
		}
		if i, ok := m.itemAt(msg.Y); ok {
			m.selected = i
			return m.Update(keyPress(tea.KeyEnter))
		}
		_, shift, widgets := m.layout()
		top := 2 + shift + len(m.accounts) + 4 + widgetsGap // the cards' first screen row
		left := 2 + homePad                                 // the box's left edge and padding
		if p, ok := m.widgets.widgetAt(msg.X-left, msg.Y-top, m.contentWidth()); ok &&
			msg.Y-top < widgets {
			return m, m.widgets.click(p)
		}

	case tea.KeyPressMsg:
		if m.quitting != nil {
			return m.quitAnswered(m.quitting.key(msg.String()))
		}
		if md := m.widgets.modal; md != nil {
			_, _, _, h := m.widgetModalRect()
			page := max(1, h-5)
			switch msg.String() {
			case "esc", "q", "enter", "ctrl+c", "s":
				return m.widgetModalKey(md, msg.String())
			case "up", "k":
				md.scroll(-1, m.width, m.bodyHeight()+1)
			case "down", "j":
				md.scroll(1, m.width, m.bodyHeight()+1)
			case "pgup", "b":
				md.scroll(-page, m.width, m.bodyHeight()+1)
			case "pgdown", "space", " ", "f":
				md.scroll(page, m.width, m.bodyHeight()+1)
			case "home", "g":
				md.scroll(-maxModalScroll, m.width, m.bodyHeight()+1)
			case "end", "G":
				md.scroll(maxModalScroll, m.width, m.bodyHeight()+1)
			}
			return m, nil
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			m.quitting = newQuitModal(m.st)
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
				m.quitting = newQuitModal(m.st)
				return m, nil
			}
			if m.selected == len(m.accounts)+1 {
				m.result = &accountChoice{add: true}
				return m, tea.Quit
			}
			if m.selected == len(m.accounts) {
				m.result = &accountChoice{settings: true}
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

// contentWidth is the cells inside the Accounts box's border and padding.
func (m accountListModel) contentWidth() int { return m.width - 4 - 2*homePad }

// widgetsGap is the blank row and the small label above the widgets.
const widgetsGap = 2

// layout returns the logo when it fits above the rows and the widgets, and
// how far the rows are moved down to make room for it, or up to keep the
// selection in view; widgets is the height left for their cards under the rows.
// With the logo, a blank row pads the box's top and bottom and one more
// separates the logo from the rows.
func (m accountListModel) layout() (logo []string, shift, widgets int) {
	rows := len(m.accounts) + 4 // the accounts, a blank row and three actions
	limit := m.bodyHeight() - 2
	wants := m.widgets.height(m.contentWidth())
	logo = strings.Split(m.st.logo(m.contentWidth()), "\n")
	section := 0
	if wants > 0 {
		section = widgetsGap + wants
	}
	if 1+len(logo)+1+rows+section+1 <= limit {
		return logo, len(logo) + 2, wants
	}
	selRow := m.selected
	if m.selected >= len(m.accounts) {
		selRow++
	}
	if shift = -max(0, selRow-limit+1); shift < 0 {
		return nil, shift, 0
	}
	if wants > 0 && limit-rows-widgetsGap >= wants {
		widgets = wants
	}
	return nil, 0, widgets
}

// onlineKeys names the online accounts, so a change shows as a new string.
func onlineKeys(mgr *account.AccountManager) string {
	var keys []string
	for _, a := range mgr.List() {
		if s := a.Snapshot(); s.Backend != nil && s.Status == "online" {
			keys = append(keys, mgr.AccountKey(a))
		}
	}
	return strings.Join(keys, " ")
}

// itemAt maps a screen row to an account or action; the blank row between
// them is none.
func (m accountListModel) itemAt(y int) (int, bool) {
	_, shift, _ := m.layout()
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
		keys, notice = m.quitting.helpKeys(), ""
	} else if md := m.widgets.modal; md != nil {
		x, y, _, _ := m.widgetModalRect()
		body = m.st.fadeUnder(body, md.box(m.st, m.width, m.bodyHeight()+1), x, y)
		keys, notice = append([]helpKey{{"↑↓", "scroll"}}, widgetModalKeys...), ""
	}
	content := body + "\n" + m.st.keyStrip(keys, notice, m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// frame is the screen above the footer, also the backdrop of the modals
// opened from it.
func (m accountListModel) frame() string {
	inner := m.contentWidth()
	rows := m.accountRows(inner)
	rows = append(rows, "")
	for i, label := range []string{"Settings…", "Add account…", "Quit"} {
		if len(m.accounts)+i == m.selected {
			rows = append(rows, "   "+m.st.brand.Render("› ")+m.st.accent.Render(label))
		} else {
			rows = append(rows, m.st.subtle.Render("     "+label))
		}
	}
	logo, shift, widgets := m.layout()
	if widgets > 0 {
		rows = append(rows, "", m.st.dim.Render("── widgets"))
		rows = append(rows, strings.Split(m.widgets.view(inner, widgets), "\n")...)
	}
	if shift > 0 {
		top := []string{""}
		for _, line := range logo {
			top = append(top, lipgloss.PlaceHorizontal(inner, lipgloss.Center, line))
		}
		rows = append(append(top, ""), rows...)
	} else {
		rows = rows[-shift:]
	}
	return m.st.header(m.width) + "\n" +
		renderBox("accounts", padLines(rows, homePad), m.width, m.bodyHeight(), m.st.accentColor)
}

// accountRows draws one row per account in columns: the selection caret, the
// number, the status glyph, the service tag, the label, the status and a chip
// per bot. The label and status columns are as wide as their widest entry.
func (m accountListModel) accountRows(width int) []string {
	status := func(a *account.Account) string {
		if a == m.active {
			return a.Snapshot().Status + " · current"
		}
		return a.Snapshot().Status
	}
	labelW, statusW := 0, 0
	for _, a := range m.accounts {
		labelW = max(labelW, textfmt.DisplayWidth(accountLabel(a)))
		statusW = max(statusW, textfmt.DisplayWidth(status(a)))
	}
	const lead = 12 // caret, number, glyph and tag, each with the space after it
	labelW = max(8, min(labelW, 30, width-lead-statusW-2))
	var rows []string
	for i, a := range m.accounts {
		s := a.Snapshot()
		selected := i == m.selected
		caret, tag := "  ", m.st.tag
		if selected {
			caret, tag = m.st.brand.Render("› "), m.st.badge
		}
		label := truncate(accountLabel(a), labelW)
		label += strings.Repeat(" ", labelW-textfmt.DisplayWidth(label))
		if a == m.active {
			label = styleTitle.Render(label)
		}
		st := status(a)
		row := caret + m.st.dim.Render(fmt.Sprintf("%2d", i+1)) + " " + m.st.accountGlyph(s) + " " +
			tag.Render(" "+backendTag(a.BackendType)+" ") + " " + label + "  " +
			m.st.subtle.Render(st) + strings.Repeat(" ", statusW-textfmt.DisplayWidth(st))
		if m.mgr.Agents != nil {
			for _, rec := range m.mgr.Agents.AgentsFor(a) {
				name := textfmt.Plain(rec.Name)
				if selected { // on the accent fill, like the tag, to stand out of the tint
					row += "  " + m.st.badge.Render(" "+agentIcon+" "+name+" ")
					continue
				}
				chip := " " + m.st.agentMark(rec.Enabled) + " " + m.st.subtle.Render(name) + " "
				row += "  " + shadeLine(chip, m.st.selBgSGR, 0)
			}
		}
		if selected {
			row = shadeLine(textfmt.ClipLine(row, width), m.st.selBgSGR, width)
		}
		rows = append(rows, row)
	}
	return rows
}

// sectionRule is a divider that names the section under it.
func sectionRule(label string, width int) string {
	return label + strings.Repeat("─", max(0, width-textfmt.DisplayWidth(label)))
}

func (m accountListModel) quitAnswered(quit, done bool) (tea.Model, tea.Cmd) {
	switch {
	case quit:
		m.result = &accountChoice{quit: true}
		return m, tea.Quit
	case done:
		m.quitting = nil
	}
	return m, nil
}

// quitRect is where the quit modal sits: centred over the screen.
func (m accountListModel) quitRect() (x, y, w, h int) {
	w, h = m.quitting.size()
	return max(0, (m.width-w)/2), max(0, (m.bodyHeight()+1-h)/2), w, h
}

// widgetModalKey closes the widget's modal, or with s leaves for the plugin's
// settings.
func (m accountListModel) widgetModalKey(md *widgetModal, k string) (tea.Model, tea.Cmd) {
	m.widgets.modal = nil
	if k == "s" {
		m.result = &accountChoice{settings: true, plugin: md.p.Name}
		return m, tea.Quit
	}
	return m, nil
}

// widgetModalRect is where a widget's modal sits: centred over the screen.
func (m accountListModel) widgetModalRect() (x, y, w, h int) {
	w, h = m.widgets.modal.size(m.width, m.bodyHeight()+1)
	w, h = w+4, h+4
	return max(0, (m.width-w)/2), max(0, (m.bodyHeight()+1-h)/2), w, h
}

func (m accountListModel) helpKeys() []helpKey {
	return []helpKey{{"↑↓", "select"}, {"enter", "switch"}, {"r", "reconnect"}, {"esc", "quit"}}
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
	if final.result.settings {
		to, err := tuiSettings(ctx, mgr, active, true, final.result.plugin)
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
		wiz:      newWizard(st, "add account", addAccountSteps(st, needsAPI), nil),
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
		var cmd tea.Cmd
		m.wiz, cmd = m.wiz.modalClick(msg.X, msg.Y, 0, m.width, m.bodyHeight())
		return m, cmd
	}
	var cmd tea.Cmd
	m.wiz, cmd = m.wiz.Update(msg)
	return m, cmd
}

func (m addAccountModel) bodyHeight() int { return max(4, m.height-1) }

// formBox draws a form modal tall enough for lines, keeping the last ones
// when it doesn't fit, and returns how many lines were cut from the top.
func formBox(title string, lines []string, keys []helpKey, width, bodyHeight int,
	st *styles) (string, int) {
	h := min(bodyHeight, max(8, len(lines)+4)) // the borders and the keys
	top := max(0, len(lines)-(h-4))
	body := append(fillLines(slices.Clone(lines[top:]), h-4), st.modalKeys(keys, "", width-4)...)
	return st.modalBox(title, body, width, h), top
}

func (m addAccountModel) View() tea.View {
	box, _ := m.wiz.modal(m.width, m.bodyHeight())
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
		done := m.st.dim.Render("✓")
		if modalMark+labelW+2+textfmt.DisplayWidth(t.reply) > inner {
			lines = append(lines, m.st.modalRow(done, textfmt.ClipLine(t.ask, inner-modalMark),
				false, inner), textfmt.ClipLine("     "+m.st.subtle.Render(t.reply), inner))
			continue
		}
		pad := strings.Repeat(" ", labelW+2-textfmt.DisplayWidth(t.ask))
		lines = append(lines, m.st.modalRow(done, t.ask+pad+m.st.subtle.Render(t.reply),
			false, inner))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	if m.current != nil {
		lines = append(lines, styleTitle.Render(textfmt.ClipLine(loginLabel(m.current.Label), inner)))
		if _, help, ok := strings.Cut(m.current.Label, "\n"); ok {
			wrapped := lipgloss.NewStyle().Width(inner).Render(strings.TrimSpace(help))
			for _, l := range strings.Split(wrapped, "\n") {
				lines = append(lines, m.st.subtle.Render(l))
			}
		}
		lines = append(lines, "")
		m.input.SetWidth(max(10, inner-6))
		lines = append(lines, m.st.modalField("›", m.input.View(), inner)...)
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
	keys := []helpKey{{"enter", "answer"}, {"esc", "cancel"}}
	box, _ := formBox("sign in — "+m.label, lines, keys, w, bodyHeight, m.st)
	content := m.st.overlay(m.backdrop, box, m.width, bodyHeight) + "\n" +
		m.st.footer(keys, "", m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// loginLabel turns a backend prompt like "Phone (international format): " into
// bubble text. Lines after the first are help shown under the question.
func loginLabel(label string) string {
	label, _, _ = strings.Cut(label, "\n")
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
