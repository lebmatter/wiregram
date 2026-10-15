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
embeds a Google OAuth client. An email account's chats are its folders:
Inbox, Sent, Archive (where the server has one) and the Gmail labels (other
servers' folders) picked in its Labels… row (Space shows or hides one, Enter
goes to it). A folder opens as a list of conversations, grouped by reply
headers or subject. Mail is cached in wiregram.db for the window set in an
email account's Settings (90 days by default); mail on the server is never
deleted by it.
A new account opens with an Account connected card listing the keys to get
around and search; any key or click closes it.
Config and sessions are stored in ~/.config/wiregram/.

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The sidebar lists every account with the active one expanded to its chats;
Enter on another account switches to it; the last two rows open Home (the
account list) and Settings.
The account list shows an unread digest below it: one card per connected
account, with its busiest chats. Settings can leave an account out.
Plugins are Lua scripts; enabled ones may add widget cards under the
digest, such as the built-in Weather (on by default) and Hacker News.
Switch them and set them up in Settings > Plugins. Your own go in
~/.config/wiregram/plugins/NAME.lua.

AI powered chat bots answer an account's incoming private messages as you,
through an OpenAI-compatible provider added in Settings > AI providers. Every account
but email can have several: under its Search… sits a Bots section with a
NAME @handle row for each, which opens the bot's settings in place of the
chat (a bot registered on Mattermost or Telegram also gets an Inbox row: its
own private chats, where what you send goes out as the bot), then New bot….
On Telegram, bots are created through a manager bot of yours (Bot Management
on in @BotFather), whose token goes in a bot pane's Manager bot row.
A bot answers all of the account's private chats or the ones picked for it;
no chat has two bots. The @handle is unique on the account and names the bot
if it is registered on the network later. A violet ✦ marks accounts with a
bot, the private chats one answers while on, chats with a reply waiting for
approval, and the messages a bot sent. Messages go to the provider.

The mouse wheel scrolls the list under the pointer, or the thread pane.
Clicking a chat,
account, card, menu row or picker entry opens it, as Enter would. Clicking a
message selects it; right-clicking it opens its actions. Clicking a reaction
under it adds yours or takes it back, and the reaction blinks. Clicking the
sender's name opens their profile (not on email). Clicking its
attachment (the image or the [tag] row) downloads it and opens it with
xdg-open (open on macOS, the default app on Windows). Hold Shift
while dragging to select text with the terminal.

Quitting, deleting a chat, message, bot or provider, removing an account
and Reset ask first in a modal: y or Enter on the action goes ahead, Esc
cancels. Deletes and Reset start on Cancel. Moving mail to Trash does not
ask: it can be moved back.

Commands (for scripts and AI coding tools; output is one JSON object per line):

  wiregram accounts
      List the accounts: backend, account name, label, status.
  wiregram chats BACKEND ACCOUNT
      List the account's chats: chat ID, title, kind, unread count. An
      email account lists its folders (kind folder).
  wiregram stream [BACKEND [ACCOUNT [RECIPIENT]]]
      Print incoming messages as they arrive, from every signed-in account,
      every account of a backend, one account, or one of its chats.
      Until Ctrl+C. Connection changes are reported on stderr.
  wiregram send [-reply ID] BACKEND ACCOUNT RECIPIENT [MESSAGE...]
      Send MESSAGE, or stdin when none is given, and print the sent message.
      Email goes to an address and answers the newest mail with it; a first
      line "Subject: ..." starts a new conversation.
  wiregram dry-run [-message ID]... BACKEND ACCOUNT RECIPIENT
      Print the reply the bot answering the chat would give to each message
      ID, or to the newest incoming message, without sending it. Works
      while the bot is paused.
  wiregram reset [-y]
      Sign out of every account and delete all local data, as Settings >
      Reset does. Asks first unless -y is given.

  BACKEND is telegram, mattermost, matrix or email (tg, mm, mx, em).
  WhatsApp is not available: it allows one connection, and a command would
  disconnect the running app. ACCOUNT is the Telegram phone number, the
  Mattermost username, the Matrix ID or the email address, as accounts
  shows it. RECIPIENT is a chat ID or title, a username, a Matrix ID or
  an email address; naming a public channel you have not joined joins it.
  Email is streamed by folder (as chats lists them) and sent to an address.
  Accounts are signed in from the app first.
  The commands can run beside the app.

