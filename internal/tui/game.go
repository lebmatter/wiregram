package tui

import (
	"encoding/json"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
	"wiregram/internal/game"
	"wiregram/internal/textfmt"
)

type gameState int

const (
	gameOpening gameState = iota // the host waits for cloudflared
	gameWaiting                  // the invite is out
	gameJoining                  // the guest is connecting
	gamePlaying
	gameOver
)

// gameModel is the one game of the account screen, in a pane beside the chat
// it was started from. The host runs the script for both seats; the guest
// sends keys and draws the frames it gets back.
type gameModel struct {
	st    *styles
	id    int // tags its messages, so those of a closed game are dropped
	chat  backend.ChatEntry
	host  bool
	state gameState
	token string
	info  game.Info
	names []string
	// script and session run on the host only.
	script  game.Script
	session *game.Session
	server  *game.Host
	tunnel  *game.Tunnel
	conn    *game.Conn
	board   []string
	status  string
	result  string
	// sent and sentStatus are the guest's board and status as last sent.
	sent       []string
	sentStatus string
	ended      string // why the game stopped early, shown in the pane
	confirm    *confirmModal
}

// The game's messages carry its id.
type (
	gameChallengeReq struct{}
	gameJoinReq      struct {
		host, token string
		own         bool // the user sent the invite
	}
	gameTunnelOpened struct {
		id     int
		tunnel *game.Tunnel
		err    error
	}
	gameInviteSent struct {
		id  int
		res chatSendResult
	}
	gameJoined struct {
		id     int
		joined game.Joined
	}
	gameDialed struct {
		id   int
		conn *game.Conn
		err  error
	}
	gameEnvelope struct {
		id  int
		env game.Envelope
		ok  bool
	}
	gameTick    struct{ id int }
	gameExpired struct{ id int }
)

// gameFallbackWidth is the guest's pane width before the host names the game.
const gameFallbackWidth = 36

// gameChatMinWidth is the narrowest the chat gets beside a game; narrower,
// the game takes the chat's place.
const gameChatMinWidth = 50

// gameTitle is the pane's title: the game and who plays it.
func (g *gameModel) title() string {
	name := g.info.Name
	if name == "" {
		name = "game"
	}
	if len(g.names) == 2 {
		other := g.names[1]
		if !g.host {
			other = g.names[0]
		}
		return "game · " + name + " · you vs " + textfmt.Plain(other)
	}
	return "game · " + name
}

func (g *gameModel) width() int {
	if g.info.MinW == 0 {
		return gameFallbackWidth
	}
	return g.info.MinW + 4
}

// live reports whether leaving would end a game someone is playing.
func (g *gameModel) live() bool { return g.state == gamePlaying }

// close ends everything the game holds: the other player is told.
func (g *gameModel) close() {
	if g.conn != nil {
		g.conn.Close()
	}
	if g.session != nil {
		g.session.Close()
	}
	if g.server != nil {
		g.server.Close()
	}
	if g.tunnel != nil {
		g.tunnel.Close()
	}
	g.conn, g.session, g.server, g.tunnel = nil, nil, nil, nil
}

// stop ends a game early but keeps the pane to say why.
func (g *gameModel) stop(why string) {
	g.close()
	g.state, g.ended = gameOver, why
}

func (g *gameModel) seat() int {
	if g.host {
		return 1
	}
	return 2
}

func (g *gameModel) opponent() string {
	if len(g.names) < 2 {
		return "the other player"
	}
	return textfmt.Plain(g.names[2-g.seat()])
}

func (g *gameModel) listen() tea.Cmd {
	conn, id := g.conn, g.id
	if conn == nil {
		return nil
	}
	return func() tea.Msg {
		env, ok := <-conn.Events
		return gameEnvelope{id, env, ok}
	}
}

func (g *gameModel) tick() tea.Cmd {
	rate, id := g.info.TickRate(), g.id
	if rate == 0 || !g.host || g.state != gamePlaying {
		return nil
	}
	return tea.Tick(rate, func(time.Time) tea.Msg { return gameTick{id} })
}

