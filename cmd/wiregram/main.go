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
	"path/filepath"
	"syscall"

	"wiregram/internal/account"
	"wiregram/internal/config"
	"wiregram/internal/store"
	"wiregram/internal/textfmt"
	"wiregram/internal/tui"
)

// version is the release tag, set at build time with -ldflags "-X main.version=...".
var version = "dev"

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
servers' folders) picked with its labels chip (Space shows or hides one, Enter
goes to it). A folder opens as a list of conversations, grouped by reply
headers or subject. Mail is cached in wiregram.db for the window set in an
email account's Settings (90 days by default); mail on the server is never
deleted by it.
A new account opens with an Account connected card listing the keys to get
around and search; any key or click closes it. A failed sign-in shows the
server's reason in a Sign-in failed alert; any key closes it.
Config and sessions are stored in ~/.config/wiregram/.

Layout: terminals 100+ columns wide show the chat list beside the open chat.
The sidebar lists every account with the active one expanded to a line of
chips (unread, @mentions, you, archived, search), then its chats in
sections that Space folds (teams on Mattermost, else groups or rooms and
channels, then direct), unread chats first, then its bots; Enter on another
account switches to it; home (the account list) and settings… stay pinned
at the bottom.
Plugins are Lua scripts; enabled ones may add widget cards under the
account list, such as the built-in Unread (unread messages per account, DMs
apart from groups and channels; click it to read them), Weather (click it for today's hourly forecast),
Calendar (a Google Calendar's secret iCal address; click it for upcoming and
recent events) and Hacker News. Unread and Weather are on by default.
Switch them and set them up in Settings > Plugins. Your own go in
~/.config/wiregram/plugins/NAME.lua.

AI powered chat bots answer an account's incoming private messages as you,
through an OpenAI-compatible provider added in Settings > AI. Every account
but email can have several: under its chats sits a bots section with a
NAME @handle row for each, which opens the bot's settings in place of the
chat (a bot registered on Mattermost or Telegram also gets an Inbox row: its
own private chats, where what you send goes out as the bot), then new bot….
On Telegram, bots are created through a manager bot of yours (Bot Management
on in @BotFather), whose token goes in a bot pane's Manager bot row.
A bot answers all of the account's private chats or the ones picked for it;
no chat has two bots. The @handle is unique on the account and names the bot
if it is registered on the network later. A violet ✦ marks accounts with a
bot, the private chats one answers while on, chats with a reply waiting for
approval, and the messages a bot sent. Messages go to the provider.

The same providers summarise and rewrite for you, each task with the
provider Settings > AI gives it. Summarise chat (and Summarise thread) in a
message's actions, or s on an Unread card, streams a summary of the unread
messages, else the newest loaded ones, into a modal: c copies it, r writes
it again. Alt+R rewrites the draft in the input. The chat's messages go to
that provider.

The mouse wheel scrolls the list under the pointer, or the thread pane.
Clicking a chat,
account, card, menu row or picker entry opens it, as Enter would. Clicking a
message selects it. Clicking the blank part of its row, beside the text, in
wide terminals also opens its thread beside the chat, an empty one to start a
thread when it has no replies. Double-clicking its text or right-clicking it
opens its actions. Clicking a reaction
under it adds yours or takes it back, and the reaction blinks. Clicking the
sender's name or an @name in a message opens their profile (not on
email), where Enter or a click on go to chat opens your private chat with
them. Clicking its
attachment (the image or the [tag] row) downloads it and opens it with
xdg-open (open on macOS, the default app on Windows). Dragging across
the chat or the thread pane selects its text and copies it to the clipboard
when let go (wl-copy, xclip or xsel, else the terminal's OSC 52; tmux needs
set-clipboard on); while it is shown, Ctrl+C
or Ctrl+Shift+C copies it again. Hold Shift while dragging
to select text with the terminal instead.

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
  wiregram summarize [-n COUNT] BACKEND ACCOUNT RECIPIENT
      Print a summary of the chat's unread messages, or of its recent ones,
      or of its newest COUNT, written by the provider Settings > AI sets
      for summaries.
  wiregram game [-name NAME] [INVITE]
      Play a game without a chat. With no INVITE, pick a game and host
      it: the invite link is shown and copied, for the other player to
      join with wiregram game INVITE (or a click in a wiregram chat).
      NAME is shown to the other player ($USER by default). Hosting needs
      cloudflared.
  wiregram reset [-y]
      Sign out of every account and delete all local data, as Settings >
      Reset does. Asks first unless -y is given.
  wiregram --version
      Print the version.
  wiregram --debug
      Run the app, writing the steps of each Mattermost sign-in to
      ~/.config/wiregram/debug.log: the server's status and error, never
      the password, code or token.

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
    s                 In a widget's modal: open the plugin's settings

  Chat list
    Up/k, Down/j      Select chat, chip, section or account
    Enter             Open chat (on email, a folder) or chip, switch to the
                      selected account, fold a section, or open a bot's
                      settings or create one (new bot…), under bots
    Space             Fold or unfold the selected chat's section
    c                 Compose a mail (email)
    Ctrl+F            Search chats, people, groups and channels (a modal
                      over the screen); on Mattermost picking a public
                      channel joins it
    u                 Unread: cards for every chat with unread messages
    m                 Mentions: cards for the groups and channels with the
                      latest messages mentioning you (not email)
    a                 Accounts (add, sign in, retry, Settings…)
    r                 Refresh chats
    Alt+D             Delete private chat (you only)
    Ctrl+Alt+D        Delete private chat (both sides)
    Esc               Close the open chat (wide terminals), else back to Accounts

  Unread / Mentions / Search results
    Up/k, Down/j      Select card
    Enter             Open the chat; a mention or a match opens selected, in
                      its thread on Mattermost
    t                 Open the newest reply's thread (Mattermost)
    m                 Mark the chat read (Unread)
    s                 Summarise the card's messages (Unread)
    Ctrl+F            Search
    r                 Reload
    Esc               Back

  Search / Forward to / Mention (modals)
    Type              Filter
    Up/Ctrl+P, Down/Ctrl+N  Select
    PgUp/PgDn         Page
    Enter             After typing, search all chats' messages (the first
                      row, Telegram, Mattermost and Matrix); else open the
                      chat, go to the message (search from a chat), forward
                      the message to the chat, or insert the mention
    Esc               Clear the query, then close; Mention closes at once
                      and types the @ and query as written
    Click outside     Close

  Settings
    Up/k, Down/j      Select an account on the left, or a setting on the right
    Enter             Show the account's settings, or change the selected one:
                      open or sign in, reconnect,
                      how long mail stays cached (email), remove account
    Tab, Right/l      Move to the account's settings
    Space             Change the selected setting
    Themes…           Pick a theme (Zest, Copper) from sample cards with the
                      arrows; Enter applies it to every screen
    Chat…             Enter switches the nick style: nick: (text right after
                      the name) or <nick> (names padded to one column), or
                      Keep chats in memory: a reopened chat draws at once,
                      then refreshes from the server (on by default)
    AI…               Enter on Summarise with or Rewrite with gives the task
                      to the next provider; on a provider it opens its name,
                      URL, key variable and model, each edited in place, or
                      deletes it (not while a bot uses it); Add provider…
                      asks step by step
    Reset…            Sign out of every account and delete all local data
                      (settings, plugins, bots, cached mail and media),
                      then start over as a fresh installation
    Home              Back to the account chooser
    Esc, Left/h       Back to the accounts, then to where Settings was opened,
                      or quit when there is nowhere to go back to

  Chat
    Up/Down           Select message (Up at the top loads older ones)
    Enter             Send text; with an empty input, open the selected
                      message's actions
    Ctrl+U            Upload a file, in a modal over the chat (or click
                      upload+ at the end of the input)
    Ctrl+F            Search, in a modal over the chat: chats and people,
                      then this chat's messages (Enter or a click goes to one)
    @                 Mention a member (group chats, at the start of a word):
                      channel members first on Mattermost; Telegram members
                      with a username; Matrix room members; WhatsApp group
    Alt+T             Open the selected message's thread beside the chat
    Alt+G             Challenge the chat to a game (DMs and groups; or click
                      game+ at the end of the input). Needs cloudflared
    Alt+H             Toggle private mode (hide history)
    Alt+R             Rewrite the draft: pick a style (shorter, clearer,
                      formal, friendly, fix grammar, translate to English)
                      and the rewrite streams into a card over the input;
                      Ctrl+Y puts it in the input, Ctrl+X or Esc drops it
    Alt+D             Delete chat (you only)
    Ctrl+Alt+D        Delete chat (both sides)
    Ctrl+Y            Approve the bot's suggested reply (empty input)
    Ctrl+E            Edit it: the text moves into the input, sent as yours
    Ctrl+X            Discard it
    Esc               Cancel the reply, else back to Unread (or Mentions)
    Ctrl+C            Close chat
    Alt+L             Lock the chat (it also locks when the terminal loses focus)
    Any key           Unlock the locked chat

  Email folder (a list of conversations under day headings, the newest first)
    Up/k, Down/j      Select a conversation, or a mail in an expanded one
    PgUp/PgDn, g/G    Page, top, bottom
    Space             Expand or collapse the conversation
    Left, Right       With no mail open, move to the sidebar and back
                      (wide terminals)
    Enter             Read the mail (a conversation: its newest), marking it
                      read; panes 120+ columns wide open the reader beside
                      the list, focused; ] and [ move between them
    c                 Compose a new mail
    r, R, f           Reply, reply to all, forward
    d                 Move to Trash
    a                 Archive (where the server has an archive)
    m                 Move to another folder or label (a picker)
    u                 Mark read, or the newest mail unread
    /                 Search the folder on the server (Esc clears the results)
    v                 Visual mode: Space picks conversations or mails, then
                      d, a, m or u acts on them all; Esc leaves it
    Esc               Close the reader, else back

  Mail reader
    Up/k, Down/j      Scroll
    PgUp/b, PgDn/Space  Page
    Home/g, End/G     Top, bottom
    o, s              Open the attachment, or save it to ~/Downloads
    r, R, f, d, a, m, u  As in the folder, on this mail
    Enter             Type a quick reply in the box under the mail; Enter
                      sends it (quoting the mail), Esc stops typing
    Esc               Close

  Mail composer
    Tab, Shift+Tab    Next and previous field: To, Cc, Subject, Attach, text;
                      in To and Cc, Tab first takes the address offered
    Enter             In Attach, attach the file typed (Tab completes paths)
    Backspace         In an empty Attach, drop the last file
    Ctrl+S            Send
    Esc               Close, keeping the draft (c opens it again)
    Ctrl+X            Discard the draft

  Game (a pane beside the chat; a click on a 🎮 invite joins its game)
    Keys              Play: each game shows its own keys under the board
    Tab               Move between the sidebar, the chat and the game
    Esc               Leave the game (it asks first while one is being played)

  Thread (opens by itself on a threaded message in wide terminals, and on a click
  beside a message's text; also Alt+T, a click on a replies chip, or Thread in
  the actions)
    Up/Down           Move through the chat's messages
    Enter             Reply in thread; with an empty input, open the
                      selected message's actions
    Ctrl+U            Upload a file into the thread (or click its upload+)
    @                 Mention a member, as in the chat
    Esc               Close thread

  Upload (a modal over the faded chat; a click outside closes it)
    Type              A path, starting in the last folder; the entries it
                      matches are listed under it
    Up/Down, Tab      Choose an entry, put it in the path
    Enter             Open the folder or pick the file (or click it), then
                      send with the optional caption
    Esc               Back, then close

  Message actions (over the faded screen)
    Up/Down           Select an action: reply, thread, go to quote, edit,
                      forward, open/play/save attachment, pin, delete
    Type              Search reactions by name in the "reaction:" field
                      (Backspace deletes); the emoji row shows the matches
    Left/Right        Select a reaction on the emoji row
    Enter             Do it (deletes ask first)
    Esc               Close (a click outside it closes it too)

  Bot (a row in the Bots section)
    p                 Pause or resume the bot (the chip on top)
    Up/k, Down/j      Select a setting: name, @handle, provider,
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
    Space             Pick the radio row, or tick or untick the person,
                      saved at once;
                      chats another bot answers show its name and cannot
                      be ticked (all is unavailable while another bot
                      answers any)
    Type              Under Selected users, filter the people recently
                      chatted with; on Telegram and Mattermost it also
                      searches everyone, for a DM yet to start
    Enter, Esc        Close (a click outside it closes too)

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
	showVersion := flags.Bool("version", false, "")
	debug := flags.Bool("debug", false, "")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usageText)
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if *showVersion {
		fmt.Println("wiregram", version)
		return nil
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unknown command %q; wiregram -h lists the commands", flags.Arg(0))
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return fmt.Errorf("wiregram needs an interactive terminal")
	}
	for {
		dir, err := config.Dir()
		if err != nil {
			return err
		}
		err = session(ctx, dir, *debug)
		if _, reset := err.(tui.ResetRequested); !reset {
			return err
		}
		if err := config.Wipe(dir); err != nil {
			return err
		}
	}
}

// session runs wiregram on the data in dir until it quits or is reset. With
// debug, sign-in steps go to dir/debug.log.
func session(ctx context.Context, dir string, debug bool) error {
	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	var debugLog *log.Logger
	if debug {
		f, err := os.OpenFile(filepath.Join(dir, "debug.log"),
			os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open debug log: %w", err)
		}
		defer f.Close()
		debugLog = log.New(f, "", log.LstdFlags)
	}
	if err := account.ImportLegacy(db, dir); err != nil {
		return err
	}
	cfg, err := config.LoadConfig(db)
	if err != nil {
		return err
	}
	accounts, err := account.LoadAccounts(cfg, dir, db, debugLog)
	if err != nil {
		return err
	}
	return tui.RunAccounts(ctx, accounts)
}

// isTerminal reports whether f is a terminal rather than a pipe or file.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
