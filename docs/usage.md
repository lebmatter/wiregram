# Usage

Run `wiregram` with no arguments in an interactive terminal. The footer shows the keys
for the current screen; [Keybinds](/keybinds) and `wiregram -h` list all of them.

<img class="wg-shot" src="/img/chat-mode.png" alt="Chat mode with the account sidebar">

## Screens

1. **Splash.** The logo shows for about a second.
2. **Account chooser.** Every account with its status (and its agent, if it has
   one), then Settings…, Add account and Quit. Under them sits the [unread digest](/features/accounts#unread-digest)
   and any [plugin widgets](/features/plugins).
3. **Chat mode.** The chat list and the open chat. Inside an account the screen is
   the body and a footer with the keys for what has focus; notices show on the right
   of the footer.
4. **Settings.** Opened from the chooser's `Settings…` row or the sidebar's last row.
   See [Configuration](/configuration#settings).

## Layout

Terminals 100 columns wide or wider show the chat list as a sidebar beside the open chat;
<kbd>Tab</kbd> moves focus between them. Narrower terminals show one pane at a time.

The sidebar is an account tree. Every account is a row with an arrow, a status glyph,
a tag such as `TG1` (backend and position) and its label. The active account is expanded
to its chats, Unread, Mentions and Search…, and, except for email, its
[agent](/features/chat-agents) row. <kbd>Enter</kbd> on another account switches to it. The last row opens Settings.

Chat text wraps at 120 columns or the terminal width, whichever is smaller.

## Mouse

- The **wheel** moves the selection of the list under the pointer, as <kbd>↑</kbd>/<kbd>↓</kbd>
  would, without moving focus. Over the thread pane it scrolls the thread.
- A **click** on a chat, account, card, menu row or picker entry opens it, as
  <kbd>Enter</kbd> would.
- A **click on a message** selects it; a **right click** opens its actions.
- A **click on an attachment** (the image or the `[tag]` row) downloads it and opens it
  with `xdg-open` (`open` on macOS).
- Hold <kbd>Shift</kbd> while dragging to select text with the terminal.

The login and connecting screens leave the mouse to the terminal.

## Modals

Questions and pickers open as modals over the screen, with everything behind them faded:

- **Confirmations.** Quitting, deleting a chat, message, agent or provider, removing an
  account and Reset ask first. <kbd>y</kbd> or <kbd>Enter</kbd> on the action goes
  ahead, <kbd>Esc</kbd> cancels. Deletes and Reset start on Cancel.
- **Search users, Forward to and Mention** share one picker: type to filter,
  <kbd>Enter</kbd> to pick.
- **Message actions** open with <kbd>Enter</kbd> on an empty composer or a right click.
  See [Chats](/features/chats#message-actions).
- **Account connected.** A new account opens with a card listing the keys to get around
  and search; any key or click closes it.

## Inactivity lock

Telegram accounts lock the open chat after a spell of inactivity, hiding its messages.
Any key unlocks it. <kbd>Alt+H</kbd> toggles private mode, which hides the history on
demand.
