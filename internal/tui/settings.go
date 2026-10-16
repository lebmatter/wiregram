package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/mail"
	"wiregram/internal/textfmt"
)

// settingsActions follow the accounts in the left pane, under a separator.
var settingsActions = []string{"+ Add account…", "Themes…", "Chat…", "Plugins…", "AI providers…", "Reset…", "Home",
	"Quit"}

const (
	actionAdd = iota
	actionThemes
	actionChat
	actionPlugins
	actionProviders
	actionReset
	actionHome
	actionQuit
)

type settingsPane int

const (
	paneAccounts settingsPane = iota
	paneItems
)

// settingsItem is one changeable row of an account's settings.
type settingsItem int

const (
	itemOpen settingsItem = iota
	itemReconnect
	itemRetention
	itemRemove
)

// itemsTop is how many lines sit above the items in the right pane.
const itemsTop = 2

type settingsResult struct {
	account *account.Account // switch to it
	back    bool             // return to the active account
	home    bool             // go to the account chooser
	add     bool
	reset   bool // every account was removed; delete all local data
}

// settingsReset reports that every account has been signed out and removed.
type settingsReset struct{}

type settingsRemoved struct {
	a   *account.Account
	err error
}

// settingsModel is the two-pane Settings screen: accounts on the left, the
// selected account's settings on the right.
type settingsModel struct {
	st       *styles
	ctx      context.Context
	mgr      *account.AccountManager
	accounts []*account.Account
	active   *account.Account

	// fromAccounts: opened from the account chooser, so Esc goes back there.
	fromAccounts bool

	focus settingsPane
	left  int // left row: an account, then the actions
	sel   int // right row: an item of the selected account, or a theme

	result     settingsResult
	confirm    *confirmModal
	confirmRun func() tea.Cmd
	busy       bool // a removal is running
	edit       textinput.Model
	editing    bool // a plugin's or provider's text setting is being typed
	// openProvider is the provider whose fields are listed, 0 for none.
	openProvider int64
	wiz          *wizardModel // adding a provider
	notice       string
	width        int
	height       int
}

func newSettingsModel(ctx context.Context, mgr *account.AccountManager, active *account.Account, fromAccounts bool) settingsModel {
	w, h := termSize()
	m := settingsModel{st: newStyles(loadTheme(mgr.Store)), ctx: ctx, mgr: mgr,
		accounts: mgr.List(), active: active,
		fromAccounts: fromAccounts, width: w, height: h}
	for i, a := range m.accounts {
		if a == active {
			m.left = i
		}
	}
	return m
}

func (m settingsModel) Init() tea.Cmd { return listenAccounts(m.mgr) }

func (m settingsModel) leftRows() int { return len(m.accounts) + len(settingsActions) }

// selected is the account chosen in the left pane, or nil on an action row.
func (m settingsModel) selected() *account.Account {
	if m.left < len(m.accounts) {
		return m.accounts[m.left]
	}
	return nil
}

// onThemes reports whether the Themes row is chosen, whose pane lists themes.
func (m settingsModel) onThemes() bool { return m.left == len(m.accounts)+actionThemes }

// onChat reports whether the Chat row is chosen, whose pane holds the
// app-wide chat settings.
func (m settingsModel) onChat() bool { return m.left == len(m.accounts)+actionChat }

// onPlugins reports whether the Plugins row is chosen, whose pane lists plugins.
func (m settingsModel) onPlugins() bool { return m.left == len(m.accounts)+actionPlugins }

// onProviders reports whether the AI providers row is chosen.
func (m settingsModel) onProviders() bool { return m.left == len(m.accounts)+actionProviders }

// onReset reports whether the Reset row is chosen.
func (m settingsModel) onReset() bool { return m.left == len(m.accounts)+actionReset }

