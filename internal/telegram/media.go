package telegram

import (
	"fmt"
	"strings"

	"github.com/gotd/td/tg"

	"wiregram/internal/backend"
	"wiregram/internal/textfmt"
)

func formatDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func formatMedia(media tg.MessageMediaClass) string {
	prefix := ""
	if label := expiringMediaLabel(media); label != "" {
		prefix = "[" + label + "] "
	}
	switch m := media.(type) {
	case nil, *tg.MessageMediaEmpty:
		return ""
	case *tg.MessageMediaPhoto:
		if p, ok := m.Photo.(*tg.Photo); ok {
			w, h, size := photoSize(p.Sizes)
			return prefix + fmt.Sprintf("[photo] %dx%d, %s", w, h, textfmt.FormatSize(size))
		}
		if prefix != "" {
			return prefix + "[photo opened]"
		}
		return "[photo]"
	case *tg.MessageMediaDocument:
		if d, ok := m.Document.(*tg.Document); ok {
			return prefix + formatDocument(d)
		}
		if prefix != "" {
			return prefix + "[media opened]"
		}
		return "[file]"
	case *tg.MessageMediaGeo:
		return formatLocation("location", m.Geo)
	case *tg.MessageMediaGeoLive:
		return formatLocation("live location", m.Geo)
	case *tg.MessageMediaContact:
		return fmt.Sprintf("[contact] %s, %s", strings.TrimSpace(m.FirstName+" "+m.LastName), m.PhoneNumber)
	case *tg.MessageMediaPoll:
		return fmt.Sprintf("[poll] \"%s\" (%d options)", m.Poll.Question, len(m.Poll.Answers))
	case *tg.MessageMediaVenue:
		return fmt.Sprintf("[venue] %s, %s", m.Title, m.Address)
	case *tg.MessageMediaGame:
		return "[game] " + m.Game.Title
	case *tg.MessageMediaWebPage:
		return "" // shown as a preview card instead
	case *tg.MessageMediaInvoice:
		return fmt.Sprintf("[invoice] %s, %s %s", m.Title, formatAmount(m.TotalAmount, m.Currency), m.Currency)
	default:
		return "[unsupported media]"
	}
}

// inlineImage reports whether media is a still image to draw in the chat.
// Expiring media is left alone so it is only opened on purpose.
func inlineImage(media tg.MessageMediaClass) bool {
	if expiringMediaLabel(media) != "" {
		return false
	}
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		_, ok := m.Photo.(*tg.Photo)
		return ok
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok || d.Size > backend.MaxInlineImage {
			return false
		}
		switch d.MimeType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			return true
		}
	}
	return false
}

// webPagePreview returns the link preview Telegram attached, or nil while it
// is still pending or has nothing to show.
func webPagePreview(media tg.MessageMediaClass) *backend.LinkPreview {
	m, ok := media.(*tg.MessageMediaWebPage)
	if !ok {
		return nil
	}
	page, ok := m.Webpage.(*tg.WebPage)
	if !ok || (page.Title == "" && page.Description == "") {
		return nil
	}
	site := page.SiteName
	if site == "" {
		site = page.DisplayURL
	}
	return &backend.LinkPreview{
		URL: page.URL, Site: site, Title: page.Title, Description: page.Description,
	}
}

func expiringMediaLabel(media tg.MessageMediaClass) string {
	var ttl int
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		ttl = m.TTLSeconds
	case *tg.MessageMediaDocument:
		ttl = m.TTLSeconds
	}
	if ttl == 0x7fffffff {
		return "view-once media"
	}
	if ttl != 0 {
		return "self-destructing media"
	}
	return ""
}

func photoSize(sizes []tg.PhotoSizeClass) (int, int, int64) {
	var width, height int
	var size int64
	for _, value := range sizes {
		var w, h int
		var bytes int64
		switch s := value.(type) {
		case *tg.PhotoSize:
			w, h, bytes = s.W, s.H, int64(s.Size)
		case *tg.PhotoCachedSize:
			w, h, bytes = s.W, s.H, int64(len(s.Bytes))
		case *tg.PhotoSizeProgressive:
			w, h = s.W, s.H
			for _, n := range s.Sizes {
				if int64(n) > bytes {
					bytes = int64(n)
				}
			}
		}
		if w*h > width*height || (w*h == width*height && bytes > size) {
			width, height, size = w, h, bytes
		}
	}
	return width, height, size
}

func formatDocument(d *tg.Document) string {
	var video *tg.DocumentAttributeVideo
	var audio *tg.DocumentAttributeAudio
	var sticker *tg.DocumentAttributeSticker
	var width, height int
	filename, animated := "unnamed", d.MimeType == "image/gif"
	for _, value := range d.Attributes {
		switch a := value.(type) {
		case *tg.DocumentAttributeVideo:
			video, width, height = a, a.W, a.H
		case *tg.DocumentAttributeAudio:
			audio = a
		case *tg.DocumentAttributeSticker:
			sticker = a
		case *tg.DocumentAttributeAnimated:
			animated = true
		case *tg.DocumentAttributeImageSize:
			width, height = a.W, a.H
		case *tg.DocumentAttributeFilename:
			filename = a.FileName
		}
	}
	switch {
	case sticker != nil:
		return "[sticker] " + sticker.Alt
	case video != nil && video.RoundMessage:
		return "[video note] " + formatDuration(int(video.Duration))
	case audio != nil && audio.Voice:
		return "[voice] " + formatDuration(audio.Duration)
	case animated:
		return fmt.Sprintf("[gif] %dx%d", width, height)
	case video != nil:
		return fmt.Sprintf("[video] %s, %dx%d, %s", formatDuration(int(video.Duration)), width, height, textfmt.FormatSize(d.Size))
	case audio != nil:
		return fmt.Sprintf("[audio] %s - %s, %s", audio.Performer, audio.Title, formatDuration(audio.Duration))
	default:
		return fmt.Sprintf("[file] %s (%s)", filename, textfmt.FormatSize(d.Size))
	}
}

func formatLocation(tag string, point tg.GeoPointClass) string {
	if p, ok := point.(*tg.GeoPoint); ok {
		return fmt.Sprintf("[%s] %g, %g", tag, p.Lat, p.Long)
	}
	return "[" + tag + "]"
}

func formatAmount(amount int64, currency string) string {
	digits := 2
	switch currency {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF", "XTR":
		digits = 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		digits = 3
	case "CLF", "UYW":
		digits = 4
	}
	if digits == 0 {
		return fmt.Sprint(amount)
	}
	divisor := int64(1)
	for i := 0; i < digits; i++ {
		divisor *= 10
	}
	fraction := amount % divisor
	if fraction < 0 {
		fraction = -fraction
	}
	prefix := ""
	if amount < 0 && amount > -divisor {
		prefix = "-"
	}
	return fmt.Sprintf("%s%d.%0*d", prefix, amount/divisor, digits, fraction)
}
