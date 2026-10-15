package telegram

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"wiregram/internal/backend"
	"wiregram/internal/config"
)

// Telegram bots are created as managed bots: the user's session creates the
// bot with a manager bot of the user's as its controller, and only that
// manager can export the bot's token. The bot then signs in with the token.

const (
	botUsernameMax = 32
	botChatKeep    = 200 // messages a bot keeps per chat; it cannot load history
)

// botUsername is the Telegram username for a handle: Telegram requires a bot
// username to end in "bot".
func botUsername(handle string) string {
	if strings.HasSuffix(handle, "bot") {
		return handle
	}
	return strings.TrimRight(handle[:min(len(handle), botUsernameMax-4)], "_") + "_bot"
}

// botError turns a refusal from Telegram into one plain line.
func botError(err error, username string) error {
	switch {
	case tgerr.Is(err, "USERNAME_OCCUPIED"):
		return fmt.Errorf("@%s is taken on Telegram; change the handle", username)
	case tgerr.Is(err, "USERNAME_INVALID", "USERNAME_SUFFIX_MISSING"):
		return fmt.Errorf("Telegram does not accept @%s as a bot username", username)
	case tgerr.Is(err, "BOT_CREATE_LIMIT_EXCEEDED"):
		return fmt.Errorf("you own as many bots as Telegram allows; delete one in @BotFather " +
			"(Premium raises the limit)")
	case tgerr.Is(err, "MANAGER_PERMISSION_MISSING", "MANAGER_INVALID"):
		return fmt.Errorf("the manager bot may not manage bots; turn on Bot Management " +
			"for it in @BotFather")
	case tgerr.Is(err, "ACCESS_TOKEN_INVALID", "ACCESS_TOKEN_EXPIRED"):
		return fmt.Errorf("Telegram refused the bot token")
	case tgerr.Is(err, "NAME_INVALID"):
		return fmt.Errorf("Telegram does not accept the bot's name")
	}
	return err
}

// botSession signs in as a bot on a throwaway session and runs fn.
func botSession(ctx context.Context, cfg config.Config, token string,
	fn func(ctx context.Context, c *Client, self *tg.User) error) error {
	c := newClient(cfg, &session.StorageMemory{}, func(error) {})
	return c.Run(ctx, func(ctx context.Context) error {
		if _, err := c.Telegram.Auth().Bot(ctx, token); err != nil {
			return botError(err, "")
		}
		self, err := c.Telegram.Self(ctx)
		if err != nil {
			return fmt.Errorf("load the bot: %w", err)
		}
		return fn(ctx, c, self)
	})
}

// resolveUser finds a user by username, with the access hash of api's session.
func resolveUser(ctx context.Context, api *tg.Client, username string) (*tg.User, error) {
	res, err := api.ContactsResolveUsername(ctx,
		&tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return nil, fmt.Errorf("find @%s: %w", username, err)
	}
	peer, ok := res.Peer.(*tg.PeerUser)
	if ok {
		for _, value := range res.Users {
			if u, isUser := value.(*tg.User); isUser && u.ID == peer.UserID {
				return u, nil
			}
		}
	}
	return nil, fmt.Errorf("@%s is not a user or bot", username)
}

func (tb *TelegramBackend) CheckManager(ctx context.Context, token string) (backend.BotAccount, error) {
	var mgr backend.BotAccount
	err := botSession(ctx, tb.C.cfg, token, func(_ context.Context, _ *Client, self *tg.User) error {
		switch {
		case !self.Bot:
			return fmt.Errorf("that token is not a bot's")
		case !self.BotCanManageBots:
			return fmt.Errorf("@%s may not manage bots; turn on Bot Management for it in "+
				"@BotFather", self.Username)
		}
		mgr = backend.BotAccount{UserID: strconv.FormatInt(self.ID, 10),
			Username: self.Username, Token: token}
		return nil
	})
	return mgr, err
}

