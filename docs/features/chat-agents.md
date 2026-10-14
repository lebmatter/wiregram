# Chat Agents

Chat Agents are optional AI bots that answer **incoming private messages** for an
account. Each account can have one agent. They work with any OpenAI-compatible API.

<img class="wg-shot" src="/img/agent-pane.png" alt="An account's agent settings">

::: warning Your messages go to the provider
Every conversation an agent answers is sent to the AI provider you configure.
:::

## What agents do and don't do

- They answer **DMs only**. They never act in groups or channels, and never start a
  conversation.
- They never answer email.
- Each account has at most one agent, and an agent belongs to one account.
- An agent either **asks you first** with a suggested reply, or **replies automatically**.
- On Mattermost, where threads live on the server, an agent answers where the message
  was written: inside its thread, with that thread as the conversation, or in the main
  chat. On Telegram, Matrix and WhatsApp its reply quotes the message it answers.

Violet marks everything agent-related: the ✦ icon, the suggestion card and the messages
an agent sent.

## AI providers

Providers are kept in **Settings → AI providers…**. The right pane lists each provider
with its default model; <kbd>Enter</kbd> on one opens it (`▸`/`▾`) to show its rows:
Name, URL, Key from (the environment variable) and Model. <kbd>Enter</kbd> edits a row in
place, <kbd>Enter</kbd> again saves and <kbd>Esc</kbd> cancels. **Delete provider…** asks
first, and is refused while an agent uses that provider.

**+ Add provider…** asks for a preset, a name, the URL (custom only), the variable that
holds the API key and a default model, then Save, Start over or Cancel. Presets:

| Preset | Base URL | API key variable | Default model |
|--------|----------|------------------|---------------|
| OpenRouter | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openai/gpt-4o-mini` |
| OpenAI | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `gpt-4o-mini` |
| Ollama (this machine) | `http://localhost:11434/v1` | none | `llama3.2` |

Or choose **Other OpenAI-compatible API** and give its base URL, which must use HTTPS
unless it runs on this machine.

Wiregram never stores API keys. You name the **environment variable** that holds the key
(letters, digits and `_`), and Wiregram reads it at run time, so set it in the shell you
start Wiregram from:

```sh
export OPENROUTER_API_KEY=...   # from your secret manager, not shell history
wiregram
```

## Creating an agent

Each expanded account in the chat sidebar (except email) ends with an agent row, under
Unread, Mentions, Archived chats and Search…. It reads **New agent…** until the account
has one. Without a provider, an alert over the screen asks you to add one in
Settings → AI providers first (any key or click closes it); otherwise a
short scripted wizard opens. Every step is a fixed choice or a typed value;
<kbd>Esc</kbd> goes back a question.

1. **Name**, e.g. `Support Bot`.
2. **Provider** and **model** (the provider's default, or another).
3. **Prompt**: how it should reply. Leave empty for a brief, helpful default.
4. **Knowledge files** (optional): comma-separated paths it reads, e.g. `~/notes/faq.md`.
   Relative paths are inside the Wiregram config folder.
5. **What it understands**:
   - text only (media messages never reach it)
   - text + media descriptions (`[photo]`, `[file]`…)
   - text + vision (incoming photos go to the model)
6. **How it replies**: ask me first, or automatically.
7. **History**: keep conversation history for 7, 30 or 90 days.
8. **Create agent.**

A new agent answers all of the account's private chats.

## The agent pane

Once it exists the row reads **Agent: NAME ✦**. <kbd>Enter</kbd> on it opens the agent's
settings in place of the chat. <kbd>↑</kbd>/<kbd>↓</kbd> choose a row and
<kbd>Enter</kbd> changes it:

| Row | Value |
|-----|-------|
| Status | on or paused |
| Name, Model, Prompt, Knowledge | edited in place |
| Provider | cycles through your providers |
| Understands | text only, media descriptions or vision |
| Replies | ask me first, or automatically |
| History | 7, 30 or 90 days |
| Chats | all private chats, or selected (N) |
| Delete agent… | asks first, starting on Cancel |

With Chats set to selected, the account's private chats are listed below as `[x] Title`
rows; <kbd>Enter</kbd> toggles one. If the last reply failed, a line on top says why.
<kbd>Esc</kbd> goes back, and <kbd>Tab</kbd> moves focus as usual.

In the sidebar a violet ✦ follows the account's name (dim while the agent is paused),
and the account chooser shows it beside the agent's name. While the agent is on, each
private chat it answers gets a ✦ before its title.

## Approving replies

With **Ask me first**, a chat whose agent has a reply waiting gets a ✦ before its title in
the sidebar. In the open chat a violet **✦ Agent suggests** card sits above the composer
with the reply:

| Key | Action |
|-----|--------|
| <kbd>Ctrl+Y</kbd> | Approve: send it as the agent |
| <kbd>Ctrl+E</kbd> | Edit: move the text into the composer; you then send it as yourself |
| <kbd>Ctrl+X</kbd> | Discard |

The keys work while the composer is empty, and the card's buttons can be clicked.
Sending your own reply also drops a waiting suggestion. Suggestions are kept in memory
only, so quitting loses them.

## Which messages came from the agent

Every message an agent sends is recorded, and the chat shows `✦ NAME` in violet in its
nick column instead of `you`, including after a restart.

## Testing with dry-run

[`wiregram dry-run`](/features/cli#dry-run) prints the reply the account's agent would
give to a message, without sending or recording anything. It works while the agent is
paused, so you can try the prompt before turning it on.

```sh
wiregram dry-run mattermost alice bob
```

## Storage

Providers, agents, their chats, conversation history and the record of sent messages are
kept in `wiregram.db`. The sent-message record outlives the history setting. Removing an
account deletes its agent and that record.

::: info Upgrading
Agents from older versions could serve several accounts, so they are removed once on the
first start of this version. Providers are kept; create each account's agent again from
its **New agent…** row.
:::
