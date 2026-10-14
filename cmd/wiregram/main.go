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

const usageText = `Usage: wiregram [COMMAND]

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
A new account opens with an Account connected card listing the keys to get
around and search; any key or click closes it.
Config and sessions are stored in ~/.config/wiregram/.

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The sidebar lists every account with the active one expanded to its chats;
Enter on another account switches to it; the last row opens Settings.
The account list shows an unread digest below it: one card per connected
account, with its busiest chats. Settings can leave an account out.
Plugins are Lua scripts; enabled ones may add widget cards under the
digest, such as the built-in Weather (on by default) and Hacker News.
Switch them and set them up in Settings > Plugins. Your own go in
~/.config/wiregram/plugins/NAME.lua.

The mouse wheel scrolls the list under the pointer, or the thread pane.
Clicking a chat,
account, card, menu row or picker entry opens it, as Enter would. Clicking a
message selects it; right-clicking it opens its actions. Clicking its
attachment (the image or the [tag] row) downloads it and opens it with
xdg-open (open on macOS). Hold Shift
while dragging to select text with the terminal.

Quitting, deleting a chat, message, agent or provider, removing an account
and Reset ask first in a modal: y or Enter on the action goes ahead, Esc
cancels. Deletes and Reset start on Cancel.

Commands (for scripts and AI agents; output is one JSON object per line):

  wiregram accounts
      List the accounts: backend, account name, label, status.
  wiregram chats BACKEND ACCOUNT
      List the account's chats: chat ID, title, kind, unread count.
  wiregram stream [BACKEND [ACCOUNT [RECIPIENT]]]
      Print incoming messages as they arrive, from every signed-in account,
      every account of a backend, one account, or one of its chats.
      Until Ctrl+C. Connection changes are reported on stderr.
  wiregram send [-reply ID] BACKEND ACCOUNT RECIPIENT [MESSAGE...]
      Send MESSAGE, or stdin when none is given, and print the sent message.
  wiregram reset [-y]
      Sign out of every account and delete all local data, as Settings >
      Reset does. Asks first unless -y is given.

  BACKEND is telegram, mattermost, matrix or email (tg, mm, mx, em).
  WhatsApp is not available: it allows one connection, and a command would
  disconnect the running app. ACCOUNT is the Telegram phone number, the
  Mattermost username, the Matrix ID or the email address, as accounts
  shows it. RECIPIENT is a chat ID or title, a username, a Matrix ID or
  an email address; naming a public channel you have not joined joins it. Accounts are signed in from the app first.
  The commands can run beside the app.

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
    q, Ctrl+C         Quit (asks first)

  Chat list
    Up/k, Down/j      Select chat or account
    Enter             Open chat, or switch to the selected account
    /                 Search users (a modal over the screen); on Mattermost
                      also channels, joining a public one when picked
    u                 Unread: cards for every chat with unread messages
    m                 Mentions: cards for the groups and channels with the
                      latest messages mentioning you (not email)
    a                 Accounts (add, sign in, retry, Settings…)
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    Esc               Close the open chat (wide terminals), else back to Accounts

  Unread / Mentions
    Up/k, Down/j      Select card
    Enter             Open the chat; a mention opens selected, in its thread
                      on Mattermost
    /                 Search users
    r                 Reload
    Esc               Back

  Search users / Forward to / Mention (modals)
    Type              Filter
    Up/Ctrl+P, Down/Ctrl+N  Select
    PgUp/PgDn         Page
    Enter             Open the chat, forward the message to it, or insert
                      the mention
    Esc               Clear the query, then close; Mention closes at once
                      and types the @ and query as written
    Click outside     Close

  Settings
    Up/k, Down/j      Select an account on the left, or a setting on the right
    Enter             Show the account's settings, or change the selected one:
                      open or sign in, reconnect, unread digest shown/hidden,
                      how long mail stays cached (email), remove account
    Tab, Right/l      Move to the account's settings
    Space             Change the selected setting
    Themes…           Pick a theme (Zest, Copper) from sample cards with the
                      arrows; Enter applies it to every screen
    Reset…            Sign out of every account and delete all local data
                      (settings, plugins, agents, cached mail and media),
                      then start over as a fresh installation
    Esc, Left/h       Back to the accounts, then to where Settings was opened
    q                 Quit

  Chat
    Up/Down           Select message (Up at the top loads older ones)
    Enter             Send text; with an empty input, open the selected
                      message's actions
    Ctrl+U            Upload file (browse, or / to type a path)
    Ctrl+F            Search messages (Up/Down, Enter jumps, Esc cancels)
    @                 Mention a member (group chats, at the start of a word):
                      channel members first on Mattermost; Telegram members
                      with a username; Matrix room members; WhatsApp group
    Alt+H             Toggle private mode (hide history)
    Alt+D             Delete chat (you only)
    Ctrl+Alt+D        Delete chat (both sides)
    Esc               Cancel the reply, else back to Unread (or Mentions)
    Ctrl+C            Close chat
    Any key           Unlock after the inactivity lock

  Email chat
    Up/Down           Select mail
    Enter             Open the selected mail's actions (Read first)
    Type              Write: Enter adds a line, Ctrl+S sends, Esc keeps the draft
                      A new mail's first line is its subject; a first line
                      "Subject: ..." starts a new conversation instead of a reply

  Mail reader
    Up/k, Down/j      Scroll
    PgUp/b, PgDn/Space  Page
    Home/g, End/G     Top, bottom
    Enter             Actions (reply, save attachment, move to Trash…)
    Esc, q            Close

  Thread (opens by itself on a threaded message in wide terminals)
    Up/Down           Move through the chat's messages
    Enter             Reply in thread; with an empty input, open the
                      selected message's actions
    @                 Mention a member, as in the chat
    Esc               Close thread

  Upload
    Arrows, Enter     Browse folders and pick a file
    /, ~              Type a path instead (Tab completes, Up/Down cycle matches)
    Enter             Send with the optional caption
    Esc               Back, then cancel

  Message actions (over the faded screen)
    Up/Down           Select an action: reply, thread, go to quote, edit,
                      forward, open/play/save attachment, pin, delete
    Type              Search reactions by name in the "reaction:" field
                      (Backspace deletes); the emoji row shows the matches
    Left/Right        Select a reaction on the emoji row
    Enter             Do it (deletes ask first)
    Esc               Close (a click outside it closes it too)

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 {
		if command, ok := commands[os.Args[1]]; ok {
			return command(ctx, os.Args[2:])
		}
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
		return fmt.Errorf("unknown command %q; wiregram -h lists the commands", flags.Arg(0))
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("wiregram needs an interactive terminal")
	}
	for {
		dir, err := config.Dir()
		if err != nil {
			return err
		}
		err = session(ctx, dir)
		if _, reset := err.(tui.ResetRequested); !reset {
			return err
		}
		if err := config.Wipe(dir); err != nil {
			return err
		}
	}
}

// session runs wiregram on the data in dir until it quits or is reset.
func session(ctx context.Context, dir string) error {
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
