// Package store owns wiregram.db: settings and per-account data live here
// beside the Chat Agent tables.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrNotFound reports a missing setting or account value.
var ErrNotFound = errors.New("not found")

// Names of the per-account values.
const (
	Identity        = "identity"
	TelegramSession = "telegram_session"
	SyncToken       = "sync_token"
	DigestSkip      = "digest_skip"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_data (
	account TEXT NOT NULL,
	name    TEXT NOT NULL,
	value   BLOB NOT NULL,
	PRIMARY KEY (account, name)
);
`

type DB struct {
	db *sql.DB
}

func Open(dir string) (*DB, error) {
	path := filepath.Join(dir, "wiregram.db")
	// The database holds login sessions; SQLite gives its -wal and -shm files
	// the main file's mode, so tightening this one covers all three.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("protect database: %w", err)
	}
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create database tables: %w", err)
	}
	return &DB{db: db}, nil
}

// SQL exposes the handle for packages that keep their own tables.
func (d *DB) SQL() *sql.DB { return d.db }

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Setting(key string) (string, error) {
	var value string
	err := d.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}
	return value, nil
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}

func (d *DB) Get(account, name string) ([]byte, error) {
	var value []byte
	err := d.db.QueryRow(`SELECT value FROM account_data WHERE account = ? AND name = ?`,
		account, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read account %s: %w", name, err)
	}
	return value, nil
}

func (d *DB) Put(account, name string, value []byte) error {
	_, err := d.db.Exec(`INSERT INTO account_data (account, name, value) VALUES (?, ?, ?)
		ON CONFLICT (account, name) DO UPDATE SET value = excluded.value`, account, name, value)
	if err != nil {
		return fmt.Errorf("save account %s: %w", name, err)
	}
	return nil
}

// DeleteAccount forgets everything stored for one account.
func (d *DB) DeleteAccount(account string) error {
	if _, err := d.db.Exec(`DELETE FROM account_data WHERE account = ?`, account); err != nil {
		return fmt.Errorf("delete account data: %w", err)
	}
	return nil
}
