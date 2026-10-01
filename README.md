# Wiregram

A minimal multi-backend chat client for the terminal, written in Go. Supports
Telegram and Mattermost. Text conversations with on-demand Kitty graphics and
audio playback; no TUI framework or mouse.

## Build and run

Requires Go 1.22 or newer and an interactive terminal.

```sh
CGO_ENABLED=0 go build -o wiregram .
./wiregram
```

If your source directory contains incomplete Git metadata, build with
`go build -buildvcs=false -o wiregram .` instead.

For Telegram: builds with bundled app credentials start with your international
phone number, then your Telegram login code. If enabled, your 2FA password is
masked. Use an existing Telegram account. Plain source builds ask for an API ID
and hash first. For Mattermost: add a server entry to `config.json` (see below).

### Build with phone-number-first login

Register a Telegram application at <https://my.telegram.org/apps> once, then
supply its API ID and hash when building the binary:

```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X main.buildAPIID=${TG_API_ID} -X main.buildAPIHash=${TG_API_HASH}" \
  -o wiregram .
```

Set `TG_API_ID` and `TG_API_HASH` in the build environment to your application's
credentials. Users of that binary do not need their own API credentials; they
sign in with their own phone number, login code, and optional 2FA password.
The API ID/hash identify the app, while phone/code authentication identifies the
user. Embedded credentials are part of the binary and can be extracted.

An existing `config.json` overrides the bundled credentials. If neither exists,
Wiregram prompts for API credentials and saves them as before. Bundled credentials
do not create a config file. Both build values must be valid and present.
The two linker-set strings are build metadata and are never modified at runtime.

Configuration is stored at `os.UserConfigDir()/wiregram/config.json`, normally
`~/.config/wiregram/config.json` on Linux. Session files live in the adjacent
`session/` directory and allow subsequent launches without another login.
Directories are created with mode `0700`, and credential/session files with
mode `0600`. Keep both private; never commit them.

```json
{
  "api_id": 123456,
  "api_hash": "abcdef1234567890",
  "autolock": 1,
  "mattermost": [
    {"url": "https://chat.example.com"}
  ]
}
```

`autolock` defaults to `1`. Set it to `0` to disable the 10-second inactivity
timer. Ctrl+L remains available for manual locking.

The `mattermost` array is optional. Each entry adds a Mattermost server whose
account appears in the account switcher alongside Telegram accounts. On first
use, Wiregram prompts for email and password. To skip the interactive login,
add `"token": "<bearer-token>"` to the entry. Telegram credentials (`api_id`,
`api_hash`) are not required if only Mattermost servers are configured.

## Keyboard shortcuts

| Context | Shortcut | Action |
| --- | --- | --- |
| Chat list | Up / `k` | Select previous chat |
| Chat list | Down / `j` | Select next chat |
| Chat list | Enter | Open selected chat |
| Chat list | Enter on Archived Chats / Back to Chats | Switch chat lists |
| Chat list | `/` | Search users |
| Chat list | `a` / Enter on Accounts… | Switch accounts or add a number |
| Chat list | `r` | Refresh chats |
| Chat list | Alt+D | Delete selected private chat only for you |
| Chat list | Ctrl+Alt+D | Delete selected private chat for both sides |
| Chat list | `q` / Ctrl+D | Quit |
| User search | Type | Filter contacts and search public usernames |
| User search | Up / Down | Select previous / next user |
| User search | Enter | Open selected user's chat |
| User search | Esc | Clear the query, then return to the chat list |
| User search | Ctrl+C / Ctrl+D | Return to the chat list |
| Chat | Up / Down | Select previous / next message |
| Chat | Alt+Up / Alt+Down | Previous / next input-history entry |
| Chat | Alt+E | Edit selected outgoing text message |
| Chat | Alt+R | Reply to the selected message; press again to cancel |
| Chat | Alt+M | React to the selected message |
| Chat | Delete | Delete selected message only for you |
| Chat | Shift+Delete | Delete selected message for both sides |
| Chat | Alt+D | Delete current private chat only for you |
| Chat | Ctrl+Alt+D | Delete current private chat for both sides |
| Chat | Enter with text | Send text |
| Chat | Enter with empty input | Open selected media |
| Chat | Ctrl+U | Upload media or a file with an optional caption |
| Chat | Ctrl+F | Search messages in this chat |
| Chat | Alt+P | Pin or unpin the selected message |
| Chat | Alt+F | Forward selected message (opens search picker) |
| Chat | Alt+S | Save selected attachment to ~/Downloads |
| Chat | Alt+A | Turn the AI assistant on or off for this chat |
| AI suggestion | `y` | Send the suggested reply |
| AI suggestion | `n` / Esc | Discard the suggestion |
| AI suggestion | Any other key | Discard it and keep typing |
| Chat | Ctrl+L | Lock immediately |
| Chat | Esc while replying | Cancel the reply |
| Chat | Esc / Ctrl+C / Ctrl+D | Return to chat list when not replying |
| Emoji picker | Type | Filter emoji by name |
| Emoji picker | Up / Down | Select previous / next emoji |
| Emoji picker | Tab | Switch between reaction and draft insertion |
| Emoji picker | Enter | Send the reaction or insert the selected emoji |
| Emoji picker | Esc / Ctrl+C / Ctrl+D | Return to the chat without inserting |
| Edit prompt | Enter | Save edit |
| Edit prompt | Esc / Ctrl+C / Ctrl+D | Cancel edit |
| Delete confirmation | `y` / `Y` | Confirm deletion |
| Delete confirmation | Any other key | Cancel deletion |
| Media viewer/download | Esc / Ctrl+C / Ctrl+D | Stop and return to chat |
| Locked screen | Any key | Unlock; the key is consumed |

