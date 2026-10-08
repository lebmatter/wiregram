package textfmt

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
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

// DisplayWidth is the cells text takes, measured as the renderer measures
// it (wcwidth per codepoint), so padded boxes line up on screen.
func DisplayWidth(text string) int {
	return ansi.WcWidth.StringWidth(text)
}

func takeCells(text string, limit int) (string, string) {
	if limit < 1 {
		limit = 1
	}
	used, end := 0, 0
	for end < len(text) {
		if text[end] == '\033' {
			end = escapeEnd(text, end)
			continue
		}
		r, size := utf8.DecodeRuneInString(text[end:])
		cells := runeWidth(r)
		if used > 0 && used+cells > limit {
			break
		}
		used += cells
		end += size
	}
	return text[:end], text[end:]
}

// escapeEnd returns the index just past the escape sequence at text[i]. An
// OSC (hyperlinks) runs to BEL or ST, since its URL is full of letters; any
// other sequence ends at its first letter.
func escapeEnd(text string, i int) int {
	if i+1 < len(text) && text[i+1] == ']' {
		for j := i + 2; j < len(text); j++ {
			if text[j] == '\a' {
				return j + 1
			}
			if text[j] == '\033' && j+1 < len(text) && text[j+1] == '\\' {
				return j + 2
			}
		}
		return len(text)
	}
	for j := i + 1; j < len(text); j++ {
		if c := text[j]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return j + 1
		}
	}
	return len(text)
}

func runeWidth(r rune) int {
	return ansi.WcWidth.StringWidth(string(r))
}

func ClipLine(text string, width int) string {
	var out strings.Builder
	used := 0
	for i := 0; i < len(text); {
		if text[i] == '\033' {
			next := escapeEnd(text, i)
			out.WriteString(text[i:next])
			i = next
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		cells := runeWidth(r)
		if used+cells > width {
			break
		}
		out.WriteString(text[i : i+size])
		used += cells
		i += size
	}
	return out.String()
}
