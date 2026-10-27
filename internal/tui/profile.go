package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// profileOpen asks the root to show the profile of msg's sender, or of the
// person an @name in its text mentions, fetched from b, the backend the chat
// runs on.
type profileOpen struct {
	b       backend.Backend
	ctx     context.Context
	msg     backend.Message
	mention string // the @name clicked, or "" for the sender
}

type profileLoaded struct {
	key     string // what the modal was opened for, see newProfileModal
	userID  string
	profile backend.Profile
	err     error
}

// profileChatOpened answers go to chat with the private chat to open.
type profileChatOpened struct {
	entry backend.ChatEntry
	err   error
}

// profileModal shows what the backend's user API tells about a person, with
// a go to chat row that opens their private chat. Enter or a click on that
// row goes there; any other key or click closes it.
type profileModal struct {
	st      *styles
	ctx     context.Context
	p       backend.Profiler
	key     string
	userID  string
	profile backend.Profile
	loading bool
	err     string
	noUser  bool // the @name clicked names no one
	chat    bool // go to chat is offered: the chat runs on the account's own connection
}

const (
	profileModalMinWidth = 40
	profileModalMaxWidth = 64
)

func newProfileModal(st *styles, open profileOpen, p backend.Profiler, chat bool) *profileModal {
	name := open.mention
	if name == "" {
		name = backend.GenericMessageSender(open.msg)
	}
	return &profileModal{st: st, ctx: open.ctx, p: p, key: open.msg.ID + " " + open.mention,
		loading: true, chat: chat, profile: backend.Profile{Name: name}}
}

func loadProfile(open profileOpen, p backend.Profiler) tea.Cmd {
	key := open.msg.ID + " " + open.mention
	return func() tea.Msg {
		userID := open.msg.SenderID
		if open.mention != "" {
			var err error
			if userID, err = p.MentionedUser(open.ctx, open.msg, open.mention); err != nil {
				return profileLoaded{key: key, err: err}
			}
		}
		profile, err := p.Profile(open.ctx, userID)
		return profileLoaded{key, userID, profile, err}
	}
}

func (m *profileModal) loaded(msg profileLoaded) {
	if msg.key != m.key {
		return
	}
	m.loading = false
	if errors.Is(msg.err, backend.ErrNoUser) {
		m.noUser = true
		return
	}
	if msg.err != nil {
		m.err = textfmt.CleanError(msg.err)
		return
	}
	m.userID, m.profile = msg.userID, msg.profile
}

// canChat reports whether go to chat is shown.
func (m *profileModal) canChat() bool {
	return m.chat && m.userID != ""
}

// chatRow is the index in lines of the go to chat row.
func (m *profileModal) chatRow() int {
	return len(m.lines()) - 1
}

// openChat finds the private chat with the person.
func (m *profileModal) openChat() tea.Cmd {
	ctx, p, userID := m.ctx, m.p, m.userID
	return func() tea.Msg {
		entry, err := p.DirectChat(ctx, userID)
		return profileChatOpened{entry, err}
	}
}

func (m *profileModal) lines() []string {
	inner := profileModalMaxWidth - 4
	body := []string{m.st.brand.Render(textfmt.Plain(m.profile.Name)), ""}
	switch {
	case m.loading:
		return append(body, m.st.subtle.Render("loading…"))
	case m.noUser:
		return append(body, m.st.subtle.Render("No one goes by "+textfmt.Plain(m.profile.Name)+"."))
	case m.err != "":
		return append(body, strings.Split(ansi.Wordwrap(m.err, inner, ""), "\n")...)
	case len(m.profile.Fields) == 0:
		body = append(body, m.st.subtle.Render("Nothing more is shared."))
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
	if m.canChat() {
		body = append(body, "", m.st.modalRow(m.st.accent.Render("›"),
			m.st.accent.Render("go to chat"), false, inner))
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
	body := append(m.lines(), m.st.modalKeys(m.helpKeys(), "", w-4)...)
	return m.st.modalBox("profile", body, w, h)
}

func (m *profileModal) helpKeys() []helpKey {
	if m.canChat() {
		return []helpKey{{"enter", "go to chat"}, {"any key", "close"}}
	}
	return []helpKey{{"any key", "close"}}
}