// rightRows is how many rows the right pane selects among.
func (m settingsModel) rightRows() int {
	if m.onThemes() {
		return len(themes)
	}
	if m.onPlugins() {
		return len(m.pluginRows())
	}
	if m.onProviders() {
		return len(m.providerRows())
	}
	if m.onChat() {
		return 1
	}
	return len(m.items())
}

func (m settingsModel) items() []settingsItem {
	a := m.selected()
	if a == nil {
		return nil
	}
	items := []settingsItem{itemOpen, itemReconnect}
	if a.BackendType == "email" {
		items = append(items, itemRetention)
	}
	return append(items, itemRemove)
}

func (m settingsModel) canGoBack() bool {
	return m.fromAccounts || m.active != nil && m.active.Snapshot().Status == "online"
}

func (m *settingsModel) reload() {
	m.accounts = m.mgr.List()
	m.left = clampIndex(m.left, m.leftRows())
	m.sel = clampIndex(m.sel, m.rightRows())
	if m.selected() == nil && !m.onThemes() && !m.onChat() && !m.onPlugins() &&
		!m.onProviders() {
		m.focus = paneAccounts
	}
}

func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case accountsChanged:
		m.reload()
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
		m.reload()
		if len(m.accounts) == 0 {
			m.result.add = true
			return m, tea.Quit
		}
	case settingsNotice:
		m.notice = string(msg)
		m.reload()
	case settingsReset:
		m.result.reset = true
		return m, tea.Quit
	case wizardFinished:
		return m.providerAdded(msg.answers)
	case wizardCancelled:
		m.wiz = nil
	case tea.KeyPressMsg:
		if m.wiz != nil {
			wiz, cmd := m.wiz.Update(msg)
			m.wiz = &wiz
			return m, cmd
		}
		if m.editing {
			return m.editKey(msg)
		}
		return m.handleKey(msg.String())
	case tea.PasteMsg:
		if m.wiz != nil {
			wiz, cmd := m.wiz.Update(msg)
			m.wiz = &wiz
			return m, cmd
		}
		if m.editing {
			var cmd tea.Cmd
			m.edit, cmd = m.edit.Update(msg)
			return m, cmd
		}
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
		return m.confirmAnswered(m.confirm.key(k))
	}
	m.notice = ""
	if m.focus == paneAccounts {
		return m.accountsKey(k)
	}
	return m.itemsKey(k)
}

func (m settingsModel) accountsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c":
		if m.canGoBack() {
			m.result.back = true
		}
		return m, tea.Quit
	case "up", "k":
		m.left, m.sel = max(0, m.left-1), 0
	case "down", "j":
		m.left, m.sel = min(m.leftRows()-1, m.left+1), 0
	case "enter", "tab", "right", "l":
		if m.onThemes() {
			for i, t := range themes {
				if t.name == m.st.theme.name {
					m.sel = i
				}
			}
		}
		if m.selected() != nil || m.onThemes() || m.onChat() {
			m.focus = paneItems
			return m, nil
		}
		if m.onPlugins() || m.onProviders() {
			if m.rightRows() > 0 {
				m.focus = paneItems
			}
			return m, nil
		}
		if k != "enter" {
			return m, nil
		}
		switch m.left - len(m.accounts) {
		case actionAdd:
			m.result.add = true
		case actionHome:
			m.result.home = true
		case actionReset:
			m.confirm = newConfirmModal(m.st, "Reset",
				"Delete all accounts and data and start fresh? This cannot be undone.",
				"Reset", true)
			m.confirmRun = func() tea.Cmd { return m.resetAll }
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m settingsModel) itemsKey(k string) (tea.Model, tea.Cmd) {
	if m.onThemes() {
		return m.themesKey(k)
	}
	if m.onPlugins() {
		return m.pluginsKey(k)
	}
	if m.onProviders() {
		return m.providersKey(k)
	}
	if m.onChat() {
		return m.chatKey(k)
	}
	items := m.items()
	switch k {
	case "esc", "ctrl+c", "tab", "left", "h":
		m.focus = paneAccounts
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(len(items)-1, m.sel+1)
	case "enter", "space", " ", "right", "l":
		if m.sel < len(items) {
			return m.apply(items[m.sel])
		}
	}
	return m, nil
}

