-- 2048. Slide the tiles; equal tiles merge. Reach 2048 to win.

game = {
  info = { id = "2048", name = "2048", version = 1,
           desc = "Merge tiles up to 2048", players = 1,
           help = "arrows, wasd or hjkl slide",
           min_w = 29, min_h = 17, tick_ms = 0 },
}

local N = 4
local CW = 6 -- cell width

local grid, score, won, stuck

local function spawn()
  local free = {}
  for r = 1, N do
    for c = 1, N do
      if grid[r][c] == 0 then table.insert(free, { r, c }) end
    end
  end
  if #free == 0 then return end
  local p = free[math.random(1, #free)]
  grid[p[1]][p[2]] = math.random() < 0.9 and 2 or 4
end

local function canMove()
  for r = 1, N do
    for c = 1, N do
      local v = grid[r][c]
      if v == 0 then return true end
      if c < N and grid[r][c + 1] == v then return true end
      if r < N and grid[r + 1][c] == v then return true end
    end
  end
  return false
end

function game.init()
  grid = {}
  for r = 1, N do grid[r] = { 0, 0, 0, 0 } end
  score, won, stuck = 0, false, false
  spawn()
  spawn()
end

function game.over() return won or stuck end

function game.result()
  if won then return "win" end
  if stuck then return "lose" end
end

-- slide packs one line of values toward index 1, merging each pair once.
local function slide(line)
  local out, gained = {}, 0
  for _, v in ipairs(line) do
    if v ~= 0 then
      local last = out[#out]
      if last and last.v == v and not last.merged then
        last.v, last.merged = v * 2, true
        gained = gained + v * 2
      else
        table.insert(out, { v = v })
      end
    end
  end
  local res = {}
  for i = 1, N do res[i] = out[i] and out[i].v or 0 end
  return res, gained
end

-- cells lists the grid positions of line i, starting from the side tiles move to.
local function cells(dir, i)
  local t = {}
  for k = 1, N do
    if dir == "left" then t[k] = { i, k }
    elseif dir == "right" then t[k] = { i, N + 1 - k }
    elseif dir == "up" then t[k] = { k, i }
    else t[k] = { N + 1 - k, i } end
  end
  return t
end

local keys = {
  left = "left", h = "left", a = "left",
  right = "right", l = "right", d = "right",
  up = "up", k = "up", w = "up",
  down = "down", j = "down", s = "down",
}

function game.key(_, key)
  local dir = keys[key]
  if not dir or game.over() then return end
  local moved = false
  for i = 1, N do
    local pos, line = cells(dir, i), {}
    for k = 1, N do line[k] = grid[pos[k][1]][pos[k][2]] end
    local res, gained = slide(line)
    score = score + gained
    for k = 1, N do
      if res[k] ~= line[k] then moved = true end
      grid[pos[k][1]][pos[k][2]] = res[k]
      if res[k] >= 2048 then won = true end
    end
  end
  if moved then spawn() end
  stuck = not canMove()
end

local function edge(l, m, r)
  local seg = string.rep("─", CW)
  return l .. seg .. m .. seg .. m .. seg .. m .. seg .. r
end

local function center(s)
  local left = math.floor((CW - #s) / 2)
  return string.rep(" ", left) .. s .. string.rep(" ", CW - #s - left)
end

function game.view()
  local blank = "│" .. string.rep(string.rep(" ", CW) .. "│", N)
  local out = { edge("┌", "┬", "┐") }
  for r = 1, N do
    local row = {}
    for c = 1, N do
      local v = grid[r][c]
      row[c] = center(v == 0 and "" or tostring(v))
    end
    table.insert(out, blank)
    table.insert(out, "│" .. table.concat(row, "│") .. "│")
    table.insert(out, blank)
    table.insert(out, r < N and edge("├", "┼", "┤") or edge("└", "┴", "┘"))
  end

  local status
  if won then
    status = "you reached 2048 · score " .. score
  elseif stuck then
    status = "no moves left · score " .. score
  else
    status = "score " .. score
  end
  return table.concat(out, "\n"), status
end
