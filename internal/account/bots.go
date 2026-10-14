package account

import (
	"context"
	"fmt"

	"wiregram/internal/backend"
)

// A bot registered on its network gets a connection of its own, signed in
// with its token, and answers the DMs people send it as itself. Those
// conversations are separate from the account's DMs, which bots answer as
// the user, and every DM to the bot is the bot's to answer.

// botConn is one registered bot's live connection.
type botConn struct {
	cancel   context.CancelFunc
	token    string
	textOnly bool
	b        backend.Backend // set once signed in; guarded by agentRuntime.mu
}

// syncBots keeps a connection for every enabled, registered bot whose account
// is online, and none for the rest.
func (rt *agentRuntime) syncBots() {
	accounts := rt.mgr.List()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	wanted := make(map[int64]bool)
	for _, acct := range accounts {
		s := acct.Snapshot()
		reg, ok := s.Backend.(backend.BotRegistrar)
		if !ok || s.Status != "online" {
			continue
		}
		key := rt.mgr.AccountKey(acct)
		for _, rec := range rt.records[key] {
			bot, registered := rt.regs[rec.ID]
			if rt.agents[rec.ID] == nil || !registered {
				continue
			}
			wanted[rec.ID] = true
			c := rt.bots[rec.ID]
			if c != nil && c.token == bot.Token && c.textOnly == rec.TextOnly() {
				continue
			}
			if c != nil {
				c.cancel()
			}
			ctx, cancel := context.WithCancel(rt.ctx)
			c = &botConn{cancel: cancel, token: bot.Token, textOnly: rec.TextOnly()}
			rt.bots[rec.ID] = c
			rt.wg.Add(1)
			go rt.runBot(ctx, acct, key, rec.ID, reg, bot, c)
		}
	}
	for id, c := range rt.bots {
		if !wanted[id] {
			c.cancel()
			delete(rt.bots, id)
		}
	}
}

// runBot signs in as the bot and answers its DMs until ctx ends.
func (rt *agentRuntime) runBot(ctx context.Context, acct *Account, key string, agentID int64,
	reg backend.BotRegistrar, bot backend.BotAccount, c *botConn) {
	defer rt.wg.Done()
	b, err := reg.ConnectBot(ctx, bot)
	if err != nil {
		if ctx.Err() == nil {
			rt.fail(agentID, err)
		}
		return
	}
	sub := b.Events().Subscribe(backend.Filter{
		Kinds:    backend.EventMessage,
		DMOnly:   true,
		TextOnly: c.textOnly,
	})
	defer sub.Close()
	rt.mu.Lock()
	c.b = b
	rt.mu.Unlock()
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	defer func() { <-done }()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			msg, text, ok := dmText(ev)
			if !ok {
				continue
			}
			rt.mu.Lock()
			ag := rt.agents[agentID]
			rt.mu.Unlock()
			if ag != nil {
				rt.enqueue(acct, ConvKey{AgentID: agentID, AccountKey: key, Chat: ev.Chat,
					Bot: true}, ag, msg, text)
			}
		}
	}
}

