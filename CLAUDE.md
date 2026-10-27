# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is the single source of project instructions. (It replaces the former
AGENTS.md, whose durable rules were folded in here.)

## Project

Wiregram is a minimal multi-backend chat client for the terminal, in Go. No cgo. Mouse input
is the left and right click and the wheel. The wheel moves the selection of the list under the pointer
as ↑/↓ would, without moving focus. A click on a row selects it and acts as Enter. A click on
a chat message's attachment (image card or `[tag]` row) opens it with `xdg-open`;
a left click on a reaction in its meta row toggles the user's own (`reactionAt`, with a short
blink); a left click on the sender's name or an `@name` (`mentionAt`, `Profiler.MentionedUser`)
opens their profile modal (`backend.Profiler`, `internal/tui/profile.go`) on backends
that have one, whose go to chat row (Enter) opens the DM (`Profiler.DirectChat`); a left click elsewhere on a message selects it and a right click on a message opens its
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

- **No new dependencies** beyond the approved set: gotd/td (v0.162+ for
  managed bots; it pulls in github.com/gotd/log,
  github.com/refraction-networking/utls, github.com/andybalholm/brotli and
  github.com/cespare/xxhash/v2, approved as transitive dependencies),
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
- The sidebar is an account tree (`internal/tui/list.go`), `sidebarWidth` (40) wide:
  every account is a row (`▸`/`▾`, status glyph, `TG` tag on a fill that turns accent
  when expanded, number, label, unseen count while collapsed, a violet `✦` when it
  has a bot), the active one expanded to a line of chips, each a stop for ↑/↓: unread,
  @ mentions, you (`backend.SelfChatter`: the chat with oneself on Telegram, Mattermost
  and WhatsApp), archived (Telegram only) and / search. Then its chats in sections under
  ruled headers that Space, Enter or a click folds for the session (`folded`): the team
  (`ChatEntry.Section`, whose `[Team] ` title prefix the sidebar drops) on Mattermost,
  else groups (rooms on Matrix) and channels (`ChatEntry.IsChannel`, without
  `Channel: `), then direct (`entrySection`). Rows start with `#`, `@`, or `✦` when a
  bot answers the chat; unread chats come first with an accent count, read ones dimmed.
  Then a bots section: a `✦ NAME @handle` row per bot with its chat count, then
  `+ new bot…`. An email account is expanded to compose and labels chips
  (`backend.Labeler`, the modal in `internal/tui/labels.go` that shows or hides each
  label), then its folders in server order. home and settings… are pinned under the
  tree, which scrolls above them. The selected row has an accent `▎` in its first
  column and, when focused, the selection tint. Enter on another account switches to
  it. There are no modes.
- After the splash comes the account chooser (`internal/tui/account.go`) with the
  logo: an `accounts` box padded two cells inside its border, one row per account in
  columns (`›` caret, number, glyph, service tag on a fill that turns accent when
  selected, label, status, a chip per bot), the actions indented under them, the widget
  cards with a one-cell gutter, and a filled key strip (`keyStrip`) for its footer; its `Settings…` row and the sidebar's last row
  open Settings (`internal/tui/settings.go`): accounts on the left (then Add account,
  Themes, Chat, Plugins, AI providers, Reset, Home, Quit), the selected account's settings on the right, each a row chosen
  with the arrows and changed with Enter — no per-setting shortcuts. Values sit in a column
  beside their labels (`formRow`), never at the far edge. Removing an account logs out on the server
  (`backend.LogoutBackend`) when online, then deletes its local data and bots
  (with their `agent_sent` rows): its `wiregram.db` rows, plus the directory of a WhatsApp account. Reset removes every account that way, then `main` deletes the config
  and cache directories (`config.Wipe`) and starts again as a fresh install.
