package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"wiregram/internal/account"
	"wiregram/internal/config"
	"wiregram/internal/textfmt"
	"wiregram/internal/tui"
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

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The header lists every account; Alt+1..9 switches to that account.

Keyboard shortcuts:

  Anywhere
    Alt+1..9          Switch to account N
    Tab               Move between chat list and chat (wide terminals)
    ?                 Show all shortcuts

  Chat list
    Up/k, Down/j     Select chat
    Enter             Open chat
    /                 Search users
    a                 Switch accounts
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    Esc               Close the open chat (wide terminals)
    q, Ctrl+D         Quit

  Chat
    Up/Down           Select message
    Enter             Send text
    o                 Open selected media (when input is empty)
    Ctrl+U            Upload file (browse, or / to type a path)
    Ctrl+F            Search messages
    Alt+R             Reply (toggle)
    Alt+G             Jump to the quoted message
    Alt+T             Open the selected message's thread
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
    Esc               Back to chat list
    Ctrl+C            Close chat

  Thread
    Enter             Reply in thread
    Esc               Close thread

  Upload
    Arrows, Enter     Browse folders and pick a file
    /, ~              Type a path instead
    Enter             Send with the optional caption
    Esc               Back, then cancel

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
		fmt.Fprintln(os.Stderr, textfmt.CleanError(err))
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
	var creds config.CredStore
	if *insecure {
		creds = &config.InsecureStore{}
	} else {
		kr, err := config.NewOSKeyring()
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
	cfg, dir, err := config.LoadConfig(prompt, os.Stdout)
	if err != nil {
		return err
	}
	accounts, err := account.LoadAccounts(cfg, dir, creds)
	if err != nil {
		return err
	}
	return tui.RunAccounts(ctx, accounts)
}
