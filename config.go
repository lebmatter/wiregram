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
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

type promptFunc func(string, bool) (string, error)

func loadConfig(prompt promptFunc, out io.Writer) (config, string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return config{}, "", fmt.Errorf("locate configuration directory: %w", err)
	}
	dir := filepath.Join(base, "mintg")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return config{}, "", fmt.Errorf("create configuration directory: %w", err)
	}
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var cfg config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, dir, fmt.Errorf("read configuration: %w", err)
		}
		if cfg.APIID <= 0 || strings.TrimSpace(cfg.APIHash) == "" {
			return cfg, dir, fmt.Errorf("configuration needs a positive API ID and an API hash")
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
		return config{APIID: id, APIHash: strings.TrimSpace(buildAPIHash)}, dir, nil
	}
	fmt.Fprintln(out, "mintg: no config found.\nGet your API credentials at https://my.telegram.org/apps")
	var cfg config
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
	return cfg, dir, nil
}