// themesKey moves between the theme cards, which may sit side by side, so
// every arrow moves; Left on the first card goes back to the accounts.
func (m settingsModel) themesKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c", "tab":
		m.focus = paneAccounts
	case "left", "h", "up", "k":
		if m.sel == 0 && (k == "left" || k == "h") {
			m.focus = paneAccounts
		}
		m.sel = max(0, m.sel-1)
	case "right", "l", "down", "j":
		m.sel = min(len(themes)-1, m.sel+1)
	case "enter", "space", " ":
		t := themes[m.sel]
		if err := saveTheme(m.mgr.Store, t); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.st = newStyles(t)
		m.notice = "Theme: " + t.name
	}
	return m, nil
}

// chatKey handles the Chat pane, whose one row switches the nick style.
func (m settingsModel) chatKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c", "tab", "left", "h":
		m.focus = paneAccounts
	case "enter", "space", " ", "right", "l":
		next := nickStyleColumn
		if loadNickStyle(m.mgr.Store) == nickStyleColumn {
			next = nickStyleColon
		}
		if err := m.mgr.Store.SetSetting(nickStyleSetting, next); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.notice = "Nick style: " + next
	}
	return m, nil
}

// apply changes or runs one setting of the selected account.
func (m settingsModel) apply(item settingsItem) (tea.Model, tea.Cmd) {
	a := m.selected()
	short := accountShort(m.mgr, m.mgr.AccountKey(a))
	switch item {
	case itemOpen:
		s := a.Snapshot()
		if s.Status == "online" || s.NeedsLogin || s.Identity.ID == 0 {
			m.result.account = a
			return m, tea.Quit
		}
		m.mgr.Start(m.ctx, a, nil)
	case itemReconnect:
		m.mgr.Start(m.ctx, a, nil)
		m.notice = "Reconnecting " + short
	case itemRetention:
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
	case itemRemove:
		m.confirm = newConfirmModal(m.st, "Remove account",
			fmt.Sprintf("Remove %s %s? It signs out and deletes its local data.",
				short, textfmt.Plain(a.Label())), "Remove", false)
		m.confirmRun = func() tea.Cmd {
			return func() tea.Msg { return settingsRemoved{a: a, err: m.mgr.Remove(m.ctx, a)} }
		}
	}
	return m, nil
}

func (m settingsModel) confirmAnswered(yes, done bool) (tea.Model, tea.Cmd) {
	if !done {
		return m, nil
	}
	run := m.confirmRun
	m.confirm, m.confirmRun = nil, nil
	if !yes {
		return m, nil
	}
	if m.onProviders() {
		return m, run() // a provider delete, which is quick
	}
	m.busy, m.notice = true, "Signing out…"
	return m, run()
}

