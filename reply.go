package main

import (
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
)

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

func (c *client) replySummary(message *tg.Message, entry chat) string {
	return c.messageSender(message, entry) + "> " + shortExcerpt(messageText(message))
}

func (c *client) replyLabel(message *tg.Message, entry chat, messages map[int]*tg.Message) string {
	value, ok := message.GetReplyTo()
	if !ok {
		return ""
	}
	header, ok := value.(*tg.MessageReplyHeader)
	if !ok {
		return "↪ story"
	}
	id, ok := header.GetReplyToMsgID()
	if !ok || id <= 0 {
		return ""
	}
	if original := messages[id]; original != nil {
		return "↪ " + c.replySummary(original, entry)
	}
	if quote, ok := header.GetQuoteText(); ok && quote != "" {
		return fmt.Sprintf("↪ message #%d: %s", id, shortExcerpt(quote))
	}
	return fmt.Sprintf("↪ message #%d", id)
}
