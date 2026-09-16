package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

type emojiOption struct {
	value string
	name  string
}

func emojiCatalog() []emojiOption {
	return []emojiOption{
		{"😀", "grinning face happy"}, {"😃", "smiling face happy"},
		{"😄", "smile happy laugh"}, {"😁", "beaming face grin"},
		{"😂", "tears of joy laugh"}, {"🤣", "rolling laughing"},
		{"😊", "smiling blush"}, {"🙂", "slightly smiling"},
		{"😉", "wink"}, {"😍", "heart eyes love"},
		{"🥰", "smiling hearts love"}, {"😘", "kiss"},
		{"😋", "delicious yum"}, {"😎", "sunglasses cool"},
		{"🤔", "thinking"}, {"🤗", "hug"},
		{"🤭", "hand over mouth"}, {"😐", "neutral face"},
		{"🙄", "rolling eyes"}, {"😴", "sleeping"},
		{"😢", "cry sad"}, {"😭", "loud crying sad"},
		{"😡", "angry"}, {"🤯", "mind blown shocked"},
		{"🥳", "party celebration"}, {"😇", "angel innocent"},
		{"🤡", "clown"}, {"💀", "skull dead"},
		{"👍", "thumbs up yes like"}, {"👎", "thumbs down no dislike"},
		{"👏", "clap applause"}, {"🙌", "raised hands celebrate"},
		{"🙏", "folded hands please thanks prayer"}, {"🤝", "handshake"},
		{"👋", "wave hello goodbye"}, {"👌", "ok hand"},
		{"✌️", "victory peace"}, {"🤞", "fingers crossed luck"},
		{"💪", "strong muscle"}, {"❤️", "red heart love"},
		{"🧡", "orange heart"}, {"💛", "yellow heart"},
		{"💚", "green heart"}, {"💙", "blue heart"},
		{"💜", "purple heart"}, {"🖤", "black heart"},
		{"💔", "broken heart"}, {"💕", "two hearts love"},
		{"🔥", "fire hot"}, {"✨", "sparkles"},
		{"🎉", "party popper celebration"}, {"🎂", "birthday cake"},
		{"🎁", "gift present"}, {"🌟", "star glowing"},
		{"✅", "check mark done yes"}, {"❌", "cross mark no"},
		{"⚠️", "warning"}, {"💯", "hundred perfect"},
		{"🚀", "rocket launch"}, {"☕", "coffee"},
		{"🍻", "beer cheers"}, {"🌹", "rose flower"},
		{"🐶", "dog"}, {"🐱", "cat"},
	}
}

func reactionCatalog() []emojiOption {
	return []emojiOption{
		{"❤", "heart love"}, {"👍", "thumbs up yes like"},
		{"👎", "thumbs down no dislike"}, {"🔥", "fire hot"},
		{"🥰", "smiling hearts love"}, {"👏", "clap applause"},
		{"😁", "beaming face grin"}, {"🤔", "thinking"},
		{"🤯", "mind blown shocked"}, {"😢", "cry sad"},
		{"🎉", "party celebration"}, {"🙏", "folded hands please thanks prayer"},
		{"👌", "ok hand"}, {"🤡", "clown"},
		{"💯", "hundred perfect"}, {"🤣", "rolling laughing"},
		{"💔", "broken heart"}, {"😐", "neutral face"},
		{"😭", "loud crying sad"}, {"😴", "sleeping"},
		{"😍", "heart eyes love"}, {"🤝", "handshake"},
		{"😇", "angel innocent"}, {"😡", "angry"},
	}
}

func (c *client) recentEmoji() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.emojiRecent...)
}

func (c *client) rememberEmoji(value string) {
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

func (c *client) choicesForEmojiCatalog(catalog []emojiOption, query string) []emojiOption {
	query = strings.ToLower(strings.TrimSpace(query))
	if query != "" {
		var matches []emojiOption
		for _, option := range catalog {
			if strings.Contains(option.name, query) || strings.Contains(option.value, query) {
				matches = append(matches, option)
			}
		}
		return matches
	}
	byValue := make(map[string]emojiOption, len(catalog))
	for _, option := range catalog {
		byValue[option.value] = option
	}
	choices := make([]emojiOption, 0, len(catalog))
	seen := make(map[string]bool)
	for _, value := range c.recentEmoji() {
		if option, ok := byValue[value]; ok {
			choices = append(choices, option)
			seen[value] = true
		}
	}
	for _, option := range catalog {
		if !seen[option.value] {
			choices = append(choices, option)
		}
	}
	return choices
}

func (c *client) emojiChoices(query string) []emojiOption {
	return c.choicesForEmojiCatalog(emojiCatalog(), query)
}

func (c *client) reactionChoices(query string) []emojiOption {
	return c.choicesForEmojiCatalog(reactionCatalog(), query)
}

func emojiPickerLines(choices []emojiOption, selected int, hasRecent, reactionMode bool) []string {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 20 || rows < 8 {
		cols, rows = 80, 24
	}
	limit := max(1, rows-4)
	start := 0
	if selected >= limit {
		start = selected - limit + 1
	}
	end := min(len(choices), start+limit)
	heading := "Emoji for draft — type to filter; Tab: react"
	if reactionMode {
		heading = "React — type to filter; Tab: insert into draft"
	}
	if hasRecent {
		heading += "; recent first"
	}
	lines := []string{heading, ""}
	for index := start; index < end; index++ {
		marker := "  "
		if index == selected {
			marker = "> "
		}
		lines = append(lines, clipLine(fmt.Sprintf("%s%s  %s", marker, choices[index].value, choices[index].name), cols-1))
	}
	if len(choices) == 0 {
		lines = append(lines, "  No matching emoji")
	}
	return lines
}
