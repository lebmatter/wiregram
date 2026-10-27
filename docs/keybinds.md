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
| <kbd>Esc</kbd>, <kbd>Ctrl+C</kbd> | Quit (asks first) |
| <kbd>s</kbd> | In a widget's modal: open the plugin's settings |

## Chat list

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select chat, chip, section header or account |
| <kbd>Enter</kbd> | Open chat (on email, a folder) or chip, switch to the selected account, fold a section, or open a bot, its inbox, or create one (**new bot…**) in the bots section |
| <kbd>Space</kbd> | Fold or unfold the selected chat's section |
| <kbd>c</kbd> | Compose a mail (email) |
| <kbd>Ctrl+F</kbd> | Search chats, people, groups and channels; on Mattermost picking a public channel joins it |
| <kbd>u</kbd> | Unread: cards for every chat with unread messages |
| <kbd>m</kbd> | Mentions: cards for the groups and channels mentioning you (not email) |
| <kbd>a</kbd> | Accounts (add, sign in, retry, Settings…) |
| <kbd>r</kbd> | Refresh chats |
| <kbd>Alt+D</kbd> | Delete private chat (you only) |
| <kbd>Ctrl+Alt+D</kbd> | Delete private chat (both sides) |
| <kbd>Esc</kbd> | Close the open chat (wide terminals), else back to Accounts |

## Unread / Mentions / Search results

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select card |
| <kbd>Enter</kbd> | Open the chat; a mention opens selected, in its thread on Mattermost |
| <kbd>t</kbd> | Open the newest reply's thread (Mattermost) |
| <kbd>m</kbd> | Mark the chat read (Unread) |
| <kbd>Ctrl+F</kbd> | Search |
| <kbd>r</kbd> | Reload |
| <kbd>Esc</kbd> | Back |

## Search / Forward to / Mention

| Key | Action |
|-----|--------|
| Type | Filter |
| <kbd>↑</kbd>/<kbd>Ctrl+P</kbd>, <kbd>↓</kbd>/<kbd>Ctrl+N</kbd> | Select |
| <kbd>PgUp</kbd>/<kbd>PgDn</kbd> | Page |
| <kbd>Enter</kbd> | After typing, search all chats' messages (the first row; Telegram, Mattermost and Matrix); else open the chat, go to the message (search from a chat), forward the message to it, or insert the mention |
| <kbd>Esc</kbd> | Clear the query, then close; Mention closes at once and types the `@` and query as written |
| Click outside | Close |

## Settings

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select an account on the left, or a setting on the right |
| <kbd>Enter</kbd> | Show the account's settings, or change the selected one |
| <kbd>Tab</kbd>, <kbd>→</kbd>/<kbd>l</kbd> | Move to the account's settings |
| <kbd>Space</kbd> | Change the selected setting |
| <kbd>Esc</kbd>, <kbd>←</kbd>/<kbd>h</kbd> | Back to the accounts, then to where Settings was opened, or quit when there is nowhere to go back to |

In **AI providers…**, <kbd>Enter</kbd> opens a provider, edits one of its rows in place
(<kbd>Enter</kbd> saves, <kbd>Esc</kbd> cancels), or runs **+ Add provider…** or
**Delete provider…**.

