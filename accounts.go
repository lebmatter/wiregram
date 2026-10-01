package main

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
	"golang.org/x/term"
)

type accountIdentity struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

type accountState struct {
	identity   accountIdentity
	status     string
	notice     string
	client     *client
	backend    backend
	ctx        context.Context
	needsLogin bool
}

type account struct {
	dir         string
	backendType string // "telegram", "mattermost", "matrix", or "whatsapp"
	mmBackend   *mmBackend
	mxBackend   *mxBackend
	waBackend   *waBackend
	mu          sync.Mutex
	state       accountState
	cancel      context.CancelFunc
	done        chan struct{}
	histories   map[chatID][]string // Owned exclusively by the UI.
}

func (a *account) snapshot() accountState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *account) label() string {
	if a.backendType == "mattermost" && a.mmBackend != nil {
		if a.mmBackend.username != "" {
			return a.mmBackend.AccountLabel()
		}
		return "[MM] awaiting sign-in"
	}
	if a.backendType == "matrix" && a.mxBackend != nil {
		if a.mxBackend.userID != "" {
			return a.mxBackend.AccountLabel()
		}
		return "[MX] awaiting sign-in"
	}
	if a.backendType == "whatsapp" && a.waBackend != nil {
		return a.waBackend.AccountLabel()
	}
	s := a.snapshot()
	if s.identity.ID == 0 {
		return "[TG] awaiting sign-in"
	}
	return fmt.Sprintf("[TG] %s (+%s)", plain(s.identity.Name), strings.TrimPrefix(plain(s.identity.Phone), "+"))
}

type accountManager struct {
	cfg        config
	dir        string
	creds      credStore
	accounts   []*account // Owned exclusively by the UI after discovery.
	changed    chan struct{}
	connecting chan struct{}
	mu         sync.Mutex
	owners     map[int64]*account
}

func (m *accountManager) notify() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (a *account) set(m *accountManager, update func(*accountState)) {
	a.mu.Lock()
	update(&a.state)
	a.mu.Unlock()
	m.notify()
}