func (tb *TelegramBackend) WithManager(manager backend.BotAccount) backend.BotRegistrar {
	return tgRegistrar{tb: tb, manager: manager}
}

// tgRegistrar manages the user's bots through their manager bot.
type tgRegistrar struct {
	tb      *TelegramBackend
	manager backend.BotAccount
}

func (r tgRegistrar) needManager() error {
	if r.manager.Token == "" {
		return backend.ErrNoBotManager
	}
	return nil
}

// exportToken gets the bot's token through the manager; revoke replaces it.
func (r tgRegistrar) exportToken(ctx context.Context, username string, revoke bool) (string, error) {
	if err := r.needManager(); err != nil {
		return "", err
	}
	var token string
	err := botSession(ctx, r.tb.C.cfg, r.manager.Token,
		func(ctx context.Context, c *Client, _ *tg.User) error {
			bot, err := resolveUser(ctx, c.Api, username)
			if err != nil {
				return err
			}
			res, err := c.Api.BotsExportBotToken(ctx,
				&tg.BotsExportBotTokenRequest{Bot: bot.AsInput(), Revoke: revoke})
			if err != nil {
				return botError(err, username)
			}
			token = res.Token
			return nil
		})
	return token, err
}

func (r tgRegistrar) RegisterBot(ctx context.Context, handle, name string) (backend.BotAccount, error) {
	username := botUsername(handle)
	if err := r.needManager(); err != nil {
		return backend.BotAccount{}, err
	}
	api := r.tb.C.Api
	mgr, err := resolveUser(ctx, api, r.manager.Username)
	if err != nil {
		return backend.BotAccount{}, fmt.Errorf("find the manager bot: %w", err)
	}
	created, err := api.BotsCreateBot(ctx, &tg.BotsCreateBotRequest{
		Name: name, Username: username, ManagerID: mgr.AsInput(),
	})
	if tgerr.Is(err, "USERNAME_OCCUPIED") {
		// One of the user's own bots under this manager, registered before:
		// take it back with a new token. Anyone else's cannot be exported.
		token, terr := r.exportToken(ctx, username, true)
		if terr != nil {
			return backend.BotAccount{}, botError(err, username)
		}
		bot, rerr := resolveUser(ctx, api, username)
		if rerr != nil {
			return backend.BotAccount{}, rerr
		}
		return backend.BotAccount{UserID: strconv.FormatInt(bot.ID, 10), Username: username,
			Token: token}, nil
	}
	if err != nil {
		return backend.BotAccount{}, botError(err, username)
	}
	bot, ok := created.(*tg.User)
	if !ok {
		return backend.BotAccount{}, fmt.Errorf("Telegram returned no bot for @%s", username)
	}
	token, err := r.exportToken(ctx, username, false)
	if err != nil {
		return backend.BotAccount{}, fmt.Errorf("created @%s but could not get its token; "+
			"register again: %w", username, err)
	}
	return backend.BotAccount{UserID: strconv.FormatInt(bot.ID, 10), Username: username,
		Token: token}, nil
}

// RenameBot changes the bot's name. Telegram bot usernames are fixed, so a
// new handle is refused unless it names the same bot.
func (r tgRegistrar) RenameBot(ctx context.Context, bot backend.BotAccount, handle, name string) error {
	if botUsername(handle) != bot.Username {
		return fmt.Errorf("a Telegram bot's username can't change; keep @%s", bot.Username)
	}
	api := r.tb.C.Api
	u, err := resolveUser(ctx, api, bot.Username)
	if err != nil {
		return err
	}
	if _, err := api.BotsSetBotInfo(ctx, &tg.BotsSetBotInfoRequest{Bot: u.AsInput(),
		Name: name}); err != nil {
		return fmt.Errorf("rename @%s: %w", bot.Username, botError(err, bot.Username))
	}
	return nil
}

