# AGENTS.md — Wiregram

## Project

Wiregram is a minimal, text-only Telegram CLI client written in Go.
No TUI framework. No mouse support. No menus. No bubbles.
Just readline, raw terminal for selection, and plain text.

Repository name: `wiregram`
Language: Go (1.22+)
License: GNU Affero General Public License v3.0 or later (AGPL-3.0-or-later)

## Principles

- YAGNI. Build only what the spec below describes. No extras.
- No external TUI frameworks (no bubbletea, no tview, no tcell).
- No cgo. Everything must cross-compile with `CGO_ENABLED=0`.
- Minimal dependencies. The full list of allowed third-party modules:
  - `github.com/gotd/td` — MTProto client (Telegram API)
  - `github.com/chzyer/readline` — line editing, history, prompts
  - `golang.org/x/term` — raw mode for chat list arrow-key navigation
  - `golang.org/x/crypto` — if needed for 2FA password prompt
- Standard library for everything else. No color libraries, no logging frameworks.
- Every error that reaches the user must be a single plain English line to stderr.
- No panics. Wrap and return errors up the stack.

## Directory layout

```
wiregram/
├── main.go          # entry point, flag parsing, run loop
├── auth.go          # phone prompt, OTP prompt, 2FA prompt
├── client.go        # gotd/td session wrapper, connect/disconnect
├── chatlist.go      # fetch dialogs, render list, arrow-key selection
├── chatview.go      # message history display, readline send loop
├── media.go         # format media-only messages into bracket tags
├── config.go        # config file (~/.config/wiregram/config.json)
├── go.mod
└── go.sum
```

Do NOT create sub-packages. Everything lives in `package main`.
Do NOT create a `cmd/` directory. `main.go` is at the repo root.

## Telegram API credentials

Wiregram uses gotd/td which speaks MTProto directly.
Users must supply their own Telegram API credentials.

On first run, if `~/.config/wiregram/config.json` does not exist, prompt:

```
wiregram: no config found.
Get your API credentials at https://my.telegram.org/apps
Enter API ID: _
Enter API Hash: _
```

Save to `~/.config/wiregram/config.json`:

```json
{
  "api_id": 123456,
  "api_hash": "abcdef1234567890",
  "autolock": 1
}
```

Session data (gotd/td session storage) goes in `~/.config/wiregram/session/`.

## Authentication flow

After config is loaded, connect and authenticate:

```
Phone (international format): +919108213XXX
Code: 54321
```

If 2FA is enabled:

```
2FA Password: ****
```

Use `gotd/td`'s `auth.Flow` with custom `auth.UserAuthenticator`.
Read phone and code via readline (with history disabled for these prompts).
Read 2FA password with terminal echo disabled (`x/term` or `readline` password mode).

After successful auth, go directly to chat list mode. No welcome banner.

## Mode 1: Chat list

Fetch the most recent 20 dialogs using `messages.GetDialogs`.
Display as a numbered plain text list:

```
  1  Alice
  2  Bob
  3  Work Group
  4  Saved Messages
  5  Channel: Tech News
> 6  Mom
  7  David
```

The `>` marker indicates the current selection. Default selection: line 1.

### Input handling

Enter raw terminal mode (`x/term`) for this screen.

| Key        | Action                              |
|------------|-------------------------------------|
| ↑ / k      | Move selection up                   |
| ↓ / j      | Move selection down                 |
| Enter      | Open selected chat (go to Mode 2)   |
| q / Ctrl+D | Exit program                        |
| r          | Refresh chat list                   |

No other keys do anything. Ignore unknown input silently.

### Rendering

Clear screen on entry. Redraw the full list on every navigation keystroke.
Use ANSI escape codes directly (CSI sequences). Allowed sequences:

- `\033[2J` — clear screen
- `\033[H` — cursor home
- `\033[K` — clear line
- `\033[7m` / `\033[0m` — reverse video for selected line (optional, `>` marker is sufficient)

No colors. No bold. Just plain text and the `>` marker.

## Mode 2: Chat view

On entering a chat, display the last 30 messages, oldest first:

```
alice> hey, are you coming tonight?
you> yeah, around 8
alice> cool, see you there
alice> [photo] 1280x720, 245 KB
you> nice place
█
```

`█` is the readline prompt cursor. The prompt is empty (no visible prompt string).
When the user types and presses Enter, send the message and display it:

```
you> message text here
```

### Readline configuration

- History: enabled, per-chat, stored in memory only (not persisted to disk).
- Prompt: empty string `""`.
- Vim mode: off. Default emacs keybindings (readline default).
- Autocomplete: disabled.

### Input handling

| Key        | Action                                    |
|------------|-------------------------------------------|
| Enter      | Send typed message                        |
| Esc        | Return to chat list (Mode 1)              |
| Ctrl+D     | Return to chat list (Mode 1)              |
| Ctrl+C     | Return to chat list (Mode 1)              |
| Ctrl+L     | Lock immediately                          |
| Ctrl+U     | Upload a file with an optional caption    |

### Incoming messages

A background goroutine listens for updates via gotd/td's update handler.
When a new message arrives in the currently open chat:

1. Call `readline.CleanLine()` on the readline instance.
2. Print the incoming message line.
3. Call `readline.RefreshLine()` to restore the prompt.

Messages from other chats while in a chat view: silently ignore.
They will appear as unread when the user returns to the chat list.

