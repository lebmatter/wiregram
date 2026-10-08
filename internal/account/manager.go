package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"go.mau.fi/whatsmeow/types"

	"wiregram/internal/agent"
	"wiregram/internal/backend"
	"wiregram/internal/config"
	"wiregram/internal/matrix"
	"wiregram/internal/mattermost"
	"wiregram/internal/store"
	"wiregram/internal/telegram"
	"wiregram/internal/textfmt"
	"wiregram/internal/whatsapp"
)

type AccountState struct {
	Identity   backend.AccountIdentity
	Status     string
	Notice     string
	client     *telegram.Client
	Backend    backend.Backend
	Ctx        context.Context
	NeedsLogin bool
	Unseen     int // incoming messages since the user last had this account open
}

type Account struct {
	Dir         string
	BackendType string // "telegram", "mattermost", "matrix", or "whatsapp"
	mmBackend   *mattermost.MmBackend
	mxBackend   *matrix.MxBackend
	waBackend   *whatsapp.WaBackend
	mu          sync.Mutex
	state       AccountState
	Cancel      context.CancelFunc
	Done        chan struct{}
	Histories   map[backend.ChatID][]string // Owned exclusively by the UI.
}

func (a *Account) Snapshot() AccountState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *Account) Label() string {
	if a.BackendType == "mattermost" && a.mmBackend != nil {
		if a.mmBackend.Username != "" {
			return a.mmBackend.AccountLabel()
		}
		return "[MM] awaiting sign-in"
	}
	if a.BackendType == "matrix" && a.mxBackend != nil {
		if a.mxBackend.UserID != "" {
			return a.mxBackend.AccountLabel()
		}
		return "[MX] awaiting sign-in"
	}
	if a.BackendType == "whatsapp" && a.waBackend != nil {
		return a.waBackend.AccountLabel()
	}
	s := a.Snapshot()
	if s.Identity.ID == 0 {
		return "[TG] awaiting sign-in"
	}
	return fmt.Sprintf("[TG] %s (+%s)", textfmt.Plain(s.Identity.Name), strings.TrimPrefix(textfmt.Plain(s.Identity.Phone), "+"))
}

type AccountManager struct {
	cfg      config.Config
	Dir      string
	Store    *store.DB
	Accounts []*Account // Owned exclusively by the UI after discovery.
	Agents   *agentRuntime
	Changed  chan struct{}
	// agentChanged and inboxChanged mirror Changed for the agent runtime and
	// the inbox watcher, so no listener takes another's signal.
	agentChanged chan struct{}
	inboxChanged chan struct{}
	connecting   chan struct{}
	mu           sync.Mutex
	owners       map[int64]*Account
}

func (m *AccountManager) notify() {
	for _, ch := range []chan struct{}{m.Changed, m.agentChanged, m.inboxChanged} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// addAccount appends under the lock: the UI owns Accounts, but the agent
// runtime reads it through List from its own goroutines.
func (m *AccountManager) addAccount(a *Account) {
	m.mu.Lock()
	m.Accounts = append(m.Accounts, a)
	m.mu.Unlock()
}

// List returns a copy of the accounts that is safe to use off the UI goroutine.
func (m *AccountManager) List() []*Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Account(nil), m.Accounts...)
}

// AccountKey is the account's stable agent-binding key: its directory name,
// or "primary" for the session stored in the config directory itself.
func (m *AccountManager) AccountKey(a *Account) string {
	return m.keyOf(a.Dir)
}

// keyOf is also the account's key in wiregram.db.
func (m *AccountManager) keyOf(dir string) string {
	if filepath.Clean(dir) == filepath.Clean(m.Dir) {
		return agent.PrimaryAccountKey
	}
	return filepath.Base(dir)
}

// identity decodes the account's saved identity; ok is false when there is none.
func (m *AccountManager) identity(dir string, v any) (bool, error) {
	data, err := m.Store.Get(m.keyOf(dir), store.Identity)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("read account identity: %w", err)
	}
	return true, nil
}

