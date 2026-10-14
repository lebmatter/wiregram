# Plugins

Wiregram runs Lua 5.1 plugins (`github.com/yuin/gopher-lua`, pure Go). The
design follows matcha's plugin system, with two differences: each plugin
gets its own VM, and a plugin can draw a widget on the account chooser.

## Where plugins come from

- **Built-in**: `internal/plugin/builtin/*.lua`, embedded in the binary.
  These start **off** because they reach the network, except Weather, which
  declares `on = true`. Switch them under Settings → Plugins….
- **User**: `~/.config/wiregram/plugins/NAME.lua` or `NAME/init.lua`. These
  start **on**. A user plugin with the same name as a built-in replaces it.

The file name is the plugin's name. It keys the plugin's settings
(`plugin.NAME.*` in the `settings` table) and its key-value rows
(`plugin_kv`). A plugin that fails to load is left out, and its error is the
account chooser's notice.

## Sandbox

- **One `LState` per plugin.** Plugins never share globals, metatables or a
  lock. A slow plugin stalls only its own card.
- **Libraries.** Only base, table, string and math are opened. There is no
  `os`, `io`, `debug` or `package`. `dofile`, `loadfile`, `load`,
  `loadstring`, `require`, `module`, `print` and `collectgarbage` are removed.
- **Time limits.** Every call into Lua runs under a context deadline, and a
  script that runs too long is stopped:
  - loading: 2 s
  - `render`: 50 ms
  - `update`: 20 s
- **Scoped API.** Every `wiregram.*` function closes over its own plugin. A
  plugin can reach only its own settings, its own kv rows and the hosts it
  declared.
- **Plain text out.** Rendered lines are stripped of control characters, so a
  plugin can't send terminal escapes. The card clips them to its width.
- **No memory cap.** Memory is not limited beyond the call stack and registry
  sizes. Plugins are local files you chose to install, not code from strangers.

## Lua API (implemented)

| Call | Notes |
|------|-------|
| `wiregram.plugin{title=, hosts={…}, on=}` | Load time only. `title` names the plugin in Settings and on its card. `hosts` is the allowlist for `http.get`. `on = true` starts a built-in on. |
| `wiregram.settings{{key=, label=, type="string"\|"choice", default=, options={…}}, …}` | Load time only. Settings lists them in this order. Returns a read-only table of live values, e.g. `cfg.city`. |
| `wiregram.widget{height=4, refresh=60, update=fn, render=fn}` | Load time only. See below. |
| `wiregram.kv.get(k)` / `.set(k, v)` / `.delete(k)` / `.keys()` | String values in `wiregram.db`: keys up to 256 bytes, values up to 64 KB. `set` and `delete` return `true`, or `nil, err`. |
| `wiregram.http.get(url)` | Returns the body, or `nil, err`. HTTPS to declared hosts only (redirects included), 10 s, 1 MB, status 200. Not available at load time. |
| `wiregram.json.decode(s)` | Returns a table, or `nil, err`. |
| `wiregram.now()` | Unix seconds. |

### Widgets

- **Card.** It sits under the unread digest in a `── Widgets` section. It has
  `height` lines inside (1–12) and is titled with the plugin's title.
- **`render(width, frame)`.** Runs four times a second while the chooser is
  open. It returns up to `height` strings for a card `width` cells wide.
  `frame` counts up, for animation. It must be fast, and it should read the kv
  store only when something changed.
- **`update()`.** Runs in the background when the card is first shown, every
  `refresh` seconds after that (at least 10), and right after a setting
  changes. Fetch and store here. Raise `error(msg, 0)` to show `msg` on the
  card's last line.
- **Shared VM.** `update` and `render` run in the same VM, so they can share
  Lua locals. While `update` runs, `render` is skipped and the card keeps its
  last frame.
- **Space.** When the screen is short, widgets give way to the digest.

`builtin/weather.lua` is the reference plugin.

## Later (not built)

Ideas for later. None of these are wired yet:
- Hooks: `wiregram.on("message", fn)` for incoming messages (text-only, the
  agent runtime's DM filter), plus chat opened and account online. These would
  run on a per-plugin goroutine fed from a `backend.Bus` subscription.
- `wiregram.notify(text)` to set the footer notice.
- Key bindings on the chooser or in a chat.
- Styled spans in widget lines, from theme colour names.
- Widgets in the chat sidebar or the Unread view.
- An `http.post`.
- A reload command, so plugins can be edited without restarting.
