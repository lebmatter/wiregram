[<img src="assets/wiregram-logo.svg" alt="Wiregram" width="360">](https://wiregram.chat)

Wiregram is a multi-backend chat client for the terminal. Telegram, WhatsApp, Matrix and
Mattermost in one keyboard-driven window, with optional AI Chat Agents that answer your
DMs. It is a single static binary, written in Go.

Visit [wiregram.chat](https://wiregram.chat) for more info.

## Installation

### Binary

- Download the [latest release](https://github.com/lebmatter/wiregram/releases/latest) for
  your platform, or see [wiregram.chat](https://wiregram.chat/#download).
- `chmod +x wiregram-*` and run it in an interactive terminal.
- Press `?` inside the app for shortcuts, or run `wiregram -h`.

```sh
curl -fLo wiregram https://github.com/lebmatter/wiregram/releases/latest/download/wiregram-linux-amd64
chmod +x wiregram
./wiregram
```

### From source

Requires Go 1.26 or newer.

```sh
git clone https://github.com/lebmatter/wiregram
cd wiregram
make
./wiregram
```

## Setup

- **Telegram** asks for an API ID and hash on first launch. Register an app at
  [my.telegram.org/apps](https://my.telegram.org/apps).
- **Matrix and Mattermost** servers go in `~/.config/wiregram/config.json`; Wiregram asks
  you to log in on first use.
- **WhatsApp** needs no config. Add it from the account picker and pair with a code.
  WhatsApp may ban numbers that use unofficial clients.
- **Media** opens in `mpv`. Run Wiregram inside Kitty to see images inline.

Credentials go to the OS keyring (`--insecure` stores them in plain files instead).

## Chat Agents

Optional AI bots, set up in agents mode (Alt+A), that answer **incoming private messages**
on the accounts you connect to them. They work with any OpenAI-compatible provider, can
draft replies for you to approve or answer on their own, and never reply in groups or start
a conversation. Conversations are sent to the provider you configure.

## Developers

Wiregram is free and open source software. See [CLAUDE.md](CLAUDE.md) for the project rules
and layout, and [`docs/`](docs) for design notes. It is pure Go with no cgo; the UI is built
on [BubbleTea](https://github.com/charmbracelet/bubbletea).

## License

Wiregram is licensed under the [AGPL v3](LICENSE) license.
