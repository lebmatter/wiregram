-- Tic-tac-toe. Seat 1 (the challenger) plays X and moves first.

game = {
  info = { id = "tictactoe", name = "Tic-tac-toe", version = 1,
           desc = "Three in a row", players = 2,
           help = "arrows move · enter places your mark",
           min_w = 21, min_h = 11, tick_ms = 0 },
}

local board, names, turn, cursor, winner, full

local lines = {
  {1, 2, 3}, {4, 5, 6}, {7, 8, 9},
  {1, 4, 7}, {2, 5, 8}, {3, 6, 9},
  {1, 5, 9}, {3, 5, 7},
}

local function check()
  for _, l in ipairs(lines) do
    local a = board[l[1]]
    if a ~= 0 and a == board[l[2]] and a == board[l[3]] then
      winner = a
      return
    end
  end
  full = true
  for i = 1, 9 do
    if board[i] == 0 then full = false end
  end
end

function game.init(n)
  names = n
  board = {0, 0, 0, 0, 0, 0, 0, 0, 0}
  turn, winner, full = 1, nil, false
  cursor = {5, 5}
end

function game.over()
  return winner ~= nil or full
end

local moves = {
  up = -3, k = -3, down = 3, j = 3, left = -1, h = -1, right = 1, l = 1,
}

function game.key(seat, key)
  if game.over() then return end
  local d = moves[key]
  if d then
    local c = cursor[seat]
    local col = (c - 1) % 3
    if (d == -1 and col == 0) or (d == 1 and col == 2) then return end
    if c + d >= 1 and c + d <= 9 then cursor[seat] = c + d end
    return
  end
  if (key == "enter" or key == "space") and seat == turn and board[cursor[seat]] == 0 then
    board[cursor[seat]] = seat
    check()
    turn = 3 - turn
  end
end

local marks = { [0] = " ", "X", "O" }

function game.view(seat, w, h)
  local rows = {}
  for r = 0, 2 do
    local cells = {}
    for c = 1, 3 do
      local i = r * 3 + c
      local m = marks[board[i]]
      if i == cursor[seat] and not game.over() then
        cells[c] = "[ " .. m .. " ]"
      else
        cells[c] = "  " .. m .. "  "
      end
    end
    table.insert(rows, "     │     │     ")
    table.insert(rows, table.concat(cells, "│"))
    table.insert(rows, "     │     │     ")
    if r < 2 then table.insert(rows, "─────┼─────┼─────") end
  end
  local status
  if winner == seat then
    status = "you won"
  elseif winner then
    status = names[winner] .. " won"
  elseif full then
    status = "draw"
  elseif turn == seat then
    status = "your turn (" .. marks[seat] .. ")"
  else
    status = names[turn] .. "'s turn"
  end
  return table.concat(rows, "\n"), status
end
