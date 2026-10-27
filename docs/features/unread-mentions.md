# Unread & Mentions

Each expanded account in the sidebar has two chips that collect what needs you. Email
accounts have neither: their folders show their unread counts.

## Unread

<kbd>u</kbd> (or the **unread** chip) shows a column of full-width cards, one per chat
with unread messages, each only as tall as its messages. The card's top border holds
`#` (group or channel) or `@` (person) and the chat's name, its team as a tag on
Mattermost, then on the right the time of the newest message (`5m ago`,
`yesterday 22:10`) and the unread count.

Inside, authors sit in a column so every message starts in line (channels, which post
as themselves, have none); replies are marked `↳`. The same message sent several
times in a row shows once with `×3`. Media is a small tag such as `photo` before its
details, emoji codes like `:fire:` are dim, and in a message mentioning you the
`@names` are in the accent.

A card shows its newest three messages, a line each. The selected card opens up to
show every unread message it loaded (up to 20) in full, wrapped and with its line
breaks; moving to another card folds it back. When it is taller than the pane, ↑/↓
and the wheel scroll through it first (the rule over its keys counts the lines above
and below), then move on to the next card.

The selected card lists its keys: <kbd>Enter</kbd> opens the chat (<kbd>Esc</kbd> there
brings you back to Unread), <kbd>t</kbd> opens the newest reply's thread where the
server keeps threads (Mattermost), and <kbd>m</kbd> marks the chat read and drops the
card. <kbd>r</kbd> reloads.

## Mentions

<kbd>m</kbd> (or the **@ mentions** chip) shows the same cards for groups and channels
with recent messages mentioning you. <kbd>Enter</kbd> opens the chat with the mention
selected, in its thread on Mattermost.

Where the mentions come from:

| Network | Source |
|---------|--------|
| Telegram | Telegram's mentions search over recent groups |
| Mattermost | The server's mention search |
| Matrix | Highlight notifications from the homeserver |
| WhatsApp | Messages seen since Wiregram connected |
| Email | No Mentions row |

A mention is `@you`, or a chat-wide `@all`, `@channel`, `@here` or `@room`. Replies
to you are not mentions.