// backendFor is what the conversation is answered through: the bot's own
// connection for a DM to the bot, else the account's backend.
func (rt *agentRuntime) backendFor(acct *Account, key ConvKey) backend.Backend {
	if !key.Bot {
		return acct.Snapshot().Backend
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if c := rt.bots[key.AgentID]; c != nil && c.b != nil {
		return c.b
	}
	return nil
}

// BotBackend is the registered bot's live connection, for its inbox; nil
// while it is not signed in.
func (rt *agentRuntime) BotBackend(agentID int64) backend.Backend {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if c := rt.bots[agentID]; c != nil {
		return c.b
	}
	return nil
}

// registrar is the account's backend when it is online and can manage bots.
func registrar(a *Account) (backend.BotRegistrar, error) {
	s := a.Snapshot()
	r, ok := s.Backend.(backend.BotRegistrar)
	switch {
	case s.Backend == nil || s.Status != "online":
		return nil, fmt.Errorf("%s is offline; connect it first", a.Handle())
	case !ok:
		return nil, fmt.Errorf("bots can't be registered on this network yet")
	}
	return r, nil
}

// CanRegister reports whether the account's bots can be registered on its
// network.
func (rt *agentRuntime) CanRegister(a *Account) bool {
	_, ok := a.Snapshot().Backend.(backend.BotRegistrar)
	return ok
}

// Registration is the agent's bot account, if it is registered.
func (rt *agentRuntime) Registration(agentID int64) (backend.BotAccount, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	bot, ok := rt.regs[agentID]
	return bot, ok
}

// Register creates the agent's bot on the account's network, named by its
// handle, and connects it.
func (rt *agentRuntime) Register(ctx context.Context, a *Account, agentID int64) error {
	r, err := registrar(a)
	if err != nil {
		return err
	}
	rec, ok := rt.Agent(agentID)
	if !ok {
		return fmt.Errorf("that bot has been deleted")
	}
	bot, err := r.RegisterBot(ctx, rec.Handle, rec.Name)
	if err != nil {
		return err
	}
	if err := rt.Db.SetRegistration(agentID, bot); err != nil {
		r.UnregisterBot(ctx, bot) // nothing local refers to it, so take it back
		return err
	}
	rt.Reload()
	return nil
}

// Rename changes a registered bot's username and display name on the
// network; the caller saves them locally once this succeeds.
func (rt *agentRuntime) Rename(ctx context.Context, a *Account, agentID int64,
	handle, name string) error {
	bot, ok := rt.Registration(agentID)
	if !ok {
		return nil
	}
	r, err := registrar(a)
	if err != nil {
		return err
	}
	if err := r.RenameBot(ctx, bot, handle, name); err != nil {
		return err
	}
	bot.Username = handle
	return rt.Db.SetRegistration(agentID, bot)
}

// RotateToken gives the registered bot a new token and revokes the old one.
func (rt *agentRuntime) RotateToken(ctx context.Context, a *Account, agentID int64) error {
	bot, ok := rt.Registration(agentID)
	if !ok {
		return fmt.Errorf("that bot is not registered")
	}
	r, err := registrar(a)
	if err != nil {
		return err
	}
	if bot, err = r.RotateBotToken(ctx, bot); err != nil {
		if bot.Token != "" {
			rt.Db.SetRegistration(agentID, bot) // the new token works; keep it
			rt.Reload()
		}
		return err
	}
	if err := rt.Db.SetRegistration(agentID, bot); err != nil {
		return err
	}
	rt.Reload()
	return nil
}

// Unregister disables the bot on the network and forgets its token.
func (rt *agentRuntime) Unregister(ctx context.Context, a *Account, agentID int64) error {
	bot, ok := rt.Registration(agentID)
	if !ok {
		return nil
	}
	r, err := registrar(a)
	if err != nil {
		return err
	}
	if err := r.UnregisterBot(ctx, bot); err != nil {
		return err
	}
	if err := rt.Db.DeleteRegistration(agentID); err != nil {
		return err
	}
	rt.Reload()
	return nil
}

// UnregisterAccount disables every registered bot of the account, for its
// removal. It goes on past a failure and returns the first.
func (rt *agentRuntime) UnregisterAccount(ctx context.Context, a *Account) error {
	var first error
	for _, rec := range rt.AgentsFor(a) {
		if err := rt.Unregister(ctx, a, rec.ID); err != nil && first == nil {
			first = fmt.Errorf("bot @%s stays on the server: %w", rec.Handle, err)
		}
	}
	return first
}

// InboxWaiting counts the replies to the bot's own DMs waiting for approval.
func (rt *agentRuntime) InboxWaiting(agentID int64) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n := 0
	for key := range rt.pending {
		if key.Bot && key.AgentID == agentID {
			n++
		}
	}
	return n
}
