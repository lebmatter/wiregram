package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/game"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

// Game mode plays the embedded Lua games against other wiregram users on one
// base. The challenger hosts: its wiregram runs the script for both seats and
// sends the guest each changed line of the guest's view.

type gameScreen int

const (
	gameSetup gameScreen = iota
	gameLobby
	gamePlay
)

const (
	settingBaseURL   = "base_url"
	settingBaseName  = "base_name"
	settingBaseToken = "base_token"

	gameNameMax = 32
	// gameFrameMax bounds a guest's board so a bad host cannot balloon it.
	gameFrameMax = 200
)

var gameFieldLabels = []string{"Base URL", "Player name", "Token (optional)"}

type gameModeModel struct {
	ctx      context.Context
	mgr      *account.AccountManager
	scripts  []game.Script
	client   *game.Client
	switchTo *account.Account

	screen  gameScreen
	inputs  []textinput.Model
	field   int
	dialing bool
	baseURL string
	name    string
	baseErr string // last error line from base, shown if it then disconnects

	players        []game.Player
	gsel, psel     int
	playersFocused bool
	invite         *game.Envelope // challenge waiting for y/n
	waiting        string         // who we challenged

	matchID     string
	script      game.Script
	seat        int
	names       []string
	session     *game.Session // host only
	guestSent   []string
	guestStatus string
	guestOver   bool
	board       []string
	status      string
	over        bool

	notice   string
	fullHelp bool
	width    int
	height   int
}

type gameDialed struct {
	c   *game.Client
	err error
}

// gameEvent is tagged with its client so events from a replaced connection
// are dropped instead of re-arming a second listener.
type gameEvent struct {
	c  *game.Client
	ev game.Event
}

type gameTick struct{ match string }

func listenGame(c *game.Client) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-c.Events
		if !ok {
			return nil
		}
		return gameEvent{c, ev}
	}
}

// tuiGameMode runs game mode. It returns the account to switch to, or nil to
// go back to wherever it was opened from.
func tuiGameMode(ctx context.Context, mgr *account.AccountManager) (*account.Account, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, h := termSize()
	m := gameModeModel{ctx: ctx, mgr: mgr, width: w, height: h}
	var err error
	if m.scripts, err = game.Catalog(); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	m.baseURL = m.setting(settingBaseURL)
	m.name = m.setting(settingBaseName)
	if m.name == "" {
		m.name = os.Getenv("USER")
	}
	m.inputs = make([]textinput.Model, len(gameFieldLabels))
	for i, v := range []string{m.baseURL, m.name, m.setting(settingBaseToken)} {
		ti := textinput.New()
		ti.Prompt = ""
		ti.SetValue(v)
		m.inputs[i] = ti
	}
	m.inputs[0].Placeholder = "ws://localhost:8765"
	m.inputs[2].EchoMode = textinput.EchoPassword
	m.inputs[0].Focus()
	m.sizeInputs()

	result, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return nil, fmt.Errorf("run game mode: %w", err)
	}
	final := result.(gameModeModel)
	if final.session != nil {
		final.session.Close()
	}
	if final.client != nil {
		final.client.Close()
	}
	return final.switchTo, nil
}

func (m *gameModeModel) sizeInputs() {
	for i := range m.inputs {
		m.inputs[i].SetWidth(max(10, min(60, m.width-8)))
	}
}

func (m *gameModeModel) setting(key string) string {
	v, err := m.mgr.Store.Setting(key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		m.notice = textfmt.CleanError(err)
	}
	return v
}

func (m gameModeModel) Init() tea.Cmd {
	if m.baseURL != "" && m.name != "" {
		return m.dial()
	}
	return textinput.Blink
}

func (m gameModeModel) dial() tea.Cmd {
	ctx, url, name, token := m.ctx, m.baseURL, m.name, m.inputs[2].Value()
	refs := make([]game.GameRef, len(m.scripts))
	for i, s := range m.scripts {
		refs[i] = s.Info.Ref()
	}
	return func() tea.Msg {
		c, err := game.Dial(ctx, url, name, token, refs)
		return gameDialed{c, err}
	}
}

