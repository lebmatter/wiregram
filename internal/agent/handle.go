package agent

import (
	"fmt"
	"strings"
)

// Handle limits fit the usernames Telegram and Mattermost both accept, so a
// handle can be registered on either later.
const (
	handleMin = 3
	handleMax = 32
)

// ValidHandle checks a handle: a lowercase letter, then letters, digits and _.
func ValidHandle(h string) error {
	if len(h) < handleMin || len(h) > handleMax {
		return fmt.Errorf("A handle has %d to %d characters.", handleMin, handleMax)
	}
	for i, r := range h {
		ok := r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '_')
		if !ok {
			return fmt.Errorf("Start with a letter; use a-z, 0-9 and _ only.")
		}
	}
	return nil
}

// SuggestHandle makes a valid handle from a name: "Ava Helper" → "ava_helper".
func SuggestHandle(name string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if gap && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			gap = false
		default:
			gap = true
		}
	}
	h := b.String()
	switch {
	case h == "":
		return "bot"
	case h[0] < 'a' || h[0] > 'z':
		h = "bot_" + h
	case len(h) < handleMin:
		h += "_bot"
	}
	return strings.TrimRight(h[:min(len(h), handleMax)], "_")
}
