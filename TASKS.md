# TASKS.md

Phased implementation plan. Each phase is a shippable commit or small series
that leaves the terminal client working. Do not start a phase before its
predecessors are done — the dependencies are real, not bureaucratic.

Design intent lives in `AI_PLAN.md`; project rules live in `CLAUDE.md`.

| Phase | Goal | Depends on |
| --- | --- | --- |
| 0 | Agree the rule changes | — |
| 1 | AI usage visibility and budget | — |
| 2 | Event stream and AI runtime extraction | — |
| 3 | Headless `serve` with a lock file | 2 |
| 4 | HTTP server and security baseline | 3 |
| 5 | Read-only web UI | 4 |
| 6 | Sending from the web UI | 5 |
| 7 | AI controls in the web UI | 5 |
| 8 | Config editing in the web UI | 5 |
| 9 | Parity extras, by demand | 6–8 |

Done: AI stage 1 — config, provider client, prompts, knowledge, Alt+A,
suggest mode, auto mode, loop and cost guards (commit `e6c703f`).

## Phase 0 — Rule changes to agree first

No code. `CLAUDE.md` currently forbids things the web UI needs. Decide and
record, rather than contradicting the guide silently:

- **Flags/subcommands.** `wiregram serve` is needed. The guide says no flags.
- **A vendored JavaScript file.** Alpine.js is one ~45 KB file with no build
  step and no transitive packages, vendored into the repo and served from
  `embed.FS`. It is not a Go module, so `go.mod` is untouched and the
  four-module rule still holds. It is still a third-party dependency: pin one
  exact 3.x release, record its `sha256` in the repo, and re-verify on every
  bump. Do not load it from a CDN — that leaks a request to a third party on
  every page view and breaks offline use, both of which defeat the point of a
  local-first tool.
- **No other new dependencies.** The server is `net/http`; live updates are
  Server-Sent Events, which need only `http.Flusher`. Deliberately avoiding
  WebSockets keeps a websocket module out of the direct dependency list.
- **Scope.** The web UI is a *local single-operator control panel* bound to
  loopback, in the spirit of a Transmission or Syncthing UI. It is explicitly
  not the multi-tenant hosted service ruled out in `AI_PLAN.md`.

**Done when:** `CLAUDE.md` states the flag rule, the vendored-asset rule, and
the loopback-only scope.

## Phase 1 — AI usage visibility and budget

Independent of the web work and worth doing first, because auto mode spends
money with no record today.

- Append one JSON object per request to `ai/usage-YYYY-MM.jsonl`: timestamp,
  account ID, peer key, model, input/output tokens, duration, outcome.
- Parse the `usage` object from the provider response; record zeros and carry
  on if a server omits it.
- Add `daily_output_tokens` per account. When exceeded, stop generating,
  report it once, and keep ordinary messaging working.
- Show today's request and token count on the accounts screen.

**Done when:** a day of suggestions produces a readable log, the cap actually
halts generation, and a provider that omits `usage` does not break anything.

## Phase 2 — Event stream and AI runtime extraction

The refactor everything else depends on. Today `client.accept` discards
updates for any chat that is not on screen, and the AI trigger lives in
`view()` — so nothing can observe a conversation unless the terminal has it
open. A second UI and a headless mode both need that fixed.

- Give `client` a subscriber list. Publish every account event (new message,
  edit, reaction, read state) to all subscribers; keep the existing
  visible-chat path as one subscriber so terminal behaviour is unchanged.
  Bound each subscriber's queue and drop-with-notice rather than blocking the
  update handler.
- Move the AI trigger, debounce, generation and delivery out of `chatview.go`
  into an account-owned `aiRuntime`. Pending suggestions become account state
  keyed by peer, not `view()` locals.
- Move sending out of the UI-owned loop into an account-owned sender, so the
  terminal, the runtime and later the web UI all queue through one place with
  one retry policy.
- Terminal `view()` becomes a subscriber that renders and sends commands.

**Done when:** terminal behaviour is unchanged to the eye; a suggestion
generated while a chat is closed is waiting when you open it; auto mode
replies in a chat you are not looking at; and `go vet` is clean.

## Phase 3 — Headless `serve`

- `wiregram serve` loads config and identities, starts the supervisor and the
  AI runtime, and blocks on signals. No terminal, no readline.
- A lock file in the config directory makes interactive and `serve` mutually
  exclusive. Sessions must never be opened by two processes. Report the
  holding PID and exit 1 on conflict; clear a stale lock safely.
