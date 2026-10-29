# Summaries & Rewrites

Wiregram can ask an AI model to summarise a chat or to rewrite what you are typing. It
uses the same providers as [AI powered chat bots](/features/chat-bots#ai-providers): a
[local model](#local-models) run inside Wiregram, or any OpenAI-compatible API, such as
OpenRouter, OpenAI, Ollama or llama.cpp's `llama-server`. Replies stream in as the model
writes them.

::: warning Your messages go to the provider
A summary sends the chat's messages, and a rewrite your draft, to the provider that does
the task. A local model, or a provider on this machine (Ollama, `llama-server`), keeps
them on it.
:::

## Choosing providers

**Settings → AI…** starts with a row per task:

- **Summarise with**
- **Rewrite with**

<kbd>Enter</kbd> on one gives the task to the next provider in the list. Until you pick one,
a task uses the first provider. Under them come the providers themselves; see
[AI providers](/features/chat-bots#ai-providers).

## Local models

Wiregram can run small models itself, through [go-llama](https://github.com/goccy/go-llama)
(llama.cpp in pure Go), with nothing else to install. No model comes with Wiregram:
**Settings → AI…** lists the ones you can download from Hugging Face under **local
models**, each with its download size, context and license:

| Model | Download | License |
|-------|----------|---------|
| Qwen2.5 0.5B Instruct | 491 MB | Apache-2.0 |
| Qwen2.5 1.5B Instruct | 1.1 GB | Apache-2.0 |
| Llama 3.2 1B Instruct | 808 MB | Llama 3.2 |
| Llama 3.2 3B Instruct | 2.0 GB | Llama 3.2 |
| Gemma 3 1B IT | 806 MB | Gemma |
| SmolLM2 1.7B Instruct | 1.1 GB | Apache-2.0 |

<kbd>Enter</kbd> on **get** downloads one into `~/.cache/wiregram/models`. The download
goes on while you use Wiregram; <kbd>Enter</kbd> again stops it, and the next
<kbd>Enter</kbd> resumes where it stopped. Each file is pinned to a Hugging Face commit and
checked against its SHA-256 before it is used. <kbd>Enter</kbd> on a downloaded model asks
before removing it.

The first download adds a **Local** provider, which tasks and bots can use like any other.
Models run on the CPU and load on first use, which takes a moment; one stays loaded
until it has gone unused for five minutes. The smallest models are quick but may get
details wrong, so prefer the 1.5B to 3B ones for summaries.

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
