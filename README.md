# <img src="assets/wiregram-logo.svg" alt="Wiregram" width="360">

A minimal multi-backend chat client for the terminal, written in Go. Supports
Telegram, Mattermost, Matrix, and WhatsApp, with optional media playback via
mpv and Kitty graphics, and optional AI agents that suggest or send replies.

## Build and run

Requires Go 1.26 or newer and an interactive terminal.

```sh
make
./wiregram
```

Press `?` inside the app for shortcuts, or run `wiregram -h`.

### Telegram credentials

Plain builds ask for a Telegram API ID and hash on first launch. To skip that
step, register an app at <https://my.telegram.org/apps> and embed its
credentials at build time (they can be extracted from the binary):

```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X wiregram/internal/config.buildAPIID=${TG_API_ID} -X wiregram/internal/config.buildAPIHash=${TG_API_HASH}" \
  -o wiregram ./cmd/wiregram
```

## Configuration

Config, sessions and the agent database live in `~/.config/wiregram/`
(`os.UserConfigDir()/wiregram`). Credentials go to the OS keyring by default;
`--insecure` stores them in plain files instead.

```json
{
  "api_id": 123456,
  "api_hash": "abcdef1234567890",
  "autolock": 1,
  "mattermost": [{"url": "https://chat.example.com"}],
  "matrix": [{"homeserver": "https://matrix.org"}]
}
```

- `autolock`: lock the screen after 10 seconds of inactivity (`0` disables).
- `mattermost` / `matrix`: optional servers; Wiregram prompts for login on
  first use.
- WhatsApp needs no config. Add it from the account picker and pair with a
  code. WhatsApp may ban numbers that use unofficial clients.

## Media

Text chat needs nothing extra. To open attachments, install `mpv`; to view
images and video, run Wiregram inside the Kitty terminal. Attachments download
on demand into temporary files that are removed afterwards.

## Agents

Optional and off by default. Open **Agents…** from the account picker to add an
OpenAI-compatible provider (API key read from an environment variable) and
assign agents to private chats. Agents either suggest replies for you to
approve or reply automatically. Conversations and knowledge files are sent to
the configured provider.

## Development

See [CLAUDE.md](CLAUDE.md) for project rules and layout, and `docs/` for design
notes. The entry point is `cmd/wiregram`; everything else is in `internal/`.

```sh
gofmt -w cmd internal
go vet ./...
make
```

Licensed under the GNU Affero General Public License v3.0 or later
(AGPL-3.0-or-later).
