package tui

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/plugin"
	"wiregram/internal/textfmt"
)

// pluginHost gives plugins with the "messages" permission the online accounts
// and their unread chats, and those with "aitasks" the user's AI tasks.
// Plugins call it from their own goroutines, so it reads the accounts through
// List and Snapshot only.
type pluginHost struct{ mgr *account.AccountManager }

// online lists the accounts that are online and have private chats, with
// their tags; email has none.
func (h pluginHost) online() (accounts []*account.Account, tags []string) {
	for i, a := range h.mgr.List() {
		s := a.Snapshot()
		if s.Backend == nil || s.Status != "online" || a.BackendType == "email" {
			continue
		}
		accounts = append(accounts, a)
		tags = append(tags, fmt.Sprintf("%s%d", backendTag(a.BackendType), i+1))
	}
	return accounts, tags
}

func (h pluginHost) Accounts() []plugin.HostAccount {
	accounts, tags := h.online()
	out := make([]plugin.HostAccount, len(accounts))
	for i, a := range accounts {
		out[i] = plugin.HostAccount{Key: h.mgr.AccountKey(a), Tag: tags[i],
			Label: accountLabel(a), Backend: a.BackendType}
	}
	return out
}

// backend is the online account key's backend.
func (h pluginHost) backend(key string) (backend.Backend, error) {
	accounts, _ := h.online()
	for _, a := range accounts {
		if h.mgr.AccountKey(a) == key {
			return a.Snapshot().Backend, nil
		}
	}
	return nil, fmt.Errorf("account %s is not online", key)
}

// unreadChats lists b's chats with unread messages, with up to n of each
// one's newest unread messages; a chat whose messages fail to load has none.
func unreadChats(ctx context.Context, b backend.Backend, n int) ([]backend.ChatEntry,
	[][]backend.Message, error) {
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return nil, nil, fmt.Errorf("list chats: %w", err)
	}
	var unread []backend.ChatEntry
	for _, e := range entries {
		if !e.IsSelf && e.Unread > 0 {
			unread = append(unread, e)
		}
	}
	msgs := make([][]backend.Message, len(unread))
	if n == 0 {
		return unread, msgs, nil
	}
	var wg sync.WaitGroup
	for i, e := range unread {
		wg.Go(func() { msgs[i], _ = unreadMessages(ctx, b, e, n) })
	}
	wg.Wait()
	return unread, msgs, nil
}

func (h pluginHost) Unread(ctx context.Context, key string, n int) ([]plugin.UnreadChat,
	error) {
	b, err := h.backend(key)
	if err != nil {
		return nil, err
	}
	unread, msgs, err := unreadChats(ctx, b, n)
	if err != nil {
		return nil, err
	}
	out := make([]plugin.UnreadChat, len(unread))
	for i, e := range unread {
		out[i] = plugin.UnreadChat{Title: textfmt.Plain(sidebarTitle(e)), DM: e.IsDM,
			Count: e.Unread, Last: e.LastAt}
		for _, m := range msgs[i] {
			out[i].Messages = append(out[i].Messages, plugin.UnreadMessage{
				Sender: backend.GenericMessageSender(m),
				Text:   backend.GenericMessageText(m), Time: m.Timestamp})
		}
	}
	return out, nil
}

// catchupMessages is how many of each chat's newest unread messages a
// catch-up summary reads.
const catchupMessages = 30

func (h pluginHost) AIReady() bool {
	if h.mgr.AI == nil {
		return false
	}
	_, err := h.mgr.AI.Provider(account.TaskCatchup)
	return err == nil
}

// Catchup summarises every online account's unread chats, private chats
// first, then the newest. An account whose chats fail to load is left out.
func (h pluginHost) Catchup(ctx context.Context, onText func(string)) error {
	if h.mgr.AI == nil {
		return fmt.Errorf("AI tasks are unavailable: their tables could not be opened")
	}
	accounts, tags := h.online()
	var chats []account.CatchupChat
	for i, a := range accounts {
		unread, msgs, err := unreadChats(ctx, a.Snapshot().Backend, catchupMessages)
		if err != nil {
			continue
		}
		order := make([]int, len(unread))
		for j := range order {
			order[j] = j
		}
		slices.SortStableFunc(order, func(x, y int) int {
			if unread[x].IsDM != unread[y].IsDM {
				if unread[x].IsDM {
					return -1
				}
				return 1
			}
			return unread[y].LastAt.Compare(unread[x].LastAt)
		})
		for _, j := range order {
			e := unread[j]
			chats = append(chats, account.CatchupChat{
				Name: textfmt.Plain(chatName(e)) + " (" + tags[i] + ")",
				DM:   e.IsDM, Unread: e.Unread, Messages: msgs[j]})
		}
	}
	s := h.mgr.AI.Catchup(ctx, chats)
	for c := range s.C {
		if c.Text != "" {
			onText(c.Text)
		}
		if c.Done {
			return c.Err
		}
	}
	return nil
}
