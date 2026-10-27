package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

type chat struct {
	key    peerKey
	peer   tg.InputPeerClass
	title  string
	unread int
	// broadcast marks a channel only its admins post in.
	broadcast bool
}

func UserName(u *tg.User) string {
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

func (c *Client) dialogs(ctx context.Context, archived bool) ([]chat, int, error) {
	request := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20,
	}
	if archived {
		request.SetFolderID(1)
	}
	result, err := c.Api.MessagesGetDialogs(ctx, request)
	if err != nil {
		return nil, 0, fmt.Errorf("load chats: %w", err)
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
		return nil, 0, fmt.Errorf("Telegram returned an unexpected chat list")
	}
	c.Remember(users, groups)
	peers := peerChats(users, groups)
	entries := make([]chat, 0, 20)
	archiveUnread := 0
	for _, value := range dialogs {
		switch d := value.(type) {
		case *tg.Dialog:
			c.rememberRead(keyOf(d.Peer), d.ReadInboxMaxID, d.ReadOutboxMaxID)
			c.rememberUnread(keyOf(d.Peer), d.UnreadCount)
			if entry, ok := peers[keyOf(d.Peer)]; ok {
				entry.unread = d.UnreadCount
				entries = append(entries, entry)
			}
		case *tg.DialogFolder:
			if d.Folder.ID == 1 {
				archiveUnread = d.UnreadMutedMessagesCount + d.UnreadUnmutedMessagesCount
			}
		}
		if len(entries) == 20 {
			break
		}
	}
	return entries, archiveUnread, nil
}

// peerChats is the chats a response names, by peer.
func peerChats(users []tg.UserClass, groups []tg.ChatClass) map[peerKey]chat {
	peers := make(map[peerKey]chat)
	for _, value := range users {
		if u, ok := value.(*tg.User); ok {
			key := peerKey{'u', u.ID}
			entry := chat{key: key, peer: &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}, title: UserName(u)}
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
			peers[key] = chat{key: key, peer: &tg.InputPeerChat{ChatID: g.ID}, title: g.Title}
		case *tg.Channel:
			key, title := peerKey{'c', g.ID}, g.Title
			if g.Broadcast {
				title = "Channel: " + title
			}
			peers[key] = chat{key: key, peer: &tg.InputPeerChannel{ChannelID: g.ID, AccessHash: g.AccessHash},
				title: title, broadcast: g.Broadcast}
		}
	}
	return peers
}
