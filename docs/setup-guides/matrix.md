# Matrix

Matrix accounts are tagged `MX`.

## Sign in

1. **Add account** → **Matrix**.
2. Enter your homeserver, e.g. `matrix.org` or `https://matrix.example.com`. A bare
   host name gets `https://`.
3. Enter your username, either the full Matrix ID (`@alice:matrix.org`) or just `alice`.
4. Enter your password.

Wiregram signs in with a password; single sign-on is not supported.

## Notes

- Link previews come from your homeserver's preview service; Wiregram never fetches
  linked pages itself.
- Mentions insert the member's Matrix ID and are sent as proper Matrix mentions.
- The Mentions row lists recent highlight notifications from the homeserver.
- The sync token is kept in `wiregram.db`, so restarts pick up where they left off.