- Errors go to stderr as single plain lines, same as the client.

**Done when:** `serve` runs with stdin closed, answers in auto mode, refuses
to start while the interactive client is running and vice versa, and exits
cleanly on SIGINT/SIGTERM.

## Phase 4 — HTTP server and security baseline

No Telegram data yet. Build the shell and prove it is safe first, because
this endpoint can read and send a person's private messages.

Non-negotiable, all in this phase:

- **Bind to `127.0.0.1` only.** Never `0.0.0.0`. If a non-loopback bind is
  ever requested, refuse and explain.
- **Token auth.** Generate a random token per `serve` start, print the full
  `http://127.0.0.1:PORT/?token=...` URL to stderr once. First request
  exchanges it for a `HttpOnly; SameSite=Strict` session cookie. Never log the
  token anywhere persistent.
- **Host header allowlist** (`127.0.0.1:PORT`, `localhost:PORT`) to defeat DNS
  rebinding, which is the standard way a web page attacks a localhost service.
- **CSRF.** Every mutating request needs an `X-Wiregram-CSRF` header matching
  a per-session value handed to the page. Reject cookie-only mutations.
  Check `Origin` on mutations too.
- **No CORS headers.** No cross-origin access is wanted.
- Static assets from `embed.FS`; vendored Alpine with its checksum recorded.
- Constant-time token comparison; no secrets in URLs after the handshake.

**Done when:** the page loads with the token and 401s without it; a request
with a foreign `Host` or `Origin` is rejected; a mutation without the CSRF
header is rejected; a non-loopback bind is refused; and the binary still
builds with `CGO_ENABLED=0`.

## Phase 5 — Read-only web UI

- `GET /api/chats`, `GET /api/chats/{peer}/messages?limit=`.
- `GET /api/events` as SSE, carrying the Phase 2 event stream.
- One `index.html` with Alpine: chat list, message pane, live append on
  events, reconnect with backoff when the stream drops.
- Server-side rendering of message text reuses `messageText`/`formatMedia`, so
  the web and terminal agree on what a message says.
- Escape all message content. Treat every value from Telegram as hostile to
  the DOM; use `x-text`, never `x-html`.

**Done when:** the browser shows the same chats and messages as the terminal,
new messages appear without a refresh, the stream survives a server restart,
and a message containing HTML renders as literal text.

## Phase 6 — Sending from the web UI

- `POST /api/chats/{peer}/messages`, queued through the Phase 2 sender.
- Optimistic render, reconciled with the delivered message.
- Reuse the stable random-ID retry path so a retry cannot duplicate a message.

**Done when:** a message sent in the browser appears in the terminal, in
Telegram, and survives a mid-send reconnect without duplicating.

## Phase 7 — AI controls in the web UI

- Per-chat toggle mirroring Alt+A, sharing the same config writer.
- Suggestion card with Send, Discard, and Edit-then-send.
- Suggestions are account state, so approving in one UI must clear it in the
  other; push the resolution over SSE.

**Done when:** a suggestion appears in both UIs, resolving it in one clears it
in the other within a second, and the terminal `y`/`n` path still works.

## Phase 8 — Config editing in the web UI

- Edit the global prompt, per-chat prompt, `autoreply`, and the knowledge file
  list. Show which knowledge files are missing or oversized.
- Writes go through the atomic, unknown-field-preserving path already used by
  the Alt+A toggle. Never expose `api_hash` or the API key to the browser.
- Validate with `normalize()` before saving and report errors inline.

**Done when:** an edit survives a restart, a hand-added unknown setting is
still present afterwards, an invalid model is rejected before saving, and no
secret ever appears in an API response.

## Phase 9 — Parity extras, by demand

Pick from evidence, not enthusiasm: media thumbnails and download, search,
account switching, reactions and replies, unread badges. Each is independent.

## Non-goals

Multi-tenant hosting, authentication for remote users, TLS termination,
exposing the server beyond loopback, a build step, npm, a JavaScript
framework larger than Alpine, and a REST API intended for third-party
clients. If remote access is ever wanted, the answer is an SSH tunnel or a
reverse proxy the operator configures, not a listener on `0.0.0.0`.

## Verification

The repository has no test suite, so each phase carries a manual checklist in
its "done when" line. Run `gofmt -l *.go`, `go vet ./...` and `make` before
every commit. For phases 4 and 6 in particular, exercise the failure paths —
wrong token, foreign Host, missing CSRF header, dropped SSE stream, reconnect
during a send — rather than only the happy path.
