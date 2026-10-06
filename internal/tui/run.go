package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

type accountsRequested struct{}

func (accountsRequested) Error() string { return "switch accounts" }

type agentsRequested struct{}

func (agentsRequested) Error() string { return "open agents" }

func RunAccounts(ctx context.Context, m *account.AccountManager) error {
	defer m.Close()

	db, err := agent.OpenAgentDB(m.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wiregram: agents disabled — %s\n", textfmt.CleanError(err))
	} else {
		m.Agents = account.NewAgentRuntime(ctx, db, m)
		m.Agents.Start()
		defer m.Agents.Stop()
	}

	var active *account.Account
	for _, a := range m.Accounts {
		m.Start(ctx, a, nil)
	}
	if len(m.Accounts) > 0 {
		tuiSplash(ctx, m)
	}
	if len(m.Accounts) == 0 {
		var err error
		active, err = tuiPickAndAdd(ctx, m)
		if err != nil {
			return err
		}
	}
	for ctx.Err() == nil {
		if active == nil {
			var err error
			active, err = tuiChooseAccount(ctx, m, nil)
			if err != nil {
				if _, ok := err.(agentsRequested); ok && m.Agents != nil {
					tuiAgentUI(ctx, m)
					continue
				}
				return err
			}
			if active == nil {
				return nil
			}
		}
		s := active.Snapshot()
		needsAuth := s.NeedsLogin
		if active.BackendType == "telegram" {
			needsAuth = needsAuth || s.Identity.ID == 0
		}
		if needsAuth {
			if err := tuiLogin(ctx, m, active); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if active.NeverAuthenticated() {
					m.Discard(active)
				}
				active = nil
				continue
			}
			s = active.Snapshot()
		}
		if !needsAuth && (s.Backend == nil || s.Status != "online") {
			if !tuiWaitConnecting(ctx, m, active) {
				active = nil
				continue
			}
			s = active.Snapshot()
		}
		if s.Backend != nil && s.Status == "online" {
			err := runModes(s.Ctx, m, active, s.Backend, active.Histories)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var sw accountSwitch
			if errors.As(err, &sw) {
				active = sw.to
				continue
			}
			if _, switching := err.(accountsRequested); !switching && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "%s\n", textfmt.CleanError(err))
			}
		}
		var err error
		active, err = tuiChooseAccount(ctx, m, active)
		if err != nil {
			if _, ok := err.(agentsRequested); ok && m.Agents != nil {
				tuiAgentUI(ctx, m)
				active = nil
				continue
			}
			return err
		}
		if active == nil {
			return nil
		}
	}
	return ctx.Err()
}

// accountSwitch asks the account loop to make another account active.
type accountSwitch struct{ to *account.Account }

func (accountSwitch) Error() string { return "switch account" }

func runModes(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, histories map[backend.ChatID][]string) error {
	for {
		entries, archiveUnread, err := b.Dialogs(ctx, false)
		if err != nil {
			if !backend.NetworkError(err) {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s\n", textfmt.CleanError(err))
			if err := backend.WaitFor(ctx, time.Second); err != nil {
				return err
			}
			continue
		}
		return tuiSelectChat(ctx, mgr, active, b, histories, entries, archiveUnread)
	}
}
