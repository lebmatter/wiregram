# Unread & Mentions

Each expanded account in the sidebar has two rows that collect what needs you. Email
accounts have neither: their folders show their unread counts.

## Unread

<kbd>u</kbd> (or the **Unread** row) shows a column of full-width cards, one per chat
with unread messages. The card's border and tag show the kind of chat:

| Kind | Colour |
|------|--------|
| DM | green |
| Group | blue |
| Channel | yellow |

Replies are marked `↳` with a `thread` tag. <kbd>Enter</kbd> opens the chat;
<kbd>Esc</kbd> in that chat brings you back to Unread. <kbd>r</kbd> reloads.

## Mentions

<kbd>m</kbd> (or the **Mentions** row) shows the same cards for groups and channels
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
