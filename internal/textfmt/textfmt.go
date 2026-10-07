package textfmt

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func Plain(s string) string {
	result := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			result = append(result, ' ')
		case r < 32 || (r >= 127 && r <= 159):
		default:
			result = append(result, r)
		}
	}
	return string(result)
}

func CleanError(err error) string { return strings.TrimSpace(Plain(err.Error())) }

func FormatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KB", b>>10)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func FormatSize(size int64) string {
	if size < 0 {
		size = 0
	}
	value := float64(size)
	units := []string{"B", "KB", "MB", "GB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

const replyExcerptRunes = 48

func ShortExcerpt(text string) string {
	text = strings.Join(strings.Fields(Plain(text)), " ")
	if text == "" {
		return "message"
	}
	runes := []rune(text)
	if len(runes) <= replyExcerptRunes {
		return text
	}
	return string(runes[:replyExcerptRunes-1]) + "…"
}

func AppendQuery(query []rune, key string) []rune {
	for len(key) > 0 {
		r, size := utf8.DecodeRuneInString(key)
		if r == utf8.RuneError && size == 1 {
			return query
		}
		if !unicode.IsControl(r) {
			query = append(query, r)
		}
		key = key[size:]
	}
	return query
}

// FirstURL returns the first http(s) link in text, without trailing
// punctuation, or "" when there is none.
func FirstURL(text string) string {
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
			link := strings.TrimRight(field, ".,;:!?)]'\"")
			if !strings.HasSuffix(link, "://") {
				return link
			}
		}
	}
	return ""
}
