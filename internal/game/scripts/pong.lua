-- Pong. Seat 1 (the challenger) has the left paddle. Terminals send no key-up,
-- so a press moves the paddle for HOLD ticks and key repeat keeps it going.

game = {
  info = { id = "pong", name = "Pong", version = 1,
           desc = "First to 5 points", players = 2,
           help = "up/down or w/s move · hold to keep moving",
           min_w = 60, min_h = 21, tick_ms = 33,
           icon = "█    ·      \n█     ·    █\n       ●   █" },
}

local W, H = 60, 18 -- play area between the walls
local PH = 4        -- paddle height
local WIN = 5
local HOLD = 5      -- about 150 ms
local SERVE = 30    -- ticks of pause before each serve
local COL = { 1, W - 2 }

local names, pad, dir, held, score, ball, wait, winner

local function clamp(v, lo, hi) return math.max(lo, math.min(hi, v)) end

local function serve(toward)
  ball = { x = W / 2, y = H / 2, vx = 0.8 * toward, vy = (math.random() - 0.5) * 0.6 }
  wait = SERVE
end

function game.init(n)
  names = n
  pad = { (H - PH) / 2, (H - PH) / 2 }
  dir, held, score = { 0, 0 }, { 0, 0 }, { 0, 0 }
  winner = nil
  serve(math.random() < 0.5 and -1 or 1)
end

function game.over() return winner ~= nil end

local ups = { up = true, k = true, w = true }
local downs = { down = true, j = true, s = true }

function game.result(seat)
  if winner then return winner == seat and "win" or "lose" end
end

function game.key(seat, key)
  if winner then return end
  local d = (ups[key] and -1) or (downs[key] and 1)
  if d then dir[seat], held[seat] = d, HOLD end
end

-- hit bounces the ball off seat's paddle if it is there.
local function hit(seat)
  local off = (ball.y - pad[seat]) / PH
  if off < -0.15 or off > 1.15 then return false end
  local speed = math.min(math.abs(ball.vx) * 1.05, 1.6)
  ball.vx = seat == 1 and speed or -speed
  ball.vy = (off - 0.5) * 1.2
  ball.x = COL[seat] + (seat == 1 and 1 or -1)
  return true
end

local function point(to)
  score[to] = score[to] + 1
  if score[to] >= WIN then
    winner = to
  else
    serve(to == 1 and 1 or -1) -- toward the player who lost the point
  end
end

function game.tick()
  if winner then return end
  for s = 1, 2 do
    if held[s] > 0 then
      pad[s] = clamp(pad[s] + dir[s] * 0.7, 0, H - PH)
      held[s] = held[s] - 1
    end
  end
  if wait > 0 then
    wait = wait - 1
    return
  end
  local px = ball.x
  ball.x, ball.y = ball.x + ball.vx, ball.y + ball.vy
  if ball.y < 0 then ball.y, ball.vy = -ball.y, -ball.vy end
  if ball.y > H - 1 then ball.y, ball.vy = 2 * (H - 1) - ball.y, -ball.vy end
  if px >= COL[1] + 1 and ball.x < COL[1] + 1 and hit(1) then return end
  if px <= COL[2] - 1 and ball.x > COL[2] - 1 and hit(2) then return end
  if ball.x < 0 then point(2) elseif ball.x > W - 1 then point(1) end
end

function game.view(seat, w, h)
  local rows = {}
  for y = 0, H - 1 do
    local r = {}
    for x = 1, W do r[x] = " " end -- 1-based: r[x + 1] is column x
    if y % 2 == 0 then r[W / 2 + 1] = "┆" end
    rows[y] = r
  end
  for s = 1, 2 do
    local top = math.floor(pad[s] + 0.5)
    for y = top, top + PH - 1 do rows[y][COL[s] + 1] = "█" end
  end
  local bx, by = math.floor(ball.x + 0.5), math.floor(ball.y + 0.5)
  if not winner and bx >= 0 and bx < W and by >= 0 and by < H then
    rows[by][bx + 1] = "●"
  end

  local left = names[1]:sub(1, 20) .. "  " .. score[1]
  local right = score[2] .. "  " .. names[2]:sub(1, 20)
  local out = { left .. string.rep(" ", math.max(1, W - #left - #right)) .. right }
  local wall = string.rep("─", W)
  table.insert(out, wall)
  for y = 0, H - 1 do
    table.insert(out, table.concat(rows[y]))
  end
  table.insert(out, wall)

  local other = 3 - seat
  local status
  if winner == seat then
    status = string.format("you won %d–%d", score[seat], score[other])
  elseif winner then
    status = string.format("%s won %d–%d", names[winner], score[winner], score[seat])
  else
    status = string.format("you %d — %d %s", score[seat], score[other], names[other])
    if wait > 0 then status = status .. " · get ready" end
  end
  return table.concat(out, "\n"), status
end
