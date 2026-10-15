# Accounts

Wiregram keeps every account connected at once: Telegram, WhatsApp, Matrix, Mattermost
and email, as many of each as you like. Switching between them never reconnects.

## Account chooser

After the splash comes the account chooser, with the logo. It lists every account with
a status glyph and its tag, and its [bots](/features/chat-bots) with a violet ✦ if
it has any, then:

- **Settings…**: open [Settings](/configuration#settings)
- **Add account**: pick a network and sign in
- **Quit**

<kbd>Enter</kbd> opens an account, <kbd>r</kbd> reconnects the selected one. An account
that needs signing in again shows it here; open it to sign in.

### Widgets

Inside the chooser's box, under Quit, enabled [plugins](/features/plugins) draw widget
cards. The built-in **Unread** widget shows, per online account, how many private chats
have unread messages; clicking it opens their newest messages. Clicking a widget may open
a modal with more detail, such as the Weather widget's hourly forecast.

## Account tree

In chat mode, the sidebar lists every account as a row: `▸`/`▾`, a status glyph, the
tag (`TG`, `WA`, `MX`, `MM`, `EM`) and the account's label. The active account is
expanded to:

- its chats, newest first, with unread badges
- **Unread** and **Mentions** (see [Unread & Mentions](/features/unread-mentions))
- **Search…**, which opens the user search

An email account is expanded to its folders instead, then **Compose…** and **Labels…**
(see [Email](/features/email#folders)).

While a WhatsApp or Matrix account is still fetching its chats for the first time
after connecting, it shows a spinner and `Loading chats…` in place of them and these
rows. An email folder shows `Loading emails…` until the first sync fills it.

<kbd>Enter</kbd> on another account switches to it. The last two rows open Home (the account chooser) and Settings.

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
data, its bots and the record of messages they sent. Removing starts on Cancel.

**Reset…** does this for every account, deletes all local data and starts over as a
fresh installation.
