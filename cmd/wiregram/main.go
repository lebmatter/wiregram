package main

import (
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
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
	"wiregram/internal/tui"
)

const usageText = `Usage: wiregram

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
wiregram.db for the window set in an email account's Settings (90 days by default); mail
on the server is never deleted by it.
Config and sessions are stored in ~/.config/wiregram/.

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The sidebar lists every account with the active one expanded to its chats;
Enter on another account switches to it; the last row opens Settings.
The account list shows an unread digest below it: one card per connected
account, with its busiest chats. Settings can leave an account out.

The mouse wheel scrolls the list under the pointer. Clicking a chat,
account, card, menu row or picker entry opens it, as Enter would. Clicking a
message with an attachment downloads it to a private temp directory and
opens it with xdg-open (open on macOS). Hold Shift while dragging to select
text with the terminal.

Keyboard shortcuts:

  Anywhere
    Alt+A             Chat Agents (from the chat list, or a chat with an empty input)
    Tab               Move between chat list and chat (wide terminals)

  Accounts
    Up/k, Down/j      Select account or menu row
    Enter             Open the account, or the selected row (Chat Agents,
                      Settings…, Add account, Quit)
    r                 Reconnect the selected account
    Esc               Back to the open account (when it is online)
    q                 Quit

  Chat list
    Up/k, Down/j      Select chat or account
    Enter             Open chat, or switch to the selected account
    /                 Search users
    u                 Unread: cards for every chat with unread messages
    a                 Accounts (add, sign in, retry, Settings…)
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    Esc               Close the open chat (wide terminals)
    q                 Quit (asks first)

  Unread
    Up/k, Down/j      Select card
    Enter             Open the chat
    r                 Reload
    Esc               Back

  Search users / Forward to
    Type              Filter
    Up/Ctrl+P, Down/Ctrl+N  Select
    PgUp/PgDn         Page
    Enter             Open the chat, or forward the message to it
    Esc               Clear the query, then close

  Settings
    Up/k, Down/j      Select an account on the left, or a setting on the right
    Enter             Show the account's settings, or change the selected one:
                      open or sign in, reconnect, unread digest shown/hidden,
                      how long mail stays cached (email), remove account
    Tab, Right/l      Move to the account's settings
    Space             Change the selected setting
    Esc, Left/h       Back to the accounts, then to where Settings was opened
    q                 Quit

  Chat
    Up/Down           Select message (Up at the top loads older ones)
    Enter             Send text
    Alt+O             Open selected media in mpv
    Ctrl+U            Upload file (browse, or / to type a path)
    Ctrl+F            Search messages (Up/Down, Enter jumps, Esc cancels)
    Alt+R             Reply (toggle)
    Alt+G             Jump to the quoted message
    Alt+T             Open the selected message's thread
    Alt+E             Edit outgoing message (Enter saves, Esc cancels)
    Alt+M             React with emoji
    Alt+F             Forward message
    Alt+S             Save attachment to ~/Downloads
    Alt+P             Pin/unpin message
    Alt+H             Toggle private mode (hide history)
    Alt+D             Delete chat (you only)
    Ctrl+Alt+D        Delete chat (both sides)
    Delete            Delete message (you only where the network allows,
                      otherwise both sides; the prompt says which)
    Shift+Delete      Delete message (both sides)
    Esc               Cancel the reply, else back to chat list
    Ctrl+C            Close chat
    Any key           Unlock after the inactivity lock

  Email chat
    Up/Down           Select mail
    Enter             Read the whole mail
    Type              Write: Enter adds a line, Ctrl+S sends, Esc keeps the draft
    Alt+R             Reply to the selected mail
    Alt+S             Save attachment to ~/Downloads
    Delete            Move the mail to Trash
                      A new mail's first line is its subject; a first line
                      "Subject: ..." starts a new conversation instead of a reply

  Mail reader
    Up/k, Down/j      Scroll
    PgUp/b, PgDn/Space  Page
    Home/g, End/G     Top, bottom
    Alt+R             Reply
    Alt+S             Save attachment
    Delete            Move to Trash
    Esc, q            Close

  Thread (opens by itself on a threaded message in wide terminals)
    Up/Down           Move through the chat's messages
    Enter             Reply in thread
    Esc               Close thread

  Upload
    Arrows, Enter     Browse folders and pick a file
    /, ~              Type a path instead (Tab completes, Up/Down cycle matches)
    Enter             Send with the optional caption
    Esc               Back, then cancel

  Emoji picker
    Type              Filter by name (Backspace deletes, Ctrl+U clears)
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
    Up/k, Down/j      Choose
    Space             Toggle an account
    Esc               Previous question
    Ctrl+C            Cancel

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