// refresh redraws the host's board and sends the guest the lines of its own
// that changed.
func (g *gameModel) refresh() error {
	board, status, err := g.session.View(1, g.info.MinW, g.info.MinH)
	if err != nil {
		return err
	}
	over, err := g.session.Over()
	if err != nil {
		return err
	}
	g.board, g.status = strings.Split(board, "\n"), status
	if over {
		if g.result, err = g.session.Result(1); err != nil {
			return err
		}
		g.state = gameOver
	}
	board, status, err = g.session.View(2, g.info.MinW, g.info.MinH)
	if err != nil {
		return err
	}
	lines := strings.Split(board, "\n")
	frame := game.FrameMsg{Height: len(lines), Status: status, Over: over}
	if over {
		if frame.Result, err = g.session.Result(2); err != nil {
			return err
		}
	}
	for i, l := range lines {
		if i >= len(g.sent) || g.sent[i] != l {
			if frame.Lines == nil {
				frame.Lines = map[int]string{}
			}
			frame.Lines[i] = l
		}
	}
	if frame.Lines == nil && len(lines) == len(g.sent) && !over && status == g.sentStatus {
		return nil
	}
	g.sent, g.sentStatus = lines, status
	return g.conn.Send(game.Envelope{T: game.TFrame, P: game.Payload(frame)})
}

// apply draws a frame from the host.
func (g *gameModel) apply(f game.FrameMsg) {
	h := min(max(f.Height, 0), game.MaxFrameLines)
	board := make([]string, h)
	copy(board, g.board)
	for i, l := range f.Lines {
		if i >= 0 && i < h {
			board[i] = textfmt.Plain(l)
		}
	}
	g.board, g.status = board, textfmt.Plain(f.Status)
	if f.Over {
		g.state = gameOver
		switch f.Result {
		case "win", "lose", "draw":
			g.result = f.Result
		}
	}
}

// key plays a key press from the user.
func (g *gameModel) key(k string) error {
	if g.state != gamePlaying {
		return nil
	}
	if !g.host {
		return g.conn.Send(game.Envelope{T: game.TKey, P: game.Payload(game.KeyMsg{Key: k})})
	}
	if err := g.session.Key(1, k); err != nil {
		return err
	}
	return g.refresh()
}

func (g *gameModel) view(width, height int, focused bool) string {
	right := "esc close"
	if g.live() {
		right = "esc leave"
	}
	title := g.st.brand.Render(g.title())
	var body string
	switch g.state {
	case gameOpening:
		body = g.st.subtle.Render("opening a tunnel…") + "\n" +
			g.st.dim.Render("cloudflared is starting")
	case gameWaiting:
		body = g.st.subtle.Render("waiting for someone to join…") + "\n" +
			g.st.dim.Render("the invite expires in 10 minutes")
	case gameJoining:
		body = g.st.subtle.Render("joining…")
	default:
		body = g.playView()
	}
	return renderTitledBox(title, g.st.dim.Render(right), centerLines(width-4, height-2, body),
		width, height, g.st.border(focused))
}

func (g *gameModel) playView() string {
	lines := append([]string{}, g.board...)
	lines = append(lines, "")
	if g.state == gameOver {
		switch {
		case g.ended != "":
			lines = append(lines, g.st.subtle.Render(g.ended))
		case g.result == "win":
			lines = append(lines, g.st.brand.Render("You won!"))
		case g.result == "lose":
			lines = append(lines, g.st.brand.Render("You lost"))
		case g.result == "draw":
			lines = append(lines, g.st.brand.Render("Draw"))
		default:
			lines = append(lines, g.st.brand.Render("Game over"))
		}
	}
	if g.ended == "" {
		lines = append(lines, g.st.accent.Render(g.status))
	}
	if g.state == gamePlaying && g.info.Help != "" {
		lines = append(lines, g.st.dim.Render(g.info.Help))
	}
	return strings.Join(lines, "\n")
}

func (g *gameModel) helpKeys() []helpKey {
	if g.live() {
		return []helpKey{{"keys", "play"}, {"tab", "chat"}, {"esc", "leave"}}
	}
	return []helpKey{{"tab", "chat"}, {"esc", "close"}}
}

// gamePlayerName is the user's name as the other player sees it: the
// account's name without its service tag, phone number or server.
func gamePlayerName(b backend.Backend) string {
	name := b.AccountLabel()
	if strings.HasPrefix(name, "[") {
		if i := strings.Index(name, "] "); i >= 0 {
			name = name[i+2:]
		}
	}
	if i := strings.Index(name, " (+"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "@")
	if i := strings.IndexAny(name, "@:"); i > 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(textfmt.Plain(name))
	if name == "" || strings.HasPrefix(name, "+") {
		return "player"
	}
	if r := []rune(name); len(r) > 32 {
		name = string(r[:32])
	}
	return name
}