- Lua plugins (`internal/plugin`, `docs/features/plugins.md`) each run in their own
  sandboxed VM: no `os`/`io`/`debug`/`require`, every call under a deadline,
  HTTPS only to the hosts the plugin declares. Built-ins are embedded from
  `internal/plugin/builtin` and start off unless they declare `on`; user plugins in
  `~/.config/wiregram/plugins/` start on. Enabled plugins with a widget draw
  a card in the chooser's `── widgets` section under Quit (`internal/tui/widgets.go`);
  `render` may name the card's border colour, and its lines may hold markup (`*bold in the accent*`,
  `__on the accent fill__`, backslash escapes; `wiregram.escape` for outside text). A widget's `click` may open a plugin
  modal (`wiregram.modal`, its own width and height, scrolling when render returns more
  lines) centred over the chooser, faded behind it, over a key line (`widgetModalKeys`:
  close, and settings, which opens the plugin's row in Settings' Plugins). A plugin that declares the
  `messages` permission may call `wiregram.accounts()` and `wiregram.unread()`, served by
  `pluginHost` (`internal/tui/pluginhost.go`) through `plugin.Host`.
  Settings' Plugins… row switches them and edits their settings
  (`internal/tui/plugins.go`). The built-ins are Unread (unread messages per account, DMs
  apart from the rest; a click shows their newest messages), Weather (Open-Meteo; a click shows today hour by
  hour), Calendar (a Google Calendar's secret iCal URL, a `secret` setting Settings
  masks; a click lists upcoming and recent events) and Hacker News (the Algolia HN API).
- Each expanded account but email has an unread chip (`u`, `internal/tui/unread.go`): a column
  of full-width cards as tall as their messages, one per chat with unread messages. The
  top border holds `#`/`@`, the name and a team tag, with the newest message's time
  (`whenText`) and the count on the right; borders are dim, the selected card's accent.
  Authors sit in a fixed column (none in channels), replies get `↳`, repeats collapse to
  `×N`, media is a tag, emoji codes are dim. The selected card lists its keys: `t` opens
  the newest reply's thread on `backend.ThreadFetcher` backends, `m` marks the chat read.
