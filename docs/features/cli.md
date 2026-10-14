# CLI

Besides the TUI, Wiregram has commands for scripts and AI agents. They print
[JSON Lines](https://jsonlines.org): one JSON object per line on stdout. Errors are one
plain line on stderr with exit status 1.

```sh
wiregram accounts                                   # backend, account, label, status
wiregram chats mattermost alice                     # chat ID, title, kind, unread
wiregram stream                                     # incoming messages, every account
wiregram stream mattermost alice bob                # one account, or one chat
wiregram send mattermost alice bob "On my way"      # or pipe the message on stdin
wiregram reset                                      # sign out everywhere, delete all data
```

- Accounts are signed in from the app first; the commands can't sign in. An account
  showing `"status":"needs-login"` needs you to open the app.
- The commands can run beside the open app.
- WhatsApp is not available: it allows one connection, and a command would disconnect
  the running app.

## Naming things

Every command that needs an account takes `BACKEND ACCOUNT`:

- **`BACKEND`**: `telegram`, `mattermost`, `matrix` or `email` (or `tg`, `mm`, `mx`, `em`).
- **`ACCOUNT`**: the `account` field of `wiregram accounts`: the Telegram phone number,
  Mattermost username, Matrix ID or email address.
- **`RECIPIENT`**: a chat ID (the `chat` field), a chat title, a username, a Matrix ID or
  an email address. A DM's title is the person's display name, so prefer the username for
  people. A name that matches more than one chat is an error; use the chat ID then.
  Naming a public channel you have not joined joins it.

## accounts

```sh
wiregram accounts
```
```json
{"backend":"mattermost","account":"alice","label":"[MM] alice@chat.example.com","status":"ok"}
```

## chats

```sh
wiregram chats BACKEND ACCOUNT
```
```json
{"chat":"8f3k…","title":"bob","kind":"dm","unread":2}
```

`kind` is `dm`, `group` or `channel`.

## stream

```sh
wiregram stream [BACKEND [ACCOUNT [RECIPIENT]]]
```

Prints incoming messages as they arrive, from every signed-in account, every account of
a backend, one account, or one of its chats, until <kbd>Ctrl+C</kbd>. Your own messages
are left out. Connection changes (`reconnecting`, `online`) go to stderr.

```json
{"time":"2026-10-14T10:02:03Z","backend":"mattermost","account":"alice",
 "chat":"8f3k…","chat_title":"bob","kind":"dm","id":"p9x2…","sender":"bob",
 "sender_id":"u42…","reply_to":"","text":"are you coming?","media":""}
```

`text` may contain newlines (escaped in the JSON). `media` is a tag such as
`[photo] 1280x720, 245 KB` for attachments; `text` is then the caption.

## send

```sh
wiregram send [-reply ID] BACKEND ACCOUNT RECIPIENT [MESSAGE...]
```

Sends `MESSAGE`, or stdin when none is given, and prints the sent message in the same
shape as `stream`. `-reply` goes before the positional arguments.

```sh
wiregram send mattermost alice bob "On my way"
printf 'line one\nline two\n' | wiregram send mattermost alice bob
wiregram send -reply p9x2… mattermost alice town-square "Thanks!"
```

Pass text from elsewhere on stdin, so the shell never interprets it.

## reset

```sh
wiregram reset [-y]
```

Signs out of every account and deletes all local data, as **Settings → Reset…** does.
It asks first unless `-y` is given. This cannot be undone.

## A reply loop

```sh
wiregram stream mattermost alice | while IFS= read -r line; do
  [ "$(jq -r .kind <<<"$line")" = dm ] || continue
  chat=$(jq -r .chat <<<"$line")
  text=$(jq -r .text <<<"$line")
  reply=$(make-a-reply "$text")            # your logic here
  printf '%s' "$reply" | wiregram send mattermost alice "$chat"
done
```

## For AI agents

[`skills/wiregram/SKILL.md`](https://github.com/lebmatter/wiregram/blob/main/skills/wiregram/SKILL.md)
teaches coding agents to use these commands. Point your agent at it, or install it as a
skill.
