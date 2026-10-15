# Chats

Every network's messages look the same, in the chat and in the thread pane.

## What each network supports

The footer and the actions modal only offer what the account's network supports.

| | Telegram | WhatsApp | Matrix | Mattermost |
|---|:-:|:-:|:-:|:-:|
| Reactions | ✓ | ✓ | ✓ | ✓ |
| Edit | ✓ | ✓ | ✓ | ✓ |
| Forward | ✓ | ✓ | | |
| Pin | ✓ | | | ✓ |
| Upload | ✓ | ✓ | ✓ | ✓ |
| Search messages | ✓ | | ✓ | ✓ |
| Read receipts (`✓✓`) | ✓ | ✓ | ✓ | |
| Delete chat | ✓ | | | |

Email has folders of conversations instead of chats; see [Email](/features/email).

## Messages

Each message is one block:

```
  [14:02] alice: are you coming tonight?
* [14:03] you: ┃ alice: are you coming tonight?
          on my way
          👍 2 · ✓✓
```

- A 2-cell gutter, with `*` when the message was edited while you watched.
- `[HH:MM]` and the sender; your own messages are `you`. Channels drop the sender.
- The nick style, in **Settings → Chat…**, sets how the sender is drawn. `nick:` (the
  default) starts the text right after the name and wraps it under the time. `<nick>`
  pads every name to one column so all text lines up:

  ```
    [14:02] <alice       > are you coming tonight?
  ```
- The text, keeping its line breaks. Bold, italic, strikethrough, code and links are
  drawn from each network's own formatting; links are underlined.
- Under the text: an image or link preview card, then a dim meta row with reactions
  (your own count in copper), the reply count, pinned, and `✓`/`✓✓` for sent and read.
  Clicking a reaction adds yours or takes it back; the reaction blinks as it changes.

Private chats are named by the person's first name, else username. Groups show their
title and channels show `Channel: TITLE`.

A message that mentions you (`@you`, or a chat-wide `@all`, `@channel`, `@here` or
`@room`) is tinted. A plain reply to you is not a mention.

Non-text messages show as dim tags, such as `[photo] 1280x720, 245 KB`, with ` — `
before a caption. See [Media](/features/media).

## Replies and quotes

A reply puts `┃ sender: excerpt` on its header row. If the original is older than the
loaded history, Wiregram fetches it, or reads `earlier message` where it can't.
**Go to quote** in the message's actions jumps to the original.

## Threads

Replies form threads. In wide terminals, selecting a threaded message opens the thread
pane beside the chat; <kbd>Enter</kbd> there replies in the thread and <kbd>Esc</kbd>
closes it. On Mattermost, where threads live on the server, opening one fetches the
whole thread once.

## Message actions

Select a message and press <kbd>Enter</kbd> on an empty composer, or right-click it.
The actions modal opens over the faded chat. Where the network has reactions, it starts
with a `reaction:` search field over a row of matching emoji: type to filter,
<kbd>←</kbd>/<kbd>→</kbd> to pick one, <kbd>Enter</kbd> to react.

Below come only the actions the message allows:

- reply, thread, go to quote
- edit (your own messages; the original text, formatting and all, is kept for editing)
- forward
- open, play or save the attachment
- pin
- delete (asks first)

## Composing

Type and press <kbd>Enter</kbd> to send. <kbd>Esc</kbd> cancels a reply.

- **Upload** with <kbd>Ctrl+U</kbd>: browse folders, or press <kbd>/</kbd> or
  <kbd>~</kbd> to type a path (<kbd>Tab</kbd> completes). Add an optional caption and
  press <kbd>Enter</kbd>.
- **Mention** with <kbd>@</kbd> at the start of a word in a group chat. A picker lists
  members; the pick becomes a real mention on each network: `@username` on Mattermost
  and Telegram, the Matrix ID on Matrix, `@number` on WhatsApp. Email has no mentions.

## Forwarding

Choose **forward** in a message's actions. The Forward to picker lists your chats;
type to filter and press <kbd>Enter</kbd>.

## Search

- <kbd>/</kbd> in the chat list opens **Search users**: type a name, <kbd>Enter</kbd>
  opens the chat. On Mattermost it finds channels too, and picking a public one you
  haven't joined joins it.
- <kbd>Ctrl+F</kbd> in a chat searches its messages: <kbd>↑</kbd>/<kbd>↓</kbd> move
  between hits, <kbd>Enter</kbd> jumps, <kbd>Esc</kbd> cancels.

## Link previews

A message with a link gets a small card with the site, title and two lines of
description. Previews come only from the server (Telegram, Mattermost's OpenGraph
embeds, WhatsApp, the Matrix homeserver). Wiregram never fetches linked pages itself.

## Privacy

- <kbd>Alt+H</kbd> toggles private mode, hiding the chat's history.
- The chat locks when the terminal loses focus and on <kbd>Alt+L</kbd>; any key unlocks it.
- <kbd>Alt+D</kbd> deletes a private chat for you; <kbd>Ctrl+Alt+D</kbd> deletes it
  for both sides (Telegram).
