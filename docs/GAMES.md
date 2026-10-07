# Writing a game

Games are Lua 5.1 scripts in `internal/game/scripts/`, embedded in the
wiregram binary. Adding a `.lua` file there adds the game; nothing else needs
registering.

## How a match runs

A one-player game (`players = 1`) runs in the player's own wiregram with no
base; its only seat is 1. Two-player games need a base:

- `wiregram base` lists players, passes on challenges and relays messages. It
  never runs a script.
- The challenger hosts. Its wiregram runs the script for **both** seats.
- The guest sends each key press to the host and draws the lines of its view
  that changed. The guest never runs the script.
- Scripts do no networking and do not know which seat is the host.

## The contract

```lua
game = {
  info = {
    id = "tictactoe",        -- stable id; never reuse it for another game
    name = "Tic-tac-toe",
    version = 1,             -- bump on ANY rule or view change
    desc = "Three in a row", -- one line in the lobby
    help = "arrows move · enter places your mark", -- shown under the board
    players = 2,             -- 1 runs locally; 2 needs a base
    min_w = 21, min_h = 11,  -- the board's size in cells
    tick_ms = 0,             -- 0 for turn-based, else 16 or more
  },
}

function game.init(names) end              -- names[1] is seat 1 (the challenger)
function game.key(seat, key) end           -- BubbleTea key: "up", "enter", "space", "a"…
function game.tick() end                   -- every tick_ms; needed only when tick_ms > 0
function game.view(seat, w, h)             -- w, h are min_w, min_h
  return board, status                     -- board is lines joined by "\n"
end
function game.over() return false end
```

Seats are 1 and 2 (only 1 in a one-player game). `view` is called for both seats after every key and tick,
so keep it cheap. Plain text only: control characters are stripped on the
guest's side.

## Versions

Players can only be matched when both have the same `id` **and** `version`.
Bump `version` whenever the rules or the view change, so two builds can never
play different rules against each other. An older build shows the game as
`no vN` next to the player.

## Sandbox

Only the `base`, `table`, `string` and `math` libraries are loaded.
`dofile`, `loadfile`, `load`, `loadstring`, `require`, `module`, `print`,
`collectgarbage`, `getfenv` and `setfenv` are removed. There is no `io`, `os`
or `debug`. Every call into the script has 50 ms; a call that takes longer, or
raises an error, ends the match with one notice line.

## Checking a script

A script that fails validation (missing `info` fields or functions) is left
out of the lobby, and the notice line names it. A one-player game needs no
base: open games with Alt+G and press Enter on it. Play a two-player game
against a second instance on a local base:

```sh
./wiregram base &
./wiregram                              # Alt+G, e, connect to ws://localhost:8765
XDG_CONFIG_HOME=/tmp/wg2 ./wiregram     # a second player
```