### Inactivity lock

After 10 seconds of no keyboard input in chat view, collapse the screen:

1. Clear the terminal (`\033[2J\033[H`).
2. Print a single line with that chat's unread count:
   `[wiregram: locked — N* — press any key]`
3. Drop into raw mode and block on a single keypress.
4. On any keypress, redraw the last 30 messages and restore the readline prompt.

The timeout resets on every keypress (typed character, backspace, arrow key — anything).

Implementation: use a `time.AfterFunc(10 * time.Second, lockFunc)` timer.
Reset it by calling `.Reset(10 * time.Second)` on every keystroke.
The lock function must be safe to call from the timer goroutine — use a
channel or `sync.Once`-style signal to coordinate with the readline loop.
Do not race on terminal state.

The 10-second duration is hardcoded. Do not make it configurable.

### Media rendering

All non-text messages render as bracket tags. Pattern-match on `tg.MessageMediaClass`:

| Media type             | Display format                                  |
|------------------------|------------------------------------------------ |
| Photo                  | `[photo] WxH, SIZE`                             |
| Video                  | `[video] DURATION, RESOLUTION, SIZE`             |
| Voice message          | `[voice] DURATION`                               |
| Video note (round)     | `[video note] DURATION`                          |
| Sticker                | `[sticker] EMOJI`                                |
| Animated sticker       | `[sticker] EMOJI`                                |
| GIF                    | `[gif] WxH`                                      |
| Document / file        | `[file] FILENAME (SIZE)`                         |
| Audio                  | `[audio] ARTIST - TITLE, DURATION`               |
| Location               | `[location] LAT, LONG`                           |
| Live location          | `[live location] LAT, LONG`                      |
| Contact                | `[contact] NAME, PHONE`                          |
| Poll                   | `[poll] "QUESTION" (N options)`                  |
| Venue                  | `[venue] TITLE, ADDRESS`                         |
| Game                   | `[game] TITLE`                                   |
| Invoice                | `[invoice] TITLE, AMOUNT CURRENCY`               |
| Unsupported / unknown  | `[unsupported media]`                            |

If a media message also has a caption, print it on the same line after the tag:

```
alice> [photo] 1280x720, 245 KB — look at this sunset
```

Separator between tag and caption is ` — ` (space, em dash, space).

### Formatting sizes and durations

- Sizes: bytes → KB/MB/GB with 1 decimal. e.g. `245.0 KB`, `12.3 MB`
- Durations: `M:SS` format. e.g. `0:34`, `12:05`
- Resolutions: `WxH`. e.g. `1280x720`

## User display names

- Private chats: first name only. If empty, username. If empty, `"Deleted Account"`.
- Groups/supergroups: group title.
- Channels: `"Channel: TITLE"`.
- Yourself in messages: always `"you"`.

## Config file

Path: `~/.config/wiregram/config.json`

```json
{
  "api_id": 123456,
  "api_hash": "abcdef1234567890",
  "autolock": 1
}
```

`autolock` accepts `0` or `1` and defaults to `1` when omitted. It controls only
the 10-second inactivity timer; Ctrl+L always locks an open chat immediately.

Use `os.UserConfigDir()` to resolve the base path.
Create directories with `os.MkdirAll` and `0700` permissions.

## Session storage

gotd/td supports pluggable session storage.
Use file-based session storage in `~/.config/wiregram/session/`.
This avoids re-authentication on every launch.

## Error handling

- Network errors during operation: print to stderr, do not crash. Retry connection.
- Auth errors (wrong code, wrong password): print error, re-prompt.
- Telegram flood wait: print `"Rate limited. Retry in Ns."` and wait.
- All other errors: print to stderr and continue if possible, exit 1 if fatal.

## Build

```bash
go build -o wiregram .
```

Must build with no warnings, no lint errors.
Must build with `CGO_ENABLED=0`.

## What NOT to build

Do not implement any of the following:

- Mouse support
- Color themes or color output
- Config file options beyond api_id, api_hash, and autolock
- Message search
- Message editing or deletion
- Typing indicators
- Online/offline status
- Read receipts
- Reactions
- Reply-to threading
- Forward messages
- Sticker/GIF sending
- Bot commands
- Inline keyboards
- Chat creation, deletion, or management
- User blocking
- Notification sounds
- Any form of encryption beyond what gotd/td handles
- Logging to files
- Verbose/debug flags
- Tests (for now)

## Code style

- `gofmt` formatted. No exceptions.
- No init() functions.
- No global mutable state. Pass dependencies explicitly.
- Errors: `fmt.Errorf("thing: %w", err)` wrapping. No bare string errors.
- No `log.Fatal` or `os.Exit` outside of `main()`.
- Comments only where non-obvious. No comment headers on functions with clear names.
- Max line length: soft 100 chars. No hard wrapping.

## Development order

Build and verify in this order:

1. `config.go` — load/create config file, prompt for API credentials
2. `client.go` — gotd/td client setup, connect, session storage
3. `auth.go` — phone/OTP/2FA flow using readline
4. `chatlist.go` — fetch dialogs, render list, arrow-key navigation
5. `media.go` — media type formatting (pure functions, no I/O)
6. `chatview.go` — message display, readline loop, incoming message handler
7. `main.go` — wire everything together, mode switching loop

Each step should compile and be manually testable before moving to the next.
