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
	creds    config.CredStore
	Accounts []*Account // Owned exclusively by the UI after discovery.
	Agents   *agentRuntime
	Changed  chan struct{}
	// agentChanged mirrors Changed for the agent runtime, so the runtime and
	// the UI never take each other's signal.
	agentChanged chan struct{}
	connecting   chan struct{}
	mu           sync.Mutex
	owners       map[int64]*Account
}

func (m *AccountManager) notify() {
	for _, ch := range []chan struct{}{m.Changed, m.agentChanged} {
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
	if filepath.Clean(a.Dir) == filepath.Clean(m.Dir) {
		return agent.PrimaryAccountKey
	}
	return filepath.Base(a.Dir)
}

func (a *Account) set(m *AccountManager, update func(*AccountState)) {
	a.mu.Lock()
	update(&a.state)
	a.mu.Unlock()
	m.notify()
}

func LoadAccounts(cfg config.Config, dir string, creds config.CredStore) (*AccountManager, error) {
	m := &AccountManager{cfg: cfg, Dir: dir, creds: creds, Changed: make(chan struct{}, 1), agentChanged: make(chan struct{}, 1), connecting: make(chan struct{}, 4), owners: make(map[int64]*Account)}
	add := func(path string, requireIdentity bool) error {
		a := &Account{Dir: path, BackendType: "telegram", Histories: make(map[backend.ChatID][]string)}
		a.state.Status = "offline"
		data, err := os.ReadFile(filepath.Join(path, "account.json"))
		if err == nil {
			if err := json.Unmarshal(data, &a.state.Identity); err != nil {
				return fmt.Errorf("read account identity: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read account identity: %w", err)
		}
		if requireIdentity && a.state.Identity.ID == 0 {
			os.RemoveAll(path)
			return nil
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
	if _, err := os.Stat(filepath.Join(dir, "session", "session.json")); err == nil {
		if err := add(dir, false); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect original session: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "accounts"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	mmSeen := make(map[string]bool)
	mxSeen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "whatsapp-") {
			path := filepath.Join(dir, "accounts", entry.Name())
			data, err := os.ReadFile(filepath.Join(path, "account.json"))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("read whatsapp account: %w", err)
			}
			var identity waAccountIdentity
			if err := json.Unmarshal(data, &identity); err != nil {
				return nil, fmt.Errorf("parse whatsapp account: %w", err)
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
			data, err := os.ReadFile(filepath.Join(path, "account.json"))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("read matrix account: %w", err)
			}
			var identity mxAccountIdentity
			if err := json.Unmarshal(data, &identity); err != nil {
				return nil, fmt.Errorf("parse matrix account: %w", err)
			}
			mb := matrix.NewMXBackend(identity.Homeserver)
			mb.UserID = identity.UserID
			mb.DeviceID = identity.DeviceID
			mb.ConfigDir = path
			if tok, err := creds.Get(mxKeyringKey(identity.Homeserver, identity.UserID)); err == nil && tok != "" {
				mb.AccessToken = tok
			} else {
				mb.AccessToken = identity.Token
			}
			a := &Account{
				Dir:         path,
				BackendType: "matrix",
				mxBackend:   mb,
				Histories:   make(map[backend.ChatID][]string),
			}
			a.state.Status = "offline"
			a.state.NeedsLogin = mb.AccessToken == ""
			m.addAccount(a)
			mxSeen[identity.Homeserver+"\x00"+identity.UserID] = true
		} else if entry.IsDir() && strings.HasPrefix(entry.Name(), "mattermost-") {
			path := filepath.Join(dir, "accounts", entry.Name())
			data, err := os.ReadFile(filepath.Join(path, "account.json"))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("read mattermost account: %w", err)
			}
			var identity mmAccountIdentity
			if err := json.Unmarshal(data, &identity); err != nil {
				return nil, fmt.Errorf("parse mattermost account: %w", err)
			}
			mb := mattermost.NewMMBackend(identity.ServerURL)
			mb.UserID = identity.UserID
			mb.Username = identity.Username
			if tok, err := creds.Get(mmKeyringKey(identity.ServerURL)); err == nil && tok != "" {
				mb.Token = tok
			} else {
				mb.Token = identity.Token
			}
			a := &Account{
				Dir:         path,
				BackendType: "mattermost",
				mmBackend:   mb,
				Histories:   make(map[backend.ChatID][]string),
			}
			a.state.Status = "offline"
			a.state.NeedsLogin = mb.Token == ""
			m.addAccount(a)
			mmSeen[identity.ServerURL] = true
		} else if entry.IsDir() && strings.HasPrefix(entry.Name(), "account-") {
			if err := add(filepath.Join(dir, "accounts", entry.Name()), true); err != nil {
				return nil, err
			}
		}
	}
	for _, mms := range cfg.Mattermost {
		if mmSeen[mms.URL] {
			continue
		}
		mb := mattermost.NewMMBackend(mms.URL)
		if tok, err := creds.Get(mmKeyringKey(mms.URL)); err == nil && tok != "" {
			mb.Token = tok
		} else if mms.Token != "" {
			mb.Token = mms.Token
		}
		a := &Account{
			BackendType: "mattermost",
			mmBackend:   mb,
			Histories:   make(map[backend.ChatID][]string),
		}
		a.state.Status = "offline"
		a.state.NeedsLogin = mb.Token == ""
		m.addAccount(a)
	}
	for _, mxs := range cfg.Matrix {
		if mxSeen[mxs.Homeserver+"\x00"] {
			continue
		}
		mb := matrix.NewMXBackend(mxs.Homeserver)
		if tok, err := creds.Get(mxKeyringKey(mxs.Homeserver, "")); err == nil && tok != "" {
			mb.AccessToken = tok
		}
		a := &Account{
			BackendType: "matrix",
			mxBackend:   mb,
			Histories:   make(map[backend.ChatID][]string),
		}
		a.state.Status = "offline"
		a.state.NeedsLogin = mb.AccessToken == ""
		m.addAccount(a)
	}
	return m, nil
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
	mb.ConfigDir = dir
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

func (m *AccountManager) Discard(a *Account) {
	if a.Cancel != nil {
		a.Cancel()
	}
	if a.Done != nil {
		<-a.Done
	}
	if a.BackendType == "mattermost" && a.mmBackend != nil {
		m.creds.Delete(mmKeyringKey(a.mmBackend.ServerURL))
	}
	if a.BackendType == "matrix" && a.mxBackend != nil {
		m.creds.Delete(mxKeyringKey(a.mxBackend.Homeserver, a.mxBackend.UserID))
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
		os.RemoveAll(a.Dir)
	}
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

func mmKeyringKey(serverURL string) string { return "mm-token:" + serverURL }

type mxAccountIdentity struct {
	Homeserver string `json:"homeserver"`
	Token      string `json:"token,omitempty"`
	UserID     string `json:"user_id"`
	DeviceID   string `json:"device_id"`
}

func mxKeyringKey(homeserver, userID string) string { return "mx-token:" + homeserver + "/" + userID }

type waAccountIdentity struct {
	JID      types.JID `json:"jid"`
	PushName string    `json:"push_name"`
}

func saveMXAccount(dir string, mx *matrix.MxBackend, creds config.CredStore) error {
	if err := creds.Set(mxKeyringKey(mx.Homeserver, mx.UserID), mx.AccessToken); err == nil {
		return saveMXAccountJSON(dir, mxAccountIdentity{
			Homeserver: mx.Homeserver,
			UserID:     mx.UserID,
			DeviceID:   mx.DeviceID,
		})
	}
	return saveMXAccountJSON(dir, mxAccountIdentity{
		Homeserver: mx.Homeserver,
		Token:      mx.AccessToken,
		UserID:     mx.UserID,
		DeviceID:   mx.DeviceID,
	})
}

func saveMXAccountJSON(dir string, identity mxAccountIdentity) error {
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode matrix account: %w", err)
	}
	f, err := os.CreateTemp(dir, ".mx-account-*")
	if err != nil {
		return fmt.Errorf("create matrix account file: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write matrix account: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "account.json")); err != nil {
		return fmt.Errorf("save matrix account: %w", err)
	}
	return nil
}

func saveWAAccount(dir string, wb *whatsapp.WaBackend) error {
	identity := waAccountIdentity{
		JID:      wb.Jid,
		PushName: wb.PushName,
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode whatsapp account: %w", err)
	}
	f, err := os.CreateTemp(dir, ".wa-account-*")
	if err != nil {
		return fmt.Errorf("create whatsapp account file: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write whatsapp account: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "account.json")); err != nil {
		return fmt.Errorf("save whatsapp account: %w", err)
	}
	return nil
}

func saveMMAccount(dir string, mb *mattermost.MmBackend, creds config.CredStore) error {
	if err := creds.Set(mmKeyringKey(mb.ServerURL), mb.Token); err == nil {
		// Token is in the keyring; keep it out of the JSON file.
		return saveMMAccountJSON(dir, mmAccountIdentity{
			ServerURL: mb.ServerURL,
			UserID:    mb.UserID,
			Username:  mb.Username,
		})
	}
	// Keyring unavailable (--insecure); fall back to plain JSON.
	return saveMMAccountJSON(dir, mmAccountIdentity{
		ServerURL: mb.ServerURL,
		Token:     mb.Token,
		UserID:    mb.UserID,
		Username:  mb.Username,
	})
}

func saveMMAccountJSON(dir string, identity mmAccountIdentity) error {
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mattermost account: %w", err)
	}
	f, err := os.CreateTemp(dir, ".mm-account-*")
	if err != nil {
		return fmt.Errorf("create mattermost account file: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write mattermost account: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "account.json")); err != nil {
		return fmt.Errorf("save mattermost account: %w", err)
	}
	return nil
}

func saveAccount(dir string, identity backend.AccountIdentity) error {
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode account identity: %w", err)
	}
	f, err := os.CreateTemp(dir, ".account-*")
	if err != nil {
		return fmt.Errorf("create account identity: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write account identity: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "account.json")); err != nil {
		return fmt.Errorf("save account identity: %w", err)
	}
	return nil
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
			c, err := telegram.NewClient(m.cfg, a.Dir, func(err error) {
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
					if err := saveAccount(a.Dir, identity); err != nil {
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
			if err := saveMXAccount(a.Dir, mb, m.creds); err != nil {
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
			if err := saveMMAccount(a.Dir, mb, m.creds); err != nil {
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
			if err := saveWAAccount(a.Dir, wb); err != nil {
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
