# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is the single source of project instructions. (It replaces the former
AGENTS.md, whose durable rules were folded in here.)

## Project

Wiregram is a minimal multi-backend chat client for the terminal, in Go. No cgo. Mouse input
is the left and right click and the wheel. The wheel moves the selection of the list under the pointer
as ↑/↓ would, without moving focus. A click on a row selects it and acts as Enter. A click on
a chat message's attachment (image card or `[tag]` row) opens it with `xdg-open`;
a left click elsewhere on a message selects it and a right click on a message opens its
actions modal. The login and connecting
screens leave the mouse to the terminal. Besides the TUI, `wiregram accounts|chats|stream|send|dry-run|reset`
(`cmd/wiregram/cli.go`) print JSON Lines for scripts and AI agents, naming accounts by backend
and handle (`Account.Handle`); WhatsApp is refused there since a second connection would
replace the app's. `skills/wiregram/SKILL.md` teaches agents to use them. The UI is BubbleTea v2 with lipgloss and bubbles; gotd/td provides MTProto. The entry point is `cmd/wiregram`; all other code lives in packages under `internal/`.

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
  its go-message and go-sasl) and golang.org/x/oauth2 for Google sign-in,
  and github.com/yuin/gopher-lua (pure Go Lua 5.1) for plugins.
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
- **Docs follow the code.** A change to shortcuts or user-visible behaviour updates
  `usageText` and the matching pages under `docs/` (the VitePress site). The site's
  npm packages stay pinned with a committed lockfile; add none beyond vitepress.

## Display conventions

- The UI is a BubbleTea TUI styled with lipgloss; shared colors, boxes,
  footer and account tags live in `internal/tui/render.go`. Kitty graphics sequences are
  used by the media viewer (mpv via `tea.Exec`) and for inline images in kitty and Ghostty.
- Startup shows the `wiregramLogo` splash (`internal/tui/splash.go`) for about a second.
- There is no header bar. Inside an account the screen is the body and a
  `bubbles/help` footer with the notice on the right. Help hides keys the
  backend's `capabilities` rule out. The footer is never expanded; the full
  shortcut list lives only in `wiregram -h` (`usageText`).
- The sidebar is an account tree (`internal/tui/list.go`): every
  account is a row (`▸`/`▾`, status glyph, `TG1` tag, label, a violet `✦` when it
  has a Chat Agent), the active one expanded to its chats, archive, search and,
  except on email, its agent row (`New agent…`, then `Agent: NAME`). Enter on
  another account switches to it. There are no modes.
- After the splash comes the account chooser (`internal/tui/account.go`) with the
  logo; its `Settings…` row and the sidebar's last row
  open Settings (`internal/tui/settings.go`): accounts on the left (then Add account,
  Themes, Plugins, AI providers, Reset, Quit), the selected account's settings on the right, each a row chosen
  with the arrows and changed with Enter — no per-setting shortcuts. Values sit in a column
  beside their labels (`formRow`), never at the far edge. Removing an account logs out on the server
  (`backend.LogoutBackend`) when online, then deletes its local data and agent
  (with its `agent_sent` rows): its `wiregram.db` rows, plus the directory of a WhatsApp account. Reset removes every account that way, then `main` deletes the config
  and cache directories (`config.Wipe`) and starts again as a fresh install.
- Inside the account chooser's box, under Quit, sits an unread digest (`internal/tui/digest.go`):
  a grid of cards, one per online account, each fetched once when the account comes
  online, showing its unread total and busiest chats. An account's Unread digest
  setting leaves it out (`store.DigestSkip` in `account_data`). Clicking a card opens the account.
- Lua plugins (`internal/plugin`, `docs/features/plugins.md`) each run in their own
  sandboxed VM: no `os`/`io`/`debug`/`require`, every call under a deadline,
  HTTPS only to the hosts the plugin declares. Built-ins are embedded from
  `internal/plugin/builtin` and start off; user plugins in
  `~/.config/wiregram/plugins/` start on. Enabled plugins with a widget draw
  a card in the chooser's `── Widgets` section under the digest, which
  gives way to the digest when space is short (`internal/tui/widgets.go`).
  Settings' Plugins… row switches them and edits their settings
  (`internal/tui/plugins.go`). The built-ins are Weather (Open-Meteo) and Hacker News
  (the Algolia HN API).