## Accounts

The Accounts screen lists all saved accounts — Telegram and Mattermost — and
their connection status. Use Up/Down or `j`/`k`, then Enter to switch to an
online account. Select **Add account…** to sign in with another phone number
(Telegram), code, and optional 2FA password. Ctrl+C or Ctrl+D cancels a
sign-in. An unfinished account remains available to resume. Select an account
requiring sign-in and press Enter to authenticate again; `r` retries an offline
account. Esc returns to the previously selected online account; `q`, Ctrl+D, or
the Quit row exits the application.

Telegram accounts connect in the background at startup and stay connected while
you switch. Mattermost accounts authenticate and open a WebSocket for real-time
events. Each account has independent sessions, caches, in-memory input history,
and updates. The chat-list heading identifies the active account. Only the
selected account's chats are displayed or marked read. The lock counter is the
unread count for the open chat; it is not an account-wide or cross-account
total.

### Mattermost differences

Mattermost channels from all teams appear in a single list sorted by last
activity. Non-DM channels are prefixed with the team name: `[TeamName] general`.
Features unsupported by Mattermost are hidden from the UI: forward, delete-for-me,
outbox read receipts, view-once media, and Kitty graphics playback. File
attachments can be uploaded (Ctrl+U) and saved (Alt+S). The AI assistant is
currently Telegram-only.

API credentials remain in `config.json`. New sessions are stored in private
`accounts/account-*/session/session.json` directories next to that file, with
identity metadata in `account.json`. The original `session/session.json` is
discovered in place, so an existing installation does not need to sign in again.
Identity files are written atomically with mode `0600`; directories use `0700`.
Do not run multiple Wiregram processes against the same configuration directory.

See [the account architecture](ACCOUNTS.md) for concurrency, capacity limits, and
the planned boundary for AI processing.

The selected outgoing message in a private chat shows `✓` when sent and `✓✓`
when read. Visible incoming messages are marked read after a short batching delay.
Chat text wraps at a maximum of 120 display columns, or the terminal width when
the window is narrower.
Chat rows show unread messages as `Name - N*`; the archive row uses the same
notation for its aggregate unread count. The locked screen shows the open chat's
unread message count as `N*`.
Chat deletion requires confirmation and currently applies only to private chats.
Alt+M opens a built-in reaction picker for the selected message. Press Tab in
the picker to insert emoji at the draft cursor instead. Reactions appear only on
the selected message; an asterisk marks reactions sent by the current account.
Recently chosen emoji are kept for the session.
An updated earlier message is marked with `*` until it is selected and displayed.
While composing a reply, a short excerpt of its target appears above the input.

## Media inside the terminal

Press Ctrl+U in an open chat to upload a local file. Enter a path in the file
prompt; Tab completes matching files and directories, including paths beginning
with `~/`. After selecting a regular file, enter an optional caption and press
Enter to send. Escape, Ctrl+C, or Ctrl+D cancels either prompt. Photos, videos,
audio, and GIFs use their Telegram media types; other paths are sent as files.
If a reply is active, the uploaded media is sent as that reply.

Media playback requires external software; neither mpv nor Kitty is bundled:

- Install `mpv` on your PATH for images, video, voice notes, and music. Its build
  must include the `kitty` video output for visual media; check `mpv --vo=help`.
- Run Wiregram inside the **Kitty terminal** for images and video. Installing Kitty
  alone does not enable graphics in another terminal, such as Fedora's Ptyxis.
  Other terminals implementing the Kitty graphics protocol may also work.
- Text chat needs neither dependency. Audio-only playback needs mpv but does
  not require Kitty.

From the repository directory, launch directly in Kitty:

```sh
kitty ./wiregram
```

