package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type userChoice struct {
	entry           chat
	label           string
	match           string
	resolveUsername string
}

func userChoiceFromUser(user *tg.User, source string) (userChoice, bool) {
	if user == nil || user.Deleted {
		return userChoice{}, false
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		name = user.Username
	}
	if name == "" {
		return userChoice{}, false
	}
	label := name
	if user.Username != "" && !strings.EqualFold(name, user.Username) {
		label += "  @" + user.Username
	}
	if source != "" {
		label += "  · " + source
	}
	entry := chat{
		key:   peerKey{'u', user.ID},
		peer:  &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash},
		title: UserName(user),
	}
	if user.Self {
		entry.peer, entry.title = &tg.InputPeerSelf{}, "Saved Messages"
	}
	match := strings.ToLower(strings.Join([]string{user.FirstName, user.LastName, user.Username}, " "))
	if user.Self {
		match += " saved messages"
	}
	choice := userChoice{entry: entry, label: label, match: match}
	if source == "global" {
		choice.resolveUsername = user.Username
	}
	return choice, true
}

func choicesFromUsers(users []*tg.User, source string) []userChoice {
	choices := make([]userChoice, 0, len(users))
	seen := make(map[peerKey]bool)
	for _, user := range users {
		choice, ok := userChoiceFromUser(user, source)
		if !ok || seen[choice.entry.key] {
			continue
		}
		seen[choice.entry.key] = true
		choices = append(choices, choice)
	}
	return choices
}

func mergeChoices(groups ...[]userChoice) []userChoice {
	var merged []userChoice
	seen := make(map[peerKey]bool)
	for _, choices := range groups {
		for _, choice := range choices {
			if seen[choice.entry.key] {
				continue
			}
			seen[choice.entry.key] = true
			merged = append(merged, choice)
		}
	}
	return merged
}

func (c *Client) cachedSearchUsers() (contacts, frequent []*tg.User, contactsLoaded, frequentLoaded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*tg.User(nil), c.contactUsers...),
		append([]*tg.User(nil), c.frequentUsers...),
		c.contactsLoaded,
		c.frequentLoaded
}

func (c *Client) loadContacts(ctx context.Context) ([]*tg.User, error) {
	contacts, _, loaded, _ := c.cachedSearchUsers()
	if loaded {
		return contacts, nil
	}
	result, err := c.Api.ContactsGetContacts(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("load contacts: %w", err)
	}
	var users []*tg.User
	if list, ok := result.(*tg.ContactsContacts); ok {
		c.Remember(list.Users, nil)
		for _, value := range list.Users {
			if user, ok := value.(*tg.User); ok {
				users = append(users, user)
			}
		}
	}
	c.mu.Lock()
	c.contactUsers = users
	c.contactsLoaded = true
	c.mu.Unlock()
	return users, nil
}

func (c *Client) loadFrequentUsers(ctx context.Context) ([]*tg.User, error) {
	_, frequent, _, loaded := c.cachedSearchUsers()
	if loaded {
		return frequent, nil
	}
	result, err := c.Api.ContactsGetTopPeers(ctx, &tg.ContactsGetTopPeersRequest{
		Correspondents: true,
		Limit:          10,
	})
	if err != nil {
		return nil, fmt.Errorf("load frequent users: %w", err)
	}
	var users []*tg.User
	if top, ok := result.(*tg.ContactsTopPeers); ok {
		c.Remember(top.Users, top.Chats)
		byID := make(map[int64]*tg.User)
		for _, value := range top.Users {
			if user, ok := value.(*tg.User); ok {
				byID[user.ID] = user
			}
		}
		for _, category := range top.Categories {
			if _, ok := category.Category.(*tg.TopPeerCategoryCorrespondents); !ok {
				continue
			}
			for _, peer := range category.Peers {
				if userPeer, ok := peer.Peer.(*tg.PeerUser); ok {
					if user := byID[userPeer.UserID]; user != nil {
						users = append(users, user)
					}
				}
			}
		}
	}
	c.mu.Lock()
	c.frequentUsers = users
	c.frequentLoaded = true
	c.mu.Unlock()
	return users, nil
}

func (c *Client) recentUserChoices(chats []chat, source string) []userChoice {
	c.mu.Lock()
	defer c.mu.Unlock()
	var choices []userChoice
	for _, entry := range chats {
		if entry.key.kind != 'u' {
			continue
		}
		if choice, ok := userChoiceFromUser(c.users[entry.key.id], source); ok {
			choices = append(choices, choice)
		}
	}
	return choices
}

func (c *Client) remoteUserChoices(ctx context.Context, query string) ([]userChoice, error) {
	result, err := c.Api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: query, Limit: 20})
	if err != nil {
		if tgerr.Is(err, "QUERY_TOO_SHORT") {
			return nil, nil
		}
		return nil, fmt.Errorf("search users: %w", err)
	}
	c.Remember(result.Users, result.Chats)
	users := make(map[int64]*tg.User)
	for _, value := range result.Users {
		if user, ok := value.(*tg.User); ok {
			users[user.ID] = user
		}
	}
	peers := append(append([]tg.PeerClass(nil), result.MyResults...), result.Results...)
	choices := make([]userChoice, 0, len(peers))
	for _, peer := range peers {
		if userPeer, ok := peer.(*tg.PeerUser); ok {
			if choice, ok := userChoiceFromUser(users[userPeer.UserID], "global"); ok {
				choices = append(choices, choice)
			}
		}
	}
	return mergeChoices(choices), nil
}

func (c *Client) resolveUserChoice(ctx context.Context, choice userChoice) (chat, error) {
	if choice.resolveUsername == "" {
		return choice.entry, nil
	}
	result, err := c.Api.ContactsResolveUsername(ctx, choice.resolveUsername)
	if err != nil {
		return chat{}, fmt.Errorf("open @%s: %w", choice.resolveUsername, err)
	}
	c.Remember(result.Users, result.Chats)
	peer, ok := result.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != choice.entry.key.id {
		return chat{}, fmt.Errorf("@%s now points to a different user; search again", choice.resolveUsername)
	}
	for _, value := range result.Users {
		if user, ok := value.(*tg.User); ok && user.ID == peer.UserID {
			resolved, ok := userChoiceFromUser(user, "")
			if !ok {
				break
			}
			return resolved.entry, nil
		}
	}
	return chat{}, fmt.Errorf("Telegram did not return complete details for @%s", choice.resolveUsername)
}