func (m gameModeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.sizeInputs()
		return m, nil
	case gameDialed:
		m.dialing = false
		if msg.err != nil {
			m.screen, m.notice = gameSetup, textfmt.CleanError(msg.err)
			return m, m.inputs[m.field].Focus()
		}
		m.client, m.screen, m.notice = msg.c, gameLobby, ""
		return m, listenGame(msg.c)
	case gameEvent:
		if msg.c != m.client {
			return m, nil
		}
		if msg.ev.Err != nil {
			return m.disconnected(msg.ev.Err), nil
		}
		m, cmd := m.onEnvelope(msg.ev.Env)
		if m.client == nil {
			return m, cmd
		}
		return m, tea.Batch(cmd, listenGame(m.client))
	case gameTick:
		if msg.match != m.matchID || m.session == nil || m.over {
			return m, nil
		}
		if err := m.session.Tick(); err != nil {
			return m.fail(err), nil
		}
		m = m.refresh()
		return m, m.tick()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.screen == gameSetup {
		var cmd tea.Cmd
		m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m gameModeModel) tick() tea.Cmd {
	rate, id := m.script.Info.TickRate(), m.matchID
	if rate == 0 || m.session == nil {
		return nil
	}
	return tea.Tick(rate, func(time.Time) tea.Msg { return gameTick{id} })
}

func (m gameModeModel) disconnected(err error) gameModeModel {
	m = m.endMatch()
	if m.client != nil {
		m.client.Close()
	}
	m.client, m.players, m.invite, m.waiting = nil, nil, nil, ""
	m.screen, m.notice = gameSetup, textfmt.CleanError(err)
	if m.baseErr != "" {
		m.notice, m.baseErr = m.baseErr, ""
	}
	m.inputs[m.field].Focus()
	return m
}

func (m gameModeModel) send(env game.Envelope) gameModeModel {
	if m.client == nil {
		return m
	}
	if err := m.client.Send(env); err != nil {
		m.notice = textfmt.CleanError(err)
	}
	return m
}

func (m gameModeModel) onEnvelope(env game.Envelope) (gameModeModel, tea.Cmd) {
	if env.T == game.TError {
		m.baseErr, m.notice, m.waiting = env.Error, textfmt.Plain(env.Error), ""
		return m, nil
	}
	m.baseErr = ""
	switch env.T {
	case game.TLobby:
		var players []game.Player
		for _, p := range env.Players {
			if p.Name != m.name {
				players = append(players, p)
			}
		}
		m.players = players
		m.psel = clampIndex(m.psel, len(m.players))
	case game.TChallenge:
		return m.challenged(env), nil
	case game.TDeclined:
		m.waiting, m.notice = "", textfmt.Plain(env.From)+" declined"
	case game.TMatched:
		return m.startMatch(env)
	case game.TGame:
		return m.onPayload(env.Payload)
	case game.TPeerLeft:
		switch {
		case m.invite != nil && m.invite.ID == env.ID:
			m.invite, m.notice = nil, "Challenge withdrawn"
		case m.waiting != "":
			m.notice, m.waiting = textfmt.Plain(m.waiting)+" left", ""
		case m.screen == gamePlay && m.matchID == env.ID:
			m = m.endMatch()
			m.notice = textfmt.Plain(m.opponent()) + " left the game"
		}
	}
	return m, nil
}

func (m gameModeModel) challenged(env game.Envelope) gameModeModel {
	if env.Game == nil {
		return m
	}
	s, ok := game.Find(m.scripts, *env.Game)
	from := textfmt.Plain(env.From)
	switch {
	case !ok:
		m.notice = "Declined " + from + ": unknown game"
	case m.width < s.Info.MinW+4 || m.height < s.Info.MinH+6:
		m.notice = fmt.Sprintf("Declined %s's %s: the terminal needs %d×%d",
			from, s.Info.Name, s.Info.MinW+4, s.Info.MinH+6)
	default:
		m.invite = &env
		m.notice = fmt.Sprintf("%s challenges you to %s — y/n", from, s.Info.Name)
		return m
	}
	return m.send(game.Envelope{T: game.TDecline, ID: env.ID})
}

func (m gameModeModel) startMatch(env game.Envelope) (gameModeModel, tea.Cmd) {
	if env.Game == nil || len(env.Names) != 2 || (env.Seat != 1 && env.Seat != 2) {
		return m, nil
	}
	s, ok := game.Find(m.scripts, *env.Game)
	if !ok {
		return m.send(game.Envelope{T: game.TLeave}), nil
	}
	m.invite, m.waiting, m.notice = nil, "", ""
	m.matchID, m.script, m.seat, m.names = env.ID, s, env.Seat, env.Names
	m.board, m.status, m.over = nil, "", false
	m.screen = gamePlay
	if m.seat == 2 {
		m.status = "waiting for " + textfmt.Plain(m.names[0])
		return m, nil
	}
	sess, err := game.NewSession(s, m.names)
	if err != nil {
		return m.fail(err), nil
	}
	m.session, m.guestSent, m.guestStatus, m.guestOver = sess, nil, "", false
	m = m.refresh()
	return m, m.tick()
}

func (m gameModeModel) onPayload(p json.RawMessage) (gameModeModel, tea.Cmd) {
	if m.screen != gamePlay {
		return m, nil
	}
	if m.seat == 1 {
		var k game.KeyMsg
		if m.session == nil || m.over || json.Unmarshal(p, &k) != nil {
			return m, nil
		}
		if err := m.session.Key(2, k.Key); err != nil {
			return m.fail(err), nil
		}
		return m.refresh(), nil
	}
	var f game.FrameMsg
	if json.Unmarshal(p, &f) != nil || f.Height < 0 || f.Height > gameFrameMax {
		return m, nil
	}
	board := make([]string, f.Height)
	copy(board, m.board)
	for i, line := range f.Lines {
		if i >= 0 && i < f.Height {
			board[i] = textfmt.Plain(line)
		}
	}
	m.board, m.status, m.over = board, textfmt.Plain(f.Status), f.Over
	return m, nil
}

// refresh redraws the host's view and sends the guest whatever changed in
// theirs.
func (m gameModeModel) refresh() gameModeModel {
	info := m.script.Info
	board, status, err := m.session.View(1, info.MinW, info.MinH)
	if err != nil {
		return m.fail(err)
	}
	over, err := m.session.Over()
	if err != nil {
		return m.fail(err)
	}
	m.board, m.status, m.over = strings.Split(board, "\n"), status, over

	gboard, gstatus, err := m.session.View(2, info.MinW, info.MinH)
	if err != nil {
		return m.fail(err)
	}
	lines := strings.Split(gboard, "\n")
	diff := map[int]string{}
	for i, l := range lines {
		if i >= len(m.guestSent) || m.guestSent[i] != l {
			diff[i] = l
		}
	}
	if len(diff) == 0 && len(lines) == len(m.guestSent) &&
		gstatus == m.guestStatus && over == m.guestOver {
		return m
	}
	p, err := json.Marshal(game.FrameMsg{Lines: diff, Height: len(lines), Status: gstatus, Over: over})
	if err != nil {
		return m.fail(fmt.Errorf("encode frame: %w", err))
	}
	m.guestSent, m.guestStatus, m.guestOver = lines, gstatus, over
	return m.send(game.Envelope{T: game.TGame, Payload: p})
}

// fail ends the match over a script error and tells the peer.
func (m gameModeModel) fail(err error) gameModeModel {
	m = m.send(game.Envelope{T: game.TLeave})
	m = m.endMatch()
	m.notice = textfmt.CleanError(err)
	return m
}

func (m gameModeModel) endMatch() gameModeModel {
	if m.session != nil {
		m.session.Close()
	}
	m.session, m.matchID, m.board, m.guestSent = nil, "", nil, nil
	if m.screen == gamePlay {
		m.screen = gameLobby
	}
	return m
}

func (m gameModeModel) opponent() string {
	if len(m.names) != 2 {
		return "your opponent"
	}
	return m.names[2-m.seat]
}

// --- Keys --------------------------------------------------------------------

func (m gameModeModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.screen != gamePlay {
		if len(k) == 5 && strings.HasPrefix(k, "alt+") && k[4] >= '1' && k[4] <= '9' {
			if i := int(k[4] - '1'); i < len(m.mgr.Accounts) {
				m.switchTo = m.mgr.Accounts[i]
				return m, tea.Quit
			}
			return m, nil
		}
		if k == "?" && m.screen == gameLobby {
			m.fullHelp = !m.fullHelp
			return m, nil
		}
	}
	switch m.screen {
	case gameSetup:
		return m.setupKey(msg)
	case gameLobby:
		return m.lobbyKey(k)
	}
	return m.playKey(k)
}

func (m gameModeModel) setupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		if m.client != nil {
			m.screen, m.notice = gameLobby, ""
			return m, nil
		}
		return m, tea.Quit
	case "tab", "down":
		return m.focusField(m.field + 1)
	case "shift+tab", "up":
		return m.focusField(m.field - 1)
	case "enter":
		if m.field < len(m.inputs)-1 {
			return m.focusField(m.field + 1)
		}
		return m.connect()
	}
	var cmd tea.Cmd
	m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
	return m, cmd
}

