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
	flags := flag.NewFlagSet("mintg", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, "Usage: mintg")
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("mintg does not accept positional arguments")
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
	histories := make(map[peerKey][]string)
	for {
		c, err := newClient(cfg, dir, t.report)
		if err != nil {
			return err
		}
		err = c.telegram.Run(ctx, func(ctx context.Context) error {
			prompt := func(label string, masked bool) (string, error) { return t.prompt(ctx, label, masked) }
			if err := c.authenticate(ctx, prompt, t.report); err != nil {
				return err
			}
			return c.runSession(ctx, t, histories)
		})
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		if !networkError(err) {
			return fmt.Errorf("Telegram connection: %w", err)
		}
		t.report(fmt.Errorf("Connection lost. Retrying: %w", err))
		if err := waitFor(ctx, time.Second); err != nil {
			return err
		}
	}
}

func (c *client) runModes(ctx context.Context, t *terminal, histories map[peerKey][]string) error {
	for {
		entries, err := c.dialogs(ctx)
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
		index, refresh, err := c.selectChat(ctx, t, entries)
		if err != nil {
			return err
		}
		if refresh {
			continue
		}
		if index < 0 {
			return nil
		}
		entry := entries[index]
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
}

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

func (t *terminal) lock() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.locked = true
	if t.viewing {
		fmt.Fprint(os.Stdout, "\033\\\033_Ga=d,d=A,q=2;\033\\")
	}
	fmt.Fprint(os.Stdout, "\033[2J\033[H[mintg: locked — press any key]\r\n")
}

func (t *terminal) unlock(lines []string) {
	t.mu.Lock()
	t.locked = false
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
		return nil, nil, fmt.Errorf("mintg needs an interactive terminal")
	}
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, nil, fmt.Errorf("enter raw terminal mode: %w", err)
	}
	t := &terminal{keys: readKeys(ctx)}
	return t, func() error { return term.Restore(int(os.Stdin.Fd()), state) }, nil
}

func cleanError(err error) string { return strings.TrimSpace(plain(err.Error())) }
