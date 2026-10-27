package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

// chatListModel is the sidebar tree: every account as a header, with the
// active account expanded to a line of view chips (Unread, Mentions, You,
// archive and search; on email, Compose and Labels), then its chats in
// folding sections (on email, its folders), then its bots, and the others
// collapsed above it. Home and Settings are pinned under the tree.
// Selection indexes run over the stops in display order: every chip, section
// header and row that Enter acts on.
type chatListModel struct {
	st            *styles
	ctx           context.Context
	mgr           *account.AccountManager
	active        *account.Account
	b             backend.Backend
	entries       []backend.ChatEntry
	selected      int
	archived      bool
	archiveUnread int
	caps          backend.Capabilities
	mentions      bool // the backend can list mentions
	self          bool // the backend has a chat with oneself
	labeler       bool // the backend has labels to show in the sidebar
	mailbox       bool // the chats are email folders
	// loading is set while the backend's first sync has brought no chats
	// yet; the tree shows spin, the root's spinner frame, in their place.
	loading bool
	spin    string
	// folded holds the keys of the folded sections, for this session only.
	folded        map[string]bool
	confirm       *chatListConfirm
	offset        int
	width, height int
}

// chatListConfirm is a pending question: delete entry.
type chatListConfirm struct {
	entry  backend.ChatEntry
	revoke bool
	modal  *confirmModal
}

type chatListChoice struct{ index int }
type chatListSwitch struct{ to *account.Account }
type chatListRefresh struct{}
type chatListAccounts struct{}
type chatListSettings struct{}
type chatListSearch struct{}
type chatListUnread struct{}
type chatListMentions struct{}
type chatListSelf struct{}
type chatListArchiveToggle struct{}
type chatListAgent struct{ id int64 } // 0 creates a bot
type chatListInbox struct{ bot inboxBot }
type chatListDeleteDone struct{ err error }
type chatListLabels struct{}
type chatListCompose struct{}

func newChatListModel(ctx context.Context, st *styles, mgr *account.AccountManager, active *account.Account, b backend.Backend, entries []backend.ChatEntry, archived bool, archiveUnread int) chatListModel {
	w, h := termSize()
	m := chatListModel{
		st:            st,
		ctx:           ctx,
		mgr:           mgr,
		active:        active,
		b:             b,
		entries:       entries,
		archived:      archived,
		archiveUnread: archiveUnread,
		caps:          b.Capabilities(),
		folded:        make(map[string]bool),
		width:         w,
		height:        h,
	}
	_, m.mentions = b.(backend.MentionFetcher)
	_, m.self = b.(backend.SelfChatter)
	_, m.labeler = b.(backend.Labeler)
	_, m.mailbox = b.(backend.Mailbox)
	if s, ok := b.(backend.Syncer); ok {
		m.loading = s.Syncing() && len(entries) == 0 && !archived
	}
	m.selected = m.firstChat()
	return m
}

// listAction is a chip or bot row and what Enter on it sends.
type listAction struct {
	entry backend.ChatEntry
	glyph string // styled, in the column where chats show # or @
	right string // styled, flush right
	msg   tea.Msg
}

// top is the view chips above the chats.
func (m chatListModel) top() []listAction {
	if m.loading {
		return nil // nothing to act on until the chats arrive
	}
	if m.mailbox {
		acts := []listAction{{entry: backend.ChatEntry{Title: "compose"}, msg: chatListCompose{}}}
		if m.labeler {
			acts = append(acts, listAction{entry: backend.ChatEntry{Title: "labels"},
				msg: chatListLabels{}})
		}
		return acts
	}
	unread := backend.ChatEntry{Title: "unread"}
	for _, e := range m.entries {
		unread.Unread += e.Unread
	}
	acts := []listAction{{entry: unread, msg: chatListUnread{}}}
	if m.mentions {
		acts = append(acts, listAction{entry: backend.ChatEntry{Title: "@ mentions"},
			msg: chatListMentions{}})
	}
	if m.self {
		acts = append(acts, listAction{entry: backend.ChatEntry{Title: "you"}, msg: chatListSelf{}})
	}
	if m.caps.Archive {
		archive := backend.ChatEntry{Title: "archived", Unread: m.archiveUnread}
		if m.archived {
			archive = backend.ChatEntry{Title: "‹ chats"}
		}
		acts = append(acts, listAction{entry: archive, msg: chatListArchiveToggle{}})
	}
	return append(acts, listAction{entry: backend.ChatEntry{Title: "/ search"},
		msg: chatListSearch{}})
}

