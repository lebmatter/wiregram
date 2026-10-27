package game

import (
	"os/exec"
	"syscall"
)

// setParentDeath kills cloudflared if wiregram dies without closing it, so a
// crash never leaves a tunnel open.
func setParentDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
