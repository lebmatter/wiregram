# Summaries & Rewrites

Wiregram can ask an AI model to summarise a chat or to rewrite what you are typing. It
uses the same providers as [AI powered chat bots](/features/chat-bots#ai-providers): any
OpenAI-compatible API, such as OpenRouter, OpenAI, Ollama or llama.cpp's `llama-server`.
Replies stream in as the model writes them.

::: warning Your messages go to the provider
A summary sends the chat's messages, and a rewrite your draft, to the provider that does
the task. A provider on this machine (Ollama, `llama-server`) keeps them on it.
:::

## Choosing providers

**Settings → AI…** starts with a row per task:

- **Summarise with**
- **Rewrite with**

<kbd>Enter</kbd> on one gives the task to the next provider in the list. Until you pick one,
a task uses the first provider. Under them come the providers themselves; see
[AI providers](/features/chat-bots#ai-providers).

## Summarising a chat

- **Summarise chat**, in a message's actions (<kbd>Enter</kbd> on an empty input or a
  right click), summarises the messages that were unread when you opened the chat, or the
  newest 100 loaded ones when none were.
- **Summarise thread**, on a message in a thread, summarises that thread.
- <kbd>s</kbd> on an [Unread](/features/unread-mentions) card summarises its messages.

The summary is a few bullet points: topics, decisions, open questions and anything asked
of you. It streams into a modal over the chat:

| Key | Action |
|-----|--------|
| <kbd>↑</kbd>/<kbd>↓</kbd>, wheel | Scroll |
| <kbd>c</kbd> | Copy the summary |
| <kbd>r</kbd> | Write it again |
| <kbd>Esc</kbd>, click outside | Close, stopping it if it is still being written |

A long chat is summarised in parts first, then the parts together.
[`wiregram summarize`](/features/cli#summarize) does the same from the command line.

## Rewriting a draft

Type a message, then press <kbd>Alt+R</kbd> and pick a style: Shorter, Clearer, Formal,
Friendly, Fix grammar or Translate to English. The rewrite streams into a card over the
input, where a bot's suggestion would be. <kbd>Ctrl+Y</kbd> (or **[Use ^Y]**) puts it in
the input in place of your draft, to edit or send; <kbd>Ctrl+X</kbd> or <kbd>Esc</kbd>
drops it.