Use a direct terminal session; Wiregram does not currently implement tmux/screen
graphics passthrough. Wiregram probes the terminal before downloading visual media.
If it reports that the terminal did not confirm Kitty graphics support, check
that Wiregram is actually running in Kitty. There is no desktop-window fallback.

Local image playback was confirmed with Kitty 0.43.1 and mpv 0.40.0.

Photos, common image formats, GIFs, video, video notes, voice notes, and music
open on demand. Static/video stickers work when their format is supported by
mpv. TGS/Lottie stickers, PDFs, and other non-playable attachments remain text
representations. No attachment is downloaded just by displaying or selecting it.

View-once and self-destructing photos, videos, and audio are labelled in the
message list. Press Enter to open them like any other media. After playback
completes, Wiregram acknowledges the view to Telegram so the sender sees
"opened" and the server clears the media. If playback is interrupted (lock
screen, Esc, leaving the chat), the media is not acknowledged — it remains
available for another attempt. Once acknowledged, the message shows as opened.

Enter opens media in the current terminal. Esc, Ctrl+C, or Ctrl+D closes it and
returns to the chat. Wiregram owns stdin throughout playback; other keys keep the
inactivity timer alive but do not enter the message draft. Incoming messages
are retained for the chat redraw without interrupting the viewer.

Each requested attachment downloads into a fresh `0700` temporary directory
with a `0600` file. Wiregram removes it after playback, cancellation, errors, or
normal shutdown. There is no persistent media cache. Downloads complete before
playback starts. Force-killing the process or a system crash can prevent cleanup.

After 10 seconds without keyboard input in an open chat, the screen locks when
`autolock` is enabled. Locking cancels an active download, stops
the player and its audio, deletes Kitty images from the terminal, and removes
temporary media. Any key unlocks back to the current screen and is consumed.
Reopen an attachment explicitly to view it again. Incoming messages, player
output, and terminal graphics replies do not reset the timer.

This is a screen collapse, not password protection or a terminal scrollback eraser.

## AI assistant

Optional and off by default. With no `ai` section in `config.json` nothing
changes: no network calls, no keys required. Add one to enable it:

```json
{
  "api_id": 123456,
  "api_hash": "abcdef1234567890",
  "autolock": 1,
  "ai": {
    "enabled": true,
    "model": "<model-id>",
    "prompt": "You are my assistant. Reply briefly and in my voice.",
    "autoreply": 0,
    "knowledge": ["knowledge/faq.md"]
  }
}
```

The API key is read from the environment, not the config file. The variable
name defaults to `OPENAI_API_KEY` and can be changed with `api_key_env`.
`base_url` defaults to `https://api.openai.com/v1` and may point at any
OpenAI-compatible server that implements `/chat/completions`; plain `http://`
is rejected unless the address is local. Other optional settings are
`max_output_tokens` (`0` omits the limit), `context_messages` (default 20),
and `timeout_seconds` (default 45).

Press **Alt+A** in a private chat to turn the assistant on or off there. The
choice is saved to `config.json`, so it survives a restart. A status line
shows `[AI suggest]` or `[AI auto]` while it is on.

With `autoreply` set to `0`, an incoming message produces a suggested reply:

```
alice> do you deliver on sundays?
[AI suggest — y send · n discard] No, we're closed Sundays. Pickup is Mon-Sat, 9-6.
```

Press `y` to send it, `n` or Esc to discard it, or simply keep typing to
discard it and write your own. With `autoreply` set to `1` the assistant
sends its reply itself.

Files listed in `knowledge` are read on every request and given to the model
as reference material, so editing one takes effect immediately. Paths resolve
relative to the configuration directory and are capped at 64 KB in total,
which is enough for an FAQ or a small catalogue.

Only private chats are answered; groups and channels are never automatic. The
assistant never replies to its own messages or to other bots, only one request
runs at a time per chat, and a burst of messages produces a single reply.
Sending a message yourself cancels anything pending.

Requests go to whichever `base_url` is configured, so the recent conversation
and the knowledge files leave your machine for that provider. Message text
from other people is untrusted: the assistant is instructed to ignore
instructions embedded in it, but suggestion mode is the real protection. If
you run an account that answers customers, tell them an assistant is
involved.

## Development

Follow [CLAUDE.md](CLAUDE.md), the contributor guide. All source lives in the
root `package main`. Build with `make`, format with `gofmt -w *.go`, and check
with `go vet ./...`. No test suite is included.
Direct dependencies are gotd/td, readline, x/term, and nhooyr.io/websocket; Go
resolves their transitive dependencies through `go.mod` and `go.sum`. `mpv` is an
optional runtime dependency for media; Wiregram itself remains cgo-free.

Copyright © 2026 wiregram contributors.
Licensed under the GNU Affero General Public License v3.0 or later
(AGPL-3.0-or-later).
