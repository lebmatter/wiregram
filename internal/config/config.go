package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wiregram/internal/store"
)

// Set by the Go linker for distributed builds; never changed at runtime.
var buildAPIID string
var buildAPIHash string
var buildGoogleClientID string
var buildGoogleClientSecret string

type Config struct {
	APIID    int
	APIHash  string
	AutoLock int
}

type PromptFunc func(string, bool) (string, error)

// GoogleClient returns the OAuth client embedded in the build for Google
// mail sign-in; ok is false when the build has none.
func GoogleClient() (id, secret string, ok bool) {
	return buildGoogleClientID, buildGoogleClientSecret, buildGoogleClientID != ""
}

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

// Wipe deletes the configuration directory dir and wiregram's cache
// directory, leaving the state of a fresh installation.
func Wipe(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete configuration directory: %w", err)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return nil // no cache directory, so nothing was cached
	}
	if err := os.RemoveAll(filepath.Join(base, "wiregram")); err != nil {
		return fmt.Errorf("delete cache directory: %w", err)
	}
	return nil
}

// LoadConfig reads the saved settings. Telegram API credentials come from the
// settings, else from the build, else stay empty until a Telegram account is added.
func LoadConfig(db *store.DB) (Config, error) {
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
	} else if !errors.Is(err, store.ErrNotFound) {
		return cfg, err
	}
	if cfg.APIID == 0 && (buildAPIID != "" || buildAPIHash != "") {
		id, err := strconv.Atoi(buildAPIID)
		if err != nil || id <= 0 || strings.TrimSpace(buildAPIHash) == "" {
			return cfg, fmt.Errorf("this build has incomplete or invalid Telegram API credentials; rebuild with both API ID and API hash")
		}
		cfg.APIID, cfg.APIHash = id, strings.TrimSpace(buildAPIHash)
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
