# Welcome to Wiregram

<img class="wg-logo" src="/logo.svg" alt="Wiregram">

Wiregram is a multi-backend chat client for the terminal. Telegram, WhatsApp, Matrix,
Mattermost and email in one keyboard-driven window, with optional AI bots that
answer your DMs. It is a single static binary, written in Go.

::: warning Alpha software
Wiregram is under active development. Expect rough edges, and settings or the database
format may change between releases. Please report bugs in
[issues](https://github.com/lebmatter/wiregram/issues).
:::

<img class="wg-shot" src="/img/demo.gif" alt="Wiregram in a terminal">

## Getting Started

- [Installation](/installation): download a release or build from source
- [Usage](/usage): the screens, the mouse and getting around
- [Keybinds](/keybinds): every shortcut, screen by screen
- [Configuration](/configuration): where Wiregram keeps its data, and its settings

## Setup Guides

[Telegram](/setup-guides/telegram) · [WhatsApp](/setup-guides/whatsapp) ·
[Matrix](/setup-guides/matrix) · [Mattermost](/setup-guides/mattermost) ·
[Email](/setup-guides/email)

## Core Features

- [Accounts](/features/accounts): every account in one sidebar tree, with an unread digest
- [Chats](/features/chats): replies, threads, reactions, forwarding, mentions and search
- [Unread & Mentions](/features/unread-mentions): one card per chat that needs you
- [Media](/features/media): inline images in kitty and Ghostty, half-blocks elsewhere
- [Email](/features/email): IMAP and SMTP, one chat per correspondent
- [Themes](/features/themes): Zest and Copper
- [Bots](/features/chat-agents): AI bots that answer an account's private messages, each its own chats
- [Plugins](/features/plugins): sandboxed Lua widgets
- [CLI](/features/cli): JSON Lines commands for scripts and AI agents

## License

Wiregram is free and open source software under the
[AGPL v3](https://github.com/lebmatter/wiregram/blob/main/LICENSE) license.