// resetAll signs out of and removes every account; the caller then deletes
// the whole config and cache directories.
func (m settingsModel) resetAll() tea.Msg {
	for _, a := range m.mgr.List() {
		m.mgr.Remove(m.ctx, a) // a failed server logout must not stop the reset
	}
	return settingsReset{}
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

func (m settingsModel) twoPane() bool { return m.width >= twoPaneWidth }

func (m settingsModel) paneAt(x int) settingsPane {
	if !m.twoPane() {
		return m.focus
	}
	if x < sidebarWidth {
		return paneAccounts
	}
	return paneItems
}

// handleClick selects the clicked row and acts on it as Enter would.
func (m settingsModel) handleClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	h := m.bodyHeight() - 2
	row := mouse.Y - 1 // under the box's top edge
	if mouse.Button != tea.MouseLeft || m.busy {
		return m, nil
	}
	if m.confirm != nil {
		x, y := m.confirmPos()
		return m.confirmAnswered(m.confirm.click(mouse.X, mouse.Y, x, y))
	}
	if m.wiz != nil {
		wiz, cmd := m.wiz.modalClick(mouse.X, mouse.Y, 0, m.width, m.bodyHeight())
		m.wiz = &wiz
		return m, cmd
	}
	if row < 0 || row >= h {
		return m, nil
	}
	m.editing = false
	if m.paneAt(mouse.X) == paneAccounts {
		i := m.leftStart(h) + row
		if i == len(m.accounts) {
			return m, nil // the separator
		} else if i > len(m.accounts) {
			i--
		}
		if i >= m.leftRows() {
			return m, nil
		}
		if i != m.left {
			m.sel = 0
		}
		m.focus, m.left = paneAccounts, i
		return m.accountsKey("enter")
	}
	if m.onThemes() {
		x := mouse.X - 2 // inside the box's left edge and padding
		if m.twoPane() {
			x -= sidebarWidth
		}
		i, ok := themeAt(x, row-itemsTop, m.rightWidth()-4)
		if !ok {
			return m, nil
		}
		m.focus, m.sel = paneItems, i
		return m.themesKey("enter")
	}
	i := window(m.sel, h-itemsTop) + row - itemsTop
	if i < 0 || i >= m.rightRows() {
		return m, nil
	}
	m.focus, m.sel = paneItems, i
	return m.itemsKey("enter")
}

func (m settingsModel) footer() string {
	notice := m.notice
	switch {
	case notice == "" && m.selected() != nil:
		notice = m.selected().Snapshot().Notice
	}
	return m.st.footer(m.helpKeys(), notice, m.width)
}

func (m settingsModel) bodyHeight() int { return max(4, m.height-lipgloss.Height(m.footer())) }

