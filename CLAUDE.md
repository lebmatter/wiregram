# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is the single source of project instructions. (It replaces the former
AGENTS.md, whose durable rules were folded in here.)

## Project

Wiregram is a minimal multi-backend chat client for the terminal, in Go. No cgo. The only mouse input is a
left click: on a chat message it opens the attachment with `xdg-open`, and on an Unread
card it opens that chat. The UI is BubbleTea v2 with lipgloss and bubbles; gotd/td provides MTProto. The entry point is `cmd/wiregram`; all other code lives in packages under `internal/`.

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
  github.com/sahilm/fuzzy, approved as a transitive dependency), and
  github.com/yuin/gopher-lua for the embedded game scripts.
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
- Overlays are drawn by `renderModal` (`internal/tui/render.go`), a box
  composited over the screen with lipgloss layers. Game mode uses one for
  incoming challenges and for the game-over result.
- Startup shows the `wiregramLogo` splash (`internal/tui/splash.go`) for about a second.
- There is no header bar. Inside an account the screen is the body and a
  `bubbles/help` footer with the notice on the right. Help hides keys the
  backend's `capabilities` rule out.
- The chat mode sidebar is an account tree (`internal/tui/list.go`): every
  account is a row (`▸`/`▾`, status glyph, `TG1` tag, label), the active one
  expanded to its chats, archive and search. Enter on another account, or
  Alt+N, switches to it. The agents mode sidebar lists the agents.
- Each expanded account has an Unread row (`u`, `internal/tui/unread.go`): a column
  of full-width cards, one per chat with unread messages. The card border and tag show the
  kind: DM green, group blue, channel yellow. Replies get `↳` and a `thread` tag.
- At `twoPaneWidth` (100) columns and wider the chat list is a sidebar beside
  the chat and Tab moves focus; narrower terminals show one pane at a time.
- The root `tuiModel` owns the screen's only bus subscription and routes
  events for the open chat into it, so the list and chat never race.
- `>` marks the selected row in lists and the selected message in a chat.
- Messages render as `[HH:MM] <sender> text` with a fixed-width nick column;
  the user is always `you`. A reply puts `┃ sender: excerpt` on the header
  row with its text below; threads (`internal/tui/thread.go`) are built from loaded
  reply chains only.
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
- Chat text wraps at 120 columns or the terminal width, whichever is smaller.
- Agent mode (Alt+A, `internal/tui/agent.go`) swaps the copper accent
  (`colorAccent`, 214) for violet (`colorAgent`, 141) and tags accounts as
  `[TG1]`: backend plus Alt+N number. Chat Agents are created through the
  scripted bubble wizard (`internal/tui/wizard.go`): fixed options or typed
  values only, never free chat with a model. Adding an account (network,
  server URL, then the sign-in prompts) uses the same bubble style in copper.
- Chat Agents answer incoming DMs only (`BackendEvent.DM`); they never act in
  groups or channels and never start a chat.
- Game mode (`internal/tui/game.go`) is an easter egg: only Alt+G from the
  chat list opens it, and only `--help` mentions it (no menu entry, no footer
  hint). It uses green (`colorGame`, 114). Games are versioned Lua scripts
  embedded from `internal/game/scripts/`. One-player games run locally with
  no base; two-player games are greyed out until a base is connected, and
  only players with the same id and version are matched. The challenger hosts and runs the script for both seats; the guest
  sends keys and draws the changed lines it receives. `wiregram base` only
  lists players, passes on challenges and relays; it never runs game logic.

## Architecture

```
cmd/wiregram/        main(): flags, database and config load, hands off to tui
internal/
  textfmt/           pure text helpers: plain(), wrapping, cell widths, nick colours
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
  agent/             SQLite store for agents, providers and message history
  game/              game protocol, base client, sandboxed Lua runtime and the
                     embedded game scripts (scripts/*.lua)
  base/              `wiregram base`: lobby, invites and relay for game mode
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
  (API credentials, autolock), `account_data` (identity, Telegram session and
  Matrix sync token per account key) and the Chat Agent tables
- `accounts/<backend>-*/` — one directory per added account; its name is the
  account key (`primary` for the original Telegram account). WhatsApp keeps
  whatsmeow's own `whatsapp.db` here.

Directories are `0700`; `wiregram.db` is `0600`. Wiregram writes no JSON files.
`account.ImportLegacy` imports the files older versions wrote (`config.json`,
`account.json`, `session/session.json`, `sync_token`) once at startup and
renames each to `*.bak`.

## Planned work

`docs/TASKS.md` sequences the remaining work.