func (r tgRegistrar) RotateBotToken(ctx context.Context, bot backend.BotAccount) (backend.BotAccount, error) {
	token, err := r.exportToken(ctx, bot.Username, true)
	if err != nil {
		return bot, err
	}
	bot.Token = token
	return bot, nil
}

// UnregisterBot revokes the bot's token, so it can no longer sign in.
// Telegram has no call to delete a bot; the user can in @BotFather.
func (r tgRegistrar) UnregisterBot(ctx context.Context, bot backend.BotAccount) error {
	_, err := r.exportToken(ctx, bot.Username, true)
	return err
}

func (r tgRegistrar) ConnectBot(ctx context.Context, bot backend.BotAccount) (backend.Backend, error) {
	return connectBot(ctx, r.tb.C.cfg, bot)
}

// BotBackend is a bot's own connection. A bot cannot list its chats or load
// their history, so it keeps the private chats and messages it sees while
// connected, and answers the rest of the Backend as far as a bot can.
type BotBackend struct {
	TelegramBackend
	username string
	done     chan struct{}
	err      error // why the connection ended; read after done closes

	mu   sync.Mutex
	seen map[peerKey]*botChat
}

type botChat struct {
	ch   chat
	msgs []backend.Message
	last time.Time
}

// connectBot signs in as the bot and returns once it receives updates; the
// connection lasts until ctx ends.
func connectBot(ctx context.Context, cfg config.Config, bot backend.BotAccount) (*BotBackend, error) {
	c := newClient(cfg, &session.StorageMemory{}, func(error) {})
	bb := &BotBackend{TelegramBackend: TelegramBackend{C: c}, username: bot.Username,
		done: make(chan struct{}), seen: make(map[peerKey]*botChat)}
	sub := c.events.Subscribe(backend.Filter{Kinds: backend.EventMessage | backend.EventEdit,
		DMOnly: true})
	go bb.record(sub)
	started := make(chan struct{})
	go func() {
		defer close(bb.done)
		defer sub.Close()
		bb.err = c.Run(ctx, func(ctx context.Context) error {
			if _, err := c.Telegram.Auth().Bot(ctx, bot.Token); err != nil {
				return botError(err, bot.Username)
			}
			self, err := c.Telegram.Self(ctx)
			if err != nil {
				return fmt.Errorf("load the bot: %w", err)
			}
			c.AccountID, c.AccountName = self.ID, "@"+self.Username
			c.Remember([]tg.UserClass{self}, nil)
			return c.Updates.Run(ctx, c.Api, self.ID, updates.AuthOptions{IsBot: true,
				OnStart: func(context.Context) { close(started) }})
		})
	}()
	select {
	case <-started:
		return bb, nil
	case <-bb.done:
		return nil, fmt.Errorf("@%s could not sign in; rotate its token: %w", bot.Username, bb.err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// record keeps the private chats the bot sees and their messages.
func (bb *BotBackend) record(sub *backend.Subscription) {
	for ev := range sub.C {
		if ev.Message != nil {
			bb.keep(ev.Chat, *ev.Message)
		}
	}
}

// keep adds msg to the chat's kept messages, or replaces an edited one.
func (bb *BotBackend) keep(id backend.ChatID, msg backend.Message) {
	if len(id.Id) < 2 || id.Id[0] != 'u' {
		return
	}
	userID, err := strconv.ParseInt(id.Id[1:], 10, 64)
	if err != nil {
		return
	}
	key := peerKey{'u', userID}
	bb.C.mu.Lock()
	u := bb.C.users[userID]
	bb.C.mu.Unlock()
	bb.mu.Lock()
	defer bb.mu.Unlock()
	bc := bb.seen[key]
	if bc == nil {
		if u == nil {
			return // no access hash to answer with
		}
		bc = &botChat{ch: chat{key: key, title: UserName(u),
			peer: &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}}}
		bb.seen[key] = bc
	}
	for i := range bc.msgs {
		if bc.msgs[i].ID == msg.ID {
			bc.msgs[i] = msg
			return
		}
	}
	bc.msgs = append(bc.msgs, msg)
	if len(bc.msgs) > botChatKeep {
		bc.msgs = bc.msgs[len(bc.msgs)-botChatKeep:]
	}
	bc.last = msg.Timestamp
}

func (bb *BotBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{Edit: true, Upload: true, Typing: true}
}

func (bb *BotBackend) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-bb.done:
		return bb.err
	}
}

