//go:build linux || darwin || freebsd || netbsd || openbsd

package backend

import (
	"os"
	"syscall"
	"unsafe"
)

// TermSize is the terminal's size in cells, or 80×24 when stdout is not one.
func TermSize() (cols, rows int) {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.Col < 4 || ws.Row < 4 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}