// bottom is the rows of the bots section.
func (m chatListModel) bottom() []listAction {
	if m.loading || !agentsAllowed(m.mgr, m.active) {
		return nil
	}
	key := m.mgr.AccountKey(m.active)
	var acts []listAction
	for _, rec := range m.mgr.Agents.AgentsFor(m.active) {
		chats := "all DMs"
		if n, all := m.mgr.Agents.BotChats(key, rec.ID); !all {
			chats = fmt.Sprintf("%d chats", n)
			if n == 1 {
				chats = "1 chat"
			}
		}
		acts = append(acts, listAction{
			entry: backend.ChatEntry{Title: rec.Name + " @" + rec.Handle},
			glyph: m.st.agentMark(rec.Enabled), right: m.st.dim.Render(chats),
			msg: chatListAgent{id: rec.ID},
		})
		if reg, ok := m.mgr.Agents.Registration(rec.ID); ok {
			// The registered username, which on Telegram ends in bot.
			acts = append(acts, listAction{
				entry: backend.ChatEntry{Title: "inbox", Unread: m.mgr.Agents.InboxWaiting(rec.ID)},
				glyph: m.st.dim.Render("↳"),
				msg:   chatListInbox{inboxBot{id: rec.ID, name: rec.Name, handle: reg.Username}},
			})
		}
	}
	return append(acts, listAction{entry: backend.ChatEntry{Title: "new bot…"},
		glyph: m.st.dim.Render("+"), msg: chatListAgent{}})
}

// agentsAllowed reports whether the account can have bots (Chat Agents): they
// answer DMs, and email publishes none.
func agentsAllowed(mgr *account.AccountManager, a *account.Account) bool {
	return mgr != nil && mgr.Agents != nil && a != nil && a.BackendType != "email"
}

// others is the number of each account other than the active one, all
// listed collapsed above it, and the active account's number.
func (m chatListModel) others() (rest []int, active int) {
	if m.mgr == nil {
		return nil, 0
	}
	for i, a := range m.mgr.Accounts {
		if a == m.active {
			active = i
		} else {
			rest = append(rest, i)
		}
	}
	return rest, active
}

// listStop is a place the selection can rest and what Enter there does.
type listStop struct {
	msg     tea.Msg
	chat    bool // Enter opens entries[entry]
	entry   int
	header  bool   // Enter folds or unfolds section
	section string // the section's key, for a header or a chat
	x, w    int    // the columns of a chip; w 0 is the whole row
}

// listRow is one line of the tree.
type listRow struct {
	stops []listStop
	// draw renders the row; sel is the selected stop's place in stops, or -1.
	draw func(sel int, focused bool) string
}

// stopRef locates a stop: its row in the tree, or under it when foot is set.
type stopRef struct {
	listStop
	row, part int
	foot      bool
}

// listSection is a group of chats under a ruled header that folds.
type listSection struct {
	key, name string // an empty name draws no header and never folds
	unread    int
	rows      []listRow
}

