package mail

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decodes non-UTF-8 bodies
)

// maxBody caps the text kept per mail; longer bodies are cut.
const maxBody = 256 << 10

// textPart is the part a mail's readable text comes from.
type textPart struct {
	Section  []int
	HTML     bool
	Encoding string
	Charset  string
}

// layout picks the readable text of a mail, plain text over HTML, and lists
// its attachments.
func layout(bs imap.BodyStructure) (*textPart, []part) {
	var plain, rich *textPart
	var parts []part
	bs.Walk(func(path []int, p imap.BodyStructure) bool {
		single, ok := p.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		disp := single.Disposition()
		attached := disp != nil && strings.EqualFold(disp.Value, "attachment")
		section := append([]int(nil), path...)
		switch mt := single.MediaType(); {
		case !attached && mt == "text/plain" && plain == nil:
			plain = &textPart{Section: section, Encoding: single.Encoding, Charset: single.Params["charset"]}
		case !attached && mt == "text/html" && rich == nil:
			rich = &textPart{Section: section, HTML: true, Encoding: single.Encoding,
				Charset: single.Params["charset"]}
		case !attached && single.ID != "":
			// Images embedded in an HTML body by Content-ID are not attachments.
		case single.Filename() != "" || attached || !strings.HasPrefix(mt, "text/"):
			name := single.Filename()
			if name == "" {
				name = "attachment"
			}
			parts = append(parts, part{Section: section, Name: name, Type: mt,
				Encoding: single.Encoding, Charset: single.Params["charset"],
				Size: approxDecodedSize(single)})
		}
		return true
	})
	if plain != nil {
		return plain, parts
	}
	return rich, parts
}

// approxDecodedSize undoes base64's growth so the label shows the file size.
func approxDecodedSize(p *imap.BodyStructureSinglePart) int64 {
	if strings.EqualFold(p.Encoding, "base64") {
		return int64(p.Size) * 3 / 4
	}
	return int64(p.Size)
}

// decodePart undoes a part's transfer encoding and, for text, its charset.
func decodePart(raw []byte, mediaType, encoding, charset string) ([]byte, error) {
	h := message.Header{}
	ct := mediaType
	if charset != "" {
		ct += "; charset=" + charset
	}
	h.Set("Content-Type", ct)
	if encoding != "" {
		h.Set("Content-Transfer-Encoding", encoding)
	}
	e, err := message.New(h, bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, fmt.Errorf("decode mail part: %w", err)
	}
	data, err := io.ReadAll(e.Body)
	// A body cut short by maxBody fails to decode at its end; keep the rest.
	if err != nil && len(data) == 0 {
		return nil, fmt.Errorf("decode mail part: %w", err)
	}
	return data, nil
}

// bodyText turns a fetched text part into the cached body.
func bodyText(raw []byte, t *textPart) string {
	mt := "text/plain"
	if t.HTML {
		mt = "text/html"
	}
	data, err := decodePart(raw, mt, t.Encoding, t.Charset)
	if err != nil {
		data = raw
	}
	text := string(data)
	if t.HTML {
		text = stripHTML(text)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(collapseBlank(text))
	if len(text) > maxBody {
		text = text[:maxBody] + "\n…"
	}
	return text
}

var (
	reDropped = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	reBreak   = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/h[1-6]|/ul|/ol|/blockquote)[^>]*>`)
	reItem    = regexp.MustCompile(`(?i)<li[^>]*>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces  = regexp.MustCompile(`[ \t\x{a0}]+`)
	reBlank   = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
)

// stripHTML reduces an HTML body to its text. Nothing it references, such
// as images, is ever fetched.
func stripHTML(s string) string {
	s = reDropped.ReplaceAllString(s, "")
	s = strings.NewReplacer("\r", "", "\n", " ").Replace(s)
	s = reBreak.ReplaceAllString(s, "\n")
	s = reItem.ReplaceAllString(s, "\n• ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
	}
	return strings.Join(lines, "\n")
}

// collapseBlank keeps at most one blank line between paragraphs.
func collapseBlank(s string) string {
	return reBlank.ReplaceAllString(s, "\n\n")
}
