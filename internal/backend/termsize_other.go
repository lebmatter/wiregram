//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package backend

// TermSize is 80×24 where the terminal cannot be asked; the TUI resizes on
// its first window size message.
func TermSize() (cols, rows int) { return 80, 24 }
