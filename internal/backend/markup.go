package backend

import "strings"

// Style is a set of text styles a Span applies.
type Style uint8

const (
	StyleBold Style = 1 << iota
	StyleItalic
	StyleStrike
	StyleCode
	StyleLink
)

// Span styles Text[Start:End], in bytes. A link's target is URL.
type Span struct {
	Start, End int
	Style      Style
	URL        string
}

// Markdown takes chat Markdown out of text and returns the plain text with
// spans for what it marked: emphasis, strikethrough, code, code blocks and
// links. Headings turn bold, quotes get a ┃ bar and list items a • bullet.
// Tables and anything else stay as written.
func Markdown(text string) (string, []Span) {
	var p markup
	fence, first := false, true
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		if !first {
			p.out.WriteByte('\n')
		}
		first = false
		if fence {
			start := p.out.Len()
			p.out.WriteString(line)
			p.add(start, StyleCode, "")
			continue
		}
		p.line(line)
	}
	return p.out.String(), p.spans
}

type markup struct {
	out   strings.Builder
	spans []Span
}

// add records a span from start to the end of the output so far.
func (p *markup) add(start int, style Style, url string) {
	if end := p.out.Len(); end > start {
		p.spans = append(p.spans, Span{Start: start, End: end, Style: style, URL: url})
	}
}

func (p *markup) line(line string) {
	rest := strings.TrimLeft(line, " ")
	p.out.WriteString(line[:len(line)-len(rest)])
	if n := strings.IndexFunc(rest, func(r rune) bool { return r != '#' }); n >= 1 && n <= 6 &&
		rest[n] == ' ' {
		start := p.out.Len()
		p.inline(strings.TrimSpace(rest[n:]))
		p.add(start, StyleBold, "")
		return
	}
	switch {
	case rest == ">" || strings.HasPrefix(rest, "> "):
		p.out.WriteString("┃ ")
		p.line(strings.TrimPrefix(rest[1:], " "))
	case len(rest) > 1 && strings.IndexByte("-*+", rest[0]) >= 0 && rest[1] == ' ':
		p.out.WriteString("• ")
		p.inline(rest[2:])
	default:
		p.inline(rest)
	}
}

const mdPunct = "\\`*_~[]()<>#+-.!|"

func (p *markup) inline(s string) {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte(mdPunct, s[i+1]) >= 0:
			p.out.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				start := p.out.Len()
				p.out.WriteString(s[i+1 : i+1+j])
				p.add(start, StyleCode, "")
				i += j + 2
				continue
			}
		case c == '[':
			if label, url, n := mdLink(s[i:]); n > 0 {
				start := p.out.Len()
				p.inline(label)
				p.add(start, StyleLink, url)
				i += n
				continue
			}
		case c == '<':
			if j := strings.IndexByte(s[i:], '>'); j > 0 && isWebURL(s[i+1:i+j]) {
				p.out.WriteString(s[i+1 : i+j])
				i += j + 1
				continue
			}
		}
		if mark, style := mdDelimiter(s[i:]); mark != "" {
			if j := mdClosing(s, i, mark); j > 0 {
				start := p.out.Len()
				p.inline(s[i+len(mark) : j])
				p.add(start, style, "")
				i = j + len(mark)
				continue
			}
		}
		p.out.WriteByte(c)
		i++
	}
}

// mdDelimiter is the emphasis mark s starts with, longest first.
func mdDelimiter(s string) (string, Style) {
	switch {
	case strings.HasPrefix(s, "**"), strings.HasPrefix(s, "__"):
		return s[:2], StyleBold
	case strings.HasPrefix(s, "~~"):
		return s[:2], StyleStrike
	case strings.HasPrefix(s, "*"), strings.HasPrefix(s, "_"):
		return s[:1], StyleItalic
	}
	return "", 0
}

// mdClosing finds the mark closing the one at s[i], or -1. The text between
// must not start or end with a space, and underscores inside a word, as in
// snake_case, are not emphasis.
func mdClosing(s string, i int, mark string) int {
	open := i + len(mark)
	if open >= len(s) || s[open] == ' ' {
		return -1
	}
	under := mark[0] == '_'
	if under && i > 0 && isWordByte(s[i-1]) {
		return -1
	}
	for k := open + 1; k < len(s); k++ {
		j := strings.Index(s[k:], mark)
		if j < 0 {
			return -1
		}
		j += k
		after := j + len(mark)
		switch {
		case s[j-1] == ' ':
		case len(mark) == 1 && after < len(s) && s[after] == mark[0]:
		case under && after < len(s) && isWordByte(s[after]):
		default:
			return j
		}
		k = j
	}
	return -1
}

// mdLink parses a [label](url) at the start of s and its length, or 0.
func mdLink(s string) (label, url string, n int) {
	k := strings.Index(s, "](")
	if k < 2 || strings.ContainsAny(s[1:k], "\n[") {
		return "", "", 0
	}
	m := strings.IndexByte(s[k+2:], ')')
	if m < 1 || strings.ContainsAny(s[k+2:k+2+m], " \n") {
		return "", "", 0
	}
	return s[1:k], s[k+2 : k+2+m], k + 3 + m
}

func isWebURL(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) &&
		!strings.ContainsAny(s, " \n")
}

func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