// layout builds the tree and the footer pinned under it for a width.
func (m chatListModel) layout(width int) (tree, foot []listRow) {
	rest, n := m.others()
	for _, i := range rest {
		tree = append(tree, m.accountItem(m.mgr.Accounts[i], i, false, width))
	}
	if m.active != nil {
		tree = append(tree, m.accountItem(m.active, n, true, width))
		tree = append(tree, m.chipRows(width)...)
		switch {
		case m.loading:
			tree = append(tree, static("   "+m.spin+m.st.subtle.Render(" Loading chats…")))
		case len(m.entries) == 0:
			tree = append(tree, static(m.st.subtle.Render("   No chats here")))
		}
		for _, sec := range m.sections(width) {
			if sec.name != "" {
				tree = append(tree, m.headerItem(sec, width))
				if m.folded[sec.key] {
					continue
				}
			}
			tree = append(tree, sec.rows...)
		}
	}
	home := listAction{entry: backend.ChatEntry{Title: "home"}, glyph: "⌂",
		msg: chatListAccounts{}}
	settings := listAction{entry: backend.ChatEntry{Title: "settings…"}, glyph: "⚙",
		msg: chatListSettings{}}
	for _, a := range []listAction{home, settings} {
		foot = append(foot, listRow{stops: []listStop{{msg: a.msg}},
			draw: func(sel int, focused bool) string {
				return m.draw(rowParts{prefix: m.st.subtle.Render(a.glyph) + " ",
					name: a.entry.Title, style: m.st.subtle}, sel == 0, focused, width)
			}})
	}
	return tree, foot
}

func static(line string) listRow {
	return listRow{draw: func(int, bool) string { return line }}
}

// sections groups the chats, then the bots. Within a section the chats with
// unread messages come first, each half keeping the backend's order.
func (m chatListModel) sections(width int) []listSection {
	if m.mailbox {
		sec := listSection{}
		for i := range m.entries {
			sec.rows = append(sec.rows, m.chatItem(i, "", false, width))
		}
		return []listSection{sec}
	}
	agent := func(backend.ChatEntry) bool { return false }
	if m.mgr != nil && m.mgr.Agents != nil && m.active != nil {
		key := m.mgr.AccountKey(m.active)
		pending := m.mgr.Agents.PendingChats(key, 0)
		agent = func(e backend.ChatEntry) bool { return pending[e.ID] || m.mgr.Agents.Answers(key, e) }
	}
	byKey := make(map[string]*listSection)
	var keys []string
	for i, e := range m.entries {
		key, name := entrySection(e)
		sec, ok := byKey[key]
		if !ok {
			sec = &listSection{key: key, name: name}
			byKey[key] = sec
			keys = append(keys, key)
		}
		sec.unread += e.Unread
		sec.rows = append(sec.rows, m.chatItem(i, key, agent(e), width))
	}
	// Teams in name order, then the fixed sections.
	rank := map[string]int{"groups": 1, "rooms": 2, "channels": 3, "direct": 4}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, rj := rank[keys[i]], rank[keys[j]]
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(byKey[keys[i]].name) < strings.ToLower(byKey[keys[j]].name)
	})
	var out []listSection
	for _, key := range keys {
		sec := *byKey[key]
		var unread, read []listRow
		for _, r := range sec.rows {
			if m.entries[r.stops[0].entry].Unread > 0 {
				unread = append(unread, r)
			} else {
				read = append(read, r)
			}
		}
		sec.rows = append(unread, read...)
		out = append(out, sec)
	}
	if bots := m.bottom(); len(bots) > 0 {
		sec := listSection{key: "bots", name: "bots"}
		for _, a := range bots {
			sec.unread += a.entry.Unread
			sec.rows = append(sec.rows, m.actionItem(a, "bots", width))
		}
		out = append(out, sec)
	}
	return out
}

// entrySection is the key and name of the section a chat sits in: its team,
// else the kind of chat it is on its network.
func entrySection(e backend.ChatEntry) (key, name string) {
	switch {
	case e.IsDM || e.IsSelf:
		return "direct", "direct"
	case e.Section != "":
		return "team:" + e.Section, strings.ToLower(e.Section)
	case e.IsChannel || e.ID.Backend == "mattermost":
		return "channels", "channels"
	case e.ID.Backend == "matrix":
		return "rooms", "rooms"
	}
	return "groups", "groups"
}

// sidebarTitle is the chat's title without what its section already says.
func sidebarTitle(e backend.ChatEntry) string {
	title := textfmt.Plain(e.Title)
	if e.Section != "" {
		title = strings.TrimPrefix(title, "["+e.Section+"] ")
	}
	if e.IsChannel {
		title = strings.TrimPrefix(title, "Channel: ")
	}
	return title
}