func (bb *BotBackend) Authenticate(context.Context, config.PromptFunc, func(error)) error {
	return nil
}

func (bb *BotBackend) AccountLabel() string { return "[TG] @" + bb.username }

// Dialogs lists the private chats seen since the bot connected, newest first.
func (bb *BotBackend) Dialogs(context.Context, bool) ([]backend.ChatEntry, int, error) {
	bb.mu.Lock()
	defer bb.mu.Unlock()
	chats := make([]*botChat, 0, len(bb.seen))
	for _, bc := range bb.seen {
		chats = append(chats, bc)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].last.After(chats[j].last) })
	entries := make([]backend.ChatEntry, len(chats))
	for i, bc := range chats {
		entries[i] = chatToChatEntry(bc.ch)
	}
	return entries, 0, nil
}

func (bb *BotBackend) History(_ context.Context, ch backend.ChatEntry) ([]backend.Message, error) {
	bb.mu.Lock()
	defer bb.mu.Unlock()
	if bc := bb.seen[entryToChat(ch).key]; bc != nil {
		return append([]backend.Message(nil), bc.msgs...), nil
	}
	return nil, nil
}

func (bb *BotBackend) OlderHistory(context.Context, backend.ChatEntry, string) ([]backend.Message, error) {
	return nil, nil
}

func (bb *BotBackend) UnreadMessages(context.Context, backend.ChatEntry, int) ([]backend.Message, error) {
	return nil, nil
}

func (bb *BotBackend) Send(ctx context.Context, ch backend.ChatEntry, text, replyTo string) (backend.Message, error) {
	msg, err := bb.TelegramBackend.Send(ctx, ch, text, replyTo)
	if err == nil {
		bb.keep(ch.ID, msg)
	}
	return msg, err
}

func (bb *BotBackend) SendUpload(ctx context.Context, ch backend.ChatEntry, path, caption, replyTo string) (backend.Message, error) {
	msg, err := bb.TelegramBackend.SendUpload(ctx, ch, path, caption, replyTo)
	if err == nil {
		bb.keep(ch.ID, msg)
	}
	return msg, err
}

// Bots cannot mark messages read, search or list people.

func (bb *BotBackend) MarkRead(context.Context, backend.ChatEntry, string) error { return nil }
func (bb *BotBackend) RefreshReadState(context.Context, backend.ChatEntry) error {
	return nil
}
func (bb *BotBackend) RefreshChatUnread(context.Context, backend.ChatEntry) (int, error) {
	return 0, nil
}
func (bb *BotBackend) CachedUnread(backend.ChatEntry) int { return 0 }

func (bb *BotBackend) SearchMessages(context.Context, backend.ChatEntry, string) ([]backend.Message, error) {
	return nil, fmt.Errorf("bots can't search messages")
}

func (bb *BotBackend) Mentions(context.Context, int) ([]backend.Mention, error) { return nil, nil }

func (bb *BotBackend) DeleteChat(context.Context, backend.ChatEntry, bool) error {
	return fmt.Errorf("bots can't delete chats")
}

func (bb *BotBackend) UserSearchBase(context.Context, []backend.ChatEntry) (backend.UserSearchBase, error) {
	return backend.UserSearchBase{}, nil
}

func (bb *BotBackend) UserSearchRemote(context.Context, string) ([]backend.UserCandidate, error) {
	return nil, nil
}

func (bb *BotBackend) OpenUserCandidate(context.Context, backend.UserCandidate) (backend.ChatEntry, error) {
	return backend.ChatEntry{}, fmt.Errorf("bots can't start chats")
}