func (m *AccountManager) saveIdentity(dir string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode account identity: %w", err)
	}
	return m.Store.Put(m.keyOf(dir), store.Identity, data)
}

func (a *Account) set(m *AccountManager, update func(*AccountState)) {
	a.mu.Lock()
	update(&a.state)
	a.mu.Unlock()
	m.notify()
}

func LoadAccounts(cfg config.Config, dir string, db *store.DB) (*AccountManager, error) {
	m := &AccountManager{cfg: cfg, Dir: dir, Store: db, Changed: make(chan struct{}, 1), agentChanged: make(chan struct{}, 1), inboxChanged: make(chan struct{}, 1), connecting: make(chan struct{}, 4), owners: make(map[int64]*Account)}
	add := func(path string, requireIdentity bool) error {
		a := &Account{Dir: path, BackendType: "telegram", Histories: make(map[backend.ChatID][]string)}
		a.state.Status = "offline"
		if _, err := m.identity(path, &a.state.Identity); err != nil {
			return err
		}
		if requireIdentity && a.state.Identity.ID == 0 {
			os.RemoveAll(path)
			return db.DeleteAccount(m.keyOf(path))
		}
		if a.state.Identity.ID != 0 {
			if m.owners[a.state.Identity.ID] != nil {
				return fmt.Errorf("duplicate saved account %d", a.state.Identity.ID)
			}
			m.owners[a.state.Identity.ID] = a
		}
		m.addAccount(a)
		return nil
	}
	// The original session stays in place; existing users need no migration or login.
	if _, err := db.Get(agent.PrimaryAccountKey, store.TelegramSession); err == nil {
		if err := add(dir, false); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("inspect original session: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "accounts"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "whatsapp-") {
			path := filepath.Join(dir, "accounts", entry.Name())
			var identity waAccountIdentity
			if ok, err := m.identity(path, &identity); err != nil {
				return nil, fmt.Errorf("read whatsapp account: %w", err)
			} else if !ok {
				continue
			}
			wb := whatsapp.NewWABackend(path)
			wb.Jid = identity.JID
			wb.PushName = identity.PushName
			a := &Account{
				Dir:         path,
				BackendType: "whatsapp",
				waBackend:   wb,
				Histories:   make(map[backend.ChatID][]string),
			}
			a.state.Status = "offline"
			m.addAccount(a)
		} else if entry.IsDir() && strings.HasPrefix(entry.Name(), "matrix-") {
			path := filepath.Join(dir, "accounts", entry.Name())
			var identity mxAccountIdentity
			if ok, err := m.identity(path, &identity); err != nil {
				return nil, fmt.Errorf("read matrix account: %w", err)
			} else if !ok {
				continue
			}
			mb := matrix.NewMXBackend(identity.Homeserver)
			mb.UserID = identity.UserID
			mb.DeviceID = identity.DeviceID
			mb.Store, mb.StoreKey = db, m.keyOf(path)
			mb.AccessToken = identity.Token
			a := &Account{
				Dir:         path,
				BackendType: "matrix",
				mxBackend:   mb,
				Histories:   make(map[backend.ChatID][]string),
			}
			a.state.Status = "offline"
			a.state.NeedsLogin = mb.AccessToken == ""
			m.addAccount(a)
		} else if entry.IsDir() && strings.HasPrefix(entry.Name(), "mattermost-") {
			path := filepath.Join(dir, "accounts", entry.Name())
			var identity mmAccountIdentity
			if ok, err := m.identity(path, &identity); err != nil {
				return nil, fmt.Errorf("read mattermost account: %w", err)
			} else if !ok {
				continue
			}
			mb := mattermost.NewMMBackend(identity.ServerURL)
			mb.UserID = identity.UserID
			mb.Username = identity.Username
			mb.Token = identity.Token
			a := &Account{
				Dir:         path,
				BackendType: "mattermost",
				mmBackend:   mb,
				Histories:   make(map[backend.ChatID][]string),
			}
			a.state.Status = "offline"
			a.state.NeedsLogin = mb.Token == ""
			m.addAccount(a)
		} else if entry.IsDir() && strings.HasPrefix(entry.Name(), "account-") {
			if err := add(filepath.Join(dir, "accounts", entry.Name()), true); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// HasTelegramCredentials reports whether Telegram API credentials are configured.
func (m *AccountManager) HasTelegramCredentials() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.APIID > 0
}

// SetTelegramCredentials saves the API credentials the first Telegram account needs.
func (m *AccountManager) SetTelegramCredentials(id int, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.cfg
	cfg.APIID, cfg.APIHash = id, hash
	if err := config.Save(m.Store, cfg); err != nil {
		return fmt.Errorf("save Telegram API credentials: %w", err)
	}
	m.cfg = cfg
	return nil
}

func (m *AccountManager) AddTelegram() (*Account, error) {
	root := filepath.Join(m.Dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "account-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	a := &Account{Dir: dir, BackendType: "telegram", Histories: make(map[backend.ChatID][]string)}
	a.state.Status, a.state.NeedsLogin = "sign-in required", true
	m.addAccount(a)
	return a, nil
}

func (m *AccountManager) AddMattermost(serverURL string) (*Account, error) {
	root := filepath.Join(m.Dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "mattermost-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	mb := mattermost.NewMMBackend(serverURL)
	a := &Account{
		Dir:         dir,
		BackendType: "mattermost",
		mmBackend:   mb,
		Histories:   make(map[backend.ChatID][]string),
	}
	a.state.Status, a.state.NeedsLogin = "sign-in required", true
	m.addAccount(a)
	return a, nil
}

func (m *AccountManager) AddMatrix(homeserver string) (*Account, error) {
	root := filepath.Join(m.Dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "matrix-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	mb := matrix.NewMXBackend(homeserver)
	mb.Store, mb.StoreKey = m.Store, m.keyOf(dir)
	a := &Account{
		Dir:         dir,
		BackendType: "matrix",
		mxBackend:   mb,
		Histories:   make(map[backend.ChatID][]string),
	}
	a.state.Status, a.state.NeedsLogin = "sign-in required", true
	m.addAccount(a)
	return a, nil
}

func (m *AccountManager) AddWhatsApp() (*Account, error) {
	root := filepath.Join(m.Dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "whatsapp-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	wb := whatsapp.NewWABackend(dir)
	a := &Account{
		Dir:         dir,
		BackendType: "whatsapp",
		waBackend:   wb,
		Histories:   make(map[backend.ChatID][]string),
	}
	a.state.Status, a.state.NeedsLogin = "sign-in required", true
	m.addAccount(a)
	return a, nil
}

// Remove signs the account out on its server when it is online, then deletes
// its local data and its agent binding. The local data goes even when the
// server logout fails; that failure is returned.
func (m *AccountManager) Remove(ctx context.Context, a *Account) error {
	var logoutErr error
	s := a.Snapshot()
	if lb, ok := s.Backend.(backend.LogoutBackend); ok && s.Status == "online" {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		logoutErr = lb.Logout(lctx)
		cancel()
	}
	key := m.AccountKey(a)
	m.Discard(a)
	if m.Agents != nil {
		if err := m.Agents.Db.UnbindAccount(key); err != nil {
			return err
		}
		m.Agents.Reload()
	}
	if logoutErr != nil {
		return fmt.Errorf("removed locally, but server logout failed: %w", logoutErr)
	}
	return nil
}

func (m *AccountManager) Discard(a *Account) {
	if a.Cancel != nil {
		a.Cancel()
	}
	if a.Done != nil {
		<-a.Done
	}
	if a.BackendType == "whatsapp" && a.waBackend != nil && a.waBackend.Client != nil {
		a.waBackend.Client.Disconnect()
	}
	m.mu.Lock()
	for i, x := range m.Accounts {
		if x == a {
			m.Accounts = append(m.Accounts[:i], m.Accounts[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	if a.Dir != "" {
		// The primary account's directory is the config directory itself.
		if m.keyOf(a.Dir) != agent.PrimaryAccountKey {
			if err := os.RemoveAll(a.Dir); err != nil {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}
		}
		if err := m.Store.DeleteAccount(m.keyOf(a.Dir)); err != nil {
			a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
		}
	}
	if a.BackendType == "telegram" {
		if err := m.forgetUnprovenCredentials(); err != nil {
			a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
		}
	}
}

// forgetUnprovenCredentials clears the saved Telegram API credentials while no
// Telegram account has signed in with them, so a mistyped ID or hash is asked
// for again next time. Credentials embedded in the build come back on reload.
func (m *AccountManager) forgetUnprovenCredentials() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.Accounts {
		if x.BackendType == "telegram" && !x.NeverAuthenticated() {
			return nil
		}
	}
	if err := config.Save(m.Store, config.Config{AutoLock: m.cfg.AutoLock}); err != nil {
		return fmt.Errorf("clear Telegram API credentials: %w", err)
	}
	cfg, err := config.LoadConfig(m.Store)
	if err != nil {
		return err
	}
	m.cfg = cfg
	return nil
}

func (a *Account) NeverAuthenticated() bool {
	if a.BackendType == "mattermost" {
		return a.mmBackend == nil || a.mmBackend.Token == ""
	}
	if a.BackendType == "matrix" {
		return a.mxBackend == nil || a.mxBackend.AccessToken == ""
	}
	if a.BackendType == "whatsapp" {
		return a.waBackend == nil || a.waBackend.Jid.IsEmpty()
	}
	return a.Snapshot().Identity.ID == 0
}

type mmAccountIdentity struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
}

type mxAccountIdentity struct {
	Homeserver string `json:"homeserver"`
	Token      string `json:"token"`
	UserID     string `json:"user_id"`
	DeviceID   string `json:"device_id"`
}

type waAccountIdentity struct {
	JID      types.JID `json:"jid"`
	PushName string    `json:"push_name"`
}

func (m *AccountManager) saveMXAccount(dir string, mx *matrix.MxBackend) error {
	return m.saveIdentity(dir, mxAccountIdentity{
		Homeserver: mx.Homeserver, Token: mx.AccessToken, UserID: mx.UserID, DeviceID: mx.DeviceID,
	})
}

func (m *AccountManager) saveMMAccount(dir string, mb *mattermost.MmBackend) error {
	return m.saveIdentity(dir, mmAccountIdentity{
		ServerURL: mb.ServerURL, Token: mb.Token, UserID: mb.UserID, Username: mb.Username,
	})
}

// A supervisor owns one connection lifetime. Switching the UI never stops it.
func (m *AccountManager) Start(parent context.Context, a *Account, prompt config.PromptFunc) {
	if a.BackendType == "mattermost" {
		m.startMattermost(parent, a, prompt)
		return
	}
	if a.BackendType == "matrix" {
		m.startMatrix(parent, a, prompt)
		return
	}
	if a.BackendType == "whatsapp" {
		m.startWhatsApp(parent, a, prompt)
		return
	}
	if a.Done != nil {
		select {
		case <-a.Done:
		default:
			return
		}
	}
	ctx, cancel := context.WithCancel(parent)
	a.Cancel, a.Done = cancel, make(chan struct{})
	a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "connecting", "", false })
	go func() {
		defer close(a.Done)
		defer m.notify()
		delay := time.Second
		for ctx.Err() == nil {
			select {
			case m.connecting <- struct{}{}:
			case <-ctx.Done():
				return
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { <-m.connecting }) }
			attemptCtx, stopAttempt := context.WithCancel(ctx)
			var startupTimer *time.Timer
			if prompt == nil {
				startupTimer = time.AfterFunc(30*time.Second, stopAttempt)
			}
			started := false
			m.mu.Lock()
			cfg := m.cfg
			m.mu.Unlock()
			c, err := telegram.NewClient(cfg, m.Store, m.AccountKey(a), func(err error) {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			})
			if err == nil {
				err = c.Telegram.Run(attemptCtx, func(connectionCtx context.Context) error {
					if prompt != nil {
						if err := c.Authenticate(connectionCtx, prompt, c.Report); err != nil {
							return err
						}
					} else {
						status, err := c.Telegram.Auth().Status(connectionCtx)
						if err != nil {
							return fmt.Errorf("check account authentication: %w", err)
						}
						if !status.Authorized {
							a.set(m, func(s *AccountState) { s.NeedsLogin = true })
							return fmt.Errorf("sign in to this account again")
						}
					}
					self, err := c.Telegram.Self(connectionCtx)
					if err != nil {
						return fmt.Errorf("load account: %w", err)
					}
					identity := backend.AccountIdentity{ID: self.ID, Name: telegram.UserName(self), Phone: self.Phone}
					m.mu.Lock()
					owner := m.owners[self.ID]
					previous := a.Snapshot().Identity.ID
					if (owner != nil && owner != a) || (previous != 0 && previous != self.ID) {
						m.mu.Unlock()
						return fmt.Errorf("this number already belongs to another account or differs from this saved account")
					}
					m.owners[self.ID] = a
					m.mu.Unlock()
					if err := m.saveIdentity(a.Dir, identity); err != nil {
						return err
					}
					prompt = nil // Reconnects must never read from an abandoned login UI.
					c.AccountID = identity.ID
					c.AccountName = fmt.Sprintf("%s (+%s)", textfmt.Plain(identity.Name), strings.TrimPrefix(textfmt.Plain(identity.Phone), "+"))
					c.Remember([]tg.UserClass{self}, nil)
					return c.Updates.Run(connectionCtx, c.Api, self.ID, updates.AuthOptions{OnStart: func(liveCtx context.Context) {
						started = true
						if startupTimer != nil {
							startupTimer.Stop()
						}
						delay = time.Second
						a.set(m, func(s *AccountState) {
							s.Identity, s.client, s.Backend, s.Ctx, s.Status, s.Notice = identity, c, &telegram.TelegramBackend{C: c}, liveCtx, "online", ""
						})
						release()
					}})
				})
			}
			if startupTimer != nil {
				startupTimer.Stop()
			}
			timedOut := !started && attemptCtx.Err() != nil && ctx.Err() == nil
			stopAttempt()
			release()
			a.set(m, func(s *AccountState) {
				s.client, s.Backend, s.Ctx, s.Status = nil, nil, nil, "offline"
				if err != nil {
					s.Notice = textfmt.CleanError(err)
				}
			})
			if ctx.Err() != nil || err == nil || (!backend.NetworkError(err) && !timedOut) {
				return
			}
			a.set(m, func(s *AccountState) { s.Status = "reconnecting" })
			if backend.WaitFor(ctx, delay+time.Duration(rand.Int64N(int64(delay/2)+1))) != nil {
				return
			}
			delay = min(delay*2, time.Minute)
		}
	}()
}

func (m *AccountManager) startMatrix(parent context.Context, a *Account, prompt config.PromptFunc) {
	if a.Done != nil {
		select {
		case <-a.Done:
		default:
			return
		}
	}
	mb := a.mxBackend
	ctx, cancel := context.WithCancel(parent)
	a.Cancel, a.Done = cancel, make(chan struct{})
	a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "connecting", "", false })
	go func() {
		defer close(a.Done)
		defer m.notify()
		if prompt != nil {
			if err := mb.Authenticate(ctx, prompt, func(err error) {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}); err != nil {
				a.set(m, func(s *AccountState) {
					s.Status, s.Notice = "offline", textfmt.CleanError(err)
					if !errors.Is(err, context.Canceled) {
						s.NeedsLogin = true
					}
				})
				return
			}
		} else if mb.AccessToken != "" {
			if err := mb.Authenticate(ctx, nil, func(err error) {}); err != nil {
				a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "offline", textfmt.CleanError(err), true })
				return
			}
		} else {
			a.set(m, func(s *AccountState) { s.Status, s.NeedsLogin = "offline", true })
			return
		}
		if a.Dir != "" {
			if err := m.saveMXAccount(a.Dir, mb); err != nil {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}
		}
		mb.StartSync(ctx)
		a.set(m, func(s *AccountState) {
			s.Backend, s.Ctx, s.Status, s.Notice = mb, ctx, "online", ""
		})
		<-ctx.Done()
		a.set(m, func(s *AccountState) { s.Backend, s.Ctx, s.Status = nil, nil, "offline" })
	}()
}

func (m *AccountManager) Close() {
	for _, a := range m.Accounts {
		if a.Cancel != nil {
			a.Cancel()
		}
	}
	for _, a := range m.Accounts {
		if a.Done != nil {
			<-a.Done
		}
	}
}

func (m *AccountManager) startMattermost(parent context.Context, a *Account, prompt config.PromptFunc) {
	if a.Done != nil {
		select {
		case <-a.Done:
		default:
			return
		}
	}
	mb := a.mmBackend
	ctx, cancel := context.WithCancel(parent)
	a.Cancel, a.Done = cancel, make(chan struct{})
	a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "connecting", "", false })
	go func() {
		defer close(a.Done)
		defer m.notify()
		if prompt != nil {
			if err := mb.Authenticate(ctx, prompt, func(err error) {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}); err != nil {
				a.set(m, func(s *AccountState) {
					s.Status, s.Notice = "offline", textfmt.CleanError(err)
					if !errors.Is(err, context.Canceled) {
						s.NeedsLogin = true
					}
				})
				return
			}
		} else if mb.Token != "" {
			if err := mb.Authenticate(ctx, nil, func(err error) {}); err != nil {
				a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "offline", textfmt.CleanError(err), true })
				return
			}
		} else {
			a.set(m, func(s *AccountState) { s.Status, s.NeedsLogin = "offline", true })
			return
		}
		if a.Dir != "" {
			if err := m.saveMMAccount(a.Dir, mb); err != nil {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}
		}
		mb.StartWebSocket(ctx)
		a.set(m, func(s *AccountState) {
			s.Backend, s.Ctx, s.Status, s.Notice = mb, ctx, "online", ""
		})
		<-ctx.Done()
		a.set(m, func(s *AccountState) { s.Backend, s.Ctx, s.Status = nil, nil, "offline" })
	}()
}

