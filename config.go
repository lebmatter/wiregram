package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Set by the Go linker for distributed builds; never changed at runtime.
var buildAPIID string
var buildAPIHash string

type config struct {
	APIID      int              `json:"api_id"`
	APIHash    string           `json:"api_hash"`
	AutoLock   int              `json:"autolock"`
	AI         *aiConfig        `json:"ai,omitempty"`
	Mattermost []mmServerConfig `json:"mattermost,omitempty"`
}

type mmServerConfig struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

type promptFunc func(string, bool) (string, error)

func loadConfig(prompt promptFunc, out io.Writer) (config, string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return config{}, "", fmt.Errorf("locate configuration directory: %w", err)
	}
	dir := filepath.Join(base, "wiregram")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return config{}, "", fmt.Errorf("create configuration directory: %w", err)
	}
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err == nil {
		cfg := config{AutoLock: 1}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, dir, fmt.Errorf("read configuration: %w", err)
		}
		if (cfg.APIID <= 0 || strings.TrimSpace(cfg.APIHash) == "") && len(cfg.Mattermost) == 0 {
			return cfg, dir, fmt.Errorf("configuration needs a positive API ID and an API hash (or a mattermost section)")
		}
		if cfg.AutoLock != 0 && cfg.AutoLock != 1 {
			return cfg, dir, fmt.Errorf("configuration autolock must be 0 or 1")
		}
		if cfg.AI == nil {
			cfg.AI = newAIConfig(dir)
		}
		cfg.AI.dir = dir
		// A broken AI section must not stop ordinary messaging.
		if err := cfg.AI.normalize(); err != nil {
			fmt.Fprintf(out, "wiregram: AI disabled — %s\n", cleanError(err))
			cfg.AI = newAIConfig(dir)
		}
		return cfg, dir, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return config{}, dir, fmt.Errorf("open configuration: %w", err)
	}
	if buildAPIID != "" || buildAPIHash != "" {
		id, err := strconv.Atoi(buildAPIID)
		if err != nil || id <= 0 || strings.TrimSpace(buildAPIHash) == "" {
			return config{}, dir, fmt.Errorf("this build has incomplete or invalid Telegram API credentials; rebuild with both API ID and API hash")
		}
		return config{APIID: id, APIHash: strings.TrimSpace(buildAPIHash), AutoLock: 1, AI: newAIConfig(dir)}, dir, nil
	}
	fmt.Fprintln(out, "wiregram: no config found.\nGet your API credentials at https://my.telegram.org/apps")
	cfg := config{AutoLock: 1}
	for {
		value, err := prompt("Enter API ID: ", false)
		if err != nil {
			return cfg, dir, fmt.Errorf("read API ID: %w", err)
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
			return cfg, dir, fmt.Errorf("read API hash: %w", err)
		}
		cfg.APIHash = strings.TrimSpace(value)
	}
	data, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return cfg, dir, fmt.Errorf("encode configuration: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return cfg, dir, fmt.Errorf("create configuration: %w", err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return cfg, dir, fmt.Errorf("save configuration: %w", err)
	}
	cfg.AI = newAIConfig(dir)
	return cfg, dir, nil
}
