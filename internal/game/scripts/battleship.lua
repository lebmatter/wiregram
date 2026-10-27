-- Battleship. Both players place their fleets at once, each seeing only their
-- own; then they fire in turn, seat 1 (the challenger) first. The first to sink
-- the other's whole fleet wins.

game = {
  info = { id = "battleship", name = "Battleship", version = 1,
           desc = "Hide a fleet, sink theirs", players = 2,
           help = "arrows move · r rotates · enter places or fires · a places the rest",
           min_w = 48, min_h = 14, tick_ms = 0,
           palette = { ship = "#9aa3ad", hit = "#ff5f5f", sunk = "#af5f5f", miss = "#5f87d7",
                       water = "dim", place = "accent", cursor = "accent", label = "dim" },
           icon = "     |\\     \n  ___|_\\___ \n~~\\_______/~~" },
}

local N = 10
local FLEET = {
  { name = "Carrier", size = 5 }, { name = "Battleship", size = 4 },
  { name = "Cruiser", size = 3 }, { name = "Submarine", size = 3 },
  { name = "Destroyer", size = 2 },
}
local COLS = "ABCDEFGHIJ"

-- Per seat: ships[i] = { cells = {...}, hits = n }, grid[r][c] = ship index or
-- 0, shots[r][c] = nil, "hit" or "miss" for the shots fired at the other seat.
local names, ships, grid, shots, placing, vertical, cursor, turn, winner, last

local function newGrid(v)
  local g = {}
  for r = 1, N do
    g[r] = {}
    for c = 1, N do g[r][c] = v end
  end
  return g
end

function game.init(n)
  names = n
  ships, grid, shots = { {}, {} }, { newGrid(0), newGrid(0) }, { newGrid(nil), newGrid(nil) }
  placing = { 1, 1 } -- the next ship each seat places; past #FLEET when done
  vertical = { false, false }
  cursor = { {1, 1}, {1, 1} }
  turn, winner, last = 1, nil, { "", "" }
end

local function placed(seat) return placing[seat] > #FLEET end

local function firing() return placed(1) and placed(2) end

function game.over() return winner ~= nil end

-- cellsAt are the cells a ship of size would take at r, c, or nil when it
-- would leave the grid or cross another ship.
local function cellsAt(seat, size, r, c, vert)
  local cells = {}
  for i = 0, size - 1 do
    local rr, cc = r, c + i
    if vert then rr, cc = r + i, c end
    if rr > N or cc > N or grid[seat][rr][cc] ~= 0 then return nil end
    table.insert(cells, {rr, cc})
  end
  return cells
end

local function place(seat, cells)
  local i = placing[seat]
  ships[seat][i] = { cells = cells, hits = 0 }
  for _, p in ipairs(cells) do grid[seat][p[1]][p[2]] = i end
  placing[seat] = i + 1
end

local function autoPlace(seat)
  while not placed(seat) do
    local size = FLEET[placing[seat]].size
    local cells
    for _ = 1, 1000 do
      cells = cellsAt(seat, size, math.random(1, N), math.random(1, N), math.random() < 0.5)
      if cells then break end
    end
    if not cells then return end
    place(seat, cells)
  end
end

local function fire(seat, r, c)
  local foe = 3 - seat
  if shots[seat][r][c] then return end
  local i = grid[foe][r][c]
  if i == 0 then
    shots[seat][r][c] = "miss"
    last[seat], last[foe] = "miss", names[seat] .. " missed"
  else
    shots[seat][r][c] = "hit"
    local ship = ships[foe][i]
    ship.hits = ship.hits + 1
    if ship.hits == #ship.cells then
      last[seat] = "you sank their " .. FLEET[i].name
      last[foe] = names[seat] .. " sank your " .. FLEET[i].name
      local left = false
      for _, s in ipairs(ships[foe]) do
        if s.hits < #s.cells then left = true end
      end
      if not left then
        winner = seat
        return
      end
    else
      last[seat], last[foe] = "hit", names[seat] .. " hit your " .. FLEET[i].name
    end
  end
  turn = foe
end

local moves = {
  up = {-1, 0}, k = {-1, 0}, down = {1, 0}, j = {1, 0},
  left = {0, -1}, h = {0, -1}, right = {0, 1}, l = {0, 1},
}

