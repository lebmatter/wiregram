package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbletea/v2"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

const (
	digestChats    = 3
	digestCardH    = digestChats + 3 // border, summary, chats
	digestMinCardW = 36
)

type digestCard struct {
	chats []backend.ChatEntry // the chats with the most unread messages
	total int
	count int // chats with unread messages
	err   error
}

type digestSync struct{}

type digestLoaded struct {
	a       *account.Account
	b       backend.Backend
	entries []backend.ChatEntry
	err     error
}

// digestModel is the account chooser's unread digest: one card per online
// account, fetched once when the account comes online.
type digestModel struct {
	ctx     context.Context
	mgr     *account.AccountManager
	cards   map[*account.Account]digestCard
	fetched map[*account.Account]backend.Backend
}

func newDigest(ctx context.Context, mgr *account.AccountManager) digestModel {
	return digestModel{ctx: ctx, mgr: mgr, cards: make(map[*account.Account]digestCard),
		fetched: make(map[*account.Account]backend.Backend)}
}

// digestSkipped reports whether Settings left the account out of the digest.
func digestSkipped(mgr *account.AccountManager, a *account.Account) bool {
	v, err := mgr.Store.Get(mgr.AccountKey(a), store.DigestSkip)
	return err == nil && string(v) == "1"
}

func setDigestSkipped(mgr *account.AccountManager, a *account.Account, skip bool) error {
	v := "0"
	if skip {
		v = "1"
	}
	return mgr.Store.Put(mgr.AccountKey(a), store.DigestSkip, []byte(v))
}

// sync fetches the chats of accounts that came online and drops the cards of
// those that went offline, were removed or are skipped.
func (m digestModel) sync() (digestModel, tea.Cmd) {
	want := make(map[*account.Account]backend.Backend)
	for _, a := range m.mgr.List() {
		s := a.Snapshot()
		if s.Backend != nil && s.Status == "online" && !digestSkipped(m.mgr, a) {
			want[a] = s.Backend
		}
	}
	for a, b := range m.fetched {
		if want[a] != b {
			delete(m.fetched, a)
			delete(m.cards, a)
		}
	}
	var cmds []tea.Cmd
	for a, b := range want {
		if m.fetched[a] == b {
			continue
		}
		m.fetched[a] = b
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg {
			entries, _, err := b.Dialogs(ctx, false)
			return digestLoaded{a, b, entries, err}
		})
	}
	return m, tea.Batch(cmds...)
}

func (m digestModel) loaded(msg digestLoaded) digestModel {
	if m.fetched[msg.a] != msg.b {
		return m
	}
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) {
			m.cards[msg.a] = digestCard{err: msg.err}
		}
		return m
	}
	var c digestCard
	for _, e := range msg.entries {
		if e.Unread > 0 {
			c.chats = append(c.chats, e)
			c.total += e.Unread
		}
	}
	c.count = len(c.chats)
	slices.SortStableFunc(c.chats, func(x, y backend.ChatEntry) int {
		return cmp.Compare(y.Unread, x.Unread)
	})
	c.chats = c.chats[:min(len(c.chats), digestChats)]
	m.cards[msg.a] = c
	return m
}

// order lists the accounts with cards in account order.
func (m digestModel) order() []*account.Account {
	var out []*account.Account
	for _, a := range m.mgr.Accounts {
		if _, ok := m.cards[a]; ok {
			out = append(out, a)
		}
	}
	return out
}

func (m digestModel) columns(width int) int { return max(1, width/digestMinCardW) }

// height is the rows the digest needs to show every card at this width.
func (m digestModel) height(width int) int {
	n := len(m.order())
	cols := m.columns(width)
	return (n + cols - 1) / cols * digestCardH
}

// cardWidth gives the last column whatever the division leaves over.
func cardWidth(width, cols, col int) int {
	if col == cols-1 {
		return width - width/cols*(cols-1)
	}
	return width / cols
}

// view tiles the cards in a grid, dropping the rows that don't fit.
func (m digestModel) view(width, height int) string {
	accounts := m.order()
	cols := m.columns(width)
	var rows []string
	for start := 0; start < len(accounts) && (len(rows)+1)*digestCardH <= height; start += cols {
		row := ""
		for col := 0; col < cols && start+col < len(accounts); col++ {
			card := m.card(accounts[start+col], cardWidth(width, cols, col))
			if row == "" {
				row = card
			} else {
				row = joinColumns(row, card)
			}
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (m digestModel) card(a *account.Account, width int) string {
	c := m.cards[a]
	title := accountShort(m.mgr, m.mgr.AccountKey(a)) + " " + accountLabel(a)
	border := colorDim
	var lines []string
	switch {
	case c.err != nil:
		lines = []string{styleError.Render(textfmt.CleanError(c.err))}
	case c.total == 0:
		lines = []string{styleSubtle.Render("Nothing unread")}
	default:
		border = colorAccent
		lines = []string{fmt.Sprintf("%d unread in %d %s", c.total, c.count,
			plural(c.count, "chat", "chats"))}
		for _, e := range c.chats {
			lines = append(lines, chatListLine(e, false, false, width-4))
		}
	}
	return renderBox(title, lines, width, digestCardH, border)
}

// cardAt maps a click at x, y inside the digest to its account.
func (m digestModel) cardAt(x, y, width int) (*account.Account, bool) {
	if x < 0 || y < 0 || width < 1 {
		return nil, false
	}
	cols := m.columns(width)
	col := min(cols-1, x/(width/cols))
	i := y/digestCardH*cols + col
	accounts := m.order()
	if i >= len(accounts) {
		return nil, false
	}
	return accounts[i], true
}
