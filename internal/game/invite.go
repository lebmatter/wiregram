package game

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Expiry is how long a challenge waits for someone to join.
const Expiry = 10 * time.Minute

// InviteRe matches an invite link. Only quick tunnel hosts are accepted, so a
// crafted invite cannot make a click connect anywhere else.
var InviteRe = regexp.MustCompile(`wiregame://([a-z0-9-]+\.trycloudflare\.com)/([a-z2-7]{26})`)

// NewToken is the one-time secret that lets a guest join: 128 random bits.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make game token: %w", err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// InviteText is the chat message that challenges whoever clicks it.
func InviteText(game, host, token string) string {
	return fmt.Sprintf("🎮 %s challenge · wiregame://%s/%s", game, host, token)
}

// ParseInvite finds an invite link in text.
func ParseInvite(text string) (host, token string, ok bool) {
	m := InviteRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
