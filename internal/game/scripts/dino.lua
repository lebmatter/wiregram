-- Dino jump. Jump the cacti; the ground speeds up as the score climbs.

game = {
  info = { id = "dino", name = "Dino jump", version = 1,
           desc = "Jump the cacti for as long as you can", players = 1,
           help = "space/up/w jump",
           min_w = 60, min_h = 12, tick_ms = 50 },
}

local W, H = 60, 11 -- field above the ground line
local DX = 6        -- the dino's left column
local GRAVITY, JUMP = 0.12, 1.1
local AIR = 2 * JUMP / GRAVITY -- ticks spent in the air per jump

local started, dead, y, vy, dist, speed, cacti, gap

local function newCactus()
  return { x = W, w = math.random(1, 2), h = math.random(1, 3) }
end

function game.init()
  started, dead = false, false
  y, vy, dist, speed = 0, 0, 0, 0.5
  cacti = {}
  gap = 20
end

function game.over() return dead end

function game.result()
  if dead then return "lose" end
end

local jumps = { space = true, up = true, w = true, k = true }

function game.key(_, key)
  if dead or not jumps[key] then return end
  started = true
  if y == 0 then vy = JUMP end
end

local function score() return math.floor(dist / 2) end

function game.tick()
  if dead or not started then return end
  if y > 0 or vy > 0 then
    y, vy = y + vy, vy - GRAVITY
    if y <= 0 then y, vy = 0, 0 end
  end

  dist = dist + speed
  speed = math.min(1.2, 0.5 + score() / 600)
  for _, c in ipairs(cacti) do c.x = c.x - speed end
  while cacti[1] and cacti[1].x + cacti[1].w < 0 do table.remove(cacti, 1) end

  gap = gap - speed
  if gap <= 0 then
    table.insert(cacti, newCactus())
    -- Never closer than one jump plus room to land.
    local air = AIR * speed
    gap = math.random(math.floor(air + 6), math.floor(air + 30))
  end

  local bottom = math.floor(y)
  for _, c in ipairs(cacti) do
    local cx = math.floor(c.x)
    if cx <= DX + 1 and cx + c.w - 1 >= DX and bottom < c.h then
      dead = true
    end
  end
end

function game.view()
  local rows = {}
  for r = 0, H - 1 do
    local row = {}
    for x = 1, W do row[x] = " " end
    rows[r] = row
  end
  local function put(x, r, ch)
    if x >= 0 and x < W and r >= 0 and r < H then rows[r][x + 1] = ch end
  end

  for _, c in ipairs(cacti) do
    local cx = math.floor(c.x)
    for i = 0, c.w - 1 do
      for j = 1, c.h do put(cx + i, H - j, "█") end
    end
  end
  local feet = H - 1 - math.floor(y)
  put(DX, feet - 1, "▐")
  put(DX + 1, feet - 1, "█")
  put(DX, feet, dead and "x" or "▌")
  put(DX + 1, feet, "▌")

  local out = {}
  for r = 0, H - 1 do table.insert(out, table.concat(rows[r])) end
  table.insert(out, string.rep("─", W))

  local status
  if dead then
    status = "crashed · score " .. score()
  elseif not started then
    status = "press space to start"
  else
    status = "score " .. score()
  end
  return table.concat(out, "\n"), status
end