- A message that mentions the user (`Message.Mentioned`: `@you` or a chat-wide
  `@all`/`@channel`/`@here`/`@room`, never a plain reply) is tinted with the theme's
  `mentionBg` in the chat and the thread pane; selection's tint wins. Backends with
  `backend.MentionFetcher` get a mentions chip (`m`) beside it: the same cards,
  one per group or channel with recent mentions (Telegram's MyMentions search over
  recent groups, Mattermost's mention search, Matrix highlight notifications,
  WhatsApp's in-memory messages). Email has none.
- At `twoPaneWidth` (100) columns and wider the chat list is a sidebar beside
  the chat and Tab moves focus; narrower terminals show one pane at a time.
- The root `tuiModel` owns the screen's only bus subscription and routes
  events for the open chat into it, so the list and chat never race.
- `>` marks the selected row in lists (the sidebar uses an accent `▎`). The selected message in a chat is
  tinted with `colorSelBg` across its whole block (`shadeLine`), with an accent `▎` in its
  gutter (`gutterBar`) and its time in the accent; selection never moves a column.
- Modals share one look (`modalBox`, `modalField`, `modalRule`, `modalRow`, `modalKeys` in
  `render.go`): an accent box with a lowercase bold title (names keep their case), inputs
  in a dim box of their own with an accent prompt, dim headings followed by a rule, rows
  with a mark column that the selected row turns into an accent `›` over the selection
  tint with an accent `▎` at its edge, and a key strip under a rule at the bottom.
- Every backend's messages share one block (`messageBlock`,
  `internal/tui/chatlines.go`), in the chat and the thread pane: a 2-cell
  gutter (`*` when edited live), `HH:MM sender: text` with later lines under the time,
  or with the `<nick>` style (Settings' Chat…, `nick_style`, `styles.nickColumn`)
  `HH:MM sender` in a fixed-width nick column, then the text; the user is always
  `you`. Channels drop the nick, and a message soon after one from the same sender leaves
  it out (`continues`); every other message gets a blank row above it. A reply puts a dim `↳ sender  excerpt` pointer on the header row with
  its text below, once per run of replies to the same message; an original older than the loaded history is fetched by ID
  (`backend.QuoteFetcher`) or, on WhatsApp, taken from the copy the reply
  carries (`Message.Quote`), else it reads `earlier message`. Under the text come any image or preview card, then a meta row:
  reaction chips (your own count in the accent), a `↳ N replies · last HH:MM` chip (solid
  while its thread is open; a click opens it) · pinned · `✓`/`✓✓`.
  Media `[tags]` are dim, links are underlined, `@names` coloured, emoji codes dim and
  `> ` lines quoted (`styles.inline`). The unread divider counts the unread messages.
  Formatting reaches the TUI as `Message.Spans` (bold, italic, strike, code,
  link), which each backend fills from its own markup: Telegram entities,
  `backend.Markdown` for Mattermost and formatted Matrix bodies,
  `backend.WhatsApp` for WhatsApp. `Message.Source` keeps the text as written for editing. Threads
  (`internal/tui/thread.go`) are built from loaded reply chains; selecting a threaded
  message opens its thread (`followThread`), as do Alt+T, the replies chip and the
  actions' Thread, beside the chat at 5/9 of its width, titled `thread · #chat` with
  `esc close`. The pane starts at
  the top with the root (`blockOpts.root`: day, time and sender on a line, the text from
  the left edge), then the reply count rule and the replies. Opening one fetches the whole
  thread once from backends that keep threads on the server (`backend.ThreadFetcher`,
  Mattermost). Chat and thread titles and composers use `renderTitledBox`/`chatName`
  (`#name` or `@name`, the team tag, `message #name` over a `❯` prompt; both inputs are
  textareas that wrap and grow to `composerMaxRows`, `newComposerArea`), and the footer
  leads with a `CHAT` or `THREAD` badge.
- Search users (`/`), the forward picker and the mention picker are one modal
  (`userSearchModel`, `internal/tui/search.go`) drawn over the screen with
  `fadeUnder`: search users centred on the body, forward and mention on the chat.
  A boxed `@` query, a `recent · N` (or `N matches`) heading rule, rows of a kind glyph
  (`@`, `#`, `⁂`; `backend.CandidateKind`), name, dim detail and a `[team]` tag
  (`UserCandidate.Tag`) with the query lit, then a key line with the glyph legend.
  `@` at the start of a word in a group chat's composer opens the mention picker
  when the backend implements `backend.Mentioner`; a pick inserts the
  candidate's `Mention` text and each backend turns that text into a native
  mention: `@username` on Mattermost (channel members, then `not in channel`,
  from `/users/autocomplete`) and Telegram (members with a username), the MXID
  on Matrix (`m.mentions`), `@number` on WhatsApp (`MentionedJID`). Email has
  no mentions.
- Messages have no per-message shortcuts (email aside: see its bullet). Enter on an empty
  composer or a right click opens the actions modal
  (`internal/tui/msgmenu.go`) for the selected message: when the backend has
  reactions, a `+` reaction search field (typing in the modal filters) over the
  matching emoji row, then only the actions the message allows: Edit within the
  network's window (`Editable`: Telegram 48 h but channel posts and Saved Messages,
  WhatsApp `whatsmeow.EditWindow`, Mattermost's `PostEditTimeLimit`), Delete for
  everyone within `Capabilities.RevokeWindow` (WhatsApp 60 h). The root draws
  it centred over the chat with everything behind it reduced to plain dim
  text (`fadeUnder`); the footer shows the modal's keys.
- Private chats show the first name, else username, else `Deleted Account`.
  Groups show the title; channels show `Channel: TITLE`, which the sidebar, cards and
  pane titles show as `#TITLE` (`chatName`, `sidebarTitle`).
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
  half-block play-button icon (`videoIcon`, from `assets/video-icon.png`) beside their
  details (`videoCard`); nothing is fetched for it, and a click opens the file.
- Chat text wraps at 120 columns or the terminal width, whichever is smaller.
  Multiline messages keep their line breaks.
- Email (`internal/mail`, tag `EM`, `backend.Mailbox`) has folders for chats: Dialogs
  lists Inbox, Sent, Archive (`\Archive`; Gmail archives to All Mail, without a row) and
  the shown labels. A folder opens in the chat's place as `mailListModel`
  (`internal/tui/mail.go`), as wide as the pane under an `EM` title with the unread count:
  two lines per conversation (unread dot, who it is with and the time; then subject and
  count) under day headings, unread bold and read dimmed, threaded in
  `internal/mail/thread.go` by In-Reply-To, else by a
  reply-prefixed subject, with the user's Sent mail joining the conversations it answers.
  Space expands one; Enter reads a mail with headers and its whole text, in a focused pane
  opened beside the list when the pane is `mailSplitWidth` (120) or wider, which Esc
  closes; under the text a quick reply box (`newComposerArea`) that Enter types into and
  sends as a quoting reply. Email is the exception to "no
  per-message shortcuts": `c` `r` `R` `f` `d` `a` `m` `u` `/` and `v` (visual mode,
  Space picks rows) act on the selected row, the mail read or the picked ones, and
  Trash asks nothing since it can be undone. The composer (`internal/tui/compose.go`), a large modal the root draws over
  the screen (`composeRect`), has To, Cc, Subject and Attach fields, completing addresses (`Mailbox.Addresses`) and
  paths. HTML mail is reduced to text and nothing it references is fetched. Mail is
  cached once per Message-ID (a missing one gets `syntheticID`) in `wiregram.db` for the
  Keep mail cached setting of an email account's Settings (`mail_retention_days`, default
  90); pruning never touches the server. Every folder, inbox and Sent included, records
  its mail by Message-ID in `mail_label_members` (`syncFolder`), so mail moved elsewhere
  leaves it, and actions find each mail's UID in the open folder. Labels are the server's
  folders (`isLabel`), listed at sign-in and on each label pass; only shown ones sync, in
  parallel on their own connections (`syncLabels`).