function game.key(seat, key)
  if game.over() then return end
  local cur = cursor[seat]
  local d = moves[key]
  if d then
    cur[1] = math.max(1, math.min(N, cur[1] + d[1]))
    cur[2] = math.max(1, math.min(N, cur[2] + d[2]))
    return
  end
  if not placed(seat) then
    if key == "r" then
      vertical[seat] = not vertical[seat]
    elseif key == "a" then
      autoPlace(seat)
    elseif key == "enter" or key == "space" then
      local cells = cellsAt(seat, FLEET[placing[seat]].size, cur[1], cur[2], vertical[seat])
      if cells then place(seat, cells) end
    end
    return
  end
  if firing() and seat == turn and (key == "enter" or key == "space") then
    fire(seat, cur[1], cur[2])
  end
end

function game.result(seat)
  if winner then return winner == seat and "win" or "lose" end
end

-- The two grids, side by side: your fleet with the other's shots on it, and
-- the other's waters with your shots.
local GAP = "    "

local function header()
  local cols = {}
  for c = 1, N do cols[c] = " " .. COLS:sub(c, c) end
  local line = "{label}  " .. table.concat(cols) .. "{/}"
  return line .. GAP .. line
end

local function ownCell(seat, r, c, preview)
  local foe = 3 - seat
  local shot = shots[foe][r][c]
  if shot == "hit" then return "{hit}X{/}" end
  if preview and preview[r * 100 + c] then return "{place}□{/}" end
  if grid[seat][r][c] ~= 0 then return "{ship}■{/}" end
  if shot == "miss" then return "{miss}o{/}" end
  return "{water}·{/}"
end

local function foeCell(seat, r, c)
  local shot = shots[seat][r][c]
  if shot == "miss" then return "{miss}o{/}" end
  if shot == "hit" then
    local ship = ships[3 - seat][grid[3 - seat][r][c]]
    if ship.hits == #ship.cells then return "{sunk}#{/}" end
    return "{hit}X{/}"
  end
  return "{water}·{/}"
end

function game.view(seat, w, h)
  local cur = cursor[seat]
  local preview
  if not placed(seat) then
    local cells = cellsAt(seat, FLEET[placing[seat]].size, cur[1], cur[2], vertical[seat])
    if cells then
      preview = {}
      for _, p in ipairs(cells) do preview[p[1] * 100 + p[2]] = true end
    end
  end
  local rows = {}
  table.insert(rows, "your fleet" .. string.rep(" ", 12) .. GAP .. "their waters")
  table.insert(rows, header())
  for r = 1, N do
    local mine, theirs = {}, {}
    for c = 1, N do
      local m, t = " ", " "
      if not placed(seat) and r == cur[1] and c == cur[2] then m = "{cursor}▸{/}" end
      if placed(seat) and not game.over() and r == cur[1] and c == cur[2] then
        t = "{cursor}▸{/}"
      end
      mine[c] = m .. ownCell(seat, r, c, preview)
      theirs[c] = t .. foeCell(seat, r, c)
    end
    local label = "{label}" .. string.format("%2d", r) .. "{/}"
    table.insert(rows, label .. table.concat(mine) .. GAP .. label .. table.concat(theirs))
  end
  table.insert(rows, "")
  local afloat = {}
  for i, ship in ipairs(ships[seat]) do
    if ship.hits < #ship.cells then
      table.insert(afloat, "{ship}" .. string.rep("■", #ship.cells) .. "{/}")
    end
  end
  if placed(seat) then
    table.insert(rows, "afloat: " .. table.concat(afloat, " "))
  end

  local status
  if winner == seat then
    status = "you won: their fleet is sunk"
  elseif winner then
    status = names[winner] .. " won: your fleet is sunk"
  elseif not placed(seat) then
    local ship = FLEET[placing[seat]]
    status = "place your " .. ship.name .. " (" .. ship.size .. ", " ..
      (vertical[seat] and "down" or "across") .. ")"
  elseif not firing() then
    status = "waiting for " .. names[3 - seat] .. " to place their fleet"
  elseif turn == seat then
    status = "your shot" .. (last[seat] ~= "" and " · last: " .. last[seat] or "")
  else
    status = names[turn] .. "'s shot" .. (last[seat] ~= "" and " · " .. last[seat] or "")
  end
  return table.concat(rows, "\n"), status
end
