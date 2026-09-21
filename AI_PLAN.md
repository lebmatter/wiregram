# Optional AI for Wiregram

Status: design agreed; implementation started 21 September 2026.
Research checked: 21 September 2026.

## Scope

Wiregram stays a Telegram CLI you run on your own machine for your own
accounts. AI is optional, off by default, and does one thing: answer — or
offer to answer — incoming private messages using a prompt and documents you
supply.

Two intended uses:

- **Personal**: an assistant on your own account, for chatting with yourself
  in Saved Messages or drafting replies in your own voice.
- **Small helpdesk**: one company account answering routine questions from a
  supplied knowledge file, with a human able to take over at any point.

Explicitly not in scope: hosting other people's accounts, a web service,
multi-tenant billing, bot identities, connected business bots, headless
daemons, and automated moderation. Each of those is a different product with
a different threat model. Earlier drafts of this document proposed a managed-bot
backend and a hosted service; both are dropped.

## Behaviour

- `autoreply: 0` (default): the assistant drafts a reply and shows it below the
  conversation. `y` sends it, `n` discards it, and any other key discards it and
  goes back to typing normally.
- `autoreply: 1`: the assistant sends its reply itself.
- **Alt+A** turns AI on or off for the current chat and persists that choice to
  `config.json`.
- A global `prompt` sets how the assistant behaves.
- An optional per-chat `prompt` is appended for that one conversation.
- `knowledge` files are supplied to the model as reference material.

Private chats only. Groups and channels are never answered: a wrong reply has a
larger blast radius, and mention/reply triggering is a separate design problem.

Guards against runaway cost and loops: never reply to your own messages, never
reply to another bot, at most one request in flight per chat, and a short
debounce so a burst of messages produces one reply rather than one per message.

## Boundaries and honesty

Telegram's API terms restrict AI use of platform data, and the content
licensing terms describe consent expectations for the people involved.
Enabling AI as the account owner is not the other participant's consent.
[API terms](https://core.telegram.org/api/terms),
[content licensing](https://telegram.org/tos/content-licensing)

The position this tool takes: AI is off by default, opt-in per chat, and
suggestion mode is the default so a person approves every outgoing message.
An operator running a helpdesk should tell people they are talking to an
assistant. Do not import chat history into shared indexes, do not train on
conversations, and do not enable this in chats where the other person would
object. The tool's job is to make that choice visible rather than silent.

Message text from other people is untrusted input. The system prompt states
that instructions inside messages must not be obeyed, but prompt injection is
not fully solvable — suggestion mode is the real mitigation.

Requests go to whatever `base_url` is configured, so the conversation excerpt
and knowledge files leave the machine for that provider. HTTPS is required
except for loopback addresses, which stay allowed for local model servers.

## Configuration

All settings live in the existing `config.json`. A missing `ai` section means
off. An invalid one disables AI with a clear error while ordinary messaging
continues.

```json
{
  "api_id": 123456,
  "api_hash": "...",
  "autolock": 1,
  "ai": {
    "enabled": true,
    "base_url": "https://api.openai.com/v1",
    "api_key_env": "OPENAI_API_KEY",
    "model": "<model-id>",
    "prompt": "You are my assistant. Reply briefly and in my voice.",
    "autoreply": 0,
    "max_output_tokens": 500,
    "context_messages": 20,
    "timeout_seconds": 45,
    "knowledge": ["knowledge/faq.md"],
    "chats": {
      "123456789/user:987654321": {
        "enabled": true,
        "prompt": "This person is a customer asking about orders. Be formal."
      }
    }
  }
}
```

Chat keys are `<account id>/<peer key>`, both immutable Telegram IDs, so the
same person in two of your accounts is configured separately. `knowledge`
paths resolve relative to the config directory. The API key is read from the
named environment variable and never stored in the config file.

The request uses the OpenAI **Chat Completions** shape (`POST
{base_url}/chat/completions`), not the newer Responses API, because
`base_url` is meant to be pointed at self-hosted and third-party
OpenAI-compatible servers and almost all of them implement Chat Completions.
The token limit is sent as `max_completion_tokens`, which current OpenAI
models require; `max_tokens` is deprecated and rejected by newer models. Set
`max_output_tokens` to `0` to omit the field for a server that does not accept
it.

## Storage

No database. Configuration is the only persisted state, written atomically
with unknown fields preserved so hand-edited settings survive a toggle.
Conversation context comes from the messages already loaded in the chat view;
nothing is cached to disk. Knowledge files are read at request time and capped
in total size, so a large document set is a configuration error rather than a
silent cost.

If knowledge outgrows a single prompt, add chunking and keyword ranking in Go
before considering SQLite FTS5 or embeddings. Do not add a database until a
feature actually needs durability across restarts.

## Delivery stages

1. **Core** (this stage): config parsing and validation, HTTP provider client,
   prompt assembly with knowledge and per-chat prompt, Alt+A toggle with
   persistence, suggestion mode with `y`/`n`, automatic mode, and the loop and
   cost guards above.
2. Usage visibility: per-chat request and token counts, an append-only JSONL
   log under `ai/`, and a daily token cap.
3. Knowledge that scales: chunking and ranking when whole-file prompts stop
   being adequate.
4. By demand only: groups with explicit mention triggers, voice-note
   transcription, image input, and additional providers.

Manual checks before calling a stage done, following the repository's
no-test-suite rule: AI disabled makes no network calls and changes nothing;
an invalid AI section still allows normal chatting; toggling off mid-generation
discards the result; a suggestion is discarded when you switch chats; provider
errors surface as one plain line and never crash the client; and
`CGO_ENABLED=0` still builds.

## Implementation notes

Keep `package main`. New file `ai.go` holds the config types, validation, the
provider client, and prompt assembly. `config.go` gains the `ai` field and an
atomic writer for chat toggles. `chatview.go` gains the trigger, the pending
suggestion state, the key handling, and the status line. `client.go` carries
the account ID so chat keys can be account-scoped.

AGENTS.md currently forbids dependencies beyond the three modules and lists
several of these behaviours under "what NOT to build". This design adds no new
Go module — the provider client is `net/http` and `encoding/json` — but the
feature list in AGENTS.md needs updating rather than quietly contradicting.