func loadAccounts(cfg config, dir string, creds credStore) (*accountManager, error) {
	m := &accountManager{cfg: cfg, dir: dir, creds: creds, changed: make(chan struct{}, 1), connecting: make(chan struct{}, 4), owners: make(map[int64]*account)}
	add := func(path string, requireIdentity bool) error {
		a := &account{dir: path, backendType: "telegram", histories: make(map[chatID][]string)}
		a.state.status = "offline"
		data, err := os.ReadFile(filepath.Join(path, "account.json"))
		if err == nil {
			if err := json.Unmarshal(data, &a.state.identity); err != nil {
				return fmt.Errorf("read account identity: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read account identity: %w", err)
		}
		if requireIdentity && a.state.identity.ID == 0 {
			os.RemoveAll(path)
			return nil
		}
		if a.state.identity.ID != 0 {
			if m.owners[a.state.identity.ID] != nil {
				return fmt.Errorf("duplicate saved account %d", a.state.identity.ID)
			}
			m.owners[a.state.identity.ID] = a
		}
		m.accounts = append(m.accounts, a)
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
			wb := newWABackend(path)
			wb.jid = identity.JID
			wb.pushName = identity.PushName
			a := &account{
				dir:         path,
				backendType: "whatsapp",
				waBackend:   wb,
				histories:   make(map[chatID][]string),
			}
			a.state.status = "offline"
			m.accounts = append(m.accounts, a)
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
			mb := newMXBackend(identity.Homeserver)
			mb.userID = identity.UserID
			mb.deviceID = identity.DeviceID
			mb.configDir = path
			if tok, err := creds.Get(mxKeyringKey(identity.Homeserver, identity.UserID)); err == nil && tok != "" {
				mb.accessToken = tok
			} else {
				mb.accessToken = identity.Token
			}
			a := &account{
				dir:         path,
				backendType: "matrix",
				mxBackend:   mb,
				histories:   make(map[chatID][]string),
			}
			a.state.status = "offline"
			a.state.needsLogin = mb.accessToken == ""
			m.accounts = append(m.accounts, a)
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
			mb := newMMBackend(identity.ServerURL)
			mb.userID = identity.UserID
			mb.username = identity.Username
			if tok, err := creds.Get(mmKeyringKey(identity.ServerURL)); err == nil && tok != "" {
				mb.token = tok
			} else {
				mb.token = identity.Token
			}
			a := &account{
				dir:         path,
				backendType: "mattermost",
				mmBackend:   mb,
				histories:   make(map[chatID][]string),
			}
			a.state.status = "offline"
			a.state.needsLogin = mb.token == ""
			m.accounts = append(m.accounts, a)
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
		mb := newMMBackend(mms.URL)
		if tok, err := creds.Get(mmKeyringKey(mms.URL)); err == nil && tok != "" {
			mb.token = tok
		} else if mms.Token != "" {
			mb.token = mms.Token
		}
		a := &account{
			backendType: "mattermost",
			mmBackend:   mb,
			histories:   make(map[chatID][]string),
		}
		a.state.status = "offline"
		a.state.needsLogin = mb.token == ""
		m.accounts = append(m.accounts, a)
	}
	for _, mxs := range cfg.Matrix {
		if mxSeen[mxs.Homeserver+"\x00"] {
			continue
		}
		mb := newMXBackend(mxs.Homeserver)
		if tok, err := creds.Get(mxKeyringKey(mxs.Homeserver, "")); err == nil && tok != "" {
			mb.accessToken = tok
		}
		a := &account{
			backendType: "matrix",
			mxBackend:   mb,
			histories:   make(map[chatID][]string),
		}
		a.state.status = "offline"
		a.state.needsLogin = mb.accessToken == ""
		m.accounts = append(m.accounts, a)
	}
	return m, nil
}

func (m *accountManager) addTelegram() (*account, error) {
	root := filepath.Join(m.dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "account-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	a := &account{dir: dir, backendType: "telegram", histories: make(map[chatID][]string)}
	a.state.status, a.state.needsLogin = "sign-in required", true
	m.accounts = append(m.accounts, a)
	return a, nil
}

func (m *accountManager) addMattermost(serverURL string) (*account, error) {
	root := filepath.Join(m.dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "mattermost-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	mb := newMMBackend(serverURL)
	a := &account{
		dir:         dir,
		backendType: "mattermost",
		mmBackend:   mb,
		histories:   make(map[chatID][]string),
	}
	a.state.status, a.state.needsLogin = "sign-in required", true
	m.accounts = append(m.accounts, a)
	return a, nil
}

func (m *accountManager) addMatrix(homeserver string) (*account, error) {
	root := filepath.Join(m.dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "matrix-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	mb := newMXBackend(homeserver)
	mb.configDir = dir
	a := &account{
		dir:         dir,
		backendType: "matrix",
		mxBackend:   mb,
		histories:   make(map[chatID][]string),
	}
	a.state.status, a.state.needsLogin = "sign-in required", true
	m.accounts = append(m.accounts, a)
	return a, nil
}

func (m *accountManager) addWhatsApp() (*account, error) {
	root := filepath.Join(m.dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create accounts directory: %w", err)
	}
	dir, err := os.MkdirTemp(root, "whatsapp-")
	if err != nil {
		return nil, fmt.Errorf("create account directory: %w", err)
	}
	wb := newWABackend(dir)
	a := &account{
		dir:         dir,
		backendType: "whatsapp",
		waBackend:   wb,
		histories:   make(map[chatID][]string),
	}
	a.state.status, a.state.needsLogin = "sign-in required", true
	m.accounts = append(m.accounts, a)
	return a, nil
}

func (m *accountManager) promptMatrix(ctx context.Context, t *terminal) (*account, error) {
	serverURL, err := t.prompt(ctx, "Matrix homeserver URL: ", false)
	if err != nil {
		return nil, err
	}
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, context.Canceled
	}
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		serverURL = "https://" + serverURL
	}
	return m.addMatrix(serverURL)
}

func (m *accountManager) discard(a *account) {
	if a.cancel != nil {
		a.cancel()
	}
	if a.done != nil {
		<-a.done
	}
	if a.backendType == "mattermost" && a.mmBackend != nil {
		m.creds.Delete(mmKeyringKey(a.mmBackend.serverURL))
	}
	if a.backendType == "matrix" && a.mxBackend != nil {
		m.creds.Delete(mxKeyringKey(a.mxBackend.homeserver, a.mxBackend.userID))
	}
	if a.backendType == "whatsapp" && a.waBackend != nil && a.waBackend.client != nil {
		a.waBackend.client.Disconnect()
	}
	for i, x := range m.accounts {
		if x == a {
			m.accounts = append(m.accounts[:i], m.accounts[i+1:]...)
			break
		}
	}
	if a.dir != "" {
		os.RemoveAll(a.dir)
	}
}

func (a *account) neverAuthenticated() bool {
	if a.backendType == "mattermost" {
		return a.mmBackend == nil || a.mmBackend.token == ""
	}
	if a.backendType == "matrix" {
		return a.mxBackend == nil || a.mxBackend.accessToken == ""
	}
	if a.backendType == "whatsapp" {
		return a.waBackend == nil || a.waBackend.jid.IsEmpty()
	}
	return a.snapshot().identity.ID == 0
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

func saveMXAccount(dir string, mx *mxBackend, creds credStore) error {
	if err := creds.Set(mxKeyringKey(mx.homeserver, mx.userID), mx.accessToken); err == nil {
		return saveMXAccountJSON(dir, mxAccountIdentity{
			Homeserver: mx.homeserver,
			UserID:     mx.userID,
			DeviceID:   mx.deviceID,
		})
	}
	return saveMXAccountJSON(dir, mxAccountIdentity{
		Homeserver: mx.homeserver,
		Token:      mx.accessToken,
		UserID:     mx.userID,
		DeviceID:   mx.deviceID,
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

func saveWAAccount(dir string, wb *waBackend) error {
	identity := waAccountIdentity{
		JID:      wb.jid,
		PushName: wb.pushName,
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

func saveMMAccount(dir string, mb *mmBackend, creds credStore) error {
	if err := creds.Set(mmKeyringKey(mb.serverURL), mb.token); err == nil {
		// Token is in the keyring; keep it out of the JSON file.
		return saveMMAccountJSON(dir, mmAccountIdentity{
			ServerURL: mb.serverURL,
			UserID:    mb.userID,
			Username:  mb.username,
		})
	}
	// Keyring unavailable (--insecure); fall back to plain JSON.
	return saveMMAccountJSON(dir, mmAccountIdentity{
		ServerURL: mb.serverURL,
		Token:     mb.token,
		UserID:    mb.userID,
		Username:  mb.username,
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

func (m *accountManager) pickAndAdd(ctx context.Context, t *terminal) (*account, error) {
	fmt.Fprint(t.out(), "\033[2J\033[HAdd account\n\n")
	fmt.Fprintln(t.out(), "  1  Telegram")
	fmt.Fprintln(t.out(), "  2  Mattermost")
	fmt.Fprintln(t.out(), "  3  Matrix")
	fmt.Fprintln(t.out(), "  4  WhatsApp")
	fmt.Fprintln(t.out(), "\nPress 1-4; Esc to cancel.")
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case key, ok := <-t.keys:
			if !ok {
				return nil, context.Canceled
			}
			switch key {
			case "1":
				return m.addTelegram()
			case "2":
				return m.promptMattermost(ctx, t)
			case "3":
				return m.promptMatrix(ctx, t)
			case "4":
				return m.addWhatsApp()
			case "\x1b", "\x03", "\x04":
				return nil, context.Canceled
			}
		}
	}
}

func (m *accountManager) promptMattermost(ctx context.Context, t *terminal) (*account, error) {
	serverURL, err := t.prompt(ctx, "Mattermost server URL: ", false)
	if err != nil {
		return nil, err
	}
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, context.Canceled
	}
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		serverURL = "https://" + serverURL
	}
	return m.addMattermost(serverURL)
}

func saveAccount(dir string, identity accountIdentity) error {
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
func (m *accountManager) start(parent context.Context, a *account, prompt promptFunc) {
	if a.backendType == "mattermost" {
		m.startMattermost(parent, a, prompt)
		return
	}
	if a.backendType == "matrix" {
		m.startMatrix(parent, a, prompt)
		return
	}
	if a.backendType == "whatsapp" {
		m.startWhatsApp(parent, a, prompt)
		return
	}
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return
		}
	}
	ctx, cancel := context.WithCancel(parent)
	a.cancel, a.done = cancel, make(chan struct{})
	a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "connecting", "", false })
	go func() {
		defer close(a.done)
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
			c, err := newClient(m.cfg, a.dir, func(err error) {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			})
			if err == nil {
				err = c.telegram.Run(attemptCtx, func(connectionCtx context.Context) error {
					if prompt != nil {
						if err := c.authenticate(connectionCtx, prompt, c.report); err != nil {
							return err
						}
					} else {
						status, err := c.telegram.Auth().Status(connectionCtx)
						if err != nil {
							return fmt.Errorf("check account authentication: %w", err)
						}
						if !status.Authorized {
							a.set(m, func(s *accountState) { s.needsLogin = true })
							return fmt.Errorf("sign in to this account again")
						}
					}
					self, err := c.telegram.Self(connectionCtx)
					if err != nil {
						return fmt.Errorf("load account: %w", err)
					}
					identity := accountIdentity{ID: self.ID, Name: userName(self), Phone: self.Phone}
					m.mu.Lock()
					owner := m.owners[self.ID]
					previous := a.snapshot().identity.ID
					if (owner != nil && owner != a) || (previous != 0 && previous != self.ID) {
						m.mu.Unlock()
						return fmt.Errorf("this number already belongs to another account or differs from this saved account")
					}
					m.owners[self.ID] = a
					m.mu.Unlock()
					if err := saveAccount(a.dir, identity); err != nil {
						return err
					}
					prompt = nil // Reconnects must never read from an abandoned login UI.
					c.accountID = identity.ID
					c.accountName = fmt.Sprintf("%s (+%s)", plain(identity.Name), strings.TrimPrefix(plain(identity.Phone), "+"))
					c.remember([]tg.UserClass{self}, nil)
					return c.updates.Run(connectionCtx, c.api, self.ID, updates.AuthOptions{OnStart: func(liveCtx context.Context) {
						started = true
						if startupTimer != nil {
							startupTimer.Stop()
						}
						delay = time.Second
						a.set(m, func(s *accountState) {
							s.identity, s.client, s.backend, s.ctx, s.status, s.notice = identity, c, &telegramBackend{c: c}, liveCtx, "online", ""
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
			a.set(m, func(s *accountState) {
				s.client, s.backend, s.ctx, s.status = nil, nil, nil, "offline"
				if err != nil {
					s.notice = cleanError(err)
				}
			})
			if ctx.Err() != nil || err == nil || (!networkError(err) && !timedOut) {
				return
			}
			a.set(m, func(s *accountState) { s.status = "reconnecting" })
			if waitFor(ctx, delay+time.Duration(rand.Int64N(int64(delay/2)+1))) != nil {
				return
			}
			delay = min(delay*2, time.Minute)
		}
	}()
}

func (m *accountManager) startMatrix(parent context.Context, a *account, prompt promptFunc) {
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return
		}
	}
	mb := a.mxBackend
	ctx, cancel := context.WithCancel(parent)
	a.cancel, a.done = cancel, make(chan struct{})
	a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "connecting", "", false })
	go func() {
		defer close(a.done)
		defer m.notify()
		if prompt != nil {
			if err := mb.Authenticate(ctx, prompt, func(err error) {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			}); err != nil {
				a.set(m, func(s *accountState) {
					s.status, s.notice = "offline", cleanError(err)
					if !errors.Is(err, context.Canceled) {
						s.needsLogin = true
					}
				})
				return
			}
		} else if mb.accessToken != "" {
			if err := mb.Authenticate(ctx, nil, func(err error) {}); err != nil {
				a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "offline", cleanError(err), true })
				return
			}
		} else {
			a.set(m, func(s *accountState) { s.status, s.needsLogin = "offline", true })
			return
		}
		if a.dir != "" {
			if err := saveMXAccount(a.dir, mb, m.creds); err != nil {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			}
		}
		mb.startSync(ctx)
		a.set(m, func(s *accountState) {
			s.backend, s.ctx, s.status, s.notice = mb, ctx, "online", ""
		})
		<-ctx.Done()
		a.set(m, func(s *accountState) { s.backend, s.ctx, s.status = nil, nil, "offline" })
	}()
}

type accountsRequested struct{}

func (accountsRequested) Error() string { return "switch accounts" }

func (m *accountManager) run(ctx context.Context, t *terminal) error {
	defer m.close()
	var active *account
	for _, a := range m.accounts {
		m.start(ctx, a, nil)
	}
	if len(m.accounts) == 0 {
		var err error
		active, err = m.pickAndAdd(ctx, t)
		if err != nil {
			return err
		}
	}
	defer t.disableInactivity()
	for ctx.Err() == nil {
		if active == nil {
			t.disableInactivity()
			var err error
			active, err = m.choose(ctx, t, nil)
			if err != nil || active == nil {
				return err
			}
		}
		s := active.snapshot()
		needsAuth := s.needsLogin
		if active.backendType == "telegram" {
			needsAuth = needsAuth || s.identity.ID == 0
		}
		if needsAuth {
			t.disableInactivity()
			if err := m.login(ctx, t, active); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if active.neverAuthenticated() {
					m.discard(active)
				}
				if !errors.Is(err, context.Canceled) {
					t.report(err)
				}
				active = nil
				continue
			}
			s = active.snapshot()
		}
		if !needsAuth && (s.backend == nil || s.status != "online") {
			fmt.Fprintf(t.out(), "\033[2J\033[HConnecting to %s…\n", active.label())
			done := false
			for !done {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-active.done:
					done = true
				case <-m.changed:
					s = active.snapshot()
					if (s.backend != nil && s.status == "online") || s.needsLogin {
						done = true
					}
				case key, ok := <-t.keys:
					if !ok || key == "\x1b" || key == "\x03" || key == "\x04" {
						active = nil
						done = true
					}
				}
			}
			if active == nil {
				continue
			}
			s = active.snapshot()
		}
		if s.backend != nil && s.status == "online" {
			t.disableInactivity()
			err := runModes(s.ctx, s.backend, t, active.histories)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, switching := err.(accountsRequested); !switching && !errors.Is(err, context.Canceled) {
				t.report(err)
			}
		}
		var err error
		active, err = m.choose(ctx, t, active)
		if err != nil || active == nil {
			return err
		}
	}
	return ctx.Err()
}

func (m *accountManager) close() {
	for _, a := range m.accounts {
		if a.cancel != nil {
			a.cancel()
		}
	}
	for _, a := range m.accounts {
		if a.done != nil {
			<-a.done
		}
	}
}

func (m *accountManager) startMattermost(parent context.Context, a *account, prompt promptFunc) {
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return
		}
	}
	mb := a.mmBackend
	ctx, cancel := context.WithCancel(parent)
	a.cancel, a.done = cancel, make(chan struct{})
	a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "connecting", "", false })
	go func() {
		defer close(a.done)
		defer m.notify()
		if prompt != nil {
			if err := mb.Authenticate(ctx, prompt, func(err error) {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			}); err != nil {
				a.set(m, func(s *accountState) {
					s.status, s.notice = "offline", cleanError(err)
					if !errors.Is(err, context.Canceled) {
						s.needsLogin = true
					}
				})
				return
			}
		} else if mb.token != "" {
			if err := mb.Authenticate(ctx, nil, func(err error) {}); err != nil {
				a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "offline", cleanError(err), true })
				return
			}
		} else {
			a.set(m, func(s *accountState) { s.status, s.needsLogin = "offline", true })
			return
		}
		if a.dir != "" {
			if err := saveMMAccount(a.dir, mb, m.creds); err != nil {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			}
		}
		mb.startWebSocket(ctx)
		a.set(m, func(s *accountState) {
			s.backend, s.ctx, s.status, s.notice = mb, ctx, "online", ""
		})
		<-ctx.Done()
		a.set(m, func(s *accountState) { s.backend, s.ctx, s.status = nil, nil, "offline" })
	}()
}