- Themes live in `internal/tui/theme.go`: a fixed `themes` table (Zest, the
  default, from the website's lime and purples, then Copper), one entry per theme.
  The accent, subtle, dim, selection and badge colours come from a `*styles`
  that each screen loads from the `theme` setting and passes down; status,
  chat-kind, link and nick colours are the same in every theme. Settings'
  Themes… row shows a sample card per theme and Enter applies one.
- Chat Agents are called bots in the UI (the code keeps `agent`). An account has any
  number (`agents.account_key`), each with a display Name and an `@handle` unique on the
  account (`agent.ValidHandle`), reserved for registering the bot on a network later.
  Each private chat has at most one bot: a bot takes all of the account's DMs
  (`all_chats`) or picks some (`agent_chats`), and the two exclude each other. The
  database enforces it (unique indexes and triggers, schema v5), and `SetAgentChats`
  saves a bot's choice in one transaction, naming the other bot when it conflicts.
  Providers live in Settings' AI providers… (`internal/tui/providers.go`). New bot… creates
  one through the scripted wizard (`internal/tui/agentwiz.go`), then opens the bot pane
  (`internal/tui/agentpane.go`) in the chat's place: a `● live`/`● paused` chip (`p` or a
  click switches it), rows with a `›` caret and dim values (Delete bot… and Unregister…
  apart, in red) and, when wide enough, an `agentSideWidth` column of what it is connected
  to and its newest messages (`AgentDB.LatestMessages`). The Chats row
  opens the chat picker modal (`internal/tui/dmpicker.go`), centred over the pane with it
  faded behind: a radio list of All private chats or Selected users, the latter listing
  recent DMs under a filter that, on backends with `backend.DMFinder` (Telegram,
  Mattermost), also searches the server for people whose DM has not started; chats other
  bots answer show `[-]` and the owner's name. The
  runtime routes each DM to the bot that owns it (`agent.ChatClaims`). The theme's agent
  colour (violet, `styles.agent`) and `agentIcon` `✦` mark accounts with a bot, the DMs a
  bot answers while on, chats with a suggestion waiting and the messages a bot sent; those
  are recorded in `agent_sent` and drawn with `✦ NAME` in the nick column
  (`Message.Agent`). An "ask me first" bot's draft is a card over the composer
  (`suggestionCard`, `internal/tui/chat.go`): Approve (^Y) sends it as the bot, Edit (^E)
  moves it into the composer as the user's own, Discard (^X) drops it. On backends with
  server threads (`backend.ThreadFetcher`) a bot answers in the message's own thread, with
  the thread as context (`replyTarget`); elsewhere it quotes the message.
  Every reply a bot sends starts with its Prefix row's emoji and a colon (`🤖: `, or `💻: `;
  `agents.prefix`, schema v9, `AgentRecord.Prefixed`), added in `agentRuntime.send` and
  kept out of the bot's history so the model never writes it.
  `wiregram dry-run` drafts the owning bot's reply without sending (`agentRuntime.DryRun`).