// rowParts is a row's content: prefix, then the name and a dim tail
// truncated together to fit, then right flush with the edge.
type rowParts struct {
	prefix     string // styled, after the selection bar
	name, tail string
	style      lipgloss.Style
	right      string
	rule       bool // fill the gap before right with a dim rule
}

// draw renders a row: a bar in the first column and the selection tint when
// selected, the name bold.
func (m chatListModel) draw(p rowParts, selected, focused bool, width int) string {
	bar := " "
	if selected {
		bar = m.st.accent.Render("▎")
		p.style = p.style.Bold(true)
	}
	prefix := bar + p.prefix
	used := textfmt.DisplayWidth(prefix) + textfmt.DisplayWidth(p.right)
	room := width - used
	if p.right != "" {
		room--
	}
	text := truncate(p.name+p.tail, max(1, room))
	name := p.style.Render(text)
	if len(text) > len(p.name) && strings.HasPrefix(text, p.name) {
		name = p.style.Render(p.name) + m.st.dim.Render(text[len(p.name):])
	}
	gap := max(0, width-used-textfmt.DisplayWidth(text))
	fill := strings.Repeat(" ", gap)
	if p.rule && gap > 2 {
		fill = " " + m.st.dim.Render(strings.Repeat("─", gap-2)) + " "
	}
	row := prefix + name + fill + p.right
	if selected && focused {
		row = shadeLine(row, m.st.selBgSGR, width)
	}
	return row
}

// accountItem is an account header: ▾ expanded, ▸ collapsed, then its status,
// service tag, number and label, its unseen count while collapsed and ✦ when
// it has bots.
func (m chatListModel) accountItem(a *account.Account, n int, expanded bool, width int) listRow {
	row := listRow{}
	if !expanded {
		row.stops = []listStop{{msg: chatListSwitch{a}}}
	}
	row.draw = func(sel int, focused bool) string {
		state := a.Snapshot()
		caret, tag, style := m.st.dim.Render("▸"), m.st.tag, m.st.subtle
		if expanded {
			caret, tag, style = m.st.accent.Render("▾"), m.st.badge, styleTitle
		}
		right := ""
		if !expanded && state.Unseen > 0 {
			right = m.st.tag.Render(fmt.Sprintf(" %d ", state.Unseen))
		}
		if m.mgr.Agents != nil {
			if bots := m.mgr.Agents.AgentsFor(a); len(bots) > 0 {
				right = strings.TrimPrefix(right+" "+m.st.agentMark(anyEnabled(bots)), " ")
			}
		}
		prefix := caret + " " + m.st.accountGlyph(state) + " " +
			tag.Render(" "+backendTag(a.BackendType)+" ") + " " +
			m.st.dim.Render(fmt.Sprint(n+1)) + " "
		return m.draw(rowParts{prefix: prefix, name: accountLabel(a), style: style,
			right: right}, sel == 0, focused, width)
	}
	return row
}

// chipRows lays the view chips out left to right under the account, wrapping
// when a chip would cross the edge.
func (m chatListModel) chipRows(width int) []listRow {
	const indent = 3
	var rows []listRow
	var chips [][]listAction
	x := indent
	for _, a := range m.top() {
		w := chipWidth(a)
		if len(rows) == 0 || (x > indent && x+w > width) {
			rows = append(rows, listRow{})
			chips = append(chips, nil)
			x = indent
		}
		r := len(rows) - 1
		rows[r].stops = append(rows[r].stops, listStop{msg: a.msg, x: x, w: w})
		chips[r] = append(chips[r], a)
		x += w + 1
	}
	for r := range rows {
		acts := chips[r]
		rows[r].draw = func(sel int, focused bool) string {
			line := "  "
			if sel >= 0 {
				line = m.st.accent.Render("▎") + " "
			}
			line += " "
			for i, a := range acts {
				if i > 0 {
					line += " "
				}
				line += m.chip(a, i == sel, focused)
			}
			return line
		}
	}
	// A blank row between wrapped chip rows keeps their fills apart.
	var spaced []listRow
	for i, r := range rows {
		if i > 0 {
			spaced = append(spaced, static(""))
		}
		spaced = append(spaced, r)
	}
	return spaced
}

