package telegram

import (
	"strings"

	"wiregram/internal/backend"
)

func (c *Client) recentEmoji() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.emojiRecent...)
}

func (c *Client) rememberEmoji(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	recent := []string{value}
	for _, previous := range c.emojiRecent {
		if previous != value && len(recent) < 8 {
			recent = append(recent, previous)
		}
	}
	c.emojiRecent = recent
}

func (c *Client) choicesForEmojiCatalog(catalog []backend.EmojiOption, query string) []backend.EmojiOption {
	query = strings.ToLower(strings.TrimSpace(query))
	if query != "" {
		var matches []backend.EmojiOption
		for _, option := range catalog {
			if strings.Contains(option.Name, query) || strings.Contains(option.Value, query) {
				matches = append(matches, option)
			}
		}
		return matches
	}
	byValue := make(map[string]backend.EmojiOption, len(catalog))
	for _, option := range catalog {
		byValue[option.Value] = option
	}
	choices := make([]backend.EmojiOption, 0, len(catalog))
	seen := make(map[string]bool)
	for _, value := range c.recentEmoji() {
		if option, ok := byValue[value]; ok {
			choices = append(choices, option)
			seen[value] = true
		}
	}
	for _, option := range catalog {
		if !seen[option.Value] {
			choices = append(choices, option)
		}
	}
	return choices
}

func (c *Client) emojiChoices(query string) []backend.EmojiOption {
	return c.choicesForEmojiCatalog(backend.EmojiCatalog(), query)
}

func (c *Client) reactionChoices(query string) []backend.EmojiOption {
	return c.choicesForEmojiCatalog(backend.ReactionCatalog(), query)
}
