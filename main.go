package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/chzyer/readline"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, cleanError(err))
		os.Exit(1)
	}
}

func run() (result error) {
	flags := flag.NewFlagSet("wiregram", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, "Usage: wiregram")
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("wiregram does not accept positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t, restore, err := openTerminal(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := restore(); err != nil {
			result = errors.Join(result, fmt.Errorf("restore terminal: %w", err))
		}
	}()
	prompt := func(label string, masked bool) (string, error) { return t.prompt(ctx, label, masked) }
	cfg, dir, err := loadConfig(prompt, t.out())
	if err != nil {
		return err
	}
	accounts, err := loadAccounts(cfg, dir)
	if err != nil {
		return err
	}
	return accounts.run(ctx, t)
}

func (c *client) runModes(ctx context.Context, t *terminal, histories map[peerKey][]string) error {
	archived := false
	for {
		entries, archiveUnread, err := c.dialogs(ctx, archived)
		if err != nil {
			if !networkError(err) {
				return err
			}
			t.report(err)
			if err := waitFor(ctx, time.Second); err != nil {
				return err
			}
			continue
		}
		index, refresh, err := c.selectChat(ctx, t, entries, archived, archiveUnread)
		if err != nil {
			return err
		}
		if refresh {
			continue
		}
		if index < 0 {
			return nil
		}
		if index == len(entries)+2 {
			return accountsRequested{}
		}
		if index == len(entries) {
			archived = !archived
			continue
		}
		var entry chat
		if index == len(entries)+1 {
			var selected bool
			entry, selected, err = c.searchUsers(ctx, t, entries)
			if err != nil {
				return err
			}
			if !selected {
				continue
			}
		} else {
			entry = entries[index]
		}
		history := histories[entry.key]
		if history == nil {
			history = make([]string, 0)
		}
		err = c.view(ctx, t, entry, &history)
		histories[entry.key] = history
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			t.report(err)
		}
	}
}

type terminal struct {
	mu      sync.Mutex
	locked  bool
	viewing bool
	notices []string
	editor  *readline.Instance
	keys    <-chan string
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
	terminalLockKey   = "\x00wiregram-lock\x00"
	terminalUnlockKey = "\x00wiregram-unlock\x00"
)

type terminalWriter struct {
	terminal *terminal
	file     *os.File
	closed   *bool
}

func (w terminalWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	w.terminal.mu.Lock()
	defer w.terminal.mu.Unlock()
	if w.terminal.locked || w.terminal.viewing || (w.closed != nil && *w.closed) {
		return len(data), nil
	}
	// Raw mode disables the terminal's newline conversion.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	_, err := w.file.Write(bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n")))
	return originalLength, err
}