Keyboard shortcuts:

  Anywhere
    Tab               Move between chat list and chat (wide terminals)

  Accounts
    Up/k, Down/j      Select account or menu row
    Enter             Open the account, or the selected row (Settings…,
                      Add account, Quit)
    r                 Reconnect the selected account
    Esc, Ctrl+C       Quit (asks first)

  Chat list
    Up/k, Down/j      Select chat or account
    Enter             Open chat (on email, a folder), switch to the selected
                      account, or open a bot's settings or create one
                      (New bot…), under Bots
    c                 Compose a mail (email)
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
    Chat…             Enter switches the nick style: nick: (text right after
                      the name) or <nick> (names padded to one column)
    AI providers…     Enter opens a provider's name, URL, key variable and
                      model, each edited in place, or deletes it (not while
                      a bot uses it); Add provider… asks step by step
    Reset…            Sign out of every account and delete all local data
                      (settings, plugins, bots, cached mail and media),
                      then start over as a fresh installation
    Esc, Left/h       Back to the accounts, then to where Settings was opened,
                      or quit when there is nowhere to go back to

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
    Ctrl+Y            Approve the bot's suggested reply (empty input)
    Ctrl+E            Edit it: the text moves into the input, sent as yours
    Ctrl+X            Discard it
    Esc               Cancel the reply, else back to Unread (or Mentions)
    Ctrl+C            Close chat
    Alt+L             Lock the chat (it also locks when the terminal loses focus)
    Any key           Unlock the locked chat

  Email folder (a list of conversations; the newest mail first)
    Up/k, Down/j      Select a conversation, or a mail in an expanded one
    PgUp/PgDn, g/G    Page, top, bottom
    Space             Expand or collapse the conversation
    Enter             Read the mail (a conversation: its newest), marking it
                      read; panes 140+ columns wide show the reader beside
                      the list, previewing the selected mail; ] and [ move
                      between them
    c                 Compose a new mail
    r, R, f           Reply, reply to all, forward
    d                 Move to Trash
    a                 Archive (where the server has an archive)
    m                 Move to another folder or label (a picker)
    u                 Mark read, or the newest mail unread
    /                 Search the folder on the server (Esc clears the results)
    v                 Visual mode: Space picks conversations or mails, then
                      d, a, m or u acts on them all; Esc leaves it
    Esc               Back

  Mail reader
    Up/k, Down/j      Scroll
    PgUp/b, PgDn/Space  Page
    Home/g, End/G     Top, bottom
    o, s              Open the attachment, or save it to ~/Downloads
    r, R, f, d, a, m, u  As in the folder, on this mail
    Esc               Close

  Mail composer
    Tab, Shift+Tab    Next and previous field: To, Cc, Subject, Attach, text;
                      in To and Cc, Tab first takes the address offered
    Enter             In Attach, attach the file typed (Tab completes paths)
    Backspace         In an empty Attach, drop the last file
    Ctrl+S            Send
    Esc               Close, keeping the draft (c opens it again)
    Ctrl+X            Discard the draft

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

  Bot (a row in the Bots section)
    Up/k, Down/j      Select a setting: status, name, @handle, provider,
                      model, prompt, knowledge files, media, replies (ask me
                      first or automatically), history, chats, bot account
                      (Register… creates @handle on Mattermost or Telegram,
                      then Rotate token… and Unregister…; Telegram first
                      needs the account's manager bot token), delete
    Enter, Space      Change it; text opens for typing (Enter saves, Esc
                      cancels); Chats opens the chat picker
    Esc, Left/h       Back

  Chat picker (a modal over the bot's settings)
    Up/Down           Select All private chats, Selected users or a person
    Space             Pick the radio row, or tick or untick the person;
                      chats another bot answers show its name and cannot
                      be ticked (all is unavailable while another bot
                      answers any)
    Type              Under Selected users, filter the people recently
                      chatted with; on Telegram and Mattermost it also
                      searches everyone, for a DM yet to start
    Enter             Save
    Esc               Cancel (a click outside it cancels too)

  New bot and Add provider (form modals)
    Enter             Answer or pick
    Up/k, Down/j      Choose
    Esc               Previous question, else cancel
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