// gameRect is where the game pane sits in the body, if it shows: beside its
// chat, or in the chat's place when there is no room for both.
func (m tuiModel) gameRect() (left, width int, alone, ok bool) {
	if m.game == nil || !m.chatOpen || m.chat.entry.ID != m.game.chat.ID {
		return 0, 0, false, false
	}
	area, areaWidth := 0, m.width
	if m.twoPane() {
		area, areaWidth = sidebarWidth, m.width-sidebarWidth
	}
	width = max(m.game.width(), areaWidth*4/9)
	if areaWidth-width < gameChatMinWidth {
		return area, areaWidth, true, true
	}
	return area + areaWidth - width, width, false, true
}

// gameShown reports whether the game pane is on screen.
func (m tuiModel) gameShown() bool {
	_, _, _, ok := m.gameRect()
	return ok
}

// startChallenge hosts script for the open chat: a local server, then a
// tunnel to it, then the invite.
func (m tuiModel) startChallenge(script game.Script) (tuiModel, tea.Cmd) {
	if m.game != nil {
		m = m.gameAlert("Finish the current game first.")
		return m, nil
	}
	token, err := game.NewToken()
	if err != nil {
		return m.gameAlert(textfmt.CleanError(err)), nil
	}
	server, err := game.Listen(m.ctx, token)
	if err != nil {
		return m.gameAlert(textfmt.CleanError(err)), nil
	}
	m.gameSeq++
	g := &gameModel{st: m.st, id: m.gameSeq, chat: m.chat.entry, host: true, token: token,
		info: script.Info, script: script, server: server}
	m.game, m.focus = g, focusGame
	ctx, id, addr := m.ctx, g.id, server.Addr
	open := func() tea.Msg {
		t, err := game.StartTunnel(ctx, addr)
		return gameTunnelOpened{id, t, err}
	}
	join := func() tea.Msg {
		select {
		case j := <-server.Joined:
			return gameJoined{id, j}
		case <-server.Done():
			return nil
		}
	}
	return m, tea.Batch(open, join)
}

// joinGame joins the game behind an invite clicked in the open chat.
func (m tuiModel) joinGame(req gameJoinReq) (tuiModel, tea.Cmd) {
	if g := m.game; g != nil {
		if g.token == req.token {
			if m.gameShown() {
				m.focus = focusGame
			}
			return m, nil
		}
		m = m.gameAlert("Finish the current game first.")
		return m, nil
	}
	if req.own {
		m = m.gameAlert("This is your own challenge, and it has ended.")
		return m, nil
	}
	m.gameSeq++
	g := &gameModel{st: m.st, id: m.gameSeq, chat: m.chat.entry, state: gameJoining,
		token: req.token}
	m.game, m.focus = g, focusGame
	ctx, id, name := m.ctx, g.id, gamePlayerName(m.b)
	return m, func() tea.Msg {
		conn, err := game.Dial(ctx, req.host, req.token, name)
		return gameDialed{id, conn, err}
	}
}

// closeGame ends the game and takes its pane away.
func (m tuiModel) closeGame() tuiModel {
	if m.game == nil {
		return m
	}
	m.game.close()
	m.game = nil
	if m.focus == focusGame {
		m.focus = focusChat
	}
	return m
}

// gameFailed ends the game, taking its pane away, and says why in an alert.
func (m tuiModel) gameFailed(err error) tuiModel {
	return m.closeGame().gameAlert(textfmt.CleanError(err))
}

// stopGame ends the game but keeps its pane, and says why in an alert.
func (m tuiModel) stopGame(err error) tuiModel {
	text := textfmt.CleanError(err)
	m.game.stop(text)
	return m.gameAlert(text)
}

// gameAlert tells the user what went wrong with a game, over the screen.
func (m tuiModel) gameAlert(text string) tuiModel {
	m.alert = newAlertModal(m.st, "Game", text)
	return m
}

