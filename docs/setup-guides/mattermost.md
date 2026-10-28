# Mattermost

Mattermost accounts are tagged `MM`.

## Sign in

1. **Add account** → **Mattermost**.
2. Enter the server URL, e.g. `chat.example.com`. A bare host name gets `https://`.
3. Enter your email or username.
4. Enter your password.
5. If multi-factor authentication is on, enter the code from your authenticator app.

If sign-in fails, run `wiregram --debug` and try again: the server's answers are
written to `~/.config/wiregram/debug.log`.

## Notes

- **Search users** (<kbd>/</kbd>) also finds channels; picking a public channel you
  haven't joined joins it.
- Threads live on the server: opening a thread fetches the whole conversation once.
  A mention opens in its thread.
- The <kbd>@</kbd> picker lists channel members first, then people not in the channel.
- Link previews are the server's OpenGraph embeds.
