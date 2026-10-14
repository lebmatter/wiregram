# Email

Email accounts are tagged `EM`. Wiregram reads mail over IMAP (TLS, port 993) and
sends it over SMTP (implicit TLS, port 465). See [Email](/features/email) for how mail
looks in the app.

## Sign in

1. **Add account** → **Email**.
2. Enter the IMAP server. It defaults to `imap.gmail.com`. Give just the host name.
3. Enter your email address.
4. Enter an app password, or choose Google sign-in (see below).

The SMTP server is derived from the IMAP one: `imap.example.com` sends through
`smtp.example.com`, and Gmail through `smtp.gmail.com`.

## Gmail and Google Workspace

### App password

Works with every build.

1. Turn on [2-Step Verification](https://myaccount.google.com/signinoptions/two-step-verification)
   for your Google account.
2. Create an app password at
   [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords).
3. Paste it when Wiregram asks. Spaces are ignored.

Google Workspace admins can disable app passwords for their domain.

### Google sign-in

Builds that embed a Google OAuth desktop client ask whether to use an app password or
**Google in the browser**. Choose Google, finish signing in in the browser, then press
<kbd>Enter</kbd>. Wiregram keeps only the refresh token. See
[Building](/for-developers/building#embedded-credentials) for embedding a client.

## Other providers

Any provider with IMAP and SMTP over TLS on the standard ports and password sign-in
works. Many (iCloud, Fastmail, Yahoo) require an app-specific password rather than your
normal one; create it in the provider's security settings.