func (m *accountManager) startWhatsApp(parent context.Context, a *account, prompt promptFunc) {
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return
		}
	}
	wb := a.waBackend
	ctx, cancel := context.WithCancel(parent)
	a.cancel, a.done = cancel, make(chan struct{})
	a.set(m, func(s *accountState) { s.status, s.notice, s.needsLogin = "connecting", "", false })
	go func() {
		defer close(a.done)
		defer m.notify()
		if err := wb.Authenticate(ctx, prompt, func(err error) {
			a.set(m, func(s *accountState) { s.notice = cleanError(err) })
		}); err != nil {
			a.set(m, func(s *accountState) {
				s.status, s.notice = "offline", cleanError(err)
				if !errors.Is(err, context.Canceled) {
					s.needsLogin = true
				}
			})
			return
		}
		if a.dir != "" {
			if err := saveWAAccount(a.dir, wb); err != nil {
				a.set(m, func(s *accountState) { s.notice = cleanError(err) })
			}
		}
		a.set(m, func(s *accountState) {
			s.backend, s.ctx, s.status, s.notice = wb, ctx, "online", ""
		})
		<-ctx.Done()
		if wb.client != nil {
			wb.client.Disconnect()
		}
		a.set(m, func(s *accountState) { s.backend, s.ctx, s.status = nil, nil, "offline" })
	}()
}

