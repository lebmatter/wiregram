# mintg

A minimal Telegram client for the terminal, written in Go. Text conversations
with on-demand Kitty graphics and audio playback; no TUI framework or mouse.

## Build and run

Requires Go 1.22 or newer and an interactive terminal.

```sh
CGO_ENABLED=0 go build -o mintg .
./mintg
```

If your source directory contains incomplete Git metadata, build with
`go build -buildvcs=false -o mintg .` instead.

Builds with bundled app credentials start with your international phone number,
then your Telegram login code. If enabled, your 2FA password is masked. Use an
existing Telegram account. Plain source builds ask for an API ID and hash first.

### Build with phone-number-first login

Register a Telegram application at <https://my.telegram.org/apps> once, then
supply its API ID and hash when building the binary:

```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X main.buildAPIID=${TG_API_ID} -X main.buildAPIHash=${TG_API_HASH}" \
  -o mintg .
```

Set `TG_API_ID` and `TG_API_HASH` in the build environment to your application's
credentials. Users of that binary do not need their own API credentials; they
sign in with their own phone number, login code, and optional 2FA password.
The API ID/hash identify the app, while phone/code authentication identifies the
user. Embedded credentials are part of the binary and can be extracted.

An existing `config.json` overrides the bundled credentials. If neither exists,
mintg prompts for API credentials and saves them as before. Bundled credentials
do not create a config file. Both build values must be valid and present.
The two linker-set strings are build metadata and are never modified at runtime.

Configuration is stored at `os.UserConfigDir()/mintg/config.json`, normally
`~/.config/mintg/config.json` on Linux. Session files live in the adjacent
`session/` directory and allow subsequent launches without another login.
Directories are created with mode `0700`, and credential/session files with
mode `0600`. Keep both private; never commit them.

## Controls

The chat list shows up to 20 recent dialogs.

| Chat list | Action |
| --- | --- |
| Up / `k`, Down / `j` | Move selection |
| Enter | Open chat |
| `r` | Refresh |
| `q` / Ctrl+D | Quit |

Chats display the latest 30 messages, oldest first. A `>` marker selects a
message; selection starts at the newest message. Long lines are clipped to the
terminal width, and the visible window follows the selection.

| Chat view | Action |
| --- | --- |
| Up / Down | Select the previous / next message |
| Alt+Up / Alt+Down | Recall previous / next typed text from this chat |
| Enter with an empty input | Open the selected image, video, voice note, or audio |
| Enter with typed text | Send the text |
| Esc / Ctrl+C / Ctrl+D | Return to the chat list |

Input history stays in memory, separately for each chat. Selecting messages
preserves the current draft. Incoming messages do not move a selection away
from an older message you are browsing.

## Media inside the terminal

Media playback requires external software; neither mpv nor Kitty is bundled:

- Install `mpv` on your PATH for images, video, voice notes, and music. Its build
  must include the `kitty` video output for visual media; check `mpv --vo=help`.
- Run mintg inside the **Kitty terminal** for images and video. Installing Kitty
  alone does not enable graphics in another terminal, such as Fedora's Ptyxis.
  Other terminals implementing the Kitty graphics protocol may also work.
- Text chat needs neither dependency. Audio-only playback needs mpv but does
  not require Kitty.

From the repository directory, launch directly in Kitty:

```sh
kitty ./mintg
```

Use a direct terminal session; mintg does not currently implement tmux/screen
graphics passthrough. Mintg probes the terminal before downloading visual media.
If it reports that the terminal did not confirm Kitty graphics support, check
that mintg is actually running in Kitty. There is no desktop-window fallback.

Local image playback was confirmed with Kitty 0.43.1 and mpv 0.40.0.

Photos, common image formats, GIFs, video, video notes, voice notes, and music
open on demand. Static/video stickers work when their format is supported by
mpv. TGS/Lottie stickers, PDFs, and other non-playable attachments remain text
representations. No attachment is downloaded just by displaying or selecting it.

View-once and self-destructing photos, videos, and audio are labelled and blocked
before download. Open them in an official Telegram app; mintg does not yet
implement their consumption acknowledgements and expiry lifecycle.

Enter opens media in the current terminal. Esc, Ctrl+C, or Ctrl+D closes it and
returns to the chat. Mintg owns stdin throughout playback; other keys keep the
inactivity timer alive but do not enter the message draft. Incoming messages
are retained for the chat redraw without interrupting the viewer.

Each requested attachment downloads into a fresh `0700` temporary directory
with a `0600` file. Mintg removes it after playback, cancellation, errors, or
normal shutdown. There is no persistent media cache. Downloads complete before
playback starts. Force-killing the process or a system crash can prevent cleanup.

After 10 seconds without keyboard input, the screen locks. Locking cancels an
active download, stops the player and its audio, deletes Kitty images from the
terminal, and removes temporary media. Any key unlocks back to the chat and is
consumed. Reopen an attachment explicitly to view it again. Incoming messages,
player output, and terminal graphics replies do not reset the timer.

This is a screen collapse, not password protection or a terminal scrollback eraser.

## Development

Follow [AGENTS.md](AGENTS.md). All source lives in the root `package main`.
Format with `gofmt -w *.go`, check with `go vet ./...`, and verify a build with
`CGO_ENABLED=0`. No test suite is included, as specified in the contributor guide.
Direct dependencies are gotd/td, readline, and x/term; Go resolves their
transitive dependencies through `go.mod` and `go.sum`. `mpv` is an optional runtime
dependency for media; mintg itself remains cgo-free. Media playback and the new
key bindings implement the subsequently requested extensions to `AGENTS.md`.

Licensed under the MIT license.
