-- Connect Four. Seat 1 (the challenger) drops ● and moves first; seat 2 drops ○.

game = {
  info = { id = "connectfour", name = "Connect Four", version = 1,
           desc = "Four in a row, dropped from the top", players = 2,
           help = "←/→ or 1-7 pick a column · enter drops",
           min_w = 29, min_h = 9, tick_ms = 0,
           palette = { p1 = "#ff5f5f", p2 = "#ffd75f", frame = "blue", win = "accent",
                       num = "dim" },
           icon = "│ │○│ │ │\n│●│●│○│ │\n└─┴─┴─┴─┘" },
}

local COLS, ROWS = 7, 6

local board, names, turn, cursor, winner, full, line

function game.init(n)
  names = n
  board = {}
  for r = 1, ROWS do
    board[r] = {}
    for c = 1, COLS do board[r][c] = 0 end
  end
  turn, winner, full, line = 1, nil, false, {}
  cursor = { 4, 4 }
end

function game.over()
  return winner ~= nil or full
end

-- four looks for four in a row through r, c and remembers its cells.
local function four(r, c)
  local who = board[r][c]
  for _, d in ipairs({ {0, 1}, {1, 0}, {1, 1}, {1, -1} }) do
    local cells = { {r, c} }
    for _, s in ipairs({ 1, -1 }) do
      local rr, cc = r + d[1] * s, c + d[2] * s
      while rr >= 1 and rr <= ROWS and cc >= 1 and cc <= COLS and board[rr][cc] == who do
        table.insert(cells, {rr, cc})
        rr, cc = rr + d[1] * s, cc + d[2] * s
      end
    end
    if #cells >= 4 then
      for _, p in ipairs(cells) do line[p[1] * 10 + p[2]] = true end
      return true
    end
  end
  return false
end

local function drop(c)
  for r = ROWS, 1, -1 do
    if board[r][c] == 0 then
      board[r][c] = turn
      if four(r, c) then
        winner = turn
        return
      end
      full = true
      for cc = 1, COLS do
        if board[1][cc] == 0 then full = false end
      end
      turn = 3 - turn
      return
    end
  end
end

local moves = { left = -1, h = -1, a = -1, right = 1, l = 1, d = 1 }

function game.key(seat, key)
  if game.over() then return end
  local d = moves[key]
  if d then
    cursor[seat] = math.max(1, math.min(COLS, cursor[seat] + d))
    return
  end
  local n = tonumber(key)
  if n and n >= 1 and n <= COLS then
    cursor[seat] = n
    return
  end
  if (key == "enter" or key == "space" or key == "down" or key == "j") and seat == turn
      and board[1][cursor[seat]] == 0 then
    drop(cursor[seat])
  end
end

function game.result(seat)
  if winner then return winner == seat and "win" or "lose" end
  if full then return "draw" end
end

local inked = { [0] = " ", "{p1}●{/}", "{p2}○{/}" }

function game.view(seat, w, h)
  local rows = {}
  local top = {}
  for c = 1, COLS do
    if c == cursor[seat] and not game.over() then
      top[c] = " " .. (seat == turn and inked[seat] or "{num}▼{/}") .. " "
    else
      top[c] = "   "
    end
  end
  table.insert(rows, " " .. table.concat(top, " "))
  for r = 1, ROWS do
    local cells = {}
    for c = 1, COLS do
      local m = inked[board[r][c]]
      if line[r * 10 + c] then
        cells[c] = "{win}[{/}" .. m .. "{win}]{/}"
      else
        cells[c] = " " .. m .. " "
      end
    end
    local bar = "{frame}│{/}"
    table.insert(rows, bar .. table.concat(cells, bar) .. bar)
  end
  table.insert(rows, "{frame}└" .. string.rep("───┴", COLS - 1) .. "───┘{/}")
  local nums = {}
  for c = 1, COLS do nums[c] = " " .. c .. " " end
  table.insert(rows, "{num} " .. table.concat(nums, " ") .. "{/}")
  local status
  if winner == seat then
    status = "you won"
  elseif winner then
    status = names[winner] .. " won"
  elseif full then
    status = "draw"
  elseif turn == seat then
    status = "your turn (" .. inked[seat] .. ")"
  else
    status = names[turn] .. "'s turn"
  end
  return table.concat(rows, "\n"), status
end