type loginPrompt struct {
	label  string
	masked bool
	answer chan lineResult
}

func (m *accountManager) login(ctx context.Context, t *terminal, a *account) error {
	if a.cancel != nil {
		a.cancel()
		<-a.done
	}
	requests := make(chan loginPrompt)
	loginCtx, cancel := context.WithCancel(ctx)
	// This context belongs to the running account after a successful login.
	prompt := func(label string, masked bool) (string, error) {
		if label == "Phone (international format): " {
			if phone := a.snapshot().identity.Phone; phone != "" {
				return "+" + strings.TrimPrefix(phone, "+"), nil
			}
		}
		r := loginPrompt{label, masked, make(chan lineResult, 1)}
		select {
		case requests <- r:
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
		select {
		case result := <-r.answer:
			return result.text, result.err
		case <-loginCtx.Done():
			return "", loginCtx.Err()
		}
	}
	fmt.Fprintf(t.out(), "\033[2J\033[HSign in — %s\n", a.label())
	m.start(loginCtx, a, prompt)
	stopAccount := a.cancel
	a.cancel = func() { cancel(); stopAccount() }
	for {
		if a.snapshot().status == "online" {
			return nil
		}
		select {
		case <-ctx.Done():
			cancel()
			return ctx.Err()
		case <-a.done:
			cancel()
			return fmt.Errorf("account sign-in: %s", a.snapshot().notice)
		case key, ok := <-t.keys:
			if !ok || key == "\x03" || key == "\x04" || key == "\x1b" {
				cancel()
				<-a.done
				return context.Canceled
			}
		case <-m.changed:
			if notice := a.snapshot().notice; notice != "" {
				t.report(fmt.Errorf("%s", notice))
			}
		case request := <-requests:
			value, err := t.prompt(loginCtx, request.label, request.masked)
			request.answer <- lineResult{text: value, err: err}
			if err != nil {
				cancel()
				<-a.done
				return err
			}
		}
	}
}

func (m *accountManager) choose(ctx context.Context, t *terminal, active *account) (*account, error) {
	selected := 0
	for i, a := range m.accounts {
		if a == active {
			selected = i
		}
	}
	render := func() {
		fmt.Fprint(t.out(), "\033[2J\033[HAccounts\n")
		_, rows, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || rows < 8 {
			rows = 24
		}
		limit := rows - 5
		start := max(0, selected-limit+1)
		for i := start; i < min(len(m.accounts)+2, start+limit); i++ {
			marker := " "
			if i == selected {
				marker = ">"
			}
			switch {
			case i == len(m.accounts):
				fmt.Fprintf(t.out(), "%s Add account…\n", marker)
			case i == len(m.accounts)+1:
				fmt.Fprintf(t.out(), "%s Quit\n", marker)
			default:
				a := m.accounts[i]
				s := a.snapshot()
				current := ""
				if a == active {
					current = " [current]"
				}
				fmt.Fprintf(t.out(), "%s %s — %s%s\n", marker, a.label(), s.status, current)
			}
		}
		if selected < len(m.accounts) {
			if notice := m.accounts[selected].snapshot().notice; notice != "" {
				fmt.Fprintln(t.out(), plain(notice))
			}
		}
		fmt.Fprintln(t.out(), "Enter: switch/sign in   r: reconnect   Esc: back")
	}
	render()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.changed:
			render()
		case key, ok := <-t.keys:
			if !ok {
				return nil, context.Canceled
			}
			switch key {
			case terminalLockKey:
				continue
			case terminalUnlockKey:
				render()
				continue
			case "q", "\x04":
				return nil, nil
			case "\x1b", "\x03":
				if active != nil && active.snapshot().status == "online" {
					return active, nil
				}
			case "k", "\x1b[A", "\x1bOA":
				selected = max(0, selected-1)
			case "j", "\x1b[B", "\x1bOB":
				selected = min(len(m.accounts)+1, selected+1)
			case "r":
				if selected < len(m.accounts) {
					m.start(ctx, m.accounts[selected], nil)
				}
			case "\r", "\n":
				if selected == len(m.accounts)+1 {
					return nil, nil
				}
				if selected == len(m.accounts) {
					a, err := m.pickAndAdd(ctx, t)
					if err != nil {
						if errors.Is(err, context.Canceled) {
							render()
							continue
						}
						return nil, err
					}
					if a == nil {
						render()
						continue
					}
					return a, nil
				}
				a := m.accounts[selected]
				if s := a.snapshot(); s.status == "online" || s.needsLogin || s.identity.ID == 0 {
					return a, nil
				}
				m.start(ctx, a, nil)
			}
			render()
		}
	}
}