// updateGame handles the game's messages; handled is false for any other.
func (m tuiModel) updateGame(msg tea.Msg) (tuiModel, tea.Cmd, bool) {
	g := m.game
	switch msg := msg.(type) {
	case gameChallengeReq:
		if g != nil {
			m = m.gameAlert("Finish the current game first.")
			return m, nil, true
		}
		scripts, err := game.Catalog()
		if len(scripts) == 0 {
			if err == nil {
				err = errNoGames
			}
			return m.gameAlert(textfmt.CleanError(err)), nil, true
		}
		m.gamePicker = newGamePicker(m.st, scripts)
		return m, nil, true

	case gameJoinReq:
		m, cmd := m.joinGame(msg)
		return m, cmd, true

	case gameTunnelOpened:
		if g == nil || g.id != msg.id {
			if msg.tunnel != nil {
				msg.tunnel.Close()
			}
			return m, nil, true
		}
		if msg.err != nil {
			return m.gameFailed(msg.err), nil, true
		}
		g.tunnel, g.state = msg.tunnel, gameWaiting
		ctx, b, entry, id := m.ctx, m.b, g.chat, g.id
		text := game.InviteText(g.info.Name, msg.tunnel.Host, g.token)
		send := func() tea.Msg {
			sent, err := b.Send(ctx, entry, text, "")
			return gameInviteSent{id, chatSendResult{chatTag{entry.ID}, sent, err}}
		}
		expire := tea.Tick(game.Expiry, func(time.Time) tea.Msg { return gameExpired{id} })
		return m, tea.Batch(send, expire), true

	case gameInviteSent:
		var cmd tea.Cmd
		if m.chatOpen && m.chat.entry.ID == msg.res.chatTag.chat {
			m.chat, cmd = m.chat.Update(msg.res)
		}
		if g != nil && g.id == msg.id && msg.res.err != nil {
			m = m.gameFailed(msg.res.err)
		}
		return m, cmd, true

	case gameExpired:
		if g != nil && g.id == msg.id && g.state == gameWaiting {
			m = m.gameFailed(errGameExpired)
		}
		return m, nil, true

	case gameJoined:
		if g == nil || g.id != msg.id {
			msg.joined.Conn.Close()
			return m, nil, true
		}
		g.conn = msg.joined.Conn
		g.names = []string{gamePlayerName(m.b), msg.joined.Name}
		session, err := game.NewSession(g.script, g.names)
		if err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		g.session, g.state = session, gamePlaying
		info := g.info
		if err := g.conn.Send(game.Envelope{T: game.TStart, Game: &info, Names: g.names}); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		if err := g.refresh(); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		return m, tea.Batch(g.listen(), g.tick()), true

	case gameDialed:
		if g == nil || g.id != msg.id {
			if msg.conn != nil {
				msg.conn.Close()
			}
			return m, nil, true
		}
		if msg.err != nil {
			return m.gameFailed(msg.err), nil, true
		}
		g.conn = msg.conn
		return m, g.listen(), true

	case gameEnvelope:
		if g == nil || g.id != msg.id {
			return m, nil, true
		}
		return m.gameEnvelope(msg)

	case gameTick:
		if g == nil || g.id != msg.id || g.state != gamePlaying {
			return m, nil, true
		}
		if err := g.session.Tick(); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		if err := g.refresh(); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		return m, g.tick(), true
	}
	return m, nil, false
}

func (m tuiModel) gameEnvelope(msg gameEnvelope) (tuiModel, tea.Cmd, bool) {
	g := m.game
	if !msg.ok {
		if g.state != gameOver {
			g.stop(g.opponent() + " left the game")
		}
		return m, nil, true
	}
	env := msg.env
	switch env.T {
	case game.TStart:
		if g.host || g.state != gameJoining || env.Game == nil || len(env.Names) != 2 {
			break
		}
		info := *env.Game
		info.Name, info.Help = textfmt.Plain(info.Name), textfmt.Plain(info.Help)
		info.MinW, info.MinH = min(max(info.MinW, 1), 120), min(max(info.MinH, 1), 60)
		g.info, g.state = info, gamePlaying
		g.names = []string{textfmt.Plain(env.Names[0]), textfmt.Plain(env.Names[1])}
	case game.TFrame:
		var f game.FrameMsg
		if g.host || g.state != gamePlaying || json.Unmarshal(env.P, &f) != nil {
			break
		}
		g.apply(f)
	case game.TKey:
		var k game.KeyMsg
		if !g.host || g.state != gamePlaying || json.Unmarshal(env.P, &k) != nil {
			break
		}
		if err := g.session.Key(2, k.Key); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
		if err := g.refresh(); err != nil {
			m = m.stopGame(err)
			return m, nil, true
		}
	case game.TLeave:
		if g.state != gameOver {
			g.stop(g.opponent() + " left the game")
		} else {
			g.close()
		}
		return m, nil, true
	case game.TError:
		if g.state == gameJoining {
			return m.gameFailed(gameError(textfmt.Plain(env.Error))), nil, true
		}
		m = m.stopGame(gameError(textfmt.Plain(env.Error)))
		return m, nil, true
	}
	return m, g.listen(), true
}