func (t *terminal) out() io.Writer { return terminalWriter{t, os.Stdout, nil} }
func (t *terminal) report(err error) {
	t.mu.Lock()
	if t.locked || t.viewing {
		t.notices = append(t.notices, cleanError(err))
		t.mu.Unlock()
		return
	}
	editor := t.editor
	t.mu.Unlock()
	var out io.Writer = terminalWriter{t, os.Stderr, nil}
	if editor != nil {
		out = editor.Stderr()
	}
	fmt.Fprintln(out, cleanError(err))
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

func (t *terminal) manageInactivity(ctx context.Context, input <-chan string) <-chan string {
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
			queue = append(queue, terminalLockKey)
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
					queue = append(queue, terminalUnlockKey)
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

func (t *terminal) enableChatLock(automatic bool, unread func() int, refresh func()) {
	t.configureInactivity(inactivityConfig{
		enabled: true, automatic: automatic, unread: unread, refresh: refresh,
	})
}

func (t *terminal) lockNow() {
	t.configureInactivity(inactivityConfig{lockNow: true})
}

func (t *terminal) disableInactivity() {
	t.configureInactivity(inactivityConfig{})
}

func (t *terminal) configureInactivity(config inactivityConfig) {
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

type lineResult struct {
	text string
	err  error
}

type editor struct {
	terminal *terminal
	closed   bool
	line     *readline.Instance
	input    *io.PipeWriter
	results  chan lineResult
	done     chan struct{}
	stopped  chan struct{}
}

func (t *terminal) newEditor(prompt string, masked bool, history []string, initial ...string) (*editor, error) {
	input, writer := io.Pipe()
	e := &editor{terminal: t, input: writer, results: make(chan lineResult, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	limit := -1
	if history != nil {
		limit = 500
	}
	line, err := readline.NewEx(&readline.Config{
		Prompt: prompt, Stdin: input, Stdout: terminalWriter{t, os.Stdout, &e.closed}, Stderr: terminalWriter{t, os.Stderr, &e.closed},
		HistoryLimit: limit, DisableAutoSaveHistory: history == nil, UniqueEditLine: prompt == "",
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
			if r == 0 && e.line != nil {
				e.line.Operation.SetBuffer("")
			}
			return r, r != '\t' && r != 26
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
	e.line = line
	if len(initial) > 0 {
		line.Operation.SetBuffer(initial[0])
	}
	go func() {
		defer close(e.stopped)
		for {
			text, err := line.Readline()
			select {
			case e.results <- lineResult{text, err}:
			case <-e.done:
			}
			if err != nil {
				return
			}
		}
	}()
	return e, nil
}

func (e *editor) close() {
	e.terminal.mu.Lock()
	e.closed = true
	e.terminal.mu.Unlock()
	close(e.done)
	e.input.Close()
	<-e.stopped
	e.line.Close()
}

func (t *terminal) prompt(ctx context.Context, prompt string, masked bool) (string, error) {
	e, err := t.newEditor(prompt, masked, nil)
	if err != nil {
		return "", err
	}
	defer e.close()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case result := <-e.results:
			if errors.Is(result.err, io.EOF) {
				return "", context.Canceled
			}
			return result.text, result.err
		case key, ok := <-t.keys:
			if key == terminalLockKey || key == terminalUnlockKey {
				continue
			}
			if !ok || key == "\x04" {
				return "", context.Canceled
			}
			if key == "\x03" {
				return "", context.Canceled
			}
			if _, err := io.WriteString(e.input, key); err != nil {
				return "", fmt.Errorf("read input: %w", err)
			}
		}
	}
}

func (t *terminal) lock(unread int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.locked = true
	if t.viewing {
		fmt.Fprint(os.Stdout, "\033\\\033_Ga=d,d=A,q=2;\033\\")
	}
	fmt.Fprintf(os.Stdout, "\033[2J\033[H[wiregram: locked — %d* — press any key]\r\n", unread)
}

func (t *terminal) updateLock(unread int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.locked {
		return
	}
	fmt.Fprintf(os.Stdout, "\033[2J\033[H[wiregram: locked — %d* — press any key]\r\n", unread)
}

func (t *terminal) releaseLock() {
	t.mu.Lock()
	t.locked = false
	t.mu.Unlock()
}

func (t *terminal) isLocked() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.locked
}

func (t *terminal) flushNotices() {
	t.mu.Lock()
	notices := t.notices
	t.notices = nil
	t.mu.Unlock()
	for _, notice := range notices {
		t.report(fmt.Errorf("%s", notice))
	}
}

func (t *terminal) unlock(lines []string) {
	t.mu.Lock()
	if t.locked {
		t.mu.Unlock()
		return
	}
	editor := t.editor
	notices := t.notices
	t.notices = nil
	t.mu.Unlock()
	var frame strings.Builder
	frame.WriteString("\033[2J\033[H")
	for _, line := range lines {
		frame.WriteString(line)
		frame.WriteByte('\n')
	}
	var out io.Writer = t.out()
	if editor != nil {
		out = editor.Stdout()
	}
	// Readline's writer holds its buffer lock while clearing, drawing, and restoring the draft.
	fmt.Fprint(out, frame.String())
	for _, notice := range notices {
		t.report(fmt.Errorf("%s", notice))
	}
}

func openTerminal(ctx context.Context) (*terminal, func() error, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil, nil, fmt.Errorf("wiregram needs an interactive terminal")
	}
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, nil, fmt.Errorf("enter raw terminal mode: %w", err)
	}
	t := &terminal{locking: make(chan inactivityConfig, 1), done: make(chan struct{})}
	t.keys = t.manageInactivity(ctx, readKeys(ctx))
	return t, func() error { return term.Restore(int(os.Stdin.Fd()), state) }, nil
}

func cleanError(err error) string { return strings.TrimSpace(plain(err.Error())) }
