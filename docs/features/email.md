# Email

Email accounts read mail over IMAP and send over SMTP. Set one up with the
[Email setup guide](/setup-guides/email).

## One chat per correspondent

Each person you exchange mail with is one chat. A mail's bold subject leads its block,
and the chat shows the first few lines of its text. Quoted originals are trimmed in the
chat and kept in the reader.

## Reading

Select a mail and press <kbd>Enter</kbd> to open its actions; **Read** opens the reader
with the headers and the whole text. In the reader:

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd>, <kbd>j</kbd>/<kbd>k</kbd> | Scroll |
| <kbd>Space</kbd>/<kbd>b</kbd> | Page |
| <kbd>g</kbd>/<kbd>G</kbd> | Top, bottom |
| <kbd>Enter</kbd> | Actions: reply, save attachment, move to Trash… |
| <kbd>Esc</kbd>, <kbd>q</kbd> | Close |

HTML mail is reduced to text, and nothing it references (images, trackers) is fetched.

## Writing

Email chats write in a multiline draft:

- <kbd>Enter</kbd> adds a line, <kbd>Ctrl+S</kbd> sends, <kbd>Esc</kbd> keeps the draft.
- A new mail's first line is its subject.
- A first line `Subject: ...` starts a new conversation instead of a reply.

## Cache and deleting

Mail is cached in `wiregram.db` for the window set by the account's **Keep mail cached**
setting: 30 days, 90 days (the default), 1 year or all. Pruning the cache never touches
the server.

Deleting a mail moves it to Trash on the server.

## What email doesn't do

Email has no reactions, mentions or Mentions row, and [Chat Agents](/features/chat-agents)
never answer mail.
