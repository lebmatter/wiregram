# Keybinds

The footer shows the keys for whatever has focus, and hides those the account's network
doesn't support. This page lists all of them; `wiregram -h` prints the same list.

Messages have no per-message shortcuts: select a message and press <kbd>Enter</kbd> on
an empty composer (or right-click it) to open its actions.

## Anywhere

| Key | Action |
|-----|--------|
| <kbd>Tab</kbd> | Move between chat list and chat (wide terminals) |

## Accounts

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select account or menu row |
| <kbd>Enter</kbd> | Open the account, or the selected row (Settings…, Add account, Quit) |
| <kbd>r</kbd> | Reconnect the selected account |
| <kbd>q</kbd>, <kbd>Ctrl+C</kbd> | Quit (asks first) |

## Chat list

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select chat or account |
| <kbd>Enter</kbd> | Open chat (on email, a folder), switch to the selected account, or open a bot, its Inbox, or create one (**New bot…**) in the Bots section |
| <kbd>c</kbd> | Compose a mail (email) |
| <kbd>/</kbd> | Search users; on Mattermost also channels, joining a public one when picked |
| <kbd>u</kbd> | Unread: cards for every chat with unread messages |
| <kbd>m</kbd> | Mentions: cards for the groups and channels mentioning you (not email) |
| <kbd>a</kbd> | Accounts (add, sign in, retry, Settings…) |
| <kbd>r</kbd> | Refresh chats |
| <kbd>Alt+D</kbd> | Delete private chat (you only) |
| <kbd>Ctrl+Alt+D</kbd> | Delete private chat (both sides) |
| <kbd>Esc</kbd> | Close the open chat (wide terminals), else back to Accounts |

## Unread / Mentions

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select card |
| <kbd>Enter</kbd> | Open the chat; a mention opens selected, in its thread on Mattermost |
| <kbd>/</kbd> | Search users |
| <kbd>r</kbd> | Reload |
| <kbd>Esc</kbd> | Back |

## Search users / Forward to / Mention

| Key | Action |
|-----|--------|
| Type | Filter |
| <kbd>↑</kbd>/<kbd>Ctrl+P</kbd>, <kbd>↓</kbd>/<kbd>Ctrl+N</kbd> | Select |
| <kbd>PgUp</kbd>/<kbd>PgDn</kbd> | Page |
| <kbd>Enter</kbd> | Open the chat, forward the message to it, or insert the mention |
| <kbd>Esc</kbd> | Clear the query, then close; Mention closes at once and types the `@` and query as written |
| Click outside | Close |

## Settings

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select an account on the left, or a setting on the right |
| <kbd>Enter</kbd> | Show the account's settings, or change the selected one |
| <kbd>Tab</kbd>, <kbd>→</kbd>/<kbd>l</kbd> | Move to the account's settings |
| <kbd>Space</kbd> | Change the selected setting |
| <kbd>Esc</kbd>, <kbd>←</kbd>/<kbd>h</kbd> | Back to the accounts, then to where Settings was opened |
| <kbd>q</kbd> | Quit |

In **AI providers…**, <kbd>Enter</kbd> opens a provider, edits one of its rows in place
(<kbd>Enter</kbd> saves, <kbd>Esc</kbd> cancels), or runs **+ Add provider…** or
**Delete provider…**.

## Chat

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Select message (<kbd>↑</kbd> at the top loads older ones) |
| <kbd>Enter</kbd> | Send text; with an empty input, open the selected message's actions |
| <kbd>Ctrl+U</kbd> | Upload file (browse, or <kbd>/</kbd> to type a path) |
| <kbd>Ctrl+F</kbd> | Search messages (<kbd>↑</kbd>/<kbd>↓</kbd>, <kbd>Enter</kbd> jumps, <kbd>Esc</kbd> cancels) |
| <kbd>@</kbd> | Mention a member (group chats, at the start of a word) |
| <kbd>Alt+H</kbd> | Toggle private mode (hide history) |
| <kbd>Alt+L</kbd> | Lock the chat (also locks when the terminal loses focus) |
| <kbd>Alt+D</kbd> | Delete chat (you only) |
| <kbd>Ctrl+Alt+D</kbd> | Delete chat (both sides) |
| <kbd>Esc</kbd> | Cancel the reply, else back to Unread (or Mentions) |
| <kbd>Ctrl+C</kbd> | Close chat |
| Any key | Unlock the locked chat |

