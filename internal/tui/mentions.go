package tui

import (
	"context"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
)

// mentionsFetch is how many recent mentions the Mentions view asks for.
const mentionsFetch = 40

// newMentions is the Unread view's cards filled with the latest messages
// that mention the user, one card per chat outside DMs.
func newMentions(ctx context.Context, st *styles, b backend.Backend) (unreadModel, tea.Cmd) {
	m := unreadModel{st: st, ctx: ctx, b: b, mentions: true}
	return m.load()
}

// mentionCards groups mentions, newest first, into cards of each chat's
// latest few, oldest first as in Unread.
func mentionCards(ctx context.Context, b backend.Backend) ([]unreadCard, error) {
	f, ok := b.(backend.MentionFetcher)
	if !ok {
		return nil, nil
	}
	found, err := f.Mentions(ctx, mentionsFetch)
	var cards []unreadCard
	at := make(map[backend.ChatID]int)
	for _, mn := range found {
		if mn.Chat.IsDM {
			continue
		}
		i, ok := at[mn.Chat.ID]
		if !ok {
			if len(cards) == unreadMaxChats {
				continue
			}
			i = len(cards)
			at[mn.Chat.ID] = i
			cards = append(cards, unreadCard{entry: mn.Chat})
		}
		cards[i].count++
		if len(cards[i].msgs) < unreadCardFull {
			cards[i].msgs = append([]backend.Message{mn.Message}, cards[i].msgs...)
		}
	}
	return cards, err
}