func chipWidth(a listAction) int {
	w := textfmt.DisplayWidth(a.entry.Title) + 2
	if a.entry.Unread > 0 {
		w += len(fmt.Sprint(a.entry.Unread)) + 2
	}
	return w
}

// chip is a view chip on the selection fill, or the accent fill when it is
// selected, with its unread count on the accent.
func (m chatListModel) chip(a listAction, selected, focused bool) string {
	body := " " + a.entry.Title + " "
	switch {
	case selected && focused:
		body = m.st.badge.Render(body)
	case selected || a.entry.Unread > 0:
		body = shadeLine(styleTitle.Render(body), m.st.selBgSGR, 0)
	default:
		body = shadeLine(m.st.subtle.Render(body), m.st.selBgSGR, 0)
	}
	if a.entry.Unread > 0 {
		body += m.st.badge.Render(fmt.Sprintf(" %d ", a.entry.Unread))
	}
	return body
}

// headerItem is a section's ruled header, with its unread count while folded.
func (m chatListModel) headerItem(sec listSection, width int) listRow {
	folded := m.folded[sec.key]
	return listRow{stops: []listStop{{header: true, section: sec.key}},
		draw: func(sel int, focused bool) string {
			caret, right := "▾", ""
			if folded {
				caret = "▸"
				if sec.unread > 0 {
					right = m.st.tag.Render(fmt.Sprintf(" %d ", sec.unread))
				}
			}
			return m.draw(rowParts{prefix: " " + m.st.dim.Render(caret) + " ", name: sec.name,
				style: m.st.dim, right: right, rule: true}, sel == 0, focused, width)
		}}
}

// chatItem is a chat row: # for groups and channels, @ for people, ✦ when a
// bot answers it or has a suggestion waiting; dim when it has nothing unread.
func (m chatListModel) chatItem(i int, section string, agent bool, width int) listRow {
	e := m.entries[i]
	glyph := ""
	if !m.mailbox {
		glyph = m.st.dim.Render("#") + " "
		if e.IsDM || e.IsSelf {
			glyph = m.st.dim.Render("@") + " "
		}
		if agent {
			glyph = m.st.agent.Render(agentIcon) + " "
		}
	}
	return listRow{stops: []listStop{{chat: true, entry: i, section: section}},
		draw: func(sel int, focused bool) string {
			style, right := m.st.subtle, ""
			if e.Unread > 0 {
				style, right = lipgloss.NewStyle(), m.st.badge.Render(fmt.Sprintf(" %d ", e.Unread))
			}
			return m.draw(rowParts{prefix: "  " + glyph, name: sidebarTitle(e), style: style,
				right: right}, sel == 0, focused, width)
		}}
}

// actionItem is a bot section row.
func (m chatListModel) actionItem(a listAction, section string, width int) listRow {
	return listRow{stops: []listStop{{msg: a.msg, section: section}},
		draw: func(sel int, focused bool) string {
			name, tail := a.entry.Title, ""
			if at := strings.Index(name, " @"); at > 0 {
				name, tail = name[:at], name[at:]
			}
			right := a.right
			if a.entry.Unread > 0 {
				right = m.st.badge.Render(fmt.Sprintf(" %d ", a.entry.Unread))
			}
			return m.draw(rowParts{prefix: "  " + a.glyph + " ", name: name, tail: tail,
				style: m.st.subtle, right: right}, sel == 0, focused, width)
		}}
}

func listStops(tree, foot []listRow) []stopRef {
	var refs []stopRef
	for i, r := range tree {
		for j, s := range r.stops {
			refs = append(refs, stopRef{listStop: s, row: i, part: j})
		}
	}
	for i, r := range foot {
		for j, s := range r.stops {
			refs = append(refs, stopRef{listStop: s, row: i, part: j, foot: true})
		}
	}
	return refs
}

