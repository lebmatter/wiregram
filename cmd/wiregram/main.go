package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"wiregram/internal/account"
	"wiregram/internal/base"
	"wiregram/internal/config"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
	"wiregram/internal/tui"
)

const usageText = `Usage: wiregram
       wiregram base [-addr :8765]

A minimal multi-backend chat client for the terminal. Supports Telegram,
Mattermost, Matrix, WhatsApp, and email over IMAP (Gmail and Google Workspace
included). Text conversations with on-demand Kitty graphics and audio playback
via mpv.

First launch opens Add account. Telegram asks for API credentials from
https://my.telegram.org/apps (unless embedded with -ldflags), then your
phone number, login code, and optional 2FA password.
Email accounts ask for an IMAP server (imap.gmail.com by default), the
address, and an app password, or Google sign-in in the browser when the build
embeds a Google OAuth client. Each correspondent is one chat. Mail is cached in
wiregram.db for the window set in Settings > Email (90 days by default); mail
on the server is never deleted by it.
Config and sessions are stored in ~/.config/wiregram/.

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The sidebar lists every account with the active one expanded to its chats;
Enter on another account, or Alt+1..9, switches to it.

The mouse wheel scrolls the list under the pointer. Clicking a chat,
account, card, menu row or picker entry opens it, as Enter would. Clicking a
message with an attachment downloads it to a private temp directory and
opens it with xdg-open (open on macOS). Hold Shift while dragging to select
text with the terminal.

Keyboard shortcuts:

  Anywhere
    Alt+1..9          Switch to account N
    Alt+A             Chat Agents (from the chat list)
    Alt+G             Games (from the chat list)
    Alt+S             Settings (from the chat list)
    Tab               Move between chat list and chat (wide terminals)
    ?                 Show all shortcuts

  Chat list
    Up/k, Down/j     Select chat or account
    Enter             Open chat, or switch to the selected account
    /                 Search users
    u                 Unread: cards for every chat with unread messages
    a                 Manage accounts (add, sign in, retry; Settings… is listed there)
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    Esc               Close the open chat (wide terminals)
    q, Ctrl+D         Quit

  Settings
    Up/k, Down/j     Select section or account
    Tab               Move between sections and accounts
    Enter             Open section, switch to the account, or add one;
                      in Email, change how long mail stays cached
    r                 Reconnect the selected account
    n                 Add account
    x                 Remove account: signs out on the server, deletes its data
    Esc               Back to where Settings was opened from
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
    Alt+D             Delete chat (you only)
    Ctrl+Alt+D        Delete chat (both sides)
    Delete            Delete message (you only)
    Shift+Delete      Delete message (both sides)
    Esc               Back to chat list
    Ctrl+C            Close chat

  Email chat
    Up/Down           Select mail
    Enter             Read the whole mail (Up/Down, PgUp/PgDn scroll; Esc closes)
    Type              Write: Enter adds a line, Ctrl+S sends, Esc keeps the draft
    Alt+R             Reply to the selected mail
    Delete            Move the mail to Trash
                      A new mail's first line is its subject; a first line
                      "Subject: ..." starts a new conversation instead of a reply

  Thread (opens by itself on a threaded message in wide terminals)
    Up/Down           Move through the chat's messages
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

  Chat Agents (violet)
    Up/k, Down/j      Select
    Enter             Show conversations / open one
    n                 New agent (connects a provider first if needed)
    e                 Edit agent
    c                 Connect accounts (each answers to one agent)
    Space             Pause or resume agent
    d                 Delete agent
    p                 AI providers (n new, e edit, d delete)
    y / n             Send / discard a suggested reply
    Esc               Back

  Chat Agent wizard
    Enter             Answer or pick
    Space             Toggle an account
    Esc               Previous question
    Ctrl+C            Cancel

  Media viewer
    Esc, Ctrl+C       Stop and return to chat

  Games (green)
    Up/k, Down/j      Select
    Enter             Play a one-player game, or pick a player for a
                      two-player game (needs a base); play again after a game
    Tab               Switch between games and players (when on a base)
    Enter on player   Challenge them to the selected game
    y / n             Accept / decline a challenge (Enter / Esc also work)
    e                 Connect to a base, or change player name or token
    Esc               Leave the game, cancel a challenge, or close games
    Other keys        Go to the game; each game shows its keys under the board

Game base:

  One-player games need no base. wiregram base runs a game server for the
  two-player games: it lists players, passes on challenges and relays moves
  between matched players. Games run in the players'
  wiregram, so any base can host every game. Set WIREGRAM_BASE_TOKEN to make
  players enter that token. On the internet, put it behind a TLS proxy and
  connect with wss://.

    -addr ADDR        Listen address (default :8765)
`

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, textfmt.CleanError(err))
		os.Exit(1)
	}
}

func run() (result error) {
	if len(os.Args) > 1 && os.Args[1] == "base" {
		return runBase(os.Args[2:])
	}
	flags := flag.NewFlagSet("wiregram", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("wiregram needs an interactive terminal")
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := account.ImportLegacy(db, dir); err != nil {
		return err
	}
	cfg, err := config.LoadConfig(db)
	if err != nil {
		return err
	}
	accounts, err := account.LoadAccounts(cfg, dir, db)
	if err != nil {
		return err
	}
	return tui.RunAccounts(ctx, accounts)
}

func runBase(args []string) error {
	flags := flag.NewFlagSet("wiregram base", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	addr := flags.String("addr", ":8765", "listen address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usageText)
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("wiregram base does not accept positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "wiregram base: ", log.LstdFlags)
	return base.Run(ctx, *addr, os.Getenv("WIREGRAM_BASE_TOKEN"), logger)
}
