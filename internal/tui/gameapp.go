package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbletea/v2"

	"wiregram/internal/game"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

// gameAppModel is `wiregram game`: one game outside any chat. Without an
// invite it picks a game and hosts it, showing the invite to pass on; with
// one it joins. It quits once the game is closed.
type gameAppModel struct {
	ctx           context.Context
	st            *styles
	name          string // the user's name as the other player sees it
	width, height int
	picker        *gamePickerModel
	game          *gameModel
	start         tea.Cmd // dials the invite given on the command line
	// alert says why the game cannot go on; closing it quits.
	alert *confirmModal
}

// RunGame runs `wiregram game` as name: hosting a game picked on screen when
// invite is empty, else joining the game it links to.
func RunGame(ctx context.Context, db *store.DB, invite, name string) error {
	st := loadStyles(db)
	w, h := termSize()
	m := gameAppModel{ctx: ctx, st: st, name: name, width: w, height: h}
	if invite == "" {
		scripts, err := game.Catalog()
		if len(scripts) == 0 {
			if err == nil {
				err = errNoGames
			}
			return err
		}
		m.picker = newGamePicker(st, scripts, "someone")
	} else {
		host, token, ok := game.ParseInvite(invite)
		if !ok {
			return fmt.Errorf("%q is not a game invite (wiregame://NAME.trycloudflare.com/TOKEN)",
				invite)
		}
		m.game, m.start = dialGame(ctx, st, 1, host, token, name)
	}
	result, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return fmt.Errorf("run game: %w", err)
	}
	if g := result.(gameAppModel).game; g != nil {
		g.close()
	}
	return nil
}

func (m gameAppModel) Init() tea.Cmd { return m.start }

// fail ends the game and says why; closing the alert quits.
func (m gameAppModel) fail(err error) gameAppModel {
	if m.game != nil {
		m.game.close()
		m.game = nil
	}
	m.alert = newAlertModal(m.st, "Game", textfmt.CleanError(err))
	return m
}

// stop ends the game but keeps its board, and says why.
func (m gameAppModel) stop(err error) gameAppModel {
	text := textfmt.CleanError(err)
	m.game.stop(text)
	m.alert = newAlertModal(m.st, "Game", text)
	return m
}

func (m gameAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	g := m.game
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return m.key(msg.String())

	case gameTunnelOpened:
		if g == nil || g.id != msg.id {
			if msg.tunnel != nil {
				msg.tunnel.Close()
			}
			return m, nil
		}
		if msg.err != nil {
			return m.fail(msg.err), nil
		}
		g.tunnel, g.state = msg.tunnel, gameWaiting
		g.invite = fmt.Sprintf("wiregame://%s/%s", msg.tunnel.Host, g.token)
		id := g.id
		expire := tea.Tick(game.Expiry, func(time.Time) tea.Msg { return gameExpired{id} })
		return m, tea.Batch(tea.SetClipboard(g.invite), systemCopy(g.invite), expire)

	case gameExpired:
		if g != nil && g.id == msg.id && g.state == gameWaiting {
			return m.fail(errGameExpired), nil
		}

	case gameJoined:
		if g == nil || g.id != msg.id {
			msg.joined.Conn.Close()
			return m, nil
		}
		cmd, err := g.begin(msg.joined, m.name)
		if err != nil {
			return m.stop(err), nil
		}
		return m, cmd

	case gameDialed:
		if g == nil || g.id != msg.id {
			if msg.conn != nil {
				msg.conn.Close()
			}
			return m, nil
		}
		if msg.err != nil {
			return m.fail(msg.err), nil
		}
		g.conn = msg.conn
		return m, g.listen()

	case gameEnvelope:
		if g == nil || g.id != msg.id {
			return m, nil
		}
		joining := g.state == gameJoining
		cmd, err := g.receive(msg)
		switch {
		case err != nil && joining:
			return m.fail(err), nil
		case err != nil:
			return m.stop(err), nil
		}
		return m, cmd

	case gameTick:
		if g == nil || g.id != msg.id || g.state != gamePlaying {
			return m, nil
		}
		cmd, err := g.step()
		if err != nil {
			return m.stop(err), nil
		}
		return m, cmd
	}
	return m, nil
}

func (m gameAppModel) key(k string) (tea.Model, tea.Cmd) {
	if a := m.alert; a != nil {
		if _, done := a.key(k); done {
			m.alert = nil
			if m.game == nil {
				return m, tea.Quit
			}
		}
		return m, nil
	}
	if p := m.picker; p != nil {
		pick, done := p.key(k)
		if !done {
			return m, nil
		}
		m.picker = nil
		if pick == nil {
			return m, tea.Quit
		}
		g, cmd, err := hostGame(m.ctx, m.st, 1, *pick)
		if err != nil {
			return m.fail(err), nil
		}
		m.game = g
		return m, cmd
	}
	// Ctrl+C leaves as Esc does, asking first while the game is played.
	if k == "ctrl+c" {
		k = "esc"
	}
	leave, err := m.game.press(k)
	switch {
	case leave:
		m.game.close()
		m.game = nil
		return m, tea.Quit
	case err != nil:
		return m.stop(err), nil
	}
	return m, nil
}

func (m gameAppModel) View() tea.View {
	bodyHeight := max(1, m.height-1)
	body := renderBox("", nil, m.width, bodyHeight, m.st.dimColor)
	var keys []helpKey
	var modal *confirmModal
	switch {
	case m.picker != nil:
		w, h := m.picker.size()
		x, y := modalRect(m.width, bodyHeight, w, h)
		body = m.st.fadeUnder(body, m.picker.View(), x, y)
		keys = m.picker.helpKeys()
	case m.game != nil:
		body = m.game.view(m.width, bodyHeight, true)
		keys = []helpKey{{"esc", "quit"}}
		if m.game.live() {
			keys = []helpKey{{"keys", "play"}, {"esc", "leave"}}
		}
		modal = m.game.confirm
	}
	if m.alert != nil {
		modal = m.alert
	}
	if modal != nil {
		w, h := modal.size()
		x, y := modalRect(m.width, bodyHeight, w, h)
		body = m.st.fadeUnder(body, modal.View(), x, y)
		keys = modal.helpKeys()
	}
	v := tea.NewView(body + "\n" + m.st.footer(keys, "", m.width))
	v.AltScreen = true
	return v
}