func (m settingsModel) View() tea.View {
	body := m.frame()
	if m.confirm != nil {
		x, y := m.confirmPos()
		body = m.st.fadeUnder(body, m.confirm.View(), x, y)
	}
	if m.wiz != nil {
		box, _ := m.wiz.modal(m.width, m.bodyHeight())
		body = m.st.overlay(body, box, m.width, m.bodyHeight())
	}
	v := tea.NewView(body + "\n" + m.footer())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// confirmPos is the top-left corner of the confirmation, centred on the screen.
func (m settingsModel) confirmPos() (x, y int) {
	w, h := m.confirm.size()
	return modalRect(m.width, m.bodyHeight(), w, h)
}

// frame is the screen above the footer, also the backdrop of Add account.
func (m settingsModel) frame() string {
	h := m.bodyHeight()
	title := "Settings"
	if a := m.selected(); a != nil {
		title = textfmt.Plain(a.Label())
	} else if m.onThemes() {
		title = "Themes"
	} else if m.onChat() {
		title = "Chat"
	} else if m.onPlugins() {
		title = "Plugins"
	} else if m.onProviders() {
		title = "AI providers"
	}
	var body string
	switch {
	case m.twoPane():
		left := renderBox("Settings", m.accountLines(sidebarWidth-4, h-2), sidebarWidth, h,
			m.st.border(m.focus == paneAccounts))
		right := renderBox(title, m.itemLines(m.rightWidth()-4, h-2),
			m.rightWidth(), h, m.st.border(m.focus == paneItems))
		body = joinColumns(left, right)
	case m.focus == paneAccounts:
		body = renderBox("Settings", m.accountLines(m.width-4, h-2), m.width, h, m.st.accentColor)
	default:
		body = renderBox(title, m.itemLines(m.width-4, h-2), m.width, h, m.st.accentColor)
	}
	return body
}

func (m settingsModel) accountLines(width, height int) []string {
	focused := m.focus == paneAccounts
	var lines []string
	for i := range m.accounts {
		a := m.accounts[i]
		label := textfmt.Plain(a.Label())
		if a == m.active {
			label = styleTitle.Render(label)
		}
		text := fmt.Sprintf("%s %s %s", m.st.accountGlyph(a.Snapshot()),
			m.st.dim.Render(fmt.Sprintf("%s%d", backendTag(a.BackendType), i+1)), label)
		lines = append(lines, m.st.listRow(text, "", i == m.left, focused, width))
	}
	lines = append(lines, m.st.dim.Render(strings.Repeat("─", width)))
	for i, title := range settingsActions {
		lines = append(lines, m.st.listRow(m.st.subtle.Render(title), "",
			len(m.accounts)+i == m.left, focused, width))
	}
	return lines[min(m.leftStart(height), len(lines)):]
}

// leftStart is the first visible line of the left pane. The separator has no
// row of its own, so the window follows the selection's line.
func (m settingsModel) leftStart(height int) int {
	line := m.left
	if m.left >= len(m.accounts) {
		line++
	}
	return window(line, height)
}

// itemLines is the selected account's summary line and its settings, or a
// description of the selected action.
func (m settingsModel) itemLines(width, height int) []string {
	a := m.selected()
	if a == nil {
		desc := [...]string{"Add a Telegram, WhatsApp, Matrix, Mattermost or email account.",
			"The colours of the whole app.", "How every chat draws its messages.",
			"Lua scripts that add widgets to the account list.",
			"Where bots get their replies: any OpenAI-compatible API.",
			"Delete every account and all local data, as on a fresh install.",
			"Back to the account chooser.", "Leave wiregram."}
		lines := []string{m.st.subtle.Render(textfmt.ClipLine(desc[m.left-len(m.accounts)], width))}
		if m.onThemes() {
			lines = append(append(lines, ""), m.themeLines(width)...)
		}
		if m.onChat() {
			lines = append(lines, "", m.st.formRow("Nick style",
				m.st.subtle.Render(loadNickStyle(m.mgr.Store)), 10, true, m.focus == paneItems,
				width))
		}
		if m.onPlugins() {
			lines = append(append(lines, ""), m.pluginLines(width, height-itemsTop)...)
		}
		if m.onProviders() {
			lines = append(append(lines, ""), m.providerLines(width, height-itemsTop)...)
		}
		if m.onReset() {
			for _, w := range []string{"", "Warning: this cannot be undone.",
				"Every account is signed out and removed. Settings, themes, plugins and",
				"their data, bots, cached mail and downloaded media are deleted,",
				"then wiregram starts over as a fresh installation."} {
				lines = append(lines, styleError.Render(textfmt.ClipLine(w, width)))
			}
		}
		return lines
	}
	s := a.Snapshot()
	summary := fmt.Sprintf("%s%d · %s", backendTag(a.BackendType), m.left+1, s.Status)
	if a == m.active {
		summary += " · current"
	}
	if m.mgr.Agents != nil {
		if bots := m.mgr.Agents.AgentsFor(a); len(bots) > 0 {
			summary += " · " + plural(len(bots), "bot", "bots") + ": " + botNames(bots)
		}
	}
	lines := []string{m.st.subtle.Render(textfmt.ClipLine(summary, width)), ""}
	focused := m.focus == paneItems
	items := m.items()
	labelW := 0
	for _, item := range items {
		label, _ := m.itemText(a, item)
		labelW = max(labelW, textfmt.DisplayWidth(label))
	}
	for i := window(m.sel, height-itemsTop); i < len(items) && len(lines) < height; i++ {
		label, value := m.itemText(a, items[i])
		lines = append(lines, m.st.formRow(label, m.st.subtle.Render(value), labelW, i == m.sel,
			focused, width))
	}
	return lines
}

func (m settingsModel) itemText(a *account.Account, item settingsItem) (string, string) {
	switch item {
	case itemOpen:
		s := a.Snapshot()
		if s.NeedsLogin || s.Identity.ID == 0 {
			return "Sign in", ""
		}
		return "Open", ""
	case itemReconnect:
		return "Reconnect", ""
	case itemRetention:
		return "Keep mail cached", retentionLabel(mail.RetentionDays(m.mgr.Store)) +
			" (all email accounts)"
	}
	return styleError.Render("Remove account…"), ""
}

func (m settingsModel) rightWidth() int {
	if m.twoPane() {
		return m.width - sidebarWidth
	}
	return m.width
}

// Theme cards are laid out in rows of as many as fit, a blank line between rows.
const (
	themeCardWidth  = 26
	themeCardHeight = 6
)

func themesPerRow(width int) int { return max(1, (width+1)/(themeCardWidth+1)) }

// themeLines draws every theme as a card, the chosen one's border lit.
func (m settingsModel) themeLines(width int) []string {
	perRow := themesPerRow(width)
	var lines []string
	for start := 0; start < len(themes); start += perRow {
		if start > 0 {
			lines = append(lines, "")
		}
		row := make([]string, themeCardHeight)
		for i := start; i < min(start+perRow, len(themes)); i++ {
			card := themeCard(themes[i], themes[i].name == m.st.theme.name,
				m.focus == paneItems && i == m.sel)
			for j, line := range card {
				if i > start {
					row[j] += " "
				}
				row[j] += line
			}
		}
		lines = append(lines, row...)
	}
	return lines
}

// themeAt is the theme whose card covers column x and row y of the cards.
func themeAt(x, y, width int) (int, bool) {
	perRow := themesPerRow(width)
	col, row := x/(themeCardWidth+1), y/(themeCardHeight+1)
	if x < 0 || y < 0 || col >= perRow || x%(themeCardWidth+1) == themeCardWidth ||
		y%(themeCardHeight+1) == themeCardHeight {
		return 0, false
	}
	i := row*perRow + col
	return i, i < len(themes)
}

// themeCard is a small sample of the app drawn in t: a chat list with a
// selected row and an unread badge, a message header and the footer keys.
func themeCard(t theme, current, selected bool) []string {
	st := newStyles(t)
	inner := themeCardWidth - 4
	body := []string{
		st.listRow("General", st.badge.Render(" 3 "), true, true, inner),
		st.listRow("Alice", "", false, true, inner),
		st.dim.Render("[12:04] ") + st.subtle.Render("you") + " hi!",
		st.helpBar([]helpKey{{"enter", "open"}, {"esc", "back"}}, inner),
	}
	title := t.name
	if current {
		title += " ✓"
	}
	return strings.Split(renderBox(title, body, themeCardWidth, themeCardHeight,
		st.border(selected)), "\n")
}

func (m settingsModel) helpKeys() []helpKey {
	back := helpKey{"esc", "back"}
	if !m.canGoBack() {
		back = helpKey{"esc", "quit"}
	}
	if m.confirm != nil {
		return m.confirm.helpKeys()
	}
	if m.wiz != nil {
		return m.wiz.helpKeys()
	}
	if m.focus == paneAccounts {
		return []helpKey{{"↑↓", "select"}, {"enter", "open"}, back}
	}
	if m.onThemes() {
		return []helpKey{{"←→", "select"}, {"enter", "apply"}, {"esc", "accounts"}}
	}
	if m.editing {
		return []helpKey{{"enter", "save"}, {"esc", "cancel"}}
	}
	return []helpKey{{"↑↓", "select"}, {"enter", "change"}, {"esc", "accounts"}}
}

// tuiSettings runs the Settings screen. It returns the account to open, nil
// to quit, ResetRequested after Reset, or accountsRequested for the Home row and
// when Esc leaves a Settings opened from the account chooser.
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
		case r.reset:
			return nil, ResetRequested{}
		case r.add:
			a, err := tuiPickAndAdd(ctx, mgr, final.frame())
			if errors.Is(err, context.Canceled) && ctx.Err() == nil && len(mgr.List()) > 0 {
				continue
			}
			return a, err
		case r.home, r.back && fromAccounts:
			return nil, accountsRequested{}
		case r.back:
			return active, nil
		}
		return r.account, nil
	}
}
