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

On email, `chats` lists the folders (kind `folder`, e.g. `INBOX`), `stream`
takes a folder, and `send` takes an address: it answers the newest mail with
them, or with a first line `Subject: ...`, starts a new conversation.

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

## Testing the user's bots

An account can have several AI powered chat bots (set up by the user in the
app), each answering its own private chats; no chat has two. `dry-run`
prints the reply the bot answering the chat would give, without sending or
recording anything. It works while the bot is paused.

```sh
wiregram dry-run mattermost alice bob                    # newest incoming message
wiregram dry-run -message p9x2… mattermost alice bob     # a given message id
wiregram dry-run -message p9x2… -message q7d1… mattermost alice bob
# {"agent":"Support Bot","chat":"8f3k…","chat_title":"bob","reply_to":"p9x2…",
#  "message":"are you coming?","reply":"Yes, in ten minutes."}
```

- `agent` is the name of the bot that answered.
- It uses up to 20 earlier messages as history, and the bot's prompt,
  knowledge and media mode. One line per message.
- It refuses group chats, chats no bot of the account answers, the user's
  own messages, and media messages for text-only bots.
- `-message` goes before the positional arguments.

## Summaries

`summarize` prints a summary of a chat, written by the AI provider the user
set for summaries in the app (Settings > AI). It covers the unread messages,
else the recent ones, or the newest COUNT with `-n`, with up to 20 earlier
messages of any thread they reply in as context. Names and what is asked of the
user are marked `**…**`. The chat's messages go to that provider.

```sh
wiregram summarize mattermost alice town-square
wiregram summarize -n 50 mattermost alice town-square
# {"chat":"4hd9…","chat_title":"Town Square","messages":14,
#  "via":"OpenRouter · openai/gpt-4o-mini","summary":"- Ana moved …"}
```

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
