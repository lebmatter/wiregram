[<img src="assets/wiregram-animated.svg" alt="" height="117"> <img src="assets/wiregram-logo.svg" alt="Wiregram" width="360">](https://wiregram.chat)

Wiregram is a multi-backend chat client for the terminal. Telegram, WhatsApp, Matrix,
Mattermost and email in one keyboard-driven window, with optional AI powered chat bots that
answer your DMs and two-player games you can challenge any chat to. It is a single static
binary, written in Go.

> **Alpha software.** Wiregram is under active development. Expect rough edges, and
> settings or the database format may change between releases. Please report bugs in
> [issues](https://github.com/lebmatter/wiregram/issues).

Visit [wiregram.chat](https://wiregram.chat) for more info.

## Installation

### Binary

- Download the [latest release](https://github.com/lebmatter/wiregram/releases/latest) for
  your platform, or see [wiregram.chat](https://wiregram.chat/#download).
- `chmod +x wiregram-*` and run it in an interactive terminal (Windows needs no chmod).
- Run `wiregram -h` for the shortcuts, or read the [docs](docs).

```sh
curl -fLo wiregram https://github.com/lebmatter/wiregram/releases/latest/download/wiregram-linux-amd64
chmod +x wiregram
./wiregram
```

On Windows, download `wiregram-windows-amd64.exe` (or `-arm64.exe`) and run it in
[Windows Terminal](https://aka.ms/terminal):

```powershell
Invoke-WebRequest https://github.com/lebmatter/wiregram/releases/latest/download/wiregram-windows-amd64.exe -OutFile wiregram.exe
.\wiregram.exe
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
  your default app (`xdg-open`, `open` on macOS, the default app on Windows). Hold
  Shift while dragging to select text.

## AI Powered Chat Bots

Optional bots that answer an account's **incoming private messages** as you. Add an
OpenAI-compatible provider under Settings → AI providers…, then create a bot from the
**New bot…** row in the account's Bots section. An account can have several, each with a
name and an `@handle`, answering all of its private chats or the ones picked for it, never
two bots in one chat. A bot can suggest replies for you to approve or answer on its own;
its messages are marked `✦ NAME` in the chat. It never replies in groups or starts a
conversation. Try it with `wiregram dry-run` before turning it on.

A bot can also be **registered** as a bot account of its own, so people message it
directly and it answers as itself:

- **Mattermost:** Register… creates `@handle` on the server, if your account may manage
  bots.
- **Telegram:** bots are created inside Wiregram through Telegram's managed bots, after you
  give each account a manager bot (one with Bot Management on in @BotFather).

A registered bot gets an **Inbox** of its own chats, where you approve its replies or
answer as the bot. Conversations are sent to the provider you configure.

## Multiplayer Games

Challenge anyone in a DM or group to **Chess, Battleship, Snake, Pong, Connect Four or
Tic-tac-toe** with `Alt+G` (or a click on `game+` beside the input). The game plays in a
pane beside the chat. Your wiregram hosts it on `127.0.0.1` behind a
[cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
quick tunnel and posts a `wiregame://` invite; the other player joins with a click on it
and needs only wiregram. Only the challenger needs cloudflared.

No chat needed either:

```sh
wiregram game                        # pick a game, host it and copy the invite
wiregram game wiregame://…/…         # join the game an invite links to
```

Games are small sandboxed Lua scripts; see the
[games docs](https://docs.wiregram.chat/features/games) for how to write one.

## Command line

Scripts and AI coding tools can read and send messages without the TUI. Output is one JSON
object per line.

```sh
wiregram accounts                                   # backend, account, label, status
wiregram chats mattermost alice                     # chat ID, title, kind, unread
wiregram stream                                     # incoming messages, every account
wiregram stream mattermost alice bob                # one account, or one chat
wiregram send mattermost alice bob "On my way"      # or pipe the message on stdin
wiregram dry-run mattermost alice bob               # the bot's reply, never sent
wiregram reset                                      # sign out everywhere, delete all data
```

Accounts are named by the Telegram phone number, Mattermost username, Matrix ID or email
address. Sign in to them in the app first. The commands can run beside the app. WhatsApp
is left out because it allows only one connection. AI coding tools can learn the commands
from [`skills/wiregram/SKILL.md`](skills/wiregram/SKILL.md).

## Developers

Wiregram is free and open source software. See [CLAUDE.md](CLAUDE.md) for the project rules
and layout, and [`docs/`](docs) for the documentation site. It is pure Go with no cgo; the UI is built
on [BubbleTea](https://github.com/charmbracelet/bubbletea).

## License

Wiregram is licensed under the [AGPL v3](LICENSE) license.
