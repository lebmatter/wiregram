package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// profileOpen asks the root to show the profile of msg's sender, fetched
// from b, the backend the chat runs on.
type profileOpen struct {
	b   backend.Backend
	ctx context.Context
	msg backend.Message
}

type profileLoaded struct {
	userID  string
	profile backend.Profile
	err     error
}

// profileModal shows what the backend's user API tells about a sender. Any
// key or click closes it.
type profileModal struct {
	st      *styles
	userID  string
	profile backend.Profile
	loading bool
	err     string
}

const (
	profileModalMinWidth = 40
	profileModalMaxWidth = 64
)

func newProfileModal(st *styles, msg backend.Message) *profileModal {
	return &profileModal{st: st, userID: msg.SenderID, loading: true,
		profile: backend.Profile{Name: backend.GenericMessageSender(msg)}}
}

func loadProfile(ctx context.Context, p backend.Profiler, userID string) tea.Cmd {
	return func() tea.Msg {
		profile, err := p.Profile(ctx, userID)
		return profileLoaded{userID, profile, err}
	}
}

func (m *profileModal) loaded(msg profileLoaded) {
	if msg.userID != m.userID {
		return
	}
	m.loading = false
	if msg.err != nil {
		m.err = textfmt.CleanError(msg.err)
		return
	}
	m.profile = msg.profile
}

func (m *profileModal) lines() []string {
	inner := profileModalMaxWidth - 4
	body := []string{m.st.brand.Render(textfmt.Plain(m.profile.Name)), ""}
	switch {
	case m.loading:
		return append(body, m.st.subtle.Render("loading…"))
	case m.err != "":
		return append(body, strings.Split(ansi.Wordwrap(m.err, inner, ""), "\n")...)
	case len(m.profile.Fields) == 0:
		return append(body, m.st.subtle.Render("Nothing more is shared."))
	}
	labelWidth := 0
	for _, f := range m.profile.Fields {
		labelWidth = max(labelWidth, textfmt.DisplayWidth(f.Label))
	}
	valueWidth := max(10, inner-labelWidth-2)
	indent := strings.Repeat(" ", labelWidth+2)
	for _, f := range m.profile.Fields {
		label := f.Label + strings.Repeat(" ", labelWidth+2-textfmt.DisplayWidth(f.Label))
		value := ansi.Wrap(textfmt.Plain(f.Value), valueWidth, " ")
		for i, line := range strings.Split(value, "\n") {
			if i == 0 {
				body = append(body, m.st.subtle.Render(label)+line)
			} else {
				body = append(body, indent+line)
			}
		}
	}
	return body
}

func (m *profileModal) size() (int, int) {
	lines := m.lines()
	w := 0
	for _, l := range lines {
		w = max(w, textfmt.DisplayWidth(l))
	}
	return min(profileModalMaxWidth, max(profileModalMinWidth, w+4)), len(lines) + 4
}

func (m *profileModal) View() string {
	w, h := m.size()
	body := append(m.lines(), m.st.modalKeys(profileModalHelp, "", w-4)...)
	return m.st.modalBox("profile", body, w, h)
}

var profileModalHelp = []helpKey{{"any key", "close"}}
