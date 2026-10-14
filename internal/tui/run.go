package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wiregram/internal/account"
	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/plugin"
	"wiregram/internal/textfmt"
)

type accountsRequested struct{}

func (accountsRequested) Error() string { return "switch accounts" }

type settingsRequested struct{}

func (settingsRequested) Error() string { return "open settings" }

// ResetRequested is returned by RunAccounts after Settings' Reset removed
// every account; the caller deletes all local data and starts again.
type ResetRequested struct{}

func (ResetRequested) Error() string { return "reset wiregram" }

func RunAccounts(ctx context.Context, m *account.AccountManager) error {
	defer m.Close()

	db, err := agent.OpenAgentDB(m.Store.SQL(), m.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wiregram: bots disabled — %s\n", textfmt.CleanError(err))
	} else {
		m.Agents = account.NewAgentRuntime(ctx, db, m)
		m.Agents.Start()
		defer m.Agents.Stop()
	}

	m.Plugins = plugin.Load(m.Store, filepath.Join(m.Dir, "plugins"))
	defer m.Plugins.Close()

	inboxCtx, stopInbox := context.WithCancel(ctx)
	defer stopInbox()
	go m.WatchInbox(inboxCtx)

	var active *account.Account
	for _, a := range m.Accounts {
		m.Start(ctx, a, nil)
	}
	if len(m.Accounts) > 0 {
		tuiSplash(ctx, m)
	}
	if len(m.Accounts) == 0 {
		var err error
		active, err = tuiPickAndAdd(ctx, m, "")
		if err != nil {
			return err
		}
	}
	for ctx.Err() == nil {
		if active == nil {
			var err error
			active, err = tuiChooseAccount(ctx, m, nil)
			if err != nil {
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
		// A sign-in that succeeds for a new account ends in the welcome modal.
		welcome := false
		if needsAuth {
			welcome = active.NeverAuthenticated()
			if err := tuiLogin(ctx, m, active, ""); err != nil {
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
			err := runModes(s.Ctx, m, active, s.Backend, active.Histories, welcome)
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
			if _, ok := err.(settingsRequested); ok {
				to, err := tuiSettings(ctx, m, active, false)
				if err != nil {
					return err
				}
				if to == nil {
					return nil
				}
				active = to
				continue
			}
			if _, switching := err.(accountsRequested); !switching && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "%s\n", textfmt.CleanError(err))
			}
		}
		var err error
		active, err = tuiChooseAccount(ctx, m, active)
		if err != nil {
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

func runModes(ctx context.Context, mgr *account.AccountManager, active *account.Account, b backend.Backend, histories map[backend.ChatID][]string, welcome bool) error {
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
		return tuiSelectChat(ctx, mgr, active, b, histories, entries, archiveUnread, welcome)
	}
}
