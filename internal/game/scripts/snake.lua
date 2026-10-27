-- Snake for two. Seat 1 (the challenger) is green, seat 2 blue, the same on
-- both screens. A snake dies on the walls, itself or the other; the one left
-- takes the round. Both dying at once gives the round to the longer one.

game = {
  info = { id = "snake", name = "Snake", version = 1,
           desc = "Two snakes, one field: first to 3 rounds", players = 2,
           help = "arrows or w/a/s/d turn · eat to grow, outlast the other",
           min_w = 60, min_h = 21, tick_ms = 110,
           palette = { s1 = "#87d75f", s1h = "#d7ff87", s2 = "#5fafff", s2h = "#afd7ff",
                       food = "#ff5f5f", wall = "dim" },
           icon = "██████████     \n         █  ●  \n  ●      ██████" },
}

local W, H = 30, 18 -- cells, each two columns wide so they look square
local WIN = 3
local FOOD = 3      -- food on the field at once
local START = 4     -- starting length
local WAIT = 15     -- ticks of pause before each round
local DIRS = {
  up = { 0, -1 }, k = { 0, -1 }, w = { 0, -1 },
  down = { 0, 1 }, j = { 0, 1 }, s = { 0, 1 },
  left = { -1, 0 }, h = { -1, 0 }, a = { -1, 0 },
  right = { 1, 0 }, l = { 1, 0 }, d = { 1, 0 },
}

local names, score, snakes, food, wait, winner, lastRound

local function key(x, y) return y * W + x end

-- occupied maps every cell a snake covers.
local function occupied()
  local o = {}
  for _, sn in ipairs(snakes) do
    for _, c in ipairs(sn.body) do o[key(c[1], c[2])] = true end
  end
  for _, f in ipairs(food) do o[key(f[1], f[2])] = true end
  return o
end

local function placeFood()
  local o = occupied()
  for _ = 1, 200 do
    local x, y = math.random(0, W - 1), math.random(0, H - 1)
    if not o[key(x, y)] then
      table.insert(food, { x, y })
      return
    end
  end
end

local function newRound()
  local function snake(x, y, dx)
    local body = {}
    for i = 0, START - 1 do table.insert(body, { x - i * dx, y }) end
    return { body = body, dir = { dx, 0 }, turns = {}, alive = true }
  end
  snakes = { snake(START + 1, math.floor(H / 3), 1),
             snake(W - START - 2, H - 1 - math.floor(H / 3), -1) }
  food = {}
  for _ = 1, FOOD do placeFood() end
  wait = WAIT
end

function game.init(n)
  names = n
  score = { 0, 0 }
  winner, lastRound = nil, nil
  newRound()
end

function game.over() return winner ~= nil end

function game.result(seat)
  if winner then return winner == seat and "win" or "lose" end
end

-- Turns queue up, two at most, so a quick pair of presses within one tick
-- isn't lost; a turn straight back into the snake's neck is ignored.
function game.key(seat, k)
  local d = DIRS[k]
  local sn = snakes[seat]
  if winner or not d or not sn.alive or #sn.turns >= 2 then return end
  local prev = sn.turns[#sn.turns] or sn.dir
  if d[1] == -prev[1] and d[2] == -prev[2] then return end
  if d[1] == prev[1] and d[2] == prev[2] then return end
  table.insert(sn.turns, d)
end

local function endRound()
  local a, b = snakes[1].alive, snakes[2].alive
  local to
  if a and not b then to = 1
  elseif b and not a then to = 2
  elseif #snakes[1].body ~= #snakes[2].body then
    to = #snakes[1].body > #snakes[2].body and 1 or 2
  end
  lastRound = to or 0
  if to then
    score[to] = score[to] + 1
    if score[to] >= WIN then
      winner = to
      return
    end
  end
  newRound()
end

function game.tick()
  if winner then return end
  if wait > 0 then
    wait = wait - 1
    return
  end
  local heads, eats = {}, {}
  for s, sn in ipairs(snakes) do
    if #sn.turns > 0 then sn.dir = table.remove(sn.turns, 1) end
    local h = sn.body[1]
    heads[s] = { h[1] + sn.dir[1], h[2] + sn.dir[2] }
    for i, f in ipairs(food) do
      if f[1] == heads[s][1] and f[2] == heads[s][2] then eats[s] = i end
    end
  end
  -- Tails move first, so a head may follow a tail into the cell it leaves.
  for s, sn in ipairs(snakes) do
    if not eats[s] then table.remove(sn.body) end
  end
  local body = {}
  for _, sn in ipairs(snakes) do
    for _, c in ipairs(sn.body) do body[key(c[1], c[2])] = true end
  end
  for s, sn in ipairs(snakes) do
    local h = heads[s]
    if h[1] < 0 or h[1] >= W or h[2] < 0 or h[2] >= H or body[key(h[1], h[2])] then
      sn.alive = false
    end
  end
  if heads[1][1] == heads[2][1] and heads[1][2] == heads[2][2] then
    snakes[1].alive, snakes[2].alive = false, false
  end
  if not snakes[1].alive or not snakes[2].alive then
    endRound()
    return
  end
  for s, sn in ipairs(snakes) do table.insert(sn.body, 1, heads[s]) end
  -- Both may eat the same food only by meeting head on, which ended the round.
  local gone = {}
  for s = 1, 2 do if eats[s] then gone[eats[s]] = true end end
  for i = #food, 1, -1 do
    if gone[i] then
      table.remove(food, i)
      placeFood()
    end
  end
end

function game.view(seat, w, h)
  local rows = {}
  for y = 0, H - 1 do
    local r = {}
    for x = 1, W do r[x] = "  " end
    rows[y] = r
  end
  for _, f in ipairs(food) do rows[f[2]][f[1] + 1] = "{food}●{/} " end
  for s, sn in ipairs(snakes) do
    for i = #sn.body, 1, -1 do
      local c = sn.body[i]
      if c[1] >= 0 and c[1] < W and c[2] >= 0 and c[2] < H then
        rows[c[2]][c[1] + 1] = (i == 1 and "{s" .. s .. "h}" or "{s" .. s .. "}") .. "██{/}"
      end
    end
  end

  local left = names[1]:sub(1, 20) .. "  " .. score[1]
  local right = score[2] .. "  " .. names[2]:sub(1, 20)
  local gap = string.rep(" ", math.max(1, 2 * W - #left - #right))
  local out = { "{s1}" .. left .. "{/}" .. gap .. "{s2}" .. right .. "{/}" }
  local wall = "{wall}" .. string.rep("─", 2 * W) .. "{/}"
  table.insert(out, wall)
  for y = 0, H - 1 do table.insert(out, table.concat(rows[y])) end
  table.insert(out, wall)

  local other = 3 - seat
  local me = "{s" .. seat .. "}" .. (seat == 1 and "green" or "blue") .. "{/}"
  local status
  if winner == seat then
    status = string.format("you won %d–%d", score[seat], score[other])
  elseif winner then
    status = string.format("%s won %d–%d", names[winner], score[winner], score[seat])
  else
    status = string.format("you're %s · you %d — %d %s", me, score[seat], score[other],
      names[other])
    if wait > 0 then
      if lastRound == seat then
        status = status .. " · you took that round"
      elseif lastRound == other then
        status = status .. " · " .. names[other] .. " took that round"
      elseif lastRound == 0 then
        status = status .. " · no one took that round"
      end
      status = status .. " · get ready"
    end
  end
  return table.concat(out, "\n"), status
end
