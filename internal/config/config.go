package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wiregram/internal/store"
)

// Set by the Go linker for distributed builds; never changed at runtime.
var buildAPIID string
var buildAPIHash string

type Config struct {
	APIID    int
	APIHash  string
	AutoLock int
}

type PromptFunc func(string, bool) (string, error)

// Dir returns the configuration directory, creating it if needed.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate configuration directory: %w", err)
	}
	dir := filepath.Join(base, "wiregram")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create configuration directory: %w", err)
	}
	return dir, nil
}

func LoadConfig(db *store.DB, prompt PromptFunc, out io.Writer) (Config, error) {
	cfg := Config{AutoLock: 1}
	id, err := db.Setting("api_id")
	if err == nil {
		// An API ID of 0 comes from configurations without Telegram.
		cfg.APIID, err = strconv.Atoi(id)
		if err != nil || cfg.APIID < 0 {
			return cfg, fmt.Errorf("configuration API ID must be a non-negative number")
		}
		if cfg.APIHash, err = db.Setting("api_hash"); err != nil && !errors.Is(err, store.ErrNotFound) {
			return cfg, err
		}
		if cfg.APIID > 0 && strings.TrimSpace(cfg.APIHash) == "" {
			return cfg, fmt.Errorf("configuration needs an API hash")
		}
		lock, err := db.Setting("autolock")
		if err == nil {
			cfg.AutoLock, err = strconv.Atoi(lock)
		} else if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
		if err != nil || (cfg.AutoLock != 0 && cfg.AutoLock != 1) {
			return cfg, fmt.Errorf("configuration autolock must be 0 or 1")
		}
		return cfg, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return cfg, err
	}
	if buildAPIID != "" || buildAPIHash != "" {
		id, err := strconv.Atoi(buildAPIID)
		if err != nil || id <= 0 || strings.TrimSpace(buildAPIHash) == "" {
			return cfg, fmt.Errorf("this build has incomplete or invalid Telegram API credentials; rebuild with both API ID and API hash")
		}
		cfg.APIID, cfg.APIHash = id, strings.TrimSpace(buildAPIHash)
		return cfg, nil
	}
	fmt.Fprintln(out, "wiregram: no config found.\nGet your API credentials at https://my.telegram.org/apps")
	for {
		value, err := prompt("Enter API ID: ", false)
		if err != nil {
			return cfg, fmt.Errorf("read API ID: %w", err)
		}
		cfg.APIID, err = strconv.Atoi(strings.TrimSpace(value))
		if err == nil && cfg.APIID > 0 {
			break
		}
		fmt.Fprintln(os.Stderr, "Enter a positive numeric API ID.")
	}
	for cfg.APIHash == "" {
		value, err := prompt("Enter API Hash: ", false)
		if err != nil {
			return cfg, fmt.Errorf("read API hash: %w", err)
		}
		cfg.APIHash = strings.TrimSpace(value)
	}
	if err := Save(db, cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Save writes every setting; the API ID goes last because its presence is
// what marks the configuration as complete.
func Save(db *store.DB, cfg Config) error {
	if err := db.SetSetting("api_hash", cfg.APIHash); err != nil {
		return err
	}
	if err := db.SetSetting("autolock", strconv.Itoa(cfg.AutoLock)); err != nil {
		return err
	}
	return db.SetSetting("api_id", strconv.Itoa(cfg.APIID))
}
