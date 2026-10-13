# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is the single source of project instructions. (It replaces the former
AGENTS.md, whose durable rules were folded in here.)

## Project

Wiregram is a minimal multi-backend chat client for the terminal, in Go. No cgo. Mouse input
is the left click and the wheel. The wheel moves the selection of the list under the pointer
as ↑/↓ would, without moving focus. A click on a row selects it and acts as Enter. A click on
a chat message's attachment (image card or `[tag]` row) opens it with `xdg-open`;
a click elsewhere on a message opens its actions modal. The login and connecting
screens leave the mouse to the terminal. The UI is BubbleTea v2 with lipgloss and bubbles; gotd/td provides MTProto. The entry point is `cmd/wiregram`; all other code lives in packages under `internal/`.

## Build

```sh
make                                   # CGO_ENABLED=0 go build -buildvcs=false -o wiregram ./cmd/wiregram
```

To embed app credentials (users then skip the API-ID prompt):
```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X wiregram/internal/config.buildAPIID=${TG_API_ID} -X wiregram/internal/config.buildAPIHash=${TG_API_HASH}" \
  -o wiregram ./cmd/wiregram
```
Google sign-in for email accounts needs a Google OAuth desktop client, embedded the same way
with `-X wiregram/internal/config.buildGoogleClientID=…` and `…buildGoogleClientSecret=…`;
without one, Gmail accounts sign in with an app password.

## Lint / Format

```sh
gofmt -w cmd internal
go vet ./...
```

There is no test suite. Changes are verified by building and manual testing.
When a feature needs verification, write a manual checklist rather than
adding a test framework.

## Hard rules

These are project constraints, not suggestions:

- **No new dependencies** beyond the approved set: gotd/td,
  x/crypto, nhooyr.io/websocket, go.mau.fi/whatsmeow,
  modernc.org/sqlite, charm.land/bubbletea/v2, charm.land/lipgloss/v2,
  charm.land/bubbles/v2 (its list component pulls in
  github.com/sahilm/fuzzy, approved as a transitive dependency),
  github.com/charmbracelet/x/ansi (already pulled in by bubbletea;
  textfmt measures cell widths with its `WcWidth` so padding matches the
  renderer), and
  golang.org/x/image for scaling and WebP decoding of inline images, and
  for the email backend github.com/emersion/go-imap/v2 (pinned beta, with
  its go-message and go-sasl) and golang.org/x/oauth2 for Google sign-in.
  Standard library for everything else — no logging or CLI libraries.
- **No cgo.** Must build with `CGO_ENABLED=0`.
- **No TUI frameworks** other than charm.land/bubbletea/v2 and its
  companion libraries (lipgloss, bubbles). tview and tcell remain
  prohibited.
- **YAGNI.** Build what is asked for and nothing more.
- **No panics.** Wrap and return errors: `fmt.Errorf("context: %w", err)`.
  Never a bare string error.
- **No `log.Fatal` or `os.Exit`** outside `main()`.
- **No `init()` functions. No global mutable state.** Pass dependencies explicitly.
- **Every user-facing error is one plain English line**: a notice in the
  TUI, or stderr outside it. Keep going where possible; exit 1 only when fatal.
- `gofmt` formatted, soft 100-column lines, comments only where the *why* is
  non-obvious.

## Display conventions

- The UI is a BubbleTea TUI styled with lipgloss; shared colors, boxes,
  footer and account tags live in `internal/tui/render.go`. Kitty graphics sequences are
  used only by the media viewer (mpv via `tea.Exec`).
- Startup shows the `wiregramLogo` splash (`internal/tui/splash.go`) for about a second.
- There is no header bar. Inside an account the screen is the body and a
  `bubbles/help` footer with the notice on the right. Help hides keys the
  backend's `capabilities` rule out. The footer is never expanded; the full
  shortcut list lives only in `wiregram -h` (`usageText`).
- The chat mode sidebar is an account tree (`internal/tui/list.go`): every
  account is a row (`▸`/`▾`, status glyph, `TG1` tag, label), the active one
  expanded to its chats, archive and search. Enter on another account
  switches to it. The agents mode sidebar lists the agents.
