package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wiregram/internal/agent"
	"wiregram/internal/config"
	"wiregram/internal/store"
)

// ImportLegacy moves the files older versions kept in the configuration
// directory into wiregram.db. Each file is renamed to *.bak once imported,
// so this runs once and the user decides when to delete the backups.
func ImportLegacy(db *store.DB, dir string) error {
	keys := map[string]string{dir: agent.PrimaryAccountKey}
	entries, err := os.ReadDir(filepath.Join(dir, "accounts"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("list accounts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			keys[filepath.Join(dir, "accounts", entry.Name())] = entry.Name()
		}
	}
	for path, key := range keys {
		files := map[string]string{
			"account.json":                           store.Identity,
			filepath.Join("session", "session.json"): store.TelegramSession,
			"sync_token":                             store.SyncToken,
		}
		for file, name := range files {
			if err := importFile(db, filepath.Join(path, file), key, name); err != nil {
				return err
			}
		}
	}
	if err := importConfig(db, dir); err != nil {
		return err
	}
	return dropAccountDirs(dir)
}

// dropAccountDirs removes the account directories older versions made for
// every account; only WhatsApp keeps one, for whatsmeow's database.
func dropAccountDirs(dir string) error {
	root := filepath.Join(dir, "accounts")
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("list accounts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), "whatsapp-") {
			// A directory still holding imported *.bak files is left for the user.
			os.Remove(filepath.Join(root, entry.Name()))
		}
	}
	os.Remove(root)
	return nil
}

func importFile(db *store.DB, path, account, name string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	// A value already in the database is newer than the file.
	if _, err := db.Get(account, name); errors.Is(err, store.ErrNotFound) {
		if err := db.Put(account, name, data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return retire(path)
}

func retire(path string) error {
	if err := os.Rename(path, path+".bak"); err != nil {
		return fmt.Errorf("keep imported %s as a backup: %w", filepath.Base(path), err)
	}
	return nil
}

// importConfig turns config.json into settings, and its Mattermost and
// Matrix server lists into accounts awaiting sign-in.
func importConfig(db *store.DB, dir string) error {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if _, err := db.Setting("api_id"); err == nil {
		return retire(path)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	legacy := struct {
		APIID      int    `json:"api_id"`
		APIHash    string `json:"api_hash"`
		Mattermost []struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"mattermost"`
		Matrix []struct {
			Homeserver string `json:"homeserver"`
		} `json:"matrix"`
	}{}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	for _, s := range legacy.Mattermost {
		if err := importServer(db, "mattermost-", mmAccountIdentity{ServerURL: s.URL, Token: s.Token},
			func(raw []byte) bool {
				var id mmAccountIdentity
				return json.Unmarshal(raw, &id) == nil && id.ServerURL == s.URL
			}); err != nil {
			return err
		}
	}
	for _, s := range legacy.Matrix {
		if err := importServer(db, "matrix-", mxAccountIdentity{Homeserver: s.Homeserver},
			func(raw []byte) bool {
				var id mxAccountIdentity
				return json.Unmarshal(raw, &id) == nil && id.Homeserver == s.Homeserver && id.UserID == ""
			}); err != nil {
			return err
		}
	}
	cfg := config.Config{APIID: max(legacy.APIID, 0), APIHash: strings.TrimSpace(legacy.APIHash)}
	if err := config.Save(db, cfg); err != nil {
		return err
	}
	return retire(path)
}

// importServer creates an account for a configured server unless one with
// the prefix already matches it.
func importServer(db *store.DB, prefix string, identity any, matches func([]byte) bool) error {
	keys, err := db.AccountKeys()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		raw, err := db.Get(key, store.Identity)
		if err == nil && matches(raw) {
			return nil
		}
	}
	key, err := db.NewAccountKey(prefix)
	if err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("encode account identity: %w", err)
	}
	return db.Put(key, store.Identity, data)
}
