package textfmt

import (
	"strings"
	"unicode"
)

var nickColors = [...]string{
	"\033[31m", "\033[32m", "\033[33m", "\033[34m", "\033[35m", "\033[36m",
	"\033[91m", "\033[92m", "\033[93m", "\033[94m", "\033[95m", "\033[96m",
}

func NickColor(name string) string {
	h := uint32(0)
	for _, r := range name {
		h = h*31 + uint32(r)
	}
	return nickColors[h%uint32(len(nickColors))]
}

const NickWidth = 12

func FormatNick(name string) string {
	runes := []rune(name)
	if len(runes) > NickWidth {
		runes = runes[:NickWidth]
	}
	padded := string(runes)
	for len([]rune(padded)) < NickWidth {
		padded += " "
	}
	return padded
}

const MaxChatLineWidth = 120

// wrapMessage wraps body under a fixed-width prefix. Wrapping the prefix too
// would collapse the padding that keeps the nick column aligned.
func WrapMessage(prefix, body string, width, indent int) []string {
	pad := strings.Repeat(" ", indent)
	lines := WrapLineIndent(pad+body, width, indent)
	if indent < width-4 && strings.HasPrefix(lines[0], pad) {
		lines[0] = prefix + lines[0][indent:]
	} else {
		lines[0] = ClipLine(prefix+strings.TrimLeft(lines[0], " "), width)
	}
	return lines
}

func WrapLineIndent(text string, width int, indent int) []string {
	if width < 1 {
		return []string{""}
	}
	continuation := "  "
	if indent > 0 && indent < width-4 {
		continuation = strings.Repeat(" ", indent)
	} else if width < 4 {
		continuation = " "
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	leading := text[:len(text)-len(strings.TrimLeft(text, " \t\r\n"))]
	var lines []string
	line := leading
	used := DisplayWidth(leading)
	hasWord := false
	flush := func() {
		lines = append(lines, line)
		line = continuation
		used = DisplayWidth(continuation)
		hasWord = false
	}
	for _, word := range words {
		wordWidth := DisplayWidth(word)
		separator := 0
		if hasWord {
			separator = 1
		}
		if used+separator+wordWidth <= width {
			if separator != 0 {
				line += " "
				used++
			}
			line += word
			used += wordWidth
			hasWord = true
			continue
		}
		if hasWord {
			flush()
		}
		for word != "" {
			part, rest := takeCells(word, width-used)
			line += part
			used += DisplayWidth(part)
			hasWord = true
			word = rest
			if word != "" {
				flush()
			}
		}
	}
	if hasWord {
		lines = append(lines, line)
	}
	return lines
}

func DisplayWidth(text string) int {
	width := 0
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		width += runeWidth(r)
	}
	return width
}

func takeCells(text string, limit int) (string, string) {
	if limit < 1 {
		limit = 1
	}
	used, end := 0, 0
	inEscape := false
	for index, r := range text {
		if r == '\033' {
			inEscape = true
			end = index + len(string(r))
			continue
		}
		if inEscape {
			end = index + len(string(r))
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		cells := runeWidth(r)
		if used > 0 && used+cells > limit {
			break
		}
		used += cells
		end = index + len(string(r))
	}
	return text[:end], text[end:]
}

func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe6f || r >= 0xff01 && r <= 0xff60 || r >= 0x1f000) {
		return 2
	}
	return 1
}

func ClipLine(text string, width int) string {
	var out strings.Builder
	used := 0
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
			out.WriteRune(r)
			continue
		}
		if inEscape {
			out.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		cells := runeWidth(r)
		if used+cells > width {
			break
		}
		out.WriteRune(r)
		used += cells
	}
	return out.String()
}