- After the splash comes the account chooser (`internal/tui/account.go`) with the
  logo; its `Settings…` row and the sidebar's last row
  open Settings (`internal/tui/settings.go`): accounts on the left (then Add account,
  Chat Agents, Quit), the selected account's settings on the right, each a row chosen
  with the arrows and changed with Enter — no per-setting shortcuts. Removing an account logs out on the server
  (`backend.LogoutBackend`) when online, then deletes its local data and agent
  binding; the primary account's directory is the config directory, so only its
  rows are deleted.
- Inside the account chooser's box, under Quit, sits an unread digest (`internal/tui/digest.go`):
  a grid of cards, one per online account, each fetched once when the account comes
  online, showing its unread total and busiest chats. An account's Unread digest
  setting leaves it out (`store.DigestSkip` in `account_data`). Clicking a card opens the account.
- Each expanded account has an Unread row (`u`, `internal/tui/unread.go`): a column
  of full-width cards, one per chat with unread messages. The card border and tag show the
  kind: DM green, group blue, channel yellow. Replies get `↳` and a `thread` tag.
- At `twoPaneWidth` (100) columns and wider the chat list is a sidebar beside
  the chat and Tab moves focus; narrower terminals show one pane at a time.
- The root `tuiModel` owns the screen's only bus subscription and routes
  events for the open chat into it, so the list and chat never race.
- `>` marks the selected row in lists. The selected message in a chat is
  tinted with `colorSelBg` across its whole block (`shadeLine`); selection
  never moves a column.
- Every backend's messages share one block (`messageBlock`,
  `internal/tui/chatlines.go`), in the chat and the thread pane: a 2-cell
  gutter (`*` when edited live), `[HH:MM] <sender>` with a fixed-width nick
  column, then the text; the user is always `you`. Channels drop the nick
  column. A reply puts `┃ sender: excerpt` on the header row with its text
  below; an original older than the loaded history is fetched by ID where the
  backend can (`backend.QuoteFetcher`, Telegram only), else it reads
  `earlier message`. Under the text come any image or preview card, then a dim meta row:
  reactions (your own count in copper) · reply count · pinned · `✓`/`✓✓`.
  Media `[tags]` are dim and links are underlined. Threads
  (`internal/tui/thread.go`) are built from loaded reply chains only.
- Messages have no per-message shortcuts. Enter on an empty composer (always,
  in email chats and the mail reader) or a click opens the actions modal
  (`internal/tui/msgmenu.go`) for the selected message: when the backend has
  reactions, a `reaction:` search field (typing in the modal filters) over the
  matching emoji row, then only the actions the message allows. The root draws
  it centred over the chat with everything behind it reduced to plain dim
  text (`fadeUnder`); the footer shows the modal's keys.
- Private chats show the first name, else username, else `Deleted Account`.
  Groups show the title; channels show `Channel: TITLE`.
- Non-text messages render as bracket tags (`[photo] 1280x720, 245 KB`), with
  ` — ` before a caption. See `internal/telegram/media.go` for the full set.
- A message with a link preview gets a small rounded card (site, title, two
  lines of description) under its text, in the chat and the thread pane.
  Previews come only from the server:
  Telegram webpages, Mattermost OpenGraph embeds, WhatsApp link fields, and
  the Matrix homeserver's `preview_url`, fetched once per link in the
  background. Wiregram never fetches linked pages itself.
- Still images (`Message.Image`, up to `backend.MaxInlineImage`) draw inline
  with truecolor `▄` half-blocks (`internal/halfblock`) in a rounded card
  titled with the media label, the caption inside under the image, in the chat
  and the thread pane. Only messages on screen are fetched; downloads live in
  `os.UserCacheDir()/wiregram/media`, shared with click-to-open, and are pruned
  after 7 days unseen. View-once and self-destructing media never load inline.
- Chat text wraps at 120 columns or the terminal width, whichever is smaller.
  Multiline messages keep their line breaks.
