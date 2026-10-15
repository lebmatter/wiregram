# Plugins

Wiregram runs Lua 5.1 plugins ([gopher-lua](https://github.com/yuin/gopher-lua), pure Go).
Each plugin runs in its own sandboxed VM and can draw a widget card on the account
chooser, under the unread digest.

## Using plugins

Open **Settings → Plugins…** to switch plugins on and off and edit their settings.
Enabled plugins with a widget draw a card in the chooser's `── Widgets` section. When the
screen is short, widgets give way to the digest. A widget may answer a click, often by
opening a modal over the chooser; Esc, Enter or a click outside closes it.

### Built-in plugins

| Plugin | What it shows | Reaches | Starts |
|--------|---------------|---------|--------|
| **Weather** | Current conditions for one city, with a small animated sky. Click the card for today's forecast hour by hour. Settings: city, units, refresh | Open-Meteo | on |
| **Hacker News** | The top front-page stories | The Algolia HN API | off |

Built-ins are embedded in the binary. They start **off** because they reach the network,
except Weather, which declares `on = true`.

### Your own plugins

Put them in `~/.config/wiregram/plugins/` as `NAME.lua` or `NAME/init.lua`. They start
**on**. A user plugin with the same name as a built-in replaces it.

The file name is the plugin's name. It keys the plugin's settings (`plugin.NAME.*` in
`wiregram.db`) and its key-value rows. A plugin that fails to load is left out, and its
error shows as the account chooser's notice. Restart Wiregram to load changes.

## A first plugin

`~/.config/wiregram/plugins/clock.lua`:

```lua
wiregram.plugin { title = "UTC clock" }

local function render(width, frame)
  local t = wiregram.now() % 86400
  local h, m, s = math.floor(t / 3600), math.floor(t / 60) % 60, t % 60
  return { "", string.format("  %02d:%02d:%02d UTC", h, m, s) }
end

wiregram.widget { height = 2, render = render }
```

## Sandbox

- **One VM per plugin.** Plugins never share globals, metatables or a lock. A slow plugin
  stalls only its own card.
- **Libraries.** Only base, table, string and math are opened. There is no `os`, `io`,
  `debug` or `package`. `dofile`, `loadfile`, `load`, `loadstring`, `require`, `module`,
  `print` and `collectgarbage` are removed.
- **Time limits.** Every call into Lua runs under a deadline, and a script that runs too
  long is stopped:
  - loading: 2 s
  - `render`: 50 ms
  - `update` and `click`: 20 s
- **Scoped API.** Every `wiregram.*` function is bound to its own plugin. A plugin can
  reach only its own settings, its own key-value rows and the hosts it declared.
- **Plain text out.** Rendered lines are stripped of control characters, so a plugin
  can't send terminal escapes. The card clips them to its width.
- **No memory cap.** Memory is not limited beyond the call stack and registry sizes.
  Plugins are local files you chose to install; read a plugin before installing it.

## Lua API

::: v-pre
| Call | Notes |
|------|-------|
| `wiregram.plugin{title=, hosts={…}, on=}` | Load time only. `title` names the plugin in Settings and on its card. `hosts` is the allowlist for `http.get`. `on = true` starts a built-in on. |
| `wiregram.settings{{key=, label=, type="string"\|"choice", default=, options={…}}, …}` | Load time only. Settings lists them in this order. Returns a read-only table of live values, e.g. `cfg.city`. |
| `wiregram.widget{height=4, refresh=60, update=fn, render=fn, click=fn}` | Load time only. See [Widgets](#widgets). |
| `wiregram.modal{title=, width=60, height=12, render=fn}` | From `click` only. See [Modals](#modals). |
| `wiregram.kv.get(k)` / `.set(k, v)` / `.delete(k)` / `.keys()` | String values in `wiregram.db`: keys up to 256 bytes, values up to 64 KB. `set` and `delete` return `true`, or `nil, err`. |
| `wiregram.http.get(url)` | Returns the body, or `nil, err`. HTTPS to declared hosts only (redirects included), 10 s, 1 MB, status 200. Not available at load time. |
| `wiregram.json.decode(s)` | Returns a table, or `nil, err`. |
| `wiregram.now()` | Unix seconds. |
:::

### Settings

```lua
local cfg = wiregram.settings {
  { key = "city", label = "City", type = "string", default = "Bengaluru" },
  { key = "units", label = "Units", type = "choice",
    options = { "celsius", "fahrenheit" }, default = "celsius" },
}
-- cfg.city and cfg.units always hold the current values.
```

### Widgets

- **Card.** It sits under the unread digest in the `── Widgets` section. It has `height`
  lines inside (1–12) and is titled with the plugin's title.
- **`render(width, frame)`** runs four times a second while the chooser is open. It
  returns up to `height` strings for a card `width` cells wide. `frame` counts up, for
  animation. It must be fast, and it should read the kv store only when something changed.
- **`update()`** (optional) runs in the background when the card is first shown, every
  `refresh` seconds after that (at least 10), and right after a setting changes. Fetch
  and store here. Raise `error(msg, 0)` to show `msg` on the card's last line.
- **Shared VM.** `update` and `render` run in the same VM, so they can share Lua locals.
  While `update` runs, `render` is skipped and the card keeps its last frame.

- **`click()`** (optional) runs when the card is clicked. It waits for a running `update`,
  so it may fetch too. Raise `error(msg, 0)` to show `msg` on the card.

### Modals

`wiregram.modal{…}`, called from a widget's `click`, opens a modal centred over the
account chooser when `click` returns. Everything behind it fades.

- **Size.** `width` (10–200) and `height` (1–100) are the cells inside its border. A
  modal larger than the screen is cut to fit.
- **Title.** `title` labels the border. It defaults to the plugin's title.
- **`render(width, height, frame)`** works like a widget's `render`. It gets the size the
  modal really has and returns up to `height` lines. It runs four times a second while
  the modal is open, under the same 50 ms limit.

```lua
local function click()
  wiregram.modal { title = "Details", width = 50, height = 6, render = function(w, h, frame)
    return { "", "  Shown in a " .. w .. "×" .. h .. " modal." }
  end }
end

wiregram.widget { height = 2, render = render, click = click }
```

### Fetching data

```lua
wiregram.plugin { title = "Hacker News", hosts = { "hn.algolia.com" } }

local function update()
  local body, err = wiregram.http.get(
    "https://hn.algolia.com/api/v1/search?tags=front_page&hitsPerPage=5")
  if not body then error(err, 0) end
  local data, jerr = wiregram.json.decode(body)
  if not data then error(jerr, 0) end
  wiregram.kv.set("top", data.hits[1].title)
end
```

The built-in [Weather](https://github.com/lebmatter/wiregram/blob/main/internal/plugin/builtin/weather.lua)
plugin is the reference: settings, kv caching, animation and error handling.