func (m gameModeModel) focusField(i int) (tea.Model, tea.Cmd) {
	m.inputs[m.field].Blur()
	m.field = (i + len(m.inputs)) % len(m.inputs)
	return m, m.inputs[m.field].Focus()
}

func (m gameModeModel) connect() (tea.Model, tea.Cmd) {
	if m.dialing {
		return m, nil
	}
	url := strings.TrimSpace(m.inputs[0].Value())
	name := strings.TrimSpace(m.inputs[1].Value())
	switch {
	case url == "":
		m.notice = "Enter a base URL, such as ws://localhost:8765"
		return m, nil
	case name == "" || len([]rune(name)) > gameNameMax || textfmt.Plain(name) != name:
		m.notice = fmt.Sprintf("Enter a player name of 1 to %d characters", gameNameMax)
		return m, nil
	}
	for key, v := range map[string]string{settingBaseURL: url, settingBaseName: name,
		settingBaseToken: m.inputs[2].Value()} {
		if err := m.mgr.Store.SetSetting(key, v); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
	}
	if m.client != nil {
		m.client.Close()
		m.client, m.players = nil, nil
	}
	m.baseURL, m.name, m.dialing, m.notice = url, name, true, "Connecting…"
	return m, m.dial()
}

func (m gameModeModel) lobbyKey(k string) (tea.Model, tea.Cmd) {
	if m.invite != nil && (k == "y" || k == "n") {
		t := game.TDecline
		if k == "y" {
			t = game.TAccept
		}
		id := m.invite.ID
		m.invite, m.notice = nil, ""
		return m.send(game.Envelope{T: t, ID: id}), nil
	}
	m.notice = ""
	switch k {
	case "esc":
		if m.waiting != "" {
			m.waiting, m.notice = "", "Challenge cancelled"
			return m.send(game.Envelope{T: game.TLeave}), nil
		}
		return m, tea.Quit
	case "q", "ctrl+c", "alt+g":
		return m, tea.Quit
	case "tab", "left", "right", "h", "l":
		m.playersFocused = !m.playersFocused
	case "up", "k":
		if m.playersFocused {
			m.psel = max(0, m.psel-1)
		} else {
			m.gsel = max(0, m.gsel-1)
		}
	case "down", "j":
		if m.playersFocused {
			m.psel = clampIndex(m.psel+1, len(m.players))
		} else {
			m.gsel = clampIndex(m.gsel+1, len(m.scripts))
		}
	case "enter":
		if !m.playersFocused {
			m.playersFocused = true
			return m, nil
		}
		return m.challenge()
	case "e":
		m.screen, m.notice = gameSetup, ""
		return m, m.inputs[m.field].Focus()
	}
	return m, nil
}