func (m chatListModel) refs() []stopRef {
	return listStops(m.layout(m.width))
}

// treeHeight is the rows the tree scrolls in, above the footer and its rule.
func treeHeight(height, foot int) int {
	return max(1, height-foot-1)
}

// firstChat is the selection index of the first chat row, else of the first
// stop under the active account.
func (m chatListModel) firstChat() int {
	for i, r := range m.refs() {
		if r.chat {
			return i
		}
	}
	rest, _ := m.others()
	return len(rest)
}

// selectEntry selects entries[i], unfolding its section.
func (m chatListModel) selectEntry(i int) chatListModel {
	if i < 0 || i >= len(m.entries) {
		return m
	}
	if key, _ := entrySection(m.entries[i]); !m.mailbox {
		delete(m.folded, key)
	}
	for j, r := range m.refs() {
		if r.chat && r.entry == i {
			m.selected = j
			break
		}
	}
	return m.scrolled()
}

func (m chatListModel) maxIndex() int {
	return max(0, len(m.refs())-1)
}

// chatIndex maps the selection to an entry, if a chat is selected.
func (m chatListModel) chatIndex() (int, bool) {
	refs := m.refs()
	if m.selected < 0 || m.selected >= len(refs) || !refs[m.selected].chat {
		return 0, false
	}
	return refs[m.selected].entry, true
}

func (m chatListModel) Init() tea.Cmd { return nil }

func (m chatListModel) refreshUnread() chatListModel {
	for i := range m.entries {
		m.entries[i].Unread = m.b.CachedUnread(m.entries[i])
	}
	return m
}

