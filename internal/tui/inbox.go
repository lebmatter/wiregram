package tui

import (
	"context"
	"sort"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
)

// The bot inbox is a registered bot's own private chats: the Unread cards,
// over the bot's connection rather than the account's. A chat opened from it
// runs on that connection too, so whatever is sent there is sent as the bot.

// inboxBot names the registered bot whose inbox or chat is open.
type inboxBot struct {
	id     int64
	name   string
	handle string
}

// newInbox loads the cards of the bot's private chats over b, its connection;
// waiting marks the chats with a reply waiting for approval.
func newInbox(ctx context.Context, st *styles, b backend.Backend, bot inboxBot,
	waiting map[backend.ChatID]bool) (unreadModel, tea.Cmd) {
	m := unreadModel{st: st, ctx: ctx, b: b, bot: &bot, waiting: waiting}
	return m.load()
}

// inboxCards is a card per private chat of the bot, those with a reply
// waiting first, then the most recent.
func inboxCards(ctx context.Context, b backend.Backend,
	waiting map[backend.ChatID]bool) ([]unreadCard, error) {
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return nil, err
	}
	var cards []unreadCard
	for _, e := range entries {
		if !e.IsDM || e.IsSelf || len(cards) == unreadMaxChats {
			continue
		}
		msgs, err := b.History(ctx, e)
		cards = append(cards, unreadCard{entry: e, count: e.Unread, err: err,
			msgs: msgs[max(0, len(msgs)-unreadCardLines):], waiting: waiting[e.ID]})
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].waiting && !cards[j].waiting })
	return cards, nil
}