func (m gameModeModel) challenge() (tea.Model, tea.Cmd) {
	if m.psel >= len(m.players) || m.gsel >= len(m.scripts) {
		return m, nil
	}
	p, s := m.players[m.psel], m.scripts[m.gsel]
	ref := s.Info.Ref()
	name := textfmt.Plain(p.Name)
	switch {
	case m.waiting != "":
		m.notice = "Waiting for " + textfmt.Plain(m.waiting) + "; esc cancels"
	case p.Busy:
		m.notice = name + " is busy"
	case !p.Has(ref):
		m.notice = fmt.Sprintf("%s does not have %s v%d", name, s.Info.Name, ref.Version)
	default:
		m.waiting = p.Name
		m.notice = "Waiting for " + name + " to accept… esc cancels"
		return m.send(game.Envelope{T: game.TChallenge, To: p.Name, Game: &ref}), nil
	}
	return m, nil
}

func (m gameModeModel) playKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "ctrl+c":
		// Base keeps both players busy until one leaves, even after game over.
		m = m.send(game.Envelope{T: game.TLeave})
		return m.endMatch(), nil
	}
	if m.over {
		return m, nil
	}
	if m.seat == 2 {
		p, err := json.Marshal(game.KeyMsg{Key: k})
		if err != nil {
			return m, nil
		}
		return m.send(game.Envelope{T: game.TGame, Payload: p}), nil
	}
	if m.session == nil {
		return m, nil
	}
	if err := m.session.Key(1, k); err != nil {
		return m.fail(err), nil
	}
	return m.refresh(), nil
}

// --- Rendering ---------------------------------------------------------------

