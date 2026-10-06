package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"
)

func mpvArgs(audio bool, path string) []string {
	args := []string{"--no-config", "--load-scripts=no", "--input-terminal=no",
		"--input-default-bindings=no", "--input-media-keys=no", "--osc=no",
		"--osd-level=0", "--really-quiet", "--audio-display=no",
		"--autoload-files=no", "--image-display-duration=inf"}
	if audio {
		args = append(args, "--vid=no", "--vo=null")
	} else {
		cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || cols < 1 || rows < 4 {
			cols, rows = 80, 24
		}
		args = append(args, "--profile=sw-fast", "--vo=kitty",
			"--vo-kitty-alt-screen=no", "--vo-kitty-use-shm=no",
			"--vo-kitty-config-clear=no", "--vo-kitty-top=3", "--vo-kitty-left=1",
			fmt.Sprintf("--vo-kitty-cols=%d", cols),
			fmt.Sprintf("--vo-kitty-rows=%d", rows-2),
			fmt.Sprintf("--vo-kitty-width=%d", cols*8),
			fmt.Sprintf("--vo-kitty-height=%d", (rows-2)*16))
	}
	args = append(args, "--", path)
	return args
}

// MpvCmd builds the mpv command that shows path through the Kitty video output.
func MpvCmd(ctx context.Context, player string, audio bool, path string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, player, mpvArgs(audio, path)...)
	cmd.WaitDelay = time.Second
	return cmd
}
