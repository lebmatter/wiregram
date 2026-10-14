# Chat Agents

Chat Agents are optional AI bots that answer **incoming private messages** on the
accounts you connect to them. They work with any OpenAI-compatible API.

<img class="wg-shot" src="/img/agents-mode.png" alt="Agents mode">

::: warning Your messages go to the provider
Every conversation an agent answers is sent to the AI provider you configure.
:::

## What agents do and don't do

- They answer **DMs only**. They never act in groups or channels, and never start a
  conversation.
- They never answer email.
- Each account answers to at most one agent.
- An agent either **suggests** replies for you to approve, or **replies automatically**.

## Agents mode

Press <kbd>Alt+A</kbd> from the chat list, or from a chat with an empty input. The screen
turns violet and accounts are tagged like `[TG1]` (backend and position in the account
list).

| Key | Action |
|-----|--------|
| <kbd>n</kbd> | New agent |
| <kbd>e</kbd> | Edit agent |
| <kbd>c</kbd> | Connect accounts |
| <kbd>Space</kbd> | Pause or resume |
| <kbd>d</kbd> | Delete agent |
| <kbd>p</kbd> | AI providers |
| <kbd>Enter</kbd> | Show conversations / open one |
| <kbd>y</kbd> / <kbd>n</kbd> | Send / discard a suggested reply |

## Providers

The first new agent asks for a provider. Presets:

| Preset | Base URL | API key variable | Default model |
|--------|----------|------------------|---------------|
| OpenRouter | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openai/gpt-4o-mini` |
| OpenAI | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `gpt-4o-mini` |
| Ollama (this machine) | `http://localhost:11434/v1` | none | `llama3.2` |

Or choose **Other OpenAI-compatible API** and give its base URL, which must use HTTPS
unless it runs on this machine.

Wiregram never stores API keys. You name the **environment variable** that holds the key,
and Wiregram reads it at run time, so set it in the shell you start Wiregram from:

```sh
export OPENROUTER_API_KEY=...   # from your secret manager, not shell history
wiregram
```

## Creating an agent

<kbd>n</kbd> starts a short scripted wizard. Every step is a fixed choice or a typed
value; <kbd>Esc</kbd> goes back a question.

1. **Name**, e.g. `Support Bot`.
2. **Provider** and **model** (the provider's default, or another).
3. **Instructions**: how it should reply. Leave empty for a brief, helpful default.
4. **Knowledge files** (optional): comma-separated paths it reads, e.g. `~/notes/faq.md`.
   Relative paths are inside the Wiregram config folder.
5. **What it understands**:
   - text only (media messages never reach it)
   - text + media descriptions (`[photo]`, `[file]`…)
   - text + vision (incoming photos go to the model)
6. **How it replies**: suggest replies for you to approve, or reply automatically.
7. **History**: keep conversation history for 7, 30 or 90 days.
8. **Accounts**: <kbd>Space</kbd> toggles each account it answers on.
9. **Confirm** the summary and save.

Agents, providers and conversation history are kept in `wiregram.db`.
