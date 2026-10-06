# <img src="assets/wiregram-logo.svg" alt="Wiregram" width="360">

A minimal multi-backend chat client for the terminal, written in Go. Supports
Telegram, Mattermost, Matrix, and WhatsApp, with optional media playback via
mpv and Kitty graphics, and optional Chat Agents that answer your DMs with AI.

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

## Chat Agents

Optional and off by default. Chat Agents are AI bots that answer **incoming
private messages** on the accounts you connect to them. They never reply in
groups or channels and never start a conversation.

Press **Alt+A** (or pick **Chat Agents…** in the account picker) to open agent
mode, then:

1. Connect an OpenAI-compatible provider (OpenRouter, OpenAI, a local Ollama,
   or any other). The API key is read from an environment variable and never
   stored.
2. Create an agent in the guided wizard: name, provider and model,
   instructions, knowledge files, text only or text + vision, suggest or
   auto-reply, and history retention.
3. Connect accounts. Each account answers to at most one agent.

Select an agent to see its conversations, tagged by account (`[TG1]`,
`[WA2]`, matching the Alt+N numbers). In suggest mode, open a conversation and
press `y` to send the drafted reply or `n` to discard it. Agent configuration
and conversation history are stored in `wiregram.db`. Conversations, knowledge
files and (for vision agents) incoming photos are sent to the configured
provider.

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