func (m *AccountManager) startWhatsApp(parent context.Context, a *Account, prompt config.PromptFunc) {
	if a.Done != nil {
		select {
		case <-a.Done:
		default:
			return
		}
	}
	wb := a.waBackend
	ctx, cancel := context.WithCancel(parent)
	a.Cancel, a.Done = cancel, make(chan struct{})
	a.set(m, func(s *AccountState) { s.Status, s.Notice, s.NeedsLogin = "connecting", "", false })
	go func() {
		defer close(a.Done)
		defer m.notify()
		if err := wb.Authenticate(ctx, prompt, func(err error) {
			a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
		}); err != nil {
			a.set(m, func(s *AccountState) {
				s.Status, s.Notice = "offline", textfmt.CleanError(err)
				if !errors.Is(err, context.Canceled) {
					s.NeedsLogin = true
				}
			})
			return
		}
		if a.Dir != "" {
			if err := m.saveIdentity(a.Dir, waAccountIdentity{JID: wb.Jid, PushName: wb.PushName}); err != nil {
				a.set(m, func(s *AccountState) { s.Notice = textfmt.CleanError(err) })
			}
		}
		a.set(m, func(s *AccountState) {
			s.Backend, s.Ctx, s.Status, s.Notice = wb, ctx, "online", ""
		})
		<-ctx.Done()
		if wb.Client != nil {
			wb.Client.Disconnect()
		}
		a.set(m, func(s *AccountState) { s.Backend, s.Ctx, s.Status = nil, nil, "offline" })
	}()
}

type LoginPrompt struct {
	Label  string
	Masked bool
	Answer chan LoginAnswer
}

// LoginAnswer is what the user typed at a login prompt, or why they did not.
type LoginAnswer struct {
	Text string
	Err  error
}
