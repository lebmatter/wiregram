package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chzyer/readline"
	"golang.org/x/term"

	"wiregram/internal/textfmt"
)

type Terminal struct {
	Mu      sync.Mutex
	locked  bool
	viewing bool
	notices []string
	Editor  *readline.Instance
	Keys    <-chan string
	locking chan inactivityConfig
	done    chan struct{}
}

type inactivityConfig struct {
	enabled   bool
	automatic bool
	lockNow   bool
	unread    func() int
	refresh   func()
	ack       chan struct{}
}

const (
	TerminalLockKey   = "\x00wiregram-lock\x00"
	TerminalUnlockKey = "\x00wiregram-unlock\x00"
)

type terminalWriter struct {
	terminal *Terminal
	file     *os.File
	closed   *bool
}

func (w terminalWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	w.terminal.Mu.Lock()
	defer w.terminal.Mu.Unlock()
	if w.terminal.locked || w.terminal.viewing || (w.closed != nil && *w.closed) {
		return len(data), nil
	}
	// Raw mode disables the terminal's newline conversion.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	_, err := w.file.Write(bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n")))
	return originalLength, err
}

func (t *Terminal) Out() io.Writer { return terminalWriter{t, os.Stdout, nil} }
func (t *Terminal) Report(err error) {
	t.Mu.Lock()
	if t.locked || t.viewing {
		t.notices = append(t.notices, textfmt.CleanError(err))
		t.Mu.Unlock()
		return
	}
	editor := t.Editor
	t.Mu.Unlock()
	var out io.Writer = terminalWriter{t, os.Stderr, nil}
	if editor != nil {
		out = editor.Stderr()
	}
	fmt.Fprintln(out, textfmt.CleanError(err))
}