- Each expanded account has an Unread row (`u`, `internal/tui/unread.go`): a column
  of full-width cards, one per chat with unread messages. The card border and tag show the
  kind: DM green, group blue, channel yellow. Replies get `↳` and a `thread` tag.
- A message that mentions the user (`Message.Mentioned`: `@you` or a chat-wide
  `@all`/`@channel`/`@here`/`@room`, never a plain reply) is tinted with the theme's
  `mentionBg` in the chat and the thread pane; selection's tint wins. Backends with
  `backend.MentionFetcher` get a Mentions row (`m`) under Unread: the same cards,
  one per group or channel with recent mentions (Telegram's MyMentions search over
  recent groups, Mattermost's mention search, Matrix highlight notifications,
  WhatsApp's in-memory messages). Email has none.
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
  below; an original older than the loaded history is fetched by ID
  (`backend.QuoteFetcher`) or, on WhatsApp, taken from the copy the reply
  carries (`Message.Quote`), else it reads `earlier message`. Under the text come any image or preview card, then a dim meta row:
  reactions (your own count in copper) · reply count · pinned · `✓`/`✓✓`.
  Media `[tags]` are dim and links are underlined.
  Formatting reaches the TUI as `Message.Spans` (bold, italic, strike, code,
  link), which each backend fills from its own markup: Telegram entities,
  `backend.Markdown` for Mattermost and formatted Matrix bodies,
  `backend.WhatsApp` for WhatsApp. `Message.Source` keeps the text as written for editing. Threads
  (`internal/tui/thread.go`) are built from loaded reply chains; opening one
  fetches the whole thread once from backends that keep threads on the server
  (`backend.ThreadFetcher`, Mattermost).
- Search users (`/`), the forward picker and the mention picker are one modal
  (`userSearchModel`, `internal/tui/search.go`) drawn over the screen with
  `fadeUnder`: search users centred on the body, forward and mention on the chat.
  `@` at the start of a word in a group chat's composer opens the mention picker
  when the backend implements `backend.Mentioner`; a pick inserts the
  candidate's `Mention` text and each backend turns that text into a native
  mention: `@username` on Mattermost (channel members, then `not in channel`,
  from `/users/autocomplete`) and Telegram (members with a username), the MXID
  on Matrix (`m.mentions`), `@number` on WhatsApp (`MentionedJID`). Email has
  no mentions.
- Messages have no per-message shortcuts. Enter on an empty composer (always,
  in email chats and the mail reader) or a right click opens the actions modal
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
  in a rounded card. In kitty and Ghostty outside tmux (`kittyGraphics`, from `TERM`,
  `TERM_PROGRAM` and `KITTY_WINDOW_ID`) they are kitty images sent with `tea.Raw` and drawn
  with Unicode placeholder cells (`internal/tui/kitty.go`), one terminal image per card size,
  blanked under modals. Elsewhere they use truecolor `▄` half-blocks (`internal/halfblock`). The card is
  titled with the media label, the caption inside under the image, in the chat
  and the thread pane. Only messages on screen are fetched; downloads live in
  `os.UserCacheDir()/wiregram/media`, shared with click-to-open, and are pruned
  after 7 days unseen. View-once and self-destructing media never load inline.
  Videos (`[video]`, `[video note]`, `[gif]`) get a small card of the same kind with a
  play mark beside their details (`videoCard`); nothing is fetched for it, and a click opens
  the file.
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
- Themes live in `internal/tui/theme.go`: a fixed `themes` table (Zest, the
  default, from the website's lime and purples, then Copper), one entry per theme.
  The accent, subtle, dim, selection and badge colours come from a `*styles`
  that each screen loads from the `theme` setting and passes down; status,
  chat-kind, link and nick colours are the same in every theme. Settings'
  Themes… row shows a sample card per theme and Enter applies one.
- Chat Agents belong to accounts, one each (`agents.account_key`). Providers live in
  Settings' AI providers… (`internal/tui/providers.go`). An account's agent row creates the
  agent through the scripted wizard (`internal/tui/agentwiz.go`), then opens the agent pane
  (`internal/tui/agentpane.go`) in the chat's place: Settings-style rows, including which
  private chats it answers (all, or those in `agent_chats`). The theme's agent colour
  (violet, `styles.agent`) and `agentIcon` `✦` mark accounts with an agent, the DMs it
  answers while on, chats with a suggestion waiting and the messages an agent sent; those are recorded in `agent_sent`
  and drawn with `✦ NAME` in the nick column (`Message.Agent`). An "ask me first" agent's
  draft is a card over the composer (`suggestionCard`, `internal/tui/chat.go`): Approve
  (^Y) sends it as the agent, Edit (^E) moves it into the composer as the user's own,
  Discard (^X) drops it. On backends with server threads (`backend.ThreadFetcher`) an agent
  answers in the message's own thread, with the thread as context (`replyTarget`); elsewhere
  it quotes the message. `wiregram dry-run` drafts replies without sending
  (`agentRuntime.DryRun`).
- Wizards (`internal/tui/wizard.go`) take fixed options or typed values only, never free
  chat with a model. Adding an account (network, server URL), an AI provider or an agent,
  and the sign-in prompts are form modals centred over
  the screen they were opened from, faded behind it (`styles.overlay`): answered
  steps as label and value rows, then the current question (`wizardModel.formView`).
- Every yes/no question (quit, delete chat/message/agent/provider, remove account, Reset) is a
  `confirmModal` (`internal/tui/confirm.go`) centred over the screen, or over the chat when asked
  there, never a footer notice. Deletes and Reset start on Cancel.
- A newly added account opens with an `Account connected` modal (`internal/tui/welcome.go`)
  over its screen, faded behind it: navigation and search keys; any key or click closes it.
- Chat Agents answer incoming DMs only (`BackendEvent.DM`); they never act in
  groups or channels and never start a chat. Email publishes no DM events,
  so agents never answer mail.

## Architecture

```
cmd/wiregram/        main(): flags, database and config load, hands off to tui;
                     cli.go: the non-interactive commands
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
  agent/             SQLite store for providers, per-account agents, their chats,
                     message history and the record of what agents sent
  plugin/            Lua plugin VMs, their API (settings, kv, http, widgets)
                     and the embedded built-in plugins
  account/           account manager (discovery, connect, login state) and the
                     account-owned agent runtime (and its dry runs)
  tui/               BubbleTea screens and the account/mode loop (run.go)
docs/                VitePress docs site (make docs, make deploy-docs)
skills/wiregram/     SKILL.md for AI agents using the CLI commands
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
  (API credentials, autolock, mail retention, theme), `account_data` (identity,
  Telegram session and Matrix sync token per account key), the Chat Agent
  tables, the email cache (`mail_messages`, `mail_sync`) and plugins'
  key-value rows (`plugin_kv`); plugin settings are `plugin.NAME.*` settings
- `plugins/` — user Lua plugins (`NAME.lua` or `NAME/init.lua`)
- `accounts/whatsapp-*/` — whatsmeow's own `whatsapp.db` for each WhatsApp account.

Accounts live only in `account_data`: an account is a key (`<backend>-<number>`,
or `primary` for the original Telegram account) with an `identity` row. Only
WhatsApp gets a directory, named by its key.

Directories are `0700`; `wiregram.db` is `0600`. Wiregram writes no JSON files.
`account.ImportLegacy` imports the files older versions wrote (`config.json`,
`account.json`, `session/session.json`, `sync_token`) once at startup and
renames each to `*.bak`, then removes the empty per-account directories they made
for every backend but WhatsApp.
