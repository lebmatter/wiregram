package backend

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

func mpvArgs(audio bool, path string) []string {
	args := []string{"--no-config", "--load-scripts=no", "--input-terminal=no",
		"--input-default-bindings=no", "--input-media-keys=no", "--osc=no",
		"--osd-level=0", "--really-quiet", "--audio-display=no",
		"--autoload-files=no", "--image-display-duration=inf"}
	if audio {
		args = append(args, "--vid=no", "--vo=null")
	} else {
		cols, rows := TermSize()
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

// NewMediaClient is an HTTP client for media transfers. A whole-request
// timeout would cut off a large file part way, so only the wait for the
// server to answer is limited; the caller's context ends a transfer.
func NewMediaClient() *http.Client {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	t = t.Clone()
	t.ResponseHeaderTimeout = time.Minute
	return &http.Client{Transport: t}
}

// MpvCmd builds the mpv command that shows path through the Kitty video output.
func MpvCmd(ctx context.Context, player string, audio bool, path string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, player, mpvArgs(audio, path)...)
	cmd.WaitDelay = time.Second
	return cmd
}

// SafeFilename keeps a sender-chosen attachment name inside the folder it is
// saved in, falling back to fallback when nothing usable is left.
func SafeFilename(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		return fallback
	}
	return name
}

// CreateUnique creates name in dir, adding _1, _2… before the extension
// rather than replacing a file that is already there.
func CreateUnique(dir, name string) (*os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, candidate),
			os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create file: %w", err)
		}
	}
	return nil, fmt.Errorf("create file: too many files named %s", name)
}

// WriteUnique writes data to a new file named name in dir (see CreateUnique).
func WriteUnique(dir, name string, data []byte) error {
	f, err := CreateUnique(dir, name)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("save file: %w", err)
	}
	return nil
}