## Email folder

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select a conversation, or a mail in an expanded one |
| <kbd>PgUp</kbd>/<kbd>PgDn</kbd>, <kbd>g</kbd>/<kbd>G</kbd> | Page, top, bottom |
| <kbd>Space</kbd> | Expand or collapse the conversation |
| <kbd>Enter</kbd> | Read the mail (a conversation: its newest), marking it read |
| <kbd>]</kbd>, <kbd>[</kbd> | Move to the reader and back, when it sits beside the list (140+ column pane) |
| <kbd>c</kbd> | Compose a new mail |
| <kbd>r</kbd>, <kbd>R</kbd>, <kbd>f</kbd> | Reply, reply to all, forward |
| <kbd>d</kbd> | Move to Trash |
| <kbd>a</kbd> | Archive (where the server has an archive) |
| <kbd>m</kbd> | Move to another folder or label |
| <kbd>u</kbd> | Mark read, or the newest mail unread |
| <kbd>/</kbd> | Search the folder on the server (<kbd>Esc</kbd> clears the results) |
| <kbd>v</kbd> | Visual mode: <kbd>Space</kbd> picks rows, then <kbd>d</kbd>, <kbd>a</kbd>, <kbd>m</kbd> or <kbd>u</kbd> acts on them; <kbd>Esc</kbd> leaves it |
| <kbd>Esc</kbd> | Back |

## Mail reader

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Scroll |
| <kbd>PgUp</kbd>/<kbd>b</kbd>, <kbd>PgDn</kbd>/<kbd>Space</kbd> | Page |
| <kbd>Home</kbd>/<kbd>g</kbd>, <kbd>End</kbd>/<kbd>G</kbd> | Top, bottom |
| <kbd>o</kbd>, <kbd>s</kbd> | Open the attachment, or save it to `~/Downloads` |
| <kbd>r</kbd>, <kbd>R</kbd>, <kbd>f</kbd>, <kbd>d</kbd>, <kbd>a</kbd>, <kbd>m</kbd>, <kbd>u</kbd> | As in the folder, on this mail |
| <kbd>Esc</kbd>, <kbd>q</kbd> | Close |

## Mail composer

| Key | Action |
|-----|--------|
| <kbd>Tab</kbd>, <kbd>Shift+Tab</kbd> | Next and previous field: To, Cc, Subject, Attach, text; in To and Cc, <kbd>Tab</kbd> first takes the address offered |
| <kbd>Enter</kbd> in Attach | Attach the file typed (<kbd>Tab</kbd> completes paths) |
| <kbd>Backspace</kbd> in an empty Attach | Drop the last file |
| <kbd>Ctrl+S</kbd> | Send |
| <kbd>Esc</kbd> | Close, keeping the draft (<kbd>c</kbd> opens it again) |
| <kbd>Ctrl+X</kbd> | Discard the draft |

## Thread

The thread pane opens by itself on a threaded message in wide terminals.

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Move through the chat's messages |
| <kbd>Enter</kbd> | Reply in thread; with an empty input, open the selected message's actions |
| <kbd>@</kbd> | Mention a member, as in the chat |
| <kbd>Esc</kbd> | Close thread |

## Upload

| Key | Action |
|-----|--------|
| Arrows, <kbd>Enter</kbd> | Browse folders and pick a file |
| <kbd>/</kbd>, <kbd>~</kbd> | Type a path instead (<kbd>Tab</kbd> completes, <kbd>↑</kbd>/<kbd>↓</kbd> cycle matches) |
| <kbd>Enter</kbd> | Send with the optional caption |
| <kbd>Esc</kbd> | Back, then cancel |

## Message actions

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Select an action: reply, thread, go to quote, edit, forward, open/play/save attachment, pin, delete |
| Type | Search reactions by name in the `reaction:` field (<kbd>Backspace</kbd> deletes) |
| <kbd>←</kbd>/<kbd>→</kbd> | Select a reaction on the emoji row |
| <kbd>Enter</kbd> | Do it (deletes ask first) |
| <kbd>Esc</kbd> | Close (a click outside it closes it too) |

## Bot pane

A bot's **NAME @handle** row in the account's Bots section.

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select a row |
| <kbd>Enter</kbd> | Change it (edits in place: <kbd>Enter</kbd> saves, <kbd>Esc</kbd> cancels); Chats opens the chat picker |
| <kbd>Esc</kbd> | Back |

### Chat picker

| Key | Action |
|-----|--------|
| Type | Filter the private chats by name |
| <kbd>↑</kbd>, <kbd>↓</kbd> | Select a chat |
| <kbd>Space</kbd> | Tick or untick it (chats another bot answers can't be ticked) |
| <kbd>Tab</kbd> | Switch between all private chats and the ticked ones |
| <kbd>Enter</kbd> | Save |
| <kbd>Esc</kbd> | Cancel (a click outside it cancels too) |

The New bot… and Add provider… wizards: <kbd>Enter</kbd> answers or picks,
<kbd>↑</kbd>/<kbd>↓</kbd> choose, <kbd>Esc</kbd> goes back a question.

## Bot suggestion

When a bot asks first, its reply waits in a card above the composer. These keys work
while the composer is empty; the card's buttons can be clicked too.

| Key | Action |
|-----|--------|
| <kbd>Ctrl+Y</kbd> | Approve: send it as the bot |
| <kbd>Ctrl+E</kbd> | Edit: move it into the composer, to send as yourself |
| <kbd>Ctrl+X</kbd> | Discard |

## Media viewer

| Key | Action |
|-----|--------|
| <kbd>Esc</kbd>, <kbd>Ctrl+C</kbd> | Stop and return to chat |
