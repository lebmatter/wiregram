package tui

import (
	"context"
	"fmt"
	"sync"

	"wiregram/internal/account"
	"wiregram/internal/backend"
	"wiregram/internal/plugin"
	"wiregram/internal/textfmt"
)

// pluginHost gives plugins with the "messages" permission the online accounts
// and their unread chats. Plugins call it from their own goroutines,
// so it reads the accounts through List and Snapshot only.
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

func (h pluginHost) Unread(ctx context.Context, key string, n int) ([]plugin.UnreadChat,
	error) {
	accounts, _ := h.online()
	var b backend.Backend
	for _, a := range accounts {
		if h.mgr.AccountKey(a) == key {
			b = a.Snapshot().Backend
		}
	}
	if b == nil {
		return nil, fmt.Errorf("account %s is not online", key)
	}
	entries, _, err := b.Dialogs(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	var unread []backend.ChatEntry
	for _, e := range entries {
		if !e.IsSelf && e.Unread > 0 {
			unread = append(unread, e)
		}
	}
	out := make([]plugin.UnreadChat, len(unread))
	var wg sync.WaitGroup
	for i, e := range unread {
		out[i] = plugin.UnreadChat{Title: textfmt.Plain(sidebarTitle(e)), DM: e.IsDM,
			Count: e.Unread, Last: e.LastAt}
		if n == 0 {
			continue
		}
		wg.Go(func() {
			msgs, err := unreadMessages(ctx, b, e, n)
			if err != nil {
				return // the chat still shows its count
			}
			for _, m := range msgs {
				out[i].Messages = append(out[i].Messages, plugin.UnreadMessage{
					Sender: backend.GenericMessageSender(m),
					Text:   backend.GenericMessageText(m), Time: m.Timestamp})
			}
		})
	}
	wg.Wait()
	return out, nil
}
