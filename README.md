# mintg

A minimal, text-only Telegram client for the terminal, written in Go. Uses
MTProto directly; no TUI framework, mouse, media transfers, or persisted message history.

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

Chats display the latest 30 messages, oldest first. Enter sends text;
Esc, Ctrl+C, or Ctrl+D returns to the chat list. Standard emacs editing keys
and per-chat, in-memory input history are available. Media appears as text tags.

After 10 seconds without keyboard input, the screen locks. Any key unlocks
and redraws the conversation; that key is consumed. Incoming messages do not
unlock the screen. This is a screen collapse, not password protection or a
terminal scrollback eraser.

## Development

Follow [AGENTS.md](AGENTS.md). All source lives in the root `package main`.
Format with `gofmt -w *.go`, check with `go vet ./...`, and verify a build with
`CGO_ENABLED=0`. No test suite is included, as specified in the contributor guide.
Direct dependencies are gotd/td, readline, and x/term; Go resolves their
transitive dependencies through `go.mod` and `go.sum`.

Licensed under the MIT license.