// gameKey handles a key while the game pane has the focus.
func (m tuiModel) gameKey(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	g := m.game
	if c := g.confirm; c != nil {
		yes, done := c.key(msg.String())
		if done {
			g.confirm = nil
		}
		if yes {
			m = m.closeGame()
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		if g.live() {
			g.confirm = newConfirmModal(m.st, "Leave game", "Leave the game? "+g.opponent()+
				" wins.", "Leave", true)
			return m, nil
		}
		return m.closeGame(), nil
	case "ctrl+c":
		return m, nil
	}
	if err := g.key(msg.String()); err != nil {
		m = m.stopGame(err)
		return m, nil
	}
	return m, nil
}

type gameError string

func (e gameError) Error() string { return string(e) }

const (
	errNoGames     = gameError("no games are available")
	errGameExpired = gameError("the game challenge expired")
)

// gamePickerModel picks the game to challenge the chat to.
type gamePickerModel struct {
	st      *styles
	scripts []game.Script
	sel     int
}

func newGamePicker(st *styles, scripts []game.Script) *gamePickerModel {
	return &gamePickerModel{st: st, scripts: scripts}
}

// key moves the selection; done when the picker closes, with the pick if any.
func (p *gamePickerModel) key(k string) (pick *game.Script, done bool) {
	switch k {
	case "esc", "ctrl+c":
		return nil, true
	case "up", "k":
		p.sel = max(0, p.sel-1)
	case "down", "j":
		p.sel = min(len(p.scripts)-1, p.sel+1)
	case "enter":
		return &p.scripts[p.sel], true
	}
	return nil, false
}

func (p *gamePickerModel) size() (int, int) {
	w := 30
	for _, s := range p.scripts {
		w = max(w, textfmt.DisplayWidth(s.Info.Name+"  "+s.Info.Desc)+4+modalMark)
	}
	// The borders, the heading, the rows, a blank, the key rule and keys.
	return min(w, 60), len(p.scripts) + 6
}

func (p *gamePickerModel) View() string {
	w, h := p.size()
	body := []string{p.st.modalRule("challenge to a game", w-4)}
	for i, s := range p.scripts {
		label := s.Info.Name
		if i == p.sel {
			label = p.st.brand.Render(label)
		}
		body = append(body, p.st.modalRow("", label+"  "+p.st.dim.Render(s.Info.Desc), i == p.sel,
			w-4))
	}
	body = append(body, "")
	body = append(body, p.st.modalKeys([]helpKey{{"enter", "challenge"}, {"esc", "cancel"}}, "",
		w-4)...)
	return p.st.modalBox("game", body, w, h)
}

// click picks the row at row y of the box, counted from its top edge.
func (p *gamePickerModel) click(y int) (pick *game.Script, ok bool) {
	row := y - 2 // under the top border and the heading
	if row < 0 || row >= len(p.scripts) {
		return nil, false
	}
	p.sel = row
	return &p.scripts[row], true
}

func (p *gamePickerModel) helpKeys() []helpKey {
	return []helpKey{{"↑↓", "select"}, {"enter", "challenge"}, {"esc", "cancel"}}
}

// gamePickerRect is where the picker sits: centred over the chat.
func (m tuiModel) gamePickerRect() (x, y, w, h int) {
	left := 0
	if m.twoPane() {
		left = sidebarWidth
	}
	w, h = m.gamePicker.size()
	x, y = modalRect(m.chat.width, m.bodyHeight(), w, h)
	return left + x, y, w, h
}

// chatWithGame is the open chat with its game beside it, or the game alone
// when there is no room for both.
func (m tuiModel) chatWithGame() string {
	_, width, alone, ok := m.gameRect()
	if !ok {
		return m.chat.View()
	}
	pane := m.game.view(width, m.bodyHeight(), m.focus == focusGame)
	if alone {
		return pane
	}
	return joinColumns(m.chat.View(), pane)
}

// gameNotice tells where a game is waiting while its pane is hidden.
func (m tuiModel) gameNotice() string {
	if m.game == nil || m.gameShown() {
		return ""
	}
	return "game in " + chatName(m.game.chat)
}
