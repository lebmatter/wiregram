# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

It is the single source of project instructions. (It replaces the former
AGENTS.md, whose durable rules were folded in here.)

## Project

Wiregram is a minimal Telegram CLI client in Go. No TUI framework, no mouse, no cgo. It uses gotd/td for MTProto, chzyer/readline for line editing, and x/term for raw mode. Everything is `package main` in the repo root — no sub-packages, no `cmd/` directory.

## Build

```sh
make                                   # CGO_ENABLED=0 go build -buildvcs=false -o wiregram .
```

To embed app credentials (users then skip the API-ID prompt):
```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X main.buildAPIID=${TG_API_ID} -X main.buildAPIHash=${TG_API_HASH}" \
  -o wiregram .
```

## Lint / Format

```sh
gofmt -w *.go
go vet ./...
```

There is no test suite. Changes are verified by building and manual testing.
When a feature needs verification, write a manual checklist rather than
adding a test framework.

## Hard rules

These are project constraints, not suggestions:

- **No new dependencies.** gotd/td, readline, x/term, and x/crypto only.
  Standard library for everything else — no color, logging, or CLI libraries.
- **No cgo.** Must build with `CGO_ENABLED=0`.
- **No TUI frameworks** (bubbletea, tview, tcell).
- **YAGNI.** Build what is asked for and nothing more.
- **No panics.** Wrap and return errors: `fmt.Errorf("context: %w", err)`.
  Never a bare string error.
- **No `log.Fatal` or `os.Exit`** outside `main()`.
- **No `init()` functions. No global mutable state.** Pass dependencies explicitly.
- **Every user-facing error is one plain English line** to stderr, via
  `terminal.report`. Keep going where possible; exit 1 only when fatal.
- `gofmt` formatted, soft 100-column lines, comments only where the *why* is
  non-obvious.

## Display conventions

- Plain text only. No colors, no bold. The allowed escapes are `\033[2J`
  (clear), `\033[H` (home), `\033[K` (clear line), and `\033[7m`/`\033[0m`.
  Kitty graphics sequences are used only by the media viewer.
- `>` marks the selected row in lists and the selected message in a chat.
- Messages render as `sender> text`; the user is always `you`.
- Private chats show the first name, else username, else `Deleted Account`.
  Groups show the title; channels show `Channel: TITLE`.
- Non-text messages render as bracket tags (`[photo] 1280x720, 245 KB`), with
  ` — ` before a caption. See `media.go` for the full set.
- Chat text wraps at 120 columns or the terminal width, whichever is smaller.

## Architecture

All source is flat in `package main`. The mode loop is:

1. **`main.go`** — entry point (`run()`), the `terminal` struct (raw-mode I/O, inactivity lock manager, readline-based `editor` wrapper), and the mode-switching loop (`runModes`).
2. **`accounts.go`** — multi-account manager. Discovers account directories, connects each in the background, drives the account-selection UI, and delegates to `runModes` for the active account.
3. **`client.go`** — wraps a single gotd/td connection. Holds the `users`/`chats`/`unreads` caches, the update handler that dispatches incoming messages and reactions, and the flood-wait retry middleware.
4. **`chatlist.go`** — fetches dialogs, renders the plain-text list with `>` marker, handles arrow-key selection.
5. **`chatview.go`** — the largest file. Message history display, readline send loop, incoming-message interleaving, read-receipt tracking, media open/upload dispatch, reply/edit/delete/reaction entry points.
6. **`search.go`** — contact and global username search with incremental filtering.
7. **`media.go`** — pure functions formatting media bracket tags.
8. **`mediaopen.go`** — downloads attachments to temp dirs, spawns `mpv` for playback, probes Kitty graphics support.
9. **`upload.go`** — file upload with Tab-completion path picker, MIME detection, caption prompt.
10. **`config.go`** — loads/creates `~/.config/wiregram/config.json`.
11. **`auth.go`** — phone/OTP/2FA authentication flow via gotd/td's `auth.Flow`.
12. **`emoji.go`**, **`reaction.go`**, **`reply.go`**, **`edit.go`**, **`delete.go`**, **`read.go`** — small feature files for their respective chat interactions.

### Concurrency model

- One goroutine reads stdin bytes → assembles keys (escape sequence parsing with 40ms timeout) → feeds through the inactivity lock manager → delivers to the active mode.
- Each account's gotd/td client runs its own update handler goroutine. Updates for the active chat are queued in `client.pending` and signaled via the `client.changed` channel.
- `terminal.mu` guards lock/view state and notice buffering. The `editor` wrapper pipes keys into readline's stdin to bridge raw-mode input with readline's line-editing.
- The chat view is a single select loop; all UI state lives in `view()` locals and is only touched by that goroutine. Background work returns through buffered channels.
- Read receipts are batched with a timer to avoid flooding Telegram.

### Config and session paths

`os.UserConfigDir()/wiregram/` (typically `~/.config/wiregram/`):
- `config.json` — API credentials, autolock, and the optional `ai` section
- `session/session.json` — primary account session
- `accounts/account-*/` — additional account sessions and identity files

Directories are `0700`; credential and session files are `0600`. Config
writes are atomic (temp file in the same directory, then rename) and must
preserve fields the struct does not know about.

## Planned work

`AI_PLAN.md` describes the optional AI assistant: off by default, opt-in per
private chat, suggestion mode (`y`/`n`) by default with an automatic mode.
Read it before touching `ai.go` or the AI paths in `chatview.go`.

`TASKS.md` sequences the remaining work, including a local web UI. Phase 0
of that plan lists rule changes above that need agreeing before the web work
starts — a `serve` subcommand and one vendored JavaScript file (Alpine),
neither of which is currently allowed here.
