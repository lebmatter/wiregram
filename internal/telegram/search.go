package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"wiregram/internal/terminal"
	"wiregram/internal/textfmt"
)

type userChoice struct {
	entry           chat
	label           string
	match           string
	resolveUsername string
}

type userSearchResult struct {
	generation int
	query      string
	choices    []userChoice
	err        error
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

func filterChoices(choices []userChoice, query string) []userChoice {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var matches []userChoice
	for _, choice := range choices {
		if strings.Contains(choice.match, query) {
			matches = append(matches, choice)
		}
	}
	return matches
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

func (c *Client) searchUsers(ctx context.Context, t *terminal.Terminal, recentChats []chat) (chat, bool, error) {
	fmt.Fprintln(t.Out(), "\033[2J\033[HLoading user search…")
	contacts, contactsErr := c.loadContacts(ctx)
	frequent, frequentErr := c.loadFrequentUsers(ctx)
	if ctx.Err() != nil {
		return chat{}, false, ctx.Err()
	}
	recent := c.recentUserChoices(recentChats, "recent")
	local := mergeChoices(choicesFromUsers(contacts, "contact"), recent)
	base := choicesFromUsers(frequent, "")
	baseHeading := "Frequently contacted"
	if len(base) == 0 {
		base = c.recentUserChoices(recentChats, "")
		baseHeading = "Recent"
	}
	heading := baseHeading
	choices := base
	selected := 0
	var query []rune
	searching := false
	notice := ""
	generation := 0
	results := make(chan userSearchResult, 4)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var timerC <-chan time.Time
	var searchCancel context.CancelFunc
	defer func() {
		if searchCancel != nil {
			searchCancel()
		}
	}()
	render := func() {
		labels := make([]string, len(choices))
		for i, c := range choices {
			labels[i] = c.label
		}
		terminal.RenderUserSearch(t.Out(), labels, selected, string(query), heading, notice, searching)
	}
	resetSearch := func() {
		generation++
		if searchCancel != nil {
			searchCancel()
			searchCancel = nil
		}
		if !timer.Stop() && timerC != nil {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
		text := strings.TrimSpace(string(query))
		selected = 0
		searching = false
		notice = ""
		if text == "" {
			choices, heading = base, baseHeading
			return
		}
		choices = filterChoices(local, text)
		heading = "Results"
		if utf8.RuneCountInString(text) >= 2 {
			searching = true
			timer.Reset(300 * time.Millisecond)
			timerC = timer.C
		}
	}
	if contactsErr != nil {
		t.Report(contactsErr)
		notice = textfmt.CleanError(contactsErr)
	}
	if frequentErr != nil {
		t.Report(frequentErr)
		notice = textfmt.CleanError(frequentErr)
	}
	render()
	for {
		select {
		case <-ctx.Done():
			return chat{}, false, ctx.Err()
		case <-timerC:
			timerC = nil
			searchCtx, cancel := context.WithCancel(ctx)
			searchCancel = cancel
			currentGeneration, text := generation, strings.TrimSpace(string(query))
			go func() {
				remote, err := c.remoteUserChoices(searchCtx, text)
				response := userSearchResult{currentGeneration, text, remote, err}
				select {
				case results <- response:
				case <-searchCtx.Done():
				}
			}()
		case result := <-results:
			if result.generation != generation || result.query != strings.TrimSpace(string(query)) {
				continue
			}
			searching = false
			if searchCancel != nil {
				searchCancel()
			}
			searchCancel = nil
			if result.err != nil {
				fmt.Fprintln(t.Out())
				t.Report(result.err)
				notice = textfmt.CleanError(result.err)
				render()
				continue
			}
			notice = ""
			choices = mergeChoices(filterChoices(local, result.query), result.choices)
			selected = min(selected, max(0, len(choices)-1))
			render()
		case key, ok := <-t.Keys:
			if !ok {
				return chat{}, false, context.Canceled
			}
			if key == terminal.TerminalLockKey {
				continue
			}
			if key == terminal.TerminalUnlockKey {
				render()
				t.FlushNotices()
				continue
			}
			switch key {
			case "\x03", "\x04":
				return chat{}, false, nil
			case "\x1b":
				if len(query) == 0 {
					return chat{}, false, nil
				}
				query = nil
				resetSearch()
			case "\x1b[A", "\x1bOA":
				if selected > 0 {
					selected--
				}
			case "\x1b[B", "\x1bOB":
				if selected+1 < len(choices) {
					selected++
				}
			case "\r", "\n":
				if selected < len(choices) {
					choice := choices[selected]
					notice = "Opening…"
					render()
					entry, err := c.resolveUserChoice(ctx, choice)
					if err != nil {
						fmt.Fprintln(t.Out())
						t.Report(err)
						notice = textfmt.CleanError(err)
						render()
						continue
					}
					return entry, true, nil
				}
			case "\x7f", "\x08":
				if len(query) > 0 {
					query = query[:len(query)-1]
					resetSearch()
				}
			case "\x15":
				if len(query) > 0 {
					query = nil
					resetSearch()
				}
			default:
				if strings.HasPrefix(key, "\x1b") {
					render()
					continue
				}
				previous := len(query)
				query = textfmt.AppendQuery(query, key)
				if len(query) != previous {
					resetSearch()
				}
			}
			render()
		}
	}
}
