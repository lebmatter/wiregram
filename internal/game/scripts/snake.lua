-- Snake. Eat to grow; hitting a wall or yourself ends it. Cells are two
-- columns wide so the grid looks square.

game = {
  info = { id = "snake", name = "Snake", version = 1,
           desc = "Eat, grow, and don't bite yourself", players = 1,
           help = "arrows, wasd or hjkl turn",
           min_w = 42, min_h = 17, tick_ms = 120 },
}

local GW, GH = 20, 15

local dirs = {
  up = { 0, -1 }, k = { 0, -1 }, w = { 0, -1 },
  down = { 0, 1 }, j = { 0, 1 }, s = { 0, 1 },
  left = { -1, 0 }, h = { -1, 0 }, a = { -1, 0 },
  right = { 1, 0 }, l = { 1, 0 }, d = { 1, 0 },
}

local body, dir, queue, food, eaten, dead, won, started

local function occupied(x, y)
  for _, c in ipairs(body) do
    if c.x == x and c.y == y then return true end
  end
  return false
end

local function placeFood()
  local free = {}
  for y = 0, GH - 1 do
    for x = 0, GW - 1 do
      if not occupied(x, y) then table.insert(free, { x = x, y = y }) end
    end
  end
  if #free == 0 then
    won, food = true, nil
    return
  end
  food = free[math.random(1, #free)]
end

function game.init()
  local cx, cy = math.floor(GW / 2), math.floor(GH / 2)
  body = { { x = cx, y = cy }, { x = cx - 1, y = cy }, { x = cx - 2, y = cy } }
  dir, queue = dirs.right, {}
  eaten, dead, won, started = 0, false, false, false
  placeFood()
end

function game.over() return dead or won end

function game.key(_, key)
  local d = dirs[key]
  if not d or game.over() then return end
  started = true
  -- Queue up to two turns so a quick double turn is not lost in one tick.
  local last = queue[#queue] or dir
  if #queue < 2 and d[1] ~= -last[1] and d[2] ~= -last[2] then
    table.insert(queue, d)
  end
end

function game.tick()
  if not started or game.over() then return end
  if #queue > 0 then dir = table.remove(queue, 1) end
  local head = body[1]
  local nx, ny = head.x + dir[1], head.y + dir[2]
  local grow = food and nx == food.x and ny == food.y
  if not grow then table.remove(body) end
  if nx < 0 or nx >= GW or ny < 0 or ny >= GH or occupied(nx, ny) then
    dead = true
    return
  end
  table.insert(body, 1, { x = nx, y = ny })
  if grow then
    eaten = eaten + 1
    placeFood()
  end
end

function game.view()
  local rows = {}
  for y = 0, GH - 1 do
    local row = {}
    for x = 1, GW do row[x] = "  " end
    rows[y] = row
  end
  if food then rows[food.y][food.x + 1] = "▐▌" end
  for i = #body, 1, -1 do
    local c = body[i]
    rows[c.y][c.x + 1] = i == 1 and (dead and "××" or "▓▓") or "██"
  end

  local out = { "┌" .. string.rep("─", GW * 2) .. "┐" }
  for y = 0, GH - 1 do
    table.insert(out, "│" .. table.concat(rows[y]) .. "│")
  end
  table.insert(out, "└" .. string.rep("─", GW * 2) .. "┘")

  local status
  if won then
    status = "you filled the board · score " .. eaten
  elseif dead then
    status = "crashed · score " .. eaten
  elseif not started then
    status = "press an arrow to start"
  else
    status = "score " .. eaten
  end
  return table.concat(out, "\n"), status
end
