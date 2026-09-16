package main

import (
	"context"
	"fmt"
	"io"

	"github.com/gotd/td/tg"
)

type chat struct {
	key   peerKey
	peer  tg.InputPeerClass
	title string
}

func userName(u *tg.User) string {
	if u == nil {
		return "Deleted Account"
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	if u.Username != "" {
		return u.Username
	}
	return "Deleted Account"
}

func (c *client) dialogs(ctx context.Context) ([]chat, error) {
	result, err := c.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20,
	})
	if err != nil {
		return nil, fmt.Errorf("load chats: %w", err)
	}
	var dialogs []tg.DialogClass
	var users []tg.UserClass
	var groups []tg.ChatClass
	switch d := result.(type) {
	case *tg.MessagesDialogs:
		dialogs, users, groups = d.Dialogs, d.Users, d.Chats
	case *tg.MessagesDialogsSlice:
		dialogs, users, groups = d.Dialogs, d.Users, d.Chats
	default:
		return nil, fmt.Errorf("Telegram returned an unexpected chat list")
	}
	c.remember(users, groups)
	peers := make(map[peerKey]chat)
	for _, value := range users {
		if u, ok := value.(*tg.User); ok {
			key := peerKey{'u', u.ID}
			entry := chat{key: key, peer: &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}, title: userName(u)}
			if u.Self {
				entry.title, entry.peer = "Saved Messages", &tg.InputPeerSelf{}
			}
			peers[key] = entry
		}
	}
	for _, value := range groups {
		switch g := value.(type) {
		case *tg.Chat:
			key := peerKey{'g', g.ID}
			peers[key] = chat{key, &tg.InputPeerChat{ChatID: g.ID}, g.Title}
		case *tg.Channel:
			key, title := peerKey{'c', g.ID}, g.Title
			if g.Broadcast {
				title = "Channel: " + title
			}
			peers[key] = chat{key, &tg.InputPeerChannel{ChannelID: g.ID, AccessHash: g.AccessHash}, title}
		}
	}
	entries := make([]chat, 0, 20)
	for _, value := range dialogs {
		if d, ok := value.(*tg.Dialog); ok {
			c.rememberRead(keyOf(d.Peer), d.ReadInboxMaxID, d.ReadOutboxMaxID)
			if entry, ok := peers[keyOf(d.Peer)]; ok {
				entries = append(entries, entry)
			}
		}
		if len(entries) == 20 {
			break
		}
	}
	return entries, nil
}

func renderChats(out io.Writer, chats []chat, selected int) {
	fmt.Fprint(out, "\033[2J\033[H")
	for i, entry := range chats {
		marker := " "
		if i == selected {
			marker = ">"
		}
		fmt.Fprintf(out, "%s %2d  %s\n", marker, i+1, plain(entry.title))
	}
	if len(chats) == 0 {
		fmt.Fprintln(out, "No chats. Press r to refresh or q to quit.")
	}
	marker := " "
	if selected == len(chats) {
		marker = ">"
	}
	fmt.Fprintf(out, "%s     Search users…\n", marker)
}

// Input is already raw; the application owns terminal state across both modes.
func (c *client) selectChat(ctx context.Context, t *terminal, chats []chat) (int, bool, error) {
	selected := 0
	var confirm *struct {
		entry  chat
		revoke bool
	}
	renderChats(t.out(), chats, selected)
	for {
		select {
		case <-ctx.Done():
			return -1, false, ctx.Err()
		case key, ok := <-t.keys:
			if !ok {
				return -1, false, context.Canceled
			}
			if confirm != nil {
				request := confirm
				confirm = nil
				if key == "y" || key == "Y" {
					if err := c.deleteChat(ctx, request.entry, request.revoke); err != nil {
						renderChats(t.out(), chats, selected)
						t.report(err)
						continue
					}
					return -1, true, nil
				}
				renderChats(t.out(), chats, selected)
				continue
			}
			switch key {
			case "q", "\x04":
				return -1, false, nil
			case "r":
				return -1, true, nil
			case "/":
				return len(chats), false, nil
			case "k", "\x1b[A", "\x1bOA":
				if selected > 0 {
					selected--
				}
				renderChats(t.out(), chats, selected)
			case "j", "\x1b[B", "\x1bOB":
				if selected < len(chats) {
					selected++
				}
				renderChats(t.out(), chats, selected)
			case "\x1bd", "\x1bD", "\x1b\x04":
				if selected >= len(chats) {
					continue
				}
				entry := chats[selected]
				revoke := key == "\x1b\x04"
				if entry.key.kind != 'u' {
					t.report(fmt.Errorf("chat deletion is currently supported only for private chats"))
					continue
				}
				if _, self := entry.peer.(*tg.InputPeerSelf); self && revoke {
					t.report(fmt.Errorf("Saved Messages cannot be deleted for both sides"))
					continue
				}
				confirm = &struct {
					entry  chat
					revoke bool
				}{entry, revoke}
				scope := "only for you"
				if revoke {
					scope = "for both sides"
				}
				t.report(fmt.Errorf("Delete chat with %s %s? Press y to confirm; any other key cancels.", plain(entry.title), scope))
			case "\r", "\n":
				return selected, false, nil
			}
		}
	}
}

func plain(s string) string {
	result := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			result = append(result, ' ')
		case r < 32 || (r >= 127 && r <= 159):
		default:
			result = append(result, r)
		}
	}
	return string(result)
}
