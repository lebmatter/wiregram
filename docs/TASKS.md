# TASKS.md

Phased implementation plan. Each phase is a shippable commit or small series
that leaves the terminal client working. Do not start a phase before its
predecessors are done — the dependencies are real, not bureaucratic.

Project rules live in `CLAUDE.md`.

| Phase | Goal | Depends on |
| --- | --- | --- |
| 1 | AI usage visibility and budget | — |

Done: Chat Agents — account-bound agents that answer incoming DMs only,
built through the agent-mode wizard (Alt+A), with the event stream and an
account-owned runtime (formerly phase 2).

Moved out: game mode and `wiregram base` now live in the separate wiregames
project.

## Phase 1 — AI usage visibility and budget

Worth doing first, because auto mode spends money with no record today.

- Record one row per request in `wiregram.db`: timestamp, agent, account
  key, chat, model, input/output tokens, duration, outcome.
- Parse the `usage` object from the provider response; record zeros and carry
  on if a server omits it.
- Add a daily output-token cap per agent. When exceeded, stop generating,
  report it once, and keep ordinary messaging working.
- Show today's request and token count on the accounts screen.

**Done when:** a day of suggestions produces a readable log, the cap actually
halts generation, and a provider that omits `usage` does not break anything.

## Verification

The repository has no test suite, so each phase carries a manual checklist in
its "done when" line. Run `gofmt -l cmd internal`, `go vet ./...` and `make` before
every commit.