func (m gameModeModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m gameModeModel) render() string {
	footer := renderThemedFooter(m.helpKeys(), m.fullHelp, m.notice, m.width, colorGame)
	bodyHeight := max(4, m.height-lipgloss.Height(footer))
	var body string
	switch {
	case m.screen == gameSetup:
		body = renderBox("Game base", m.setupLines(m.width-4, bodyHeight-2),
			m.width, bodyHeight, colorGame)
	case m.screen == gamePlay:
		title := fmt.Sprintf("%s · you vs %s", m.script.Info.Name, textfmt.Plain(m.opponent()))
		body = renderBox(title, m.playLines(m.width-4, bodyHeight-2), m.width, bodyHeight, colorGame)
	case m.width >= twoPaneWidth:
		left := renderBox("Games", m.gameLines(agentSidebarWidth-4, bodyHeight-2),
			agentSidebarWidth, bodyHeight, gameBorderColor(!m.playersFocused))
		right := renderBox("Players on "+m.baseURL, m.playerLines(m.width-agentSidebarWidth-4,
			bodyHeight-2), m.width-agentSidebarWidth, bodyHeight, gameBorderColor(m.playersFocused))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	case m.playersFocused:
		body = renderBox("Players on "+m.baseURL, m.playerLines(m.width-4, bodyHeight-2),
			m.width, bodyHeight, colorGame)
	default:
		body = renderBox("Games", m.gameLines(m.width-4, bodyHeight-2), m.width, bodyHeight, colorGame)
	}
	return body + "\n" + footer
}

func gameBorderColor(focused bool) color.Color {
	if focused {
		return colorGame
	}
	return colorDim
}

func (m gameModeModel) setupLines(width, height int) []string {
	lines := []string{"", styleSubtle.Render("Play the built-in games with other wiregram users on a base."), ""}
	for i, label := range gameFieldLabels {
		style := styleSubtle
		if i == m.field {
			style = styleGame
		}
		lines = append(lines, style.Render(label), "  "+m.inputs[i].View(), "")
	}
	lines = append(lines, styleDim.Render("Start a local base with: wiregram base"))
	return lines
}

func (m gameModeModel) gameLines(width, height int) []string {
	if len(m.scripts) == 0 {
		return centerLines(width, height, styleSubtle.Render("No games in this build."))
	}
	var lines []string
	for i, s := range m.scripts {
		lines = append(lines, themedListRow(s.Info.Name, styleDim.Render(fmt.Sprintf("v%d", s.Info.Version)),
			i == m.gsel, !m.playersFocused, width, colorGame))
	}
	if m.gsel < len(m.scripts) {
		lines = append(lines, "", styleSubtle.Render(truncate(m.scripts[m.gsel].Info.Desc, width)))
	}
	return lines
}

func (m gameModeModel) playerLines(width, height int) []string {
	if m.dialing || m.client == nil {
		return centerLines(width, height, styleSubtle.Render("Connecting…"))
	}
	if len(m.players) == 0 {
		return centerLines(width, height, styleSubtle.Render(
			"Nobody else is here yet.\n\nAsk a friend to open games (Alt+G)\nand connect to "+m.baseURL))
	}
	var ref game.GameRef
	if m.gsel < len(m.scripts) {
		ref = m.scripts[m.gsel].Info.Ref()
	}
	var lines []string
	for i := window(m.psel, height); i < len(m.players); i++ {
		p := m.players[i]
		right, text := "", textfmt.Plain(p.Name)
		switch {
		case p.Busy:
			right = "busy"
		case !p.Has(ref):
			right = fmt.Sprintf("no v%d", ref.Version)
		}
		if right != "" {
			text, right = styleDim.Render(text), styleDim.Render(right)
		}
		lines = append(lines, themedListRow(text, right, i == m.psel, m.playersFocused, width, colorGame))
	}
	return lines
}

func (m gameModeModel) playLines(width, height int) []string {
	status := m.status
	if m.over {
		status += " · esc for the lobby"
	}
	block := append([]string{}, m.board...)
	block = append(block, "", styleGame.Render(status))
	if help := m.script.Info.Help; help != "" && !m.over {
		block = append(block, styleDim.Render(help))
	}
	return centerLines(width, height, strings.Join(block, "\n"))
}

func (m gameModeModel) helpKeys() []helpKey {
	switch m.screen {
	case gameSetup:
		keys := []helpKey{{"tab", "next field"}, {"⏎", "connect"}}
		if m.client != nil {
			return append(keys, helpKey{"esc", "back"})
		}
		return append(keys, helpKey{"esc", "close"})
	case gamePlay:
		if m.over {
			return []helpKey{{"esc", "lobby"}}
		}
		return []helpKey{{"esc", "leave game"}}
	}
	if m.invite != nil {
		return []helpKey{{"y", "accept"}, {"n", "decline"}}
	}
	return []helpKey{
		{"↑↓", "select"},
		{"tab", "games/players"},
		{"⏎", "challenge"},
		{"e", "base"},
		{"esc", "close"},
		{"?", "more"},
	}
}
