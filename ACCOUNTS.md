# Account runtime

`accountManager` owns saved account discovery, identity ownership, supervisor
lifetimes, and the accounts UI. `account` owns one number's state and UI history.
`client` owns one connection attempt's MTProto client, update manager, peer cache,
read state, and visible-chat queue. All remain in `package main`.

## Concurrency

Each saved account has one supervisor goroutine. gotd runs additional goroutines
for transport and update synchronization: this is not literally one total
goroutine per number. Accounts have separate clients and separate session files.
There is one terminal UI, attached to one live account at a time. Switching the
UI does not reconnect or cancel the other accounts. Reading credentials is
serialized through the UI; background accounts never prompt for a password.

Four connection attempts may start concurrently. Once an account is online it
releases that startup slot but retains its connection. Noninteractive startup
attempts time out after 30 seconds. Network failures retry with exponential
backoff and jitter, up to a 60-second base delay. An authentication or other
non-network failure leaves that account offline for user intervention. A failing
account does not terminate the other supervisors. Exit cancels all supervisors
and waits for them to stop before restoring the terminal.

Account state snapshots use an account-local mutex. A small manager mutex guards
identity ownership so duplicate numbers cannot become two active runtimes. UI
state and input histories have a single owner. Account notifications coalesce
through a one-slot channel; background errors retain only the latest notice per
account. Unread refreshes for the visible account are single-flight and have a
five-second timeout. Locking uses cached state and never waits for a network call.

## Capacity

There is no fixed account-count limit and the accounts list scrolls. Resource
cost still grows with live connections, Telegram update volume, gotd's internal
state, and cached users/chats. No per-account unread polling or per-message
goroutine is created. The visible-chat pending queue and peer caches are not
hard bounded; exceptionally busy accounts can still increase memory usage.

This implementation has been exercised with one real saved Telegram account,
not load-tested with hundreds of accounts. Establish capacity using resident
memory, goroutine/file-descriptor counts, reconnect time, message lag, and queue
depth under representative traffic before making a deployment-size claim.
For a large deployment, distribute account supervisors across processes with
exclusive ownership of each session; never share a session file between live
processes. The current CLI has no cross-process session locking.

## Future AI integration

The connection runtime is independent of the terminal. The insertion point is
the account's update handler **before** visible-chat filtering. Currently
background messages are synchronized by gotd but not retained as an AI inbox.
No AI calls or automatic replies are implemented.

Add an account-tagged event envelope carrying Telegram account ID, peer kind/ID,
message ID, and update kind. Use a bounded shared worker pool for model requests,
with fairness and concurrency limits per account and ordered processing per
conversation. Do not call a model synchronously inside the update handler or
start an unbounded goroutine for each message. Route outgoing results through
the same account's live client, with account-specific send limits and cancellation.

Before reliable automation, add a durable inbox/outbox, persisted update offsets,
deduplication keyed by account + peer + message/update identity, retry policy,
and an explicit overflow/replay policy. The current update-manager offsets are
in memory; a file-backed authorization session is not a durable event log.
Treat this supervisor/UI separation as the foundation, not an exactly-once
message processor or a complete distributed account service.
