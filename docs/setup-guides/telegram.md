# Telegram

Telegram accounts are tagged `TG`. Wiregram talks MTProto directly, as the official
apps do.

## API credentials

Telegram asks every client for an API ID and hash. If your build has them embedded,
Wiregram skips this step. Otherwise:

1. Sign in at [my.telegram.org/apps](https://my.telegram.org/apps) with your phone number.
2. Create an application. Any title and short name will do.
3. Note the **App api_id** (a number) and **App api_hash**.

Wiregram asks for both when you add the first Telegram account and keeps them in
`wiregram.db`. Treat the hash like a password.

## Sign in

1. **Add account** → **Telegram**.
2. Enter your phone number in international format, e.g. `+15551234567`.
3. Enter the login code Telegram sends to your other devices (or by SMS).
4. If you use two-step verification, enter its password.

## Notes

- Telegram chats lock after a spell of inactivity; any key unlocks them.
  See [Configuration](/configuration#inactivity-lock).
- Removing the account in Settings logs the session out on Telegram's side too.
- You can add several Telegram accounts. Each is tagged with its backend and position
  in the account list, like `TG1`.
