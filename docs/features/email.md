# Email

Email accounts read mail over IMAP and send over SMTP. Set one up with the
[Email setup guide](/setup-guides/email).

## Folders

An email account's chats are its folders: **Inbox**, **Sent**, **Archive** (where the
server has one; on Gmail, archiving moves mail to All Mail, which has no row) and the
labels you show (see [Labels](#labels)). Each has its unread count. Above them sit
the **compose** and **labels** chips. Entering the account opens the inbox.

## Conversations

A folder opens as a list of conversations as wide as the pane, the newest first, under
a heading for each day. The pane's title counts the unread ones:

```
╭─ EM Inbox · 2 unread ───────────────────────────╮
│  today ───────────────────────────────────────  │
│ ▎ ● Alice Smith                           14:05 │
│ ▎   Quarterly report (3)                        │
│     Bob                                   09:12 │
│     Lunch on Friday?                            │
│  yesterday ───────────────────────────────────  │
│     Carol                                 18:40 │
│     Invoice 2291                                │
```

Each conversation takes two lines: who it is with (who wrote last, or whom you wrote
to) and the time of the newest mail, then its subject with the number of mails. Unread
conversations are bold with a dot; read ones are dimmed.
Mails are grouped by their reply headers, or failing those, by a `Re:` or `Fwd:` subject.
Your replies join the conversations they answer, wherever they are filed.

<kbd>Space</kbd> expands a conversation into its mails, oldest first, and collapses it again.
While no mail is open, <kbd>←</kbd> moves to the sidebar and <kbd>→</kbd> back to the folder
(terminals 100 columns or wider).

## Reading

<kbd>Enter</kbd> or a click opens the selected mail, or a conversation's newest, in the
reader with its headers and whole text, and marks it read. Opening a conversation marks
all of it read.

When the folder pane is 120 columns or wider, the reader opens beside the list and takes
the focus; narrower, it takes the list's place. <kbd>Esc</kbd> closes it. <kbd>[</kbd>
moves back to the list with the reader still open, and <kbd>]</kbd> to the reader again.

Under the mail sits a quick reply box. <kbd>Enter</kbd> (or a click on it) starts typing;
the box wraps and grows with the text. <kbd>Enter</kbd> sends it as a reply to the mail,
quoting it as <kbd>r</kbd> would, and <kbd>Esc</kbd> stops typing, keeping the text until
the reader closes.

In the reader:

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd>, <kbd>j</kbd>/<kbd>k</kbd> | Scroll |
| <kbd>Space</kbd>/<kbd>b</kbd> | Page |
| <kbd>g</kbd>/<kbd>G</kbd> | Top, bottom |
| <kbd>o</kbd> | Open the attachment with the default app |
| <kbd>s</kbd> | Save the attachment to `~/Downloads` |
| <kbd>Enter</kbd> | Type a quick reply |
| <kbd>Esc</kbd> | Close |

HTML mail is reduced to text, and nothing it references (images, trackers) is fetched.

## Actions

These keys work in the list, on the selected conversation or mail, and in the reader,
on the mail being read:

| Key | Action |
|-----|--------|
| <kbd>c</kbd> | Compose a new mail |
| <kbd>r</kbd> | Reply |
| <kbd>R</kbd> | Reply to all |
| <kbd>f</kbd> | Forward, with the attachments |
| <kbd>d</kbd> | Move to Trash |
| <kbd>a</kbd> | Archive (where the server has an archive) |
| <kbd>m</kbd> | Move to another folder or label, picked from a list |
| <kbd>u</kbd> | Mark read; on read mail, mark the newest unread |
| <kbd>/</kbd> | Search the folder on the server; <kbd>Esc</kbd> clears the results |

Trash doesn't ask first: mail can be moved back from it. On Gmail, Trash removes the mail
from every label.

### Several at once

<kbd>v</kbd> starts visual mode, picking the selected row. <kbd>Space</kbd> picks or drops
more conversations or mails (`*` marks them, and the title counts them), then
<kbd>d</kbd>, <kbd>a</kbd>, <kbd>m</kbd> or <kbd>u</kbd> acts on all of them at once.
<kbd>Esc</kbd> leaves visual mode.

## Writing

<kbd>c</kbd>, <kbd>r</kbd>, <kbd>R</kbd> and <kbd>f</kbd> open the composer, a large
modal over the screen: **To**, **Cc**, **Subject** and **Attach** fields over the text. Replies quote the
original below the cursor; replies to all copy the other recipients.

| Key | Action |
|-----|--------|
| <kbd>Tab</kbd>, <kbd>Shift+Tab</kbd> | Next and previous field |
| <kbd>Tab</kbd> in To or Cc | Take the address offered, from the people you have mailed |
| <kbd>Enter</kbd> in Attach | Attach the file typed; <kbd>Tab</kbd> completes the path |
| <kbd>Backspace</kbd> in an empty Attach | Drop the last file |
| <kbd>Ctrl+S</kbd> | Send |
| <kbd>Esc</kbd> | Close, keeping the draft: <kbd>c</kbd> opens it again |
| <kbd>Ctrl+X</kbd> | Discard the draft |

A mail's attachments come to at most 25 MB. One draft is kept at a time.

## Labels

Gmail labels (on other servers, folders) can be shown in the sidebar. The labels are read
when you sign in and again every few minutes. The **labels** chip opens a list of every label,
each ticked when it is in the sidebar:

| Key | Action |
|-----|--------|
| <kbd>Space</kbd>, click | Show or hide the label in the sidebar, at once |
| <kbd>Enter</kbd> | Show the label and open it |
| Type | Filter the labels |
| <kbd>Esc</kbd> | Close |

Only shown labels are synced, several at once, each on its own connection, as are the
inbox and Sent. Folders other than the inbox refresh every few minutes and after you
act on them; the inbox updates as mail arrives.

## Cache

Mail is cached in `wiregram.db` for the window set by the account's **Keep mail cached**
setting: 30 days, 90 days (the default), 1 year or all. Pruning the cache never touches
the server. Mail moved or deleted in another client leaves its folder here at the next
sync.

## What email doesn't do

Email has no reactions, mentions, Unread or Mentions rows, and
[AI powered chat bots](/features/chat-bots) never answer mail.