- A bot can be registered on its network where the backend implements
  `backend.BotRegistrar` (Mattermost) or `backend.BotManaged` (Telegram, through managed
  bots: the user's session calls `bots.createBot` with the account's manager bot, kept in
  `bot_managers`, which exports the token; a Telegram bot connects as `telegram.BotBackend`,
  which keeps the chats it sees because bots cannot load history): Register… creates `@handle` as a bot the user owns and
  stores its token in `agent_registrations`. The runtime signs each enabled, registered bot
  in with `ConnectBot` (`internal/account/bots.go`) and answers DMs sent to the bot as the
  bot (`ConvKey.Bot`); every DM to the bot is its own, outside the claims rule. Renames go
  to the server first; deleting the bot or removing its account disables it there first.
  These requests run in the background; a failure is an alert over the screen
  (`agentBotAlert`), success a footer notice.
  Messages from other bots are never answered. A registered bot's Inbox row
  (`internal/tui/inbox.go`) opens its private chats as Unread-style cards over the bot's
  connection (`BotBackend`); a chat opened there runs on that connection with its own
  subscription (`botEvents`), so the composer and approved suggestions send as the bot.
- Wizards (`internal/tui/wizard.go`) take fixed options or typed values only, never free
  chat with a model. Adding an account (network, server URL), an AI provider or a bot,
  and the sign-in prompts are form modals centred over
  the screen they were opened from, faded behind it (`styles.overlay`): answered
  steps as label and value rows, then the current question (`wizardModel.formView`).
- Every yes/no question (quit, delete chat/message/bot/provider, remove account, Reset;
  not moving mail to Trash) is a `confirmModal` (`internal/tui/confirm.go`) centred over the screen, or over the chat when asked
  there, never a footer notice. Deletes and Reset start on Cancel.
- A newly added account opens with an `Account connected` modal (`internal/tui/welcome.go`)
  over its screen, faded behind it: navigation and search keys; any key or click closes it.
- Bots answer incoming DMs only (`BackendEvent.DM`); they never act in
  groups or channels and never start a chat. The exception is the user's chat with
  themselves (`Event.Self`, `Filter.Self`): one bot per account may tick it in the chat
  picker (`agents.self_chat`, schema v8, apart from `all_chats`/`agent_chats`), and there
  the user's own messages are the asks (`ConvKey.Self`); the runtime tells its replies
  apart by ID (`selfSent`, `selfSending`), and the composer publishes its sends there
  since Telegram and WhatsApp never echo them. Email publishes no DM events
  (its events name folders), so bots never answer mail.

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
  mail/              IMAP/SMTP email backend: folders, threading, its mail cache tables
  agent/             SQLite store for providers, per-account agents (bots), the DMs
                     each answers, message history and the record of what they sent
  plugin/            Lua plugin VMs, their API (settings, kv, http, widgets, modals)
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
  for every online account, and a second subscription feeding each account's
  `ChatCache`) and the agent runtime (DM messages only, text-only
  when the agent is). `SetActive` only scopes unread suppression and typing.
- `ChatCache` (`internal/account/chatcache.go`, the Keep chats in memory setting,
  `chat_cache`) holds the messages of chats opened this session, `Raw` and all, in
  memory only. Reopening one seeds the chat with them (`chatViewModel.seed`), then
  the server's newest page replaces their newest part (`mergePage`). No message is
  ever final (Telegram, Matrix and Mattermost allow deletes at any age), so older
  cached pages survive only while the chat is `intact`: a dropped event, a closed
  subscription or `EventResync` (Mattermost reconnects, limited Matrix timelines,
  Telegram gaps too long to fill) breaks it. Telegram deletions that name no chat
  break the chats holding those IDs instead of deleting from them. Bots and the
  unread cards never read it.
- `Publish` never blocks and no lock is held across a channel operation;
  backends release their own mutex before publishing. Lock order is
  `agentRuntime.reloadMu` → `agentRuntime.mu` → `Bus.mu` → `Subscription.mu`
  (`ChatCache.mu` is a leaf); in the
  mail backend, `connMu` (the IMAP connection, held through syncs) → `mu`, so the screen
  reads the special folders under `mu` without waiting on a sync.
- The chat lock lives in the chat model (`locked`): the root sets it on `tea.BlurMsg`
  (focus reporting) and Alt+L; there is no idle timer. Media plays through
  `tea.ExecProcess`.
- Read receipts are batched with a timer to avoid flooding Telegram.

### Config and session paths

`os.UserConfigDir()/wiregram/` (typically `~/.config/wiregram/`):
- `wiregram.db` — the single SQLite database (`internal/store`): `settings`
  (API credentials, mail retention, theme), `account_data` (identity,
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
