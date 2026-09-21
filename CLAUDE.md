# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Wiregram is a minimal Telegram CLI client in Go. No TUI framework, no mouse, no cgo. It uses gotd/td for MTProto, chzyer/readline for line editing, and x/term for raw mode. Everything is `package main` in the repo root — no sub-packages, no `cmd/` directory.

## Build

```sh
CGO_ENABLED=0 go build -o wiregram .
```

If Git metadata is incomplete: `go build -buildvcs=false -o wiregram .`

To embed app credentials (users skip the API-ID prompt):
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

There is no test suite. The project is verified by building and manual testing.

## Constraints from AGENTS.md

These are the project's hard rules — not suggestions:

- **No new dependencies** beyond gotd/td, readline, x/term, and x/crypto.
- **No cgo.** Must cross-compile with `CGO_ENABLED=0`.
- **No TUI frameworks** (bubbletea, tview, tcell, etc.).
- **No panics.** Wrap and return errors up the stack with `fmt.Errorf("context: %w", err)`.
- **No `log.Fatal` or `os.Exit`** outside `main()`.
- **No `init()` functions.** No global mutable state.
- **No colors, no bold.** Plain text + ANSI clear/cursor/reverse-video only.
- **YAGNI.** Don't build features not described in AGENTS.md or README.md.

## Architecture

All source is flat in `package main`. The mode loop is:

1. **`main.go`** — entry point (`run()`), the `terminal` struct (raw-mode I/O, inactivity lock manager, readline-based `editor` wrapper), and the mode-switching loop (`runModes`).
2. **`accounts.go`** — multi-account manager. Discovers account directories, connects each in the background, drives the account-selection UI, and delegates to `runModes` for the active account.
3. **`client.go`** — wraps a single gotd/td connection. Holds the `users`/`chats`/`unreads` caches, the update handler that dispatches incoming messages and reactions, and the flood-wait retry middleware.
4. **`chatlist.go`** — fetches dialogs, renders the plain-text list with `>` marker, handles arrow-key selection.
5. **`chatview.go`** — the largest file (~1100 lines). Message history display, readline send loop, incoming-message interleaving, read-receipt tracking, media open/upload dispatch, reply/edit/delete/reaction entry points.
6. **`search.go`** — contact and global username search with incremental filtering.
7. **`media.go`** — pure functions formatting media bracket tags (`[photo]`, `[video]`, etc.).
8. **`mediaopen.go`** — downloads attachments to temp dirs, spawns `mpv` for playback, probes Kitty graphics support.
9. **`upload.go`** — file upload with Tab-completion path picker, MIME detection, caption prompt.
10. **`config.go`** — loads/creates `~/.config/wiregram/config.json`.
11. **`auth.go`** — phone/OTP/2FA authentication flow via gotd/td's `auth.Flow`.
12. **`emoji.go`**, **`reaction.go`**, **`reply.go`**, **`edit.go`**, **`delete.go`**, **`read.go`** — small feature files for their respective chat interactions.

### Concurrency model

- One goroutine reads stdin bytes → assembles keys (escape sequence parsing with 40ms timeout) → feeds through the inactivity lock manager → delivers to the active mode.
- Each account's gotd/td client runs its own update handler goroutine. Updates for the active chat are queued in `client.pending` and signaled via `client.changed` channel.
- `terminal.mu` guards lock/view state and notice buffering. The `editor` wrapper pipes keys into readline's stdin to bridge raw-mode input with readline's line-editing.
- Read receipts are batched with a timer to avoid flooding Telegram.

### Config and session paths

`os.UserConfigDir()/wiregram/` (typically `~/.config/wiregram/`):
- `config.json` — API credentials and autolock setting
- `session/session.json` — primary account session
- `accounts/account-*/` — additional account sessions and identity files