func readKeys(ctx context.Context) <-chan string {
	bytesIn := make(chan byte)
	keys := make(chan string)
	go func() {
		defer close(bytesIn)
		var buf [1]byte
		for {
			n, err := os.Stdin.Read(buf[:])
			if n > 0 {
				select {
				case bytesIn <- buf[0]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(keys)
		for {
			var b byte
			var ok bool
			select {
			case b, ok = <-bytesIn:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
			key := []byte{b}
			if b >= utf8.RuneSelf {
				for !utf8.FullRune(key) {
					select {
					case next, ok := <-bytesIn:
						if !ok {
							return
						}
						key = append(key, next)
					case <-ctx.Done():
						return
					}
				}
			}
			if b == 27 {
				// Distinguish a bare Escape from an arrow key's escape sequence.
				timer := time.NewTimer(40 * time.Millisecond)
			sequence:
				for len(key) < 4096 {
					select {
					case next, ok := <-bytesIn:
						if !ok {
							break sequence
						}
						key = append(key, next)
						if key[1] == '_' {
							if len(key) > 3 && key[len(key)-2] == 27 && next == '\\' {
								break sequence
							}
							continue
						}
						prefix := 1
						if key[1] == 27 {
							prefix = 2
							if len(key) == 2 {
								continue
							}
						}
						if len(key) == prefix+1 && next != '[' && next != 'O' {
							break sequence
						}
						if len(key) > prefix+1 && next >= 0x40 && next <= 0x7e {
							break sequence
						}
					case <-timer.C:
						break sequence
					case <-ctx.Done():
						timer.Stop()
						return
					}
				}
				timer.Stop()
			}
			select {
			case keys <- string(key):
			case <-ctx.Done():
				return
			}
		}
	}()
	return keys
}

func (t *Terminal) manageInactivity(ctx context.Context, input <-chan string) <-chan string {
	output := make(chan string)
	go func() {
		defer close(output)
		defer close(t.done)
		timer := time.NewTimer(time.Hour)
		if !timer.Stop() {
			<-timer.C
		}
		defer timer.Stop()
		var timerC <-chan time.Time
		var config inactivityConfig
		var queue []string
		locked := false
		reset := func() {
			if !timer.Stop() && timerC != nil {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(10 * time.Second)
			timerC = timer.C
		}
		lockScreen := func() {
			if !config.enabled || locked {
				return
			}
			locked = true
			unread := 0
			if config.unread != nil {
				unread = config.unread()
			}
			t.lock(unread)
			if config.refresh != nil {
				config.refresh()
			}
			queue = append(queue, TerminalLockKey)
		}
		for {
			var send chan string
			var next string
			if len(queue) > 0 {
				send, next = output, queue[0]
			}
			select {
			case <-ctx.Done():
				return
			case nextConfig := <-t.locking:
				if nextConfig.lockNow {
					lockScreen()
					close(nextConfig.ack)
					continue
				}
				config = nextConfig
				if !config.enabled {
					timer.Stop()
					timerC = nil
					if locked {
						locked = false
						t.releaseLock()
					}
					close(config.ack)
					continue
				}
				if config.automatic {
					reset()
				} else {
					timer.Stop()
					timerC = nil
				}
				if config.refresh != nil {
					config.refresh()
				}
				close(config.ack)
			case <-timerC:
				timerC = nil
				lockScreen()
			case key, ok := <-input:
				if !ok {
					return
				}
				if strings.HasPrefix(key, "\033_") {
					queue = append(queue, key)
					continue
				}
				if config.enabled && config.automatic {
					reset()
				}
				if locked {
					locked = false
					t.releaseLock()
					queue = append(queue, TerminalUnlockKey)
					continue
				}
				queue = append(queue, key)
			case send <- next:
				queue = queue[1:]
			}
		}
	}()
	return output
}

func (t *Terminal) EnableChatLock(automatic bool, unread func() int, refresh func()) {
	t.configureInactivity(inactivityConfig{
		enabled: true, automatic: automatic, unread: unread, refresh: refresh,
	})
}

func (t *Terminal) LockNow() {
	t.configureInactivity(inactivityConfig{lockNow: true})
}

func (t *Terminal) DisableInactivity() {
	t.configureInactivity(inactivityConfig{})
}

func (t *Terminal) configureInactivity(config inactivityConfig) {
	config.ack = make(chan struct{})
	select {
	case t.locking <- config:
	case <-t.done:
		return
	}
	select {
	case <-config.ack:
	case <-t.done:
	}
}

type LineResult struct {
	Text string
	Err  error
}

type Editor struct {
	terminal *Terminal
	closed   bool
	Line     *readline.Instance
	Input    *io.PipeWriter
	Results  chan LineResult
	done     chan struct{}
	stopped  chan struct{}
}

func (t *Terminal) NewEditor(prompt string, masked bool, history []string, initial ...string) (*Editor, error) {
	return t.NewEditorWithCompleter(prompt, masked, history, nil, initial...)
}

func (t *Terminal) NewEditorWithCompleter(
	prompt string,
	masked bool,
	history []string,
	completer readline.AutoCompleter,
	initial ...string,
) (*Editor, error) {
	input, writer := io.Pipe()
	e := &Editor{terminal: t, Input: writer, Results: make(chan LineResult, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	limit := -1
	if history != nil {
		limit = 500
	}
	line, err := readline.NewEx(&readline.Config{
		Prompt: prompt, Stdin: input, Stdout: terminalWriter{t, os.Stdout, &e.closed}, Stderr: terminalWriter{t, os.Stderr, &e.closed},
		HistoryLimit: limit, DisableAutoSaveHistory: history == nil, UniqueEditLine: prompt == "",
		AutoComplete:    completer,
		InterruptPrompt: "\n", EOFPrompt: "\n", EnableMask: masked, MaskRune: '*',
		ForceUseInteractive: true,
		FuncMakeRaw:         func() error { return nil }, FuncExitRaw: func() error { return nil },
		FuncOnWidthChanged: func(func()) {},
		FuncGetWidth: func() int {
			width, _, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || width < 1 {
				return 80
			}
			return width
		},
		FuncFilterInputRune: func(r rune) (rune, bool) {
			// Closing the input must discard a draft rather than flush it as a message.
			if r == 0 && e.Line != nil {
				e.Line.Operation.SetBuffer("")
			}
			return r, (r != '\t' || completer != nil) && r != 26
		},
	})
	if err != nil {
		input.Close()
		writer.Close()
		return nil, fmt.Errorf("start line editor: %w", err)
	}
	if history != nil {
		for _, text := range history {
			if err := line.SaveHistory(text); err != nil {
				line.Close()
				writer.Close()
				return nil, fmt.Errorf("restore chat history: %w", err)
			}
		}
	}
	e.Line = line
	if len(initial) > 0 {
		line.Operation.SetBuffer(initial[0])
	}
	go func() {
		defer close(e.stopped)
		for {
			text, err := line.Readline()
			select {
			case e.Results <- LineResult{text, err}:
			case <-e.done:
			}
			if err != nil {
				return
			}
		}
	}()
	return e, nil
}

func (e *Editor) Close() {
	e.terminal.Mu.Lock()
	e.closed = true
	e.terminal.Mu.Unlock()
	close(e.done)
	e.Input.Close()
	<-e.stopped
	e.Line.Close()
}

func (t *Terminal) prompt(ctx context.Context, prompt string, masked bool) (string, error) {
	e, err := t.NewEditor(prompt, masked, nil)
	if err != nil {
		return "", err
	}
	defer e.Close()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case result := <-e.Results:
			if errors.Is(result.Err, io.EOF) {
				return "", context.Canceled
			}
			return result.Text, result.Err
		case key, ok := <-t.Keys:
			if key == TerminalLockKey || key == TerminalUnlockKey {
				continue
			}
			if !ok || key == "\x04" {
				return "", context.Canceled
			}
			if key == "\x03" {
				return "", context.Canceled
			}
			if _, err := io.WriteString(e.Input, key); err != nil {
				return "", fmt.Errorf("read input: %w", err)
			}
		}
	}
}

func (t *Terminal) lock(unread int) {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	t.locked = true
	if t.viewing {
		fmt.Fprint(os.Stdout, "\033\\\033_Ga=d,d=A,q=2;\033\\")
	}
	fmt.Fprintf(os.Stdout, "\033[2J\033[H[wiregram: locked — %d* — press any key]\r\n", unread)
}

func (t *Terminal) UpdateLock(unread int) {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	if !t.locked {
		return
	}
	fmt.Fprintf(os.Stdout, "\033[2J\033[H[wiregram: locked — %d* — press any key]\r\n", unread)
}

func (t *Terminal) releaseLock() {
	t.Mu.Lock()
	t.locked = false
	t.Mu.Unlock()
}

func (t *Terminal) IsLocked() bool {
	t.Mu.Lock()
	defer t.Mu.Unlock()
	return t.locked
}

func (t *Terminal) FlushNotices() {
	t.Mu.Lock()
	notices := t.notices
	t.notices = nil
	t.Mu.Unlock()
	for _, notice := range notices {
		t.Report(fmt.Errorf("%s", notice))
	}
}

func (t *Terminal) Unlock(lines []string) {
	t.Mu.Lock()
	if t.locked {
		t.Mu.Unlock()
		return
	}
	editor := t.Editor
	notices := t.notices
	t.notices = nil
	t.Mu.Unlock()
	var frame strings.Builder
	frame.WriteString("\033[2J\033[H")
	for _, line := range lines {
		frame.WriteString(line)
		frame.WriteByte('\n')
	}
	var out io.Writer = t.Out()
	if editor != nil {
		out = editor.Stdout()
	}
	// Readline's writer holds its buffer lock while clearing, drawing, and restoring the draft.
	fmt.Fprint(out, frame.String())
	for _, notice := range notices {
		t.Report(fmt.Errorf("%s", notice))
	}
}

func WithLegacyTerminal(ctx context.Context, fn func(t *Terminal)) error {
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	legacyCtx, cancel := context.WithCancel(ctx)
	t := &Terminal{locking: make(chan inactivityConfig, 1), done: make(chan struct{})}
	t.Keys = t.manageInactivity(legacyCtx, readKeys(legacyCtx))
	fn(t)
	cancel()
	// Unblock the stdin reader goroutine so it can observe the cancelled context.
	os.Stdin.SetReadDeadline(time.Now())
	<-t.done
	os.Stdin.SetReadDeadline(time.Time{})
	fmt.Fprint(os.Stdout, "\033[2J\033[3J\033[H")
	return term.Restore(int(os.Stdin.Fd()), state)
}