// has reports whether the chat is in the list.
func (m chatListModel) has(id backend.ChatID) bool {
	for _, e := range m.entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// itemAt maps a cell of the list on screen to a selection index; headers of
// accounts, placeholders and rules are not selectable.
func (m chatListModel) itemAt(x, y int) (int, bool) {
	tree, foot := m.layout(m.width)
	th := treeHeight(m.height, len(foot))
	row, inFoot := y+m.offset, false
	if y >= th {
		row, inFoot = y-th-1, true
	}
	for i, r := range listStops(tree, foot) {
		if r.foot == inFoot && r.row == row && (r.w == 0 || x >= r.x && x < r.x+r.w) {
			return i, true
		}
	}
	return 0, false
}

// scrolled keeps the selected row inside the tree's window.
func (m chatListModel) scrolled() chatListModel {
	tree, foot := m.layout(m.width)
	refs := listStops(tree, foot)
	th := treeHeight(m.height, len(foot))
	if m.selected >= 0 && m.selected < len(refs) && !refs[m.selected].foot {
		r := refs[m.selected].row
		if r < m.offset {
			m.offset = r
		}
		if r >= m.offset+th {
			m.offset = r - th + 1
		}
	}
	m.offset = max(0, min(m.offset, len(tree)-th))
	return m
}

// toggle folds or unfolds a section, leaving the selection on its header.
func (m chatListModel) toggle(key string) chatListModel {
	if m.folded[key] {
		delete(m.folded, key)
	} else {
		m.folded[key] = true
	}
	for i, r := range m.refs() {
		if r.header && r.section == key {
			m.selected = i
			break
		}
	}
	return m.scrolled()
}

func (m chatListModel) Update(msg tea.Msg) (chatListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.confirm != nil {
			req := m.confirm
			yes, done := req.modal.key(msg.String())
			if !done {
				return m, nil
			}
			m.confirm = nil
			if yes {
				entry := req.entry
				revoke := req.revoke
				ctx := m.ctx
				b := m.b
				return m, func() tea.Msg {
					err := b.DeleteChat(ctx, entry, revoke)
					return chatListDeleteDone{err}
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return chatListAccounts{} }
		case "r":
			return m, func() tea.Msg { return chatListRefresh{} }
		case "/":
			if !m.mailbox {
				return m, func() tea.Msg { return chatListSearch{} }
			}
		case "u":
			if !m.mailbox {
				return m, func() tea.Msg { return chatListUnread{} }
			}
		case "c":
			if m.mailbox {
				return m, func() tea.Msg { return chatListCompose{} }
			}
		case "m":
			if m.mentions {
				return m, func() tea.Msg { return chatListMentions{} }
			}
		case "a":
			return m, func() tea.Msg { return chatListAccounts{} }
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "j", "down":
			if m.selected < m.maxIndex() {
				m.selected++
			}
		case "alt+d", "alt+D":
			i, ok := m.chatIndex()
			if !ok || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[i]
			if !entry.IsDM {
				return m, nil
			}
			m.confirm = m.askDelete(entry, false)
		case "ctrl+alt+d":
			i, ok := m.chatIndex()
			if !ok || !m.caps.DeleteChat {
				return m, nil
			}
			entry := m.entries[i]
			if !entry.IsDM || entry.IsSelf {
				return m, nil
			}
			m.confirm = m.askDelete(entry, true)
		case "space", " ":
			refs := m.refs()
			if m.selected < len(refs) && refs[m.selected].section != "" {
				return m.toggle(refs[m.selected].section), nil
			}
		case "enter":
			refs := m.refs()
			if m.selected >= len(refs) {
				return m, nil
			}
			r := refs[m.selected]
			switch {
			case r.header:
				return m.toggle(r.section), nil
			case r.chat:
				return m, func() tea.Msg { return chatListChoice{r.entry} }
			case r.msg != nil:
				return m, func() tea.Msg { return r.msg }
			}
		}

	case chatListDeleteDone:
		if msg.err != nil {
			return m, nil
		}
		return m, func() tea.Msg { return chatListRefresh{} }
	}
	return m.scrolled(), nil
}

func (m chatListModel) title() string {
	switch {
	case m.archived:
		return "Archived"
	case m.mailbox:
		return "Mail"
	}
	return "Chats"
}

// body renders the tree for a width×height area: its scrolled window, then
// the footer under a rule.
func (m chatListModel) body(width, height int, focused bool) []string {
	tree, foot := m.layout(width)
	refs := listStops(tree, foot)
	var cur stopRef
	if m.selected >= 0 && m.selected < len(refs) {
		cur = refs[m.selected]
	} else {
		cur.row = -1
	}
	part := func(row int, inFoot bool) int {
		if cur.row == row && cur.foot == inFoot {
			return cur.part
		}
		return -1
	}
	th := treeHeight(height, len(foot))
	var rows []string
	for r := m.offset; r < len(tree) && len(rows) < th; r++ {
		rows = append(rows, tree[r].draw(part(r, false), focused))
	}
	for len(rows) < th {
		rows = append(rows, "")
	}
	rows = append(rows, m.st.dim.Render(strings.Repeat("─", width)))
	for r := range foot {
		rows = append(rows, foot[r].draw(part(r, true), focused))
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return rows
}

func (m chatListModel) askDelete(entry backend.ChatEntry, revoke bool) *chatListConfirm {
	return &chatListConfirm{entry: entry, revoke: revoke, modal: newConfirmModal(m.st,
		"Delete chat", deleteChatQuestion(entry, revoke), "Delete", true)}
}

func (m chatListModel) helpKeys() []helpKey {
	if m.mailbox {
		return []helpKey{{"↑↓", "select"}, {"enter", "open"}, {"c", "compose"},
			{"a", "manage accounts"}, {"r", "refresh"}}
	}
	keys := []helpKey{{"↑↓", "select"}, {"enter", "open"}, {"space", "fold"},
		{"/", "search"}, {"u", "unread"}}
	if m.mentions {
		keys = append(keys, helpKey{"m", "mentions"})
	}
	keys = append(keys, helpKey{"a", "manage accounts"}, helpKey{"r", "refresh"})
	if m.caps.DeleteChat {
		keys = append(keys, helpKey{"M-d", "delete chat"})
	}
	return keys
}
