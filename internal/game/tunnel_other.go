//go:build !linux

package game

import "os/exec"

// setParentDeath has no equivalent here; the TUI closes the tunnel on quit.
func setParentDeath(*exec.Cmd) {}
