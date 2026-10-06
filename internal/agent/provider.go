package agent

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ValidateProviderURL requires HTTPS, except for providers on this machine.
func ValidateProviderURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("provider URL is not a valid address")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
		return nil
	}
	return fmt.Errorf("provider URL must use HTTPS (except for localhost)")
}

// KnowledgePath resolves a knowledge file name: ~/ is the home directory and
// relative names are taken from the config directory.
func KnowledgePath(configDir, name string) string {
	if rest, ok := strings.CutPrefix(name, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(configDir, name)
}
