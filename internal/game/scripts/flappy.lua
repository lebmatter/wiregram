-- Flappy bird. Flap through the gaps; touching a pipe or the ground ends it.

game = {
  info = { id = "flappy", name = "Flappy bird", version = 1,
           desc = "Flap through the gaps between the pipes", players = 1,
           help = "space/up/w flap",
           min_w = 50, min_h = 20, tick_ms = 50 },
}

local W, H = 50, 18 -- play area between the top and bottom lines
local BX = 10       -- the bird's column
local GRAVITY, FLAP, FALL = 0.09, -0.75, 1.0
local GAP = 6       -- rows of open space in each pipe
local SPACING = 16  -- columns between pipes
local SPEED = 0.5

local started, dead, y, vy, pipes, score, next

local function newPipe()
  return { x = W, top = math.random(2, H - GAP - 2), scored = false }
end

function game.init()
  started, dead = false, false
  y, vy, score = H / 2, 0, 0
  pipes, next = {}, 0
end

function game.over() return dead end

local flaps = { space = true, up = true, w = true, k = true }

function game.key(_, key)
  if dead or not flaps[key] then return end
  started = true
  vy = FLAP
end

local function hit(by)
  if by < 0 or by >= H then return true end
  for _, p in ipairs(pipes) do
    local px = math.floor(p.x)
    if BX >= px and BX <= px + 1 and (by < p.top or by >= p.top + GAP) then
      return true
    end
  end
  return false
end

function game.tick()
  if dead or not started then return end
  vy = math.min(vy + GRAVITY, FALL)
  y = y + vy

  for _, p in ipairs(pipes) do
    p.x = p.x - SPEED
    if not p.scored and p.x + 1 < BX then
      p.scored = true
      score = score + 1
    end
  end
  while pipes[1] and pipes[1].x < -2 do table.remove(pipes, 1) end
  next = next - SPEED
  if next <= 0 then
    table.insert(pipes, newPipe())
    next = SPACING
  end

  if hit(math.floor(y)) then dead = true end
end

function game.view()
  local rows = {}
  for r = 0, H - 1 do
    local row = {}
    for x = 1, W do row[x] = " " end
    rows[r] = row
  end
  for _, p in ipairs(pipes) do
    local px = math.floor(p.x)
    for r = 0, H - 1 do
      if r < p.top or r >= p.top + GAP then
        for x = px, px + 1 do
          if x >= 0 and x < W then rows[r][x + 1] = "█" end
        end
      end
    end
  end
  local by = math.max(0, math.min(H - 1, math.floor(y)))
  rows[by][BX + 1] = dead and "x" or "●"

  local line = string.rep("─", W)
  local out = { line }
  for r = 0, H - 1 do table.insert(out, table.concat(rows[r])) end
  table.insert(out, line)

  local status
  if dead then
    status = "crashed · score " .. score
  elseif not started then
    status = "press space to start"
  else
    status = "score " .. score
  end
  return table.concat(out, "\n"), status
end
