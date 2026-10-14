---
name: wiregram
description: Send and receive chat messages on Telegram, Mattermost, Matrix and email from the shell with the wiregram CLI. Use when asked to watch for incoming messages, reply to someone, or post to a chat or channel on an account signed in to wiregram.
---

# wiregram

`wiregram` is a terminal chat client. Its commands let you list the user's
accounts and chats, stream incoming messages, and send messages, all as
JSON Lines (one JSON object per line) on stdout. Errors are one plain line on
stderr with exit status 1.

## Before you start

- Accounts are signed in through the app (`wiregram` with no arguments) by
  the user. You cannot sign in from the commands; an account that shows
  `"status":"needs-login"` must be signed in by the user first.
- WhatsApp is not available from the commands.
- The commands run fine beside the open app.

## Naming things

Every command that needs an account takes `BACKEND ACCOUNT`:

- `BACKEND`: `telegram`, `mattermost`, `matrix` or `email` (or `tg`, `mm`, `mx`, `em`).
- `ACCOUNT`: the `account` field of `wiregram accounts` — the Telegram phone
  number, Mattermost username, Matrix ID or email address.

`RECIPIENT` is a chat ID (the `chat` field), a chat title, a username, a
Matrix ID or an email address. A DM's title is the person's display name, so
prefer the username for people. A name that matches more than
one chat is an error; use the chat ID then.

## Commands

```sh
wiregram accounts
# {"backend":"mattermost","account":"alice","label":"[MM] alice@chat.example.com","status":"ok"}

wiregram chats mattermost alice
# {"chat":"8f3k…","title":"bob","kind":"dm","unread":2}

wiregram stream                          # every signed-in account
wiregram stream mattermost               # every Mattermost account
wiregram stream mattermost alice         # one account
wiregram stream mattermost alice bob     # one chat
wiregram send mattermost alice bob "On my way"
printf 'line one\nline two\n' | wiregram send mattermost alice bob   # message from stdin
wiregram send -reply p9x2… mattermost alice town-square "Thanks!"     # reply to message id
```

`-reply` goes before the positional arguments.

## Testing the user's Chat Agent

If the account has a Chat Agent (set up by the user in the app), `dry-run`
prints the reply it would give, without sending or recording anything. It
works while the agent is paused.

```sh
wiregram dry-run mattermost alice bob                    # newest incoming message
wiregram dry-run -message p9x2… mattermost alice bob     # a given message id
wiregram dry-run -message p9x2… -message q7d1… mattermost alice bob
# {"agent":"Support Bot","chat":"8f3k…","chat_title":"bob","reply_to":"p9x2…",
#  "message":"are you coming?","reply":"Yes, in ten minutes."}
```

- It uses up to 20 earlier messages as history, and the agent's prompt,
  knowledge and media mode. One line per message.
- It refuses group chats, chats outside the agent's selected chats, the
  user's own messages, and media messages for text-only agents.
- `-message` goes before the positional arguments.

## Message lines

`stream` prints one line per incoming message (never the user's own) until it
is stopped. `send` prints the sent message in the same shape.

```json
{"time":"2026-10-14T10:02:03Z","backend":"mattermost","account":"alice",
 "chat":"8f3k…","chat_title":"bob","kind":"dm","id":"p9x2…","sender":"bob",
 "sender_id":"u42…","reply_to":"","text":"are you coming?","media":""}
```

- `kind`: `dm`, `group` or `channel`.
- `text` may contain newlines (escaped in the JSON). `media` is a tag such as
  `[photo] 1280x720, 245 KB` for attachments; `text` is then the caption.
- To answer a message, send to its `backend`, `account` and `chat`, with
  `-reply` set to its `id` when a threaded reply fits.
- Connection changes (`reconnecting`, `online`) go to stderr, not stdout.

## Reply loop

```sh
wiregram stream mattermost alice | while IFS= read -r line; do
  kind=$(jq -r .kind <<<"$line")
  [ "$kind" = dm ] || continue
  chat=$(jq -r .chat <<<"$line")
  text=$(jq -r .text <<<"$line")
  reply=$(make-a-reply "$text")            # your logic here
  printf '%s' "$reply" | wiregram send mattermost alice "$chat"
done
```

## Rules

- Answer only in direct messages (`"kind":"dm"`) unless the user asked you to
  post in a group or channel.
- Do not start conversations with people the user did not name.
- Pass message text on stdin when it comes from elsewhere, so the shell
  never interprets it.
- Never run `wiregram reset`: it signs out of every account and deletes all
  local data.
