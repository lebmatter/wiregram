package main

import (
	"bufio"
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

const usageText = `Usage: wiregram [--insecure]

A minimal multi-backend chat client for the terminal. Supports Telegram,
Mattermost, Matrix, and WhatsApp. Text conversations with on-demand Kitty graphics and
audio playback via mpv.

First launch asks for API credentials (or use -ldflags to embed them),
then your phone number, login code, and optional 2FA password.
Config and sessions are stored in ~/.config/wiregram/.

Credentials are stored in the OS keyring by default (secret-tool on Linux,
Keychain on macOS). Use --insecure to store them in plain JSON files instead.

Keyboard shortcuts:

  Chat list
    Up/k, Down/j     Select chat
    Enter             Open chat
    /                 Search users
    a                 Switch accounts
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    q, Ctrl+D         Quit

  Chat
    Up/Down           Select message
    Enter             Send text
    o                 Open selected media (when input is empty)
    Ctrl+U            Upload file
    Ctrl+F            Search messages
    Alt+R             Reply (toggle)
    Alt+E             Edit outgoing message
    Alt+M             React with emoji
    Alt+F             Forward message
    Alt+S             Save attachment to ~/Downloads
    Alt+P             Pin/unpin message
    Alt+H             Toggle private mode (hide history)
    Alt+A             Toggle AI assistant
    Alt+D             Delete chat (you only)
    Ctrl+Alt+D        Delete chat (both sides)
    Delete            Delete message (you only)
    Shift+Delete      Delete message (both sides)
    Esc, Ctrl+C       Back to chat list

  Emoji picker
    Type              Filter by name
    Up/Down           Select emoji
    Tab               Toggle reaction / draft insert
    Enter             Confirm
    Esc               Cancel

  Media viewer
    Esc, Ctrl+C       Stop and return to chat
`

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, cleanError(err))
		os.Exit(1)
	}
}

func run() (result error) {
	flags := flag.NewFlagSet("wiregram", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	insecure := flags.Bool("insecure", false, "store credentials in plain JSON instead of the OS keyring")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usageText)
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("wiregram does not accept positional arguments")
	}
	var creds credStore
	if *insecure {
		creds = &insecureStore{}
	} else {
		kr, err := newOSKeyring()
		if err != nil {
			return err
		}
		creds = kr
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("wiregram needs an interactive terminal")
	}
	initThumbnails()
	prompt := func(label string, masked bool) (string, error) {
		fmt.Fprint(os.Stdout, label)
		if masked {
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stdout)
			return string(pw), err
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", context.Canceled
		}
		return scanner.Text(), nil
	}
	cfg, dir, err := loadConfig(prompt, os.Stdout)
	if err != nil {
		return err
	}
	accounts, err := loadAccounts(cfg, dir, creds)
	if err != nil {
		return err
	}
	return accounts.run(ctx)
}

func runModes(ctx context.Context, b backend, histories map[chatID][]string) error {
	archived := false
	for {
		entries, archiveUnread, err := b.Dialogs(ctx, archived)
		if err != nil {
			if !networkError(err) {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s\n", cleanError(err))
			if err := waitFor(ctx, time.Second); err != nil {
				return err
			}
			continue
		}
		index, refresh, err := tuiSelectChat(ctx, b, histories, entries, archived, archiveUnread)
		if err != nil {
			if fwd, ok := err.(forwardReq); ok {
				if err := withLegacyTerminal(ctx, func(t *terminal) {
					b.HandleForward(ctx, t, entries, fwd.from, fwd.id)
				}); err != nil {
					return err
				}
				continue
			}
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
		var entry chatEntry
		if index == len(entries)+1 {
			var selected bool
			if err := withLegacyTerminal(ctx, func(t *terminal) {
				entry, selected, err = b.SearchUsers(ctx, t, entries)
			}); err != nil {
				return err
			}
			if err != nil {
				return err
			}
			if !selected {
				continue
			}
		} else {
			entry = entries[index]
		}
		history := histories[entry.ID]
		if history == nil {
			history = make([]string, 0)
		}
		if err := withLegacyTerminal(ctx, func(t *terminal) {
			err = viewChat(ctx, b, t, entry, &history)
		}); err != nil {
			return err
		}
		histories[entry.ID] = history
		if err != nil {
			if fwd, ok := err.(forwardReq); ok {
				if err := withLegacyTerminal(ctx, func(t *terminal) {
					b.HandleForward(ctx, t, entries, fwd.from, fwd.id)
				}); err != nil {
					return err
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s\n", cleanError(err))
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
	return t.newEditorWithCompleter(prompt, masked, history, nil, initial...)
}

func (t *terminal) newEditorWithCompleter(
	prompt string,
	masked bool,
	history []string,
	completer readline.AutoCompleter,
	initial ...string,
) (*editor, error) {
	input, writer := io.Pipe()
	e := &editor{terminal: t, input: writer, results: make(chan lineResult, 1), done: make(chan struct{}), stopped: make(chan struct{})}
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
			if r == 0 && e.line != nil {
				e.line.Operation.SetBuffer("")
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

func withLegacyTerminal(ctx context.Context, fn func(t *terminal)) error {
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	legacyCtx, cancel := context.WithCancel(ctx)
	t := &terminal{locking: make(chan inactivityConfig, 1), done: make(chan struct{})}
	t.keys = t.manageInactivity(legacyCtx, readKeys(legacyCtx))
	fn(t)
	cancel()
	// Unblock the stdin reader goroutine so it can observe the cancelled context.
	os.Stdin.SetReadDeadline(time.Now())
	<-t.done
	os.Stdin.SetReadDeadline(time.Time{})
	fmt.Fprint(os.Stdout, "\033[2J\033[3J\033[H")
	return term.Restore(int(os.Stdin.Fd()), state)
}

func cleanError(err error) string { return strings.TrimSpace(plain(err.Error())) }
