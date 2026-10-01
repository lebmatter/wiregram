package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type credStore interface {
	Set(key, value string) error
	Get(key string) (string, error)
	Delete(key string) error
}

// osKeyring stores secrets in the OS credential manager.
// Linux: secret-tool (libsecret/GNOME Keyring).
// macOS: security (Keychain).
type osKeyring struct{}

func newOSKeyring() (*osKeyring, error) {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return nil, fmt.Errorf("secret-tool not found — install libsecret-tools or run with --insecure")
		}
	case "darwin":
		if _, err := exec.LookPath("security"); err != nil {
			return nil, fmt.Errorf("security command not found")
		}
	default:
		return nil, fmt.Errorf("OS keyring not supported on %s — run with --insecure", runtime.GOOS)
	}
	return &osKeyring{}, nil
}

func (k *osKeyring) Set(key, value string) error {
	switch runtime.GOOS {
	case "linux":
		cmd := exec.Command("secret-tool", "store", "--label", "wiregram: "+key, "service", "wiregram", "account", key)
		cmd.Stdin = strings.NewReader(value)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("keyring store: %s", strings.TrimSpace(string(out)))
		}
	case "darwin":
		cmd := exec.Command("security", "add-generic-password", "-s", "wiregram", "-a", key, "-w", value, "-U")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("keyring store: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (k *osKeyring) Get(key string) (string, error) {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("secret-tool", "lookup", "service", "wiregram", "account", key).Output()
		if err != nil {
			return "", fmt.Errorf("keyring lookup: %w", err)
		}
		return strings.TrimRight(string(out), "\n"), nil
	case "darwin":
		out, err := exec.Command("security", "find-generic-password", "-s", "wiregram", "-a", key, "-w").Output()
		if err != nil {
			return "", fmt.Errorf("keyring lookup: %w", err)
		}
		return strings.TrimRight(string(out), "\n"), nil
	}
	return "", fmt.Errorf("unsupported OS")
}

func (k *osKeyring) Delete(key string) error {
	switch runtime.GOOS {
	case "linux":
		exec.Command("secret-tool", "clear", "service", "wiregram", "account", key).Run()
	case "darwin":
		exec.Command("security", "delete-generic-password", "-s", "wiregram", "-a", key).Run()
	}
	return nil
}

// insecureStore is a no-op store; credentials stay in plain JSON files.
type insecureStore struct{}

func (s *insecureStore) Set(string, string) error   { return nil }
func (s *insecureStore) Get(string) (string, error) { return "", fmt.Errorf("no keyring") }
func (s *insecureStore) Delete(string) error        { return nil }
