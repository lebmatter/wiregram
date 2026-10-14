# Accounts

Wiregram keeps every account connected at once: Telegram, WhatsApp, Matrix, Mattermost
and email, as many of each as you like. Switching between them never reconnects.

## Account chooser

After the splash comes the account chooser, with the logo. It lists every account with
a status glyph and its tag, and its [agent](/features/chat-agents) with a violet ✦ if
it has one, then:

- **Settings…**: open [Settings](/configuration#settings)
- **Add account**: pick a network and sign in
- **Quit**

<kbd>Enter</kbd> opens an account, <kbd>r</kbd> reconnects the selected one. An account
that needs signing in again shows it here; open it to sign in.

### Unread digest

Inside the chooser's box, under Quit, sits a grid of cards, one per online account.
Each shows the account's unread total and its busiest chats, fetched once when the
account comes online. Clicking a card opens the account.

To leave an account out, set its **Unread digest** setting to `hidden` in Settings.

Under the digest, enabled [plugins](/features/plugins) can draw widget cards. When the
screen is short, widgets give way to the digest.

## Account tree

In chat mode, the sidebar lists every account as a row: `▸`/`▾`, a status glyph, the
tag (`TG`, `WA`, `MX`, `MM`, `EM`) and the account's label. The active account is
expanded to:

- its chats, newest first, with unread badges
- **Unread** and **Mentions** (see [Unread & Mentions](/features/unread-mentions))
- **Search…**, which opens the user search

<kbd>Enter</kbd> on another account switches to it. The last row opens Settings.

## Adding an account

**Add account** opens a form over the screen: the network, then the server where one
is needed, then the network's sign-in questions. See the setup guide for each:
[Telegram](/setup-guides/telegram), [WhatsApp](/setup-guides/whatsapp),
[Matrix](/setup-guides/matrix), [Mattermost](/setup-guides/mattermost),
[Email](/setup-guides/email).

A newly added account opens with an **Account connected** card listing the keys to get
around and search. Any key or click closes it.

## Removing an account

In Settings, select the account and choose **Remove account…**. When the account is
online, Wiregram logs it out on the server first. Then it deletes the account's local
data, its Chat Agent and the record of messages that agent sent. Removing starts on Cancel.

**Reset…** does this for every account, deletes all local data and starts over as a
fresh installation.
