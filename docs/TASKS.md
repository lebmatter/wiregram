# TASKS.md

Phased implementation plan. Each phase is a shippable commit or small series
that leaves the terminal client working. Do not start a phase before its
predecessors are done — the dependencies are real, not bureaucratic.

Design intent lives in `AI_PLAN.md`; project rules live in `CLAUDE.md`.

| Phase | Goal | Depends on |
| --- | --- | --- |
| 1 | AI usage visibility and budget | — |
| 2 | Event stream and AI runtime extraction | — |

Done: AI stage 1 — config, provider client, prompts, knowledge, Alt+A,
suggest mode, auto mode, loop and cost guards (commit `e6c703f`).

## Phase 1 — AI usage visibility and budget

Worth doing first, because auto mode spends money with no record today.

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
open.

- Give `client` a subscriber list. Publish every account event (new message,
  edit, reaction, read state) to all subscribers; keep the existing
  visible-chat path as one subscriber so terminal behaviour is unchanged.
  Bound each subscriber's queue and drop-with-notice rather than blocking the
  update handler.
- Move the AI trigger, debounce, generation and delivery out of `chatview.go`
  into an account-owned `aiRuntime`. Pending suggestions become account state
  keyed by peer, not `view()` locals.
- Move sending out of the UI-owned loop into an account-owned sender, so the
  terminal and the runtime queue through one place with one retry policy.
- Terminal `view()` becomes a subscriber that renders and sends commands.

**Done when:** terminal behaviour is unchanged to the eye; a suggestion
generated while a chat is closed is waiting when you open it; auto mode
replies in a chat you are not looking at; and `go vet` is clean.

## Verification

The repository has no test suite, so each phase carries a manual checklist in
its "done when" line. Run `gofmt -l cmd internal`, `go vet ./...` and `make` before
every commit.