- Email (`internal/mail`, tag `EM`) is one chat per correspondent. A mail's
  bold subject leads its block and its text stops after `mailPreviewLines`;
  Enter opens the reader (`internal/tui/mail.go`) with headers and the whole
  text. Email chats write in a multiline draft sent with Ctrl+S. Quoted
  originals are trimmed in the chat, kept in the reader. HTML mail is reduced
  to text and nothing it references is fetched. Mail is cached in
  `wiregram.db` for the Keep mail cached setting of an email account's Settings (`mail_retention_days`,
  default 90); pruning never touches the server, and deleting moves to Trash.
- Agent mode (Alt+A, `internal/tui/agent.go`) swaps the copper accent
  (`colorAccent`, 214) for violet (`colorAgent`, 141) and tags accounts as
  `[TG1]`: backend plus position in the account list. Chat Agents are created through the
  scripted bubble wizard (`internal/tui/wizard.go`): fixed options or typed
  values only, never free chat with a model. Adding an account (network,
  server URL, then the sign-in prompts) uses the same bubble style in copper.
- Chat Agents answer incoming DMs only (`BackendEvent.DM`); they never act in
  groups or channels and never start a chat. Email publishes no DM events,
  so agents never answer mail.

## Architecture

```
cmd/wiregram/        main(): flags, database and config load, hands off to tui
internal/
  textfmt/           pure text helpers: plain(), wrapping, cell widths, nick colours
  halfblock/         decodes, scales and draws images as truecolor half-blocks
  store/             wiregram.db: settings and per-account data (identity,
                     Telegram session, Matrix sync token)
  config/            settings loading
  backend/           the Backend interface and shared types (ChatEntry, Message,
                     events, capabilities, emoji catalog, upload classification,
                     the mpv command)
  telegram/          gotd/td client and backend (dialogs, history, media, search…)
  matrix/            Matrix backend
  mattermost/        Mattermost backend
  whatsapp/          whatsmeow backend
  mail/              IMAP/SMTP email backend and its mail cache tables
  agent/             SQLite store for agents, providers and message history
  account/           account manager (discovery, connect, login state) and the
                     account-owned agent runtime
  tui/               BubbleTea screens and the account/mode loop (run.go)
docs/                design notes and plans
assets/              logo and other static files
```

Imports only point downwards in the list above: `textfmt` imports nothing
internal, backends never import each other or `tui`, and only `tui` and
`cmd/wiregram` import `account`. Keep it acyclic — put shared helpers in the
lowest package that needs them rather than importing sideways.

### Concurrency model

- BubbleTea owns stdin. All UI state lives in the models and is touched only
  by `Update`; background work returns as `tea.Msg` values from commands.
- Every backend publishes `backend.Event`s for every chat on its own
  `backend.Bus` (`internal/backend/bus.go`), regardless of what the UI shows.
  Consumers subscribe with a `Filter` applied at publish time: the TUI screen
  (everything), the inbox watcher (`internal/account/inbox.go`, unseen counts
  for every online account) and the agent runtime (DM messages only, text-only
  when the agent is). `SetActive` only scopes unread suppression and typing.
- `Publish` never blocks and no lock is held across a channel operation;
  backends release their own mutex before publishing. Lock order is
  `agentRuntime.mu` → `Bus.mu` → `Subscription.mu`.
- The inactivity lock lives in the chat model (`lockTick`); media plays through
  `tea.ExecProcess`.
- Read receipts are batched with a timer to avoid flooding Telegram.

### Config and session paths

`os.UserConfigDir()/wiregram/` (typically `~/.config/wiregram/`):
- `wiregram.db` — the single SQLite database (`internal/store`): `settings`
  (API credentials, autolock, mail retention), `account_data` (identity,
  Telegram session and Matrix sync token per account key), the Chat Agent
  tables and the email cache (`mail_messages`, `mail_sync`)
- `accounts/<backend>-*/` — one directory per added account; its name is the
  account key (`primary` for the original Telegram account). WhatsApp keeps
  whatsmeow's own `whatsapp.db` here.

Directories are `0700`; `wiregram.db` is `0600`. Wiregram writes no JSON files.
`account.ImportLegacy` imports the files older versions wrote (`config.json`,
`account.json`, `session/session.json`, `sync_token`) once at startup and
renames each to `*.bak`.

## Planned work

`docs/TASKS.md` sequences the remaining work.
