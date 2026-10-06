package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"

	"wiregram/internal/textfmt"
)

// The player never reads the terminal. Only the chat loop handles keyboard input.
type MediaOutput struct{ Terminal *Terminal }

func (w MediaOutput) Write(data []byte) (int, error) {
	w.Terminal.Mu.Lock()
	defer w.Terminal.Mu.Unlock()
	if w.Terminal.locked || !w.Terminal.viewing {
		return len(data), nil
	}
	return os.Stdout.Write(data)
}

func (t *Terminal) MediaScreen(label string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	t.viewing = true
	fmt.Fprintf(os.Stdout, "\033[2J\033[H%s\r\nEsc: return to chat. Inactivity stops media after 10 seconds.\r\n", textfmt.Plain(label))
}

func (t *Terminal) ClearMedia() {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	t.viewing = false
	// Terminate any interrupted graphics packet, then delete image data as well as placements.
	fmt.Fprint(os.Stdout, "\033\\\033_Ga=d,d=A,q=2;\033\\\033[?25h")
}

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

func MpvCmd(ctx context.Context, player string, audio bool, path string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, player, mpvArgs(audio, path)...)
	cmd.WaitDelay = time.Second
	return cmd
}
