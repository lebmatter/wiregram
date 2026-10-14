[<img src="assets/wiregram-animated.svg" alt="" height="117"> <img src="assets/wiregram-logo.svg" alt="Wiregram" width="360">](https://wiregram.chat)

Wiregram is a multi-backend chat client for the terminal. Telegram, WhatsApp, Matrix and
Mattermost in one keyboard-driven window, with optional AI Chat Agents that answer your
DMs. It is a single static binary, written in Go.

> **Alpha software.** Wiregram is under active development. Expect rough edges, and
> settings or the database format may change between releases. Please report bugs in
> [issues](https://github.com/lebmatter/wiregram/issues).

Visit [wiregram.chat](https://wiregram.chat) for more info.

## Installation

### Binary

- Download the [latest release](https://github.com/lebmatter/wiregram/releases/latest) for
  your platform, or see [wiregram.chat](https://wiregram.chat/#download).
- `chmod +x wiregram-*` and run it in an interactive terminal.
- Run `wiregram -h` for the shortcuts, or read the [docs](docs).

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
- **Matrix and Mattermost** are added from the account picker with the server address;
  Wiregram asks you to log in.
- **WhatsApp** needs no config. Add it from the account picker and pair with a code.
  WhatsApp may ban numbers that use unofficial clients.
- **Media** opens in `mpv` with `o`. Run Wiregram inside Kitty to see images inline.
  Or click a media message to download it to a private temp folder and open it in
  your default app (`xdg-open`, or `open` on macOS). Hold Shift while dragging to
  select text.

## Chat Agents

Optional AI bots, one per account, that answer **incoming private messages**. Add an
OpenAI-compatible provider under Settings → AI providers…, then create the agent from the
account's **New agent…** row in the sidebar. An agent can suggest replies for you to
approve or answer on its own; its messages are marked `✦ NAME` in the chat. It never replies
in groups or starts a conversation. Try it with `wiregram dry-run` before turning it on.
Conversations are sent to the provider you configure.

## Command line

Scripts and AI agents can read and send messages without the TUI. Output is one JSON
object per line.

```sh
wiregram accounts                                   # backend, account, label, status
wiregram chats mattermost alice                     # chat ID, title, kind, unread
wiregram stream                                     # incoming messages, every account
wiregram stream mattermost alice bob                # one account, or one chat
wiregram send mattermost alice bob "On my way"      # or pipe the message on stdin
wiregram dry-run mattermost alice bob               # the agent's reply, never sent
wiregram reset                                      # sign out everywhere, delete all data
```

Accounts are named by the Telegram phone number, Mattermost username, Matrix ID or email
address. Sign in to them in the app first. The commands can run beside the app. WhatsApp
is left out because it allows only one connection. Agents can learn the commands from
[`skills/wiregram/SKILL.md`](skills/wiregram/SKILL.md).

## Developers

Wiregram is free and open source software. See [CLAUDE.md](CLAUDE.md) for the project rules
and layout, and [`docs/`](docs) for the documentation site. It is pure Go with no cgo; the UI is built
on [BubbleTea](https://github.com/charmbracelet/bubbletea).

## License

Wiregram is licensed under the [AGPL v3](LICENSE) license.
