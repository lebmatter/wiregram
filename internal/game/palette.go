package game

import "strings"

// MaxPalette is the most colours a game may name.
const MaxPalette = 16

// colourNames are the colours a palette may name besides #rrggbb: the
// theme's own, which follow each player's theme, and the fixed ones.
var colourNames = map[string]bool{"accent": true, "subtle": true, "dim": true,
	"green": true, "yellow": true, "red": true, "blue": true, "violet": true}

// ValidTag reports whether name can be a palette tag: 1 to 16 of a-z, 0-9
// and _, starting with a letter.
func ValidTag(name string) bool {
	if name == "" || len(name) > 16 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// ValidColour reports whether c is a colour a palette may use.
func ValidColour(c string) bool {
	if colourNames[c] {
		return true
	}
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	for _, r := range c[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// CleanPalette keeps the valid entries of a palette from the network.
func CleanPalette(p map[string]string) map[string]string {
	out := map[string]string{}
	for name, c := range p {
		if len(out) == MaxPalette {
			break
		}
		if ValidTag(name) && ValidColour(c) {
			out[name] = c
		}
	}
	return out
}

// EscapeTags backslashes the { and \ in s, so a view shows it as written.
func EscapeTags(s string) string {
	return strings.NewReplacer(`\`, `\\`, "{", `\{`).Replace(s)
}