## Chat

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Select message (<kbd>↑</kbd> at the top loads older ones) |
| <kbd>Enter</kbd> | Send text; with an empty input, open the selected message's actions |
| <kbd>Ctrl+U</kbd> | Upload a file, in a modal over the chat (or click <kbd>upload+</kbd> at the end of the input) |
| <kbd>Ctrl+F</kbd> | Search, in a modal over the chat: chats and people, then this chat's messages (<kbd>Enter</kbd> or a click goes to one) |
| <kbd>@</kbd> | Mention a member (group chats, at the start of a word) |
| <kbd>Alt+T</kbd> | Open the selected message's thread beside the chat |
| <kbd>Alt+G</kbd> | Challenge the chat to a [game](/features/games) (DMs and groups; or click <kbd>game+</kbd> at the end of the input) |
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
| <kbd>]</kbd>, <kbd>[</kbd> | Move to the reader and back, when it sits beside the list (120+ column pane) |
| <kbd>c</kbd> | Compose a new mail |
| <kbd>r</kbd>, <kbd>R</kbd>, <kbd>f</kbd> | Reply, reply to all, forward |
| <kbd>d</kbd> | Move to Trash |
| <kbd>a</kbd> | Archive (where the server has an archive) |
| <kbd>m</kbd> | Move to another folder or label |
| <kbd>u</kbd> | Mark read, or the newest mail unread |
| <kbd>/</kbd> | Search the folder on the server (<kbd>Esc</kbd> clears the results) |
| <kbd>v</kbd> | Visual mode: <kbd>Space</kbd> picks rows, then <kbd>d</kbd>, <kbd>a</kbd>, <kbd>m</kbd> or <kbd>u</kbd> acts on them; <kbd>Esc</kbd> leaves it |
| <kbd>Esc</kbd> | Close the reader, else back |

## Mail reader

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Scroll |
| <kbd>PgUp</kbd>/<kbd>b</kbd>, <kbd>PgDn</kbd>/<kbd>Space</kbd> | Page |
| <kbd>Home</kbd>/<kbd>g</kbd>, <kbd>End</kbd>/<kbd>G</kbd> | Top, bottom |
| <kbd>o</kbd>, <kbd>s</kbd> | Open the attachment, or save it to `~/Downloads` |
| <kbd>r</kbd>, <kbd>R</kbd>, <kbd>f</kbd>, <kbd>d</kbd>, <kbd>a</kbd>, <kbd>m</kbd>, <kbd>u</kbd> | As in the folder, on this mail |
| <kbd>Enter</kbd> | Type a quick reply; <kbd>Enter</kbd> sends it, <kbd>Esc</kbd> stops typing |
| <kbd>Esc</kbd> | Close |

## Mail composer

| Key | Action |
|-----|--------|
| <kbd>Tab</kbd>, <kbd>Shift+Tab</kbd> | Next and previous field: To, Cc, Subject, Attach, text; in To and Cc, <kbd>Tab</kbd> first takes the address offered |
| <kbd>Enter</kbd> in Attach | Attach the file typed (<kbd>Tab</kbd> completes paths) |
| <kbd>Backspace</kbd> in an empty Attach | Drop the last file |
| <kbd>Ctrl+S</kbd> | Send |
| <kbd>Esc</kbd> | Close, keeping the draft (<kbd>c</kbd> opens it again) |
| <kbd>Ctrl+X</kbd> | Discard the draft |

## Game

A [game](/features/games) plays in a pane beside the chat; a click on a 🎮 invite joins it.

| Key | Action |
|-----|--------|
| Game keys | Play: each game lists its own under the board |
| <kbd>Tab</kbd> | Move between the sidebar, the chat and the game |
| <kbd>Esc</kbd> | Leave the game (asks first while it is being played) |

## Thread

The thread pane opens by itself on a threaded message in wide terminals, and on a click
beside a message's text there (empty, to start a thread, when it has no replies); also with
<kbd>Alt+T</kbd>, a click on a message's replies chip, or **Thread** in its actions.

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Move through the chat's messages |
| <kbd>Ctrl+U</kbd> | Upload a file into the thread (or click its <kbd>upload+</kbd>) |
| <kbd>Enter</kbd> | Reply in thread; with an empty input, open the selected message's actions |
| <kbd>@</kbd> | Mention a member, as in the chat |
| <kbd>Esc</kbd> | Close thread |

## Upload

The upload modal sits over the faded chat; a click outside it closes it.

| Key | Action |
|-----|--------|
| Type | A path, starting in the last folder; the entries it matches are listed under it |
| <kbd>↑</kbd>/<kbd>↓</kbd>, <kbd>Tab</kbd> | Choose an entry, put it in the path |
| <kbd>Enter</kbd> | Open the folder or pick the file (or click it), then send with the optional caption |
| <kbd>Esc</kbd> | Back, then close |

## Message actions

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd> | Select an action: reply, thread, go to quote, edit, forward, open/play/save attachment, pin, delete |
| Type | Search reactions by name in the `+` field (<kbd>Backspace</kbd> deletes) |
| <kbd>←</kbd>/<kbd>→</kbd> | Select a reaction on the emoji row |
| <kbd>Enter</kbd> | Do it (deletes ask first) |
| <kbd>Esc</kbd> | Close (a click outside it closes it too) |

## Bot pane

A bot's **NAME @handle** row in the account's Bots section.

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>k</kbd>, <kbd>↓</kbd>/<kbd>j</kbd> | Select a row |
| <kbd>Enter</kbd> | Change it (edits in place: <kbd>Enter</kbd> saves, <kbd>Esc</kbd> cancels); Chats opens the chat picker |
| <kbd>p</kbd> | Pause or resume the bot |
| <kbd>Esc</kbd> | Back |

### Chat picker

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>, <kbd>↓</kbd> | Select **All private chats**, **Selected users** or a person |
| <kbd>Space</kbd> | Pick the radio row, or tick or untick the person, saved at once (chats another bot answers can't be ticked) |
| Type | Under Selected users, filter the people recently chatted with; on Telegram and Mattermost it also searches everyone |
| <kbd>Enter</kbd>, <kbd>Esc</kbd> | Close (a click outside it closes too) |

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
