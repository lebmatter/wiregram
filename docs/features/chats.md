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

## Reopening a chat

A chat you opened before in this session draws at once from memory, then Wiregram asks
the server for its newest messages and puts them in place: anything edited, deleted or
reacted to meanwhile is updated. While the account is online, edits, deletes and
reactions keep the remembered chats current even when you are elsewhere. If events may
have been missed (a reconnect, or a gap the server could not fill), only the newest
messages are kept and older ones load again as you scroll. Messages are held in memory
only, never written to disk. Turn it off with **Keep chats in memory** in
**Settings → Chat…**.

## Messages

Each message is one block:

```
  14:02 alice: are you coming tonight?
* 14:03 you: ↳ alice  are you coming tonight?
        on my way
         👍 2   ↳ 1 reply · last 14:05  · ✓✓
```

- A 2-cell gutter, with `*` when the message was edited while you watched. The selected
  message has an accent bar there, its time in the accent and its block tinted.
- `HH:MM` and the sender; your own messages are `you`. Channels drop the sender, and a
  message soon after another from the same sender leaves the name out.
- The nick style, in **Settings → Chat…**, sets how the sender is drawn. `nick:` (the
  default) starts the text right after the name and wraps it under the time. `<nick>`
  pads every name to one column so all text lines up:

  ```
    14:02 alice         are you coming tonight?
  ```
- The text, keeping its line breaks. Bold, italic, strikethrough, code and links are
  drawn from each network's own formatting; links are underlined, `@names` coloured,
  emoji codes like `:fire:` dim, and lines starting `> ` are quotes behind a dim bar.
- Under the text: an image or link preview card, then a meta row: reactions as chips
  (your own count in the accent), a `↳ N replies · last HH:MM` chip, pinned, and
  `✓`/`✓✓` for sent and read. Clicking a reaction adds yours or takes it back; the
  reaction blinks as it changes. Clicking the replies chip opens the thread.

Private chats are named by the person's first name, else username, groups and channels
by their title. Titles put `@` before a person and `#` before a group or channel, with
the team as a tag on Mattermost.

A message that mentions you (`@you`, or a chat-wide `@all`, `@channel`, `@here` or
`@room`) is tinted. A plain reply to you is not a mention.

Non-text messages show as dim tags, such as `[photo] 1280x720, 245 KB`, with ` — `
before a caption. See [Media](/features/media).

## Replies and quotes

A reply puts a dim `↳ sender  excerpt` pointer on its header row, with its text below; a
run of replies to the same message shows the pointer once. If the original is older
than the loaded history, Wiregram fetches it, or reads `earlier message` where it can't.
**Go to quote** in the message's actions jumps to the original.

## Threads

Replies form threads. In wide terminals, selecting a threaded message opens the thread
pane beside the chat, titled `thread · #chat`, and a click on the blank part of a message's
row opens it too, empty when the message has no replies so you can start a thread; <kbd>Alt+T</kbd>, a click on its replies
chip or **Thread** in its actions open it too (in the chat's place on narrow terminals).
The open thread's replies chip is solid. The pane shows that first message with its day and
time, then the reply count and the replies. <kbd>Enter</kbd> there replies in the thread,
<kbd>↑</kbd>/<kbd>↓</kbd> still move through the chat, and <kbd>Esc</kbd> closes it. On
Mattermost, where threads live on the server, opening one fetches the whole thread once.

The footer's badge reads `CHAT`, or `THREAD` while the thread pane is open.

## Profiles

Click a sender's name, or an `@name` in a message, to open their profile over the chat. An `@name`
that names no one says so instead. It shows what the network's
user API returns:

- **Telegram:** name, usernames, phone, bio, birthday, groups in common, last seen and
  whether the account is a bot, verified, premium or a contact. Someone known only from a
  group shows what the group shared.
- **Mattermost:** name, username, position, email, custom status, status and last
  activity, local time and locale.
- **Matrix:** display name, user ID and, where the homeserver shares it, presence and
  status message.
- **WhatsApp:** name, profile name, phone, about text and, for a business, its category,
  address and email.

Under it, **go to chat** opens your private chat with them (<kbd>Enter</kbd> or a click),
starting one if there is none; on Matrix that invites them to a new room. Email has no
profiles. Any other key or click closes it.

## Message actions

Select a message and press <kbd>Enter</kbd> on an empty composer, or right-click it.
The actions modal opens over the faded chat. Where the network has reactions, it starts
with a `+` reaction search field over a row of matching emoji: type to filter,
<kbd>←</kbd>/<kbd>→</kbd> to pick one, <kbd>Enter</kbd> to react.

Below come only the actions the message allows:

- reply, thread, go to quote
- edit (your own messages; the original text, formatting and all, is kept for editing),
  within the network's time limit: 48 hours on Telegram (none for channel posts and
  Saved Messages), 20 minutes on WhatsApp, the server's limit on Mattermost, none on
  Matrix
- forward
- open, play or save the attachment
- pin
- delete (asks first); WhatsApp deletes your messages for everyone only in their first
  60 hours

## Composing

Type and press <kbd>Enter</kbd> to send. The input, in the chat and the thread pane,
wraps long text and grows with it up to eight rows, then scrolls. <kbd>Esc</kbd> cancels a reply.

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

<kbd>Ctrl+F</kbd> (or the **search** chip) opens one search modal. Type a name and it
lists your chats and the people, groups and channels the server finds, each group or
channel marked as such in dim text beside its name; <kbd>Enter</kbd> opens the chat. On
Mattermost picking a public channel you haven't joined joins it.

Opened from a chat, the modal sits over it and adds a **messages in #chat** section under
the chats: the open chat's messages that match. <kbd>↑</kbd>/<kbd>↓</kbd> run through both
sections, and <kbd>Enter</kbd> or a click on a message jumps to it.

Once you type, the first row is **search all chats for "…"**, selected from the start, so
typing and pressing <kbd>Enter</kbd> searches the messages of every chat on the account
(Telegram, Mattermost and Matrix; not WhatsApp or email). The results open in the chat's
place as cards like [Unread](/features/unread-mentions), one per chat with its matching
messages. <kbd>Enter</kbd> opens the chat on the newest match, and <kbd>Esc</kbd> from it
comes back to the results. This view is reached only from the search modal.

## Link previews

A message with a link gets a small card with the site, title and two lines of
description. Previews come only from the server (Telegram, Mattermost's OpenGraph
embeds, WhatsApp, the Matrix homeserver). Wiregram never fetches linked pages itself.

## Privacy

- <kbd>Alt+H</kbd> toggles private mode, hiding the chat's history.
- The chat locks when the terminal loses focus and on <kbd>Alt+L</kbd>; any key unlocks it.
  Focus moving to the viewer of an attachment you clicked does not lock it.
- <kbd>Alt+D</kbd> deletes a private chat for you; <kbd>Ctrl+Alt+D</kbd> deletes it
  for both sides (Telegram).
