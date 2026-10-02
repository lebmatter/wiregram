package main

import "strings"

const replyExcerptRunes = 48

func shortExcerpt(text string) string {
	text = strings.Join(strings.Fields(plain(text)), " ")
	if text == "" {
		return "message"
	}
	runes := []rune(text)
	if len(runes) <= replyExcerptRunes {
		return text
	}
	return string(runes[:replyExcerptRunes-1]) + "…"
}
