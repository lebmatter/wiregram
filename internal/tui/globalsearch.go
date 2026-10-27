package tui

import (
	"context"

	"charm.land/bubbletea/v2"

	"wiregram/internal/backend"
)

// globalSearchFetch is how many messages a search of every chat asks for.
const globalSearchFetch = 60

// newGlobalSearch is the Unread view's cards filled with the messages of
// every chat matching query, one card per chat. Only the search modal opens
// it, with the query typed there.
func newGlobalSearch(ctx context.Context, st *styles, b backend.Backend, query string) (unreadModel, tea.Cmd) {
	m := unreadModel{st: st, ctx: ctx, b: b, query: query}
	return m.load()
}

func searchCards(ctx context.Context, b backend.Backend, query string) ([]unreadCard, error) {
	s, ok := b.(backend.GlobalSearcher)
	if !ok {
		return nil, nil
	}
	found, err := s.SearchAll(ctx, query, globalSearchFetch)
	return hitCards(found, true), err
}
