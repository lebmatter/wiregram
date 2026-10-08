package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
)

const (
	splashMin = 600 * time.Millisecond
	splashMax = 1200 * time.Millisecond
)

type splashModel struct {
	mgr           *account.AccountManager
	started       time.Time
	spin          spinner.Model
	width, height int
}

func (m splashModel) Init() tea.Cmd { return m.spin.Tick }

func (m splashModel) connecting() int {
	n := 0
	for _, a := range m.mgr.Accounts {
		if s := a.Snapshot().Status; s == "connecting" || s == "reconnecting" {
			n++
		}
	}
	return n
}

func (m splashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg, tea.MouseClickMsg:
		return m, tea.Quit
	case spinner.TickMsg:
		elapsed := time.Since(m.started)
		if elapsed >= splashMax || (elapsed >= splashMin && m.connecting() == 0) {
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m splashModel) View() tea.View {
	status := "ready"
	if n := m.connecting(); n > 0 {
		status = fmt.Sprintf("%sconnecting %d %s", m.spin.View(), n, plural(n, "account", "accounts"))
		if m.mgr.Agents != nil {
			if b := m.mgr.Agents.Count(); b > 0 {
				status += fmt.Sprintf(" and %d %s", b, plural(b, "agent", "agents"))
			}
		}
		status += "…"
	}
	content := lipgloss.JoinVertical(lipgloss.Center,
		renderLogo(m.width),
		"",
		styleSubtle.Render("a minimal chat client for the terminal"),
		"",
		styleSubtle.Render(status),
	)
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// tuiSplash shows the logo briefly while accounts start connecting.
func tuiSplash(ctx context.Context, mgr *account.AccountManager) {
	w, h := termSize()
	m := splashModel{
		mgr:     mgr,
		started: time.Now(),
		spin:    spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(styleAccent)),
		width:   w,
		height:  h,
	}
	// A splash failure is cosmetic; the account screens report real errors.
	_, _ = tea.NewProgram(m, tea.WithContext(ctx)).Run()
}
