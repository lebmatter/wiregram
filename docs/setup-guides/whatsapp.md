# WhatsApp

WhatsApp accounts are tagged `WA`. Wiregram links to your phone as a companion device,
like WhatsApp Web, so your phone must have WhatsApp installed.

::: danger Unofficial client
WhatsApp may ban numbers that use unofficial clients. Use it at your own risk.
:::

## Pair

1. **Add account** → **WhatsApp**. No server or credentials are needed.
2. Enter your phone number in international format, e.g. `+15551234567`.
3. Wiregram shows an eight-character pairing code, `XXXX-XXXX`.
4. On your phone, open WhatsApp → **Linked devices** → **Link a device** →
   **Link with phone number instead**, and type the code.

The code expires after a short while; if pairing times out, add the account again.

## Notes

- WhatsApp's session lives in its own database under
  `~/.config/wiregram/accounts/whatsapp-N/`.
- WhatsApp allows one connection per linked device, so the [CLI](/features/cli) can't
  use WhatsApp accounts: a second connection would disconnect the running app.
- Mentions come from messages Wiregram has seen since it connected.
