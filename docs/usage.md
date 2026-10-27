# Usage

Run `wiregram` with no arguments in an interactive terminal. The footer shows the keys
for the current screen; [Keybinds](/keybinds) and `wiregram -h` list all of them.

<img class="wg-shot" src="/img/chat-mode.png" alt="Chat mode with the account sidebar">

## Screens

1. **Splash.** The logo shows for about a second.
2. **Account chooser.** Every account with its status (and its bots, if it has
   any), then Settings…, Add account and Quit. Under them sit the
   [plugin widgets](/features/plugins), such as Unread and Weather.
3. **Chat mode.** The chat list and the open chat. Inside an account the screen is
   the body and a footer with the keys for what has focus; notices show on the right
   of the footer.
4. **Settings.** Opened from the chooser's `Settings…` row or the sidebar's settings… row.
   See [Configuration](/configuration#settings).

## Layout

Terminals 100 columns wide or wider show the chat list as a sidebar beside the open chat;
<kbd>Tab</kbd> moves focus between them. Narrower terminals show one pane at a time.

The sidebar is an account tree. Every account is a row with an arrow, a status glyph,
a tag such as `TG` with the account's number, and its label. The active account is
expanded to a line of chips: unread, @ mentions, you (the chat with yourself: Saved
Messages on Telegram, your own DM on Mattermost and WhatsApp), archived (Telegram) and
/ search; then its chats in folding sections (teams on Mattermost, else groups or rooms
and channels, then direct), unread chats first; then its [bots](/features/chat-bots).
An email account is expanded to compose and labels chips, then its
[folders](/features/email#folders). <kbd>Space</kbd> folds a section.
<kbd>Enter</kbd> on another account switches to it. home (the account chooser) and
settings… are pinned at the bottom of the sidebar.

Chat text wraps at 120 columns or the terminal width, whichever is smaller.

## Mouse

- The **wheel** moves the selection of the list under the pointer, as <kbd>↑</kbd>/<kbd>↓</kbd>
  would, without moving focus. Over the thread pane it scrolls the thread.
- A **click** on a chat, account, card, menu row or picker entry opens it, as
  <kbd>Enter</kbd> would.
- A **click on a message** selects it. A click on the **blank part of its row**, beside the
  text, also opens its thread beside the chat in wide terminals; on a message without
  replies the thread starts empty, so you can start one.
- A **double click on its text** or a **right click** opens its actions.
- A **click on a reaction** under a message adds yours or takes it back, with a short blink.
- A **click on a sender's name** or an **`@name`** in a message opens their profile:
  what the network shares about them. <kbd>Enter</kbd> or a click on **go to chat** opens
  your private chat with them; any other key or click closes it.
- A **click on an attachment** (the image or the `[tag]` row) downloads it and opens it
  with `xdg-open` (`open` on macOS, the default app on Windows).
- Hold <kbd>Shift</kbd> while dragging to select text with the terminal.

The login and connecting screens leave the mouse to the terminal.

## Modals

Questions and pickers open as modals over the screen, with everything behind them faded:

- **Confirmations.** Quitting, deleting a chat, message, bot or provider, removing an
  account and Reset ask first. <kbd>y</kbd> or <kbd>Enter</kbd> on the action goes
  ahead, <kbd>Esc</kbd> cancels. Deletes and Reset start on Cancel.
- **Search users, Forward to and Mention** share one picker: type to filter,
  <kbd>Enter</kbd> to pick. Each row starts with `@` for a person, `#` for a channel or
  `⁂` for a group, the typed text is lit in the accent, and a Mattermost channel shows
  its team at the right edge.
- **Message actions** open with <kbd>Enter</kbd> on an empty composer or a right click.
  See [Chats](/features/chats#message-actions).
- **Account connected.** A new account opens with a card listing the keys to get around
  and search; any key or click closes it.

## Chat lock

The open chat locks, hiding the whole screen, as soon as you switch to another terminal
tab or window (in terminals that report focus; under tmux, set `focus-events on`), and
when you press <kbd>Alt+L</kbd>. Any key or click unlocks it. <kbd>Alt+H</kbd> toggles
private mode, which hides the history on demand.
