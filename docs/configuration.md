# Configuration

Wiregram has no config file to edit. Everything is set in the app, under
**Settings**, and stored in one SQLite database.

## Where data lives

Under `os.UserConfigDir()/wiregram/`, typically `~/.config/wiregram/` on Linux and
`~/Library/Application Support/wiregram/` on macOS:

| Path | Contents |
|------|----------|
| `wiregram.db` | Settings, accounts and their sessions, AI providers and bots, the email cache and plugin data |
| `plugins/` | Your own [Lua plugins](/features/plugins) |
| `accounts/whatsapp-*/` | WhatsApp's own session database, one directory per WhatsApp account |

Directories are created `0700` and `wiregram.db` is `0600`. Wiregram writes no JSON files.

Downloaded media lives under `os.UserCacheDir()/wiregram/media` (typically
`~/.cache/wiregram/media`) and is pruned after 7 days unseen.

::: tip Upgrading from an old version
Older versions wrote `config.json`, `account.json`, `session/session.json` and
`sync_token` files. Wiregram imports them into `wiregram.db` once at startup and
renames each to `*.bak`.
:::

## Settings

Open Settings from the account chooser's `Settings…` row or the sidebar's settings… row.
Accounts are listed on the left, followed by:

- **+ Add account…**: pick a network and sign in.
- **Themes…**: pick a [theme](/features/themes).
- **Chat…**: the nick style, `nick:` (default) or `<nick>` in a fixed-width column; see
  [Messages](/features/chats#messages). Saved as the `nick_style` setting. Then
  **Keep chats in memory** (on by default), which draws a reopened chat at once; see
  [Reopening a chat](/features/chats#reopening-a-chat). Saved as the `chat_cache`
  setting.
- **Plugins…**: switch [plugins](/features/plugins) on and off and edit their settings.
- **AI…**: which provider [summarises and rewrites](/features/ai), then add, edit and
  delete the providers those tasks and [AI powered chat bots](/features/chat-bots#ai-providers)
  use. The task choices are saved as the `ai.task.summarize` and `ai.task.rewrite`
  settings.
- **Reset…**: sign out of every account and delete all local data, then start over as
  a fresh installation.
- **Home**: back to the account chooser.
- **Quit**

Select an account to see its settings on the right. Each is a row; <kbd>Enter</kbd> or
<kbd>Space</kbd> changes it.

| Setting | Values |
|---------|--------|
| Open / Sign in | Opens the account, or signs it in |
| Reconnect | Drops and re-opens the connection |
| Keep mail cached | Email only: 30 days, 90 days (default), 1 year or all |
| Remove account… | Logs out on the server when online, then deletes the account's local data |

## Removing data

**Remove account** logs out on the server when the account is online, then deletes its
rows in `wiregram.db`, its bots and their sent-message record and, for
WhatsApp, its directory.

**Reset** does that for every account, then deletes the config and cache directories.
It is also available as [`wiregram reset`](/features/cli#reset).

## Telegram API credentials

Telegram needs an API ID and hash. Official builds may embed them; otherwise Wiregram
asks on first launch and stores them in `wiregram.db`. See the
[Telegram guide](/setup-guides/telegram).

