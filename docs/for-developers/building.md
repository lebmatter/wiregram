# Building

Wiregram is pure Go with no cgo. The UI is built on
[BubbleTea](https://github.com/charmbracelet/bubbletea) with lipgloss and bubbles;
[gotd/td](https://github.com/gotd/td) provides Telegram's MTProto and
[whatsmeow](https://github.com/tulir/whatsmeow) WhatsApp.

## Build

Requires Go 1.26 or newer.

```sh
git clone https://github.com/lebmatter/wiregram
cd wiregram
make            # CGO_ENABLED=0 go build -buildvcs=false -o wiregram ./cmd/wiregram
```

## Embedded credentials

Builds can embed app credentials so users skip setup steps. Pass them from the
environment or a secret manager, never commit them.

**Telegram** API ID and hash, so users skip the API-ID prompt:

```sh
CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags "-X wiregram/internal/config.buildAPIID=${TG_API_ID} \
            -X wiregram/internal/config.buildAPIHash=${TG_API_HASH}" \
  -o wiregram ./cmd/wiregram
```

**Google sign-in** for Gmail accounts needs a Google OAuth *desktop* client, embedded
the same way with `-X wiregram/internal/config.buildGoogleClientID=…` and
`-X wiregram/internal/config.buildGoogleClientSecret=…`. Without one, Gmail accounts
sign in with an app password.

## Lint and format

```sh
gofmt -w cmd internal
go vet ./...
```

There is no test suite. Changes are verified by building and manual testing.

## Layout

```
cmd/wiregram/        main(): flags, database and config load, hands off to tui;
                     cli.go: the non-interactive commands
internal/
  textfmt/           pure text helpers: wrapping, cell widths, nick colours
  halfblock/         decodes, scales and draws images as truecolor half-blocks
  store/             wiregram.db: settings and per-account data
  config/            settings loading
  backend/           the Backend interface and shared types
  telegram/          gotd/td client and backend
  matrix/            Matrix backend
  mattermost/        Mattermost backend
  whatsapp/          whatsmeow backend
  mail/              IMAP/SMTP email backend and its mail cache
  agent/             SQLite store for providers, per-account agents, their chats,
                     history and sent-message record
  plugin/            Lua plugin VMs, their API and the built-in plugins
  account/           account manager and the agent runtime, including dry runs
  tui/               BubbleTea screens and the account/mode loop
docs/                this site
skills/wiregram/     SKILL.md for AI agents using the CLI
```

Imports only point downwards in this list: backends never import each other or `tui`,
and only `tui` and `cmd/wiregram` import `account`.

[`CLAUDE.md`](https://github.com/lebmatter/wiregram/blob/main/CLAUDE.md) holds the full
project rules: approved dependencies, error style, display conventions and the
concurrency model.

## Docs site

This site is [VitePress](https://vitepress.dev), in `docs/`:

```sh
cd docs
npm ci
npm run dev        # http://localhost:5173
make docs          # from the repo root: build to docs/.vitepress/dist
```
