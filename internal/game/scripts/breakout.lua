-- Breakout. Clear every brick; miss the ball three times and it is over.
-- Terminals send no key-up, so a press moves the paddle for HOLD ticks and
-- key repeat keeps it going.

game = {
  info = { id = "breakout", name = "Breakout", version = 1,
           desc = "Clear the wall of bricks", players = 1,
           help = "left/right or a/d move · hold to keep moving · space serves",
           min_w = 50, min_h = 21, tick_ms = 33 },
}

local W, H = 50, 20 -- play area inside the top line; row H-1 is the paddle
local BW = 5        -- brick width, including the one-column gap
local ROWS, TOP = 5, 2
local PW = 8        -- paddle width
local HOLD = 5
local LIVES = 3

local bricks, left, pad, dir, held, ball, served, lives, score

local function clamp(v, lo, hi) return math.max(lo, math.min(hi, v)) end

local function reset()
  served = false
  ball = { x = pad + PW / 2, y = H - 2, vx = 0, vy = 0 }
end

function game.init()
  bricks, left = {}, 0
  for r = 1, ROWS do
    bricks[r] = {}
    for c = 1, W / BW do
      bricks[r][c] = true
      left = left + 1
    end
  end
  pad = (W - PW) / 2
  dir, held = 0, 0
  lives, score = LIVES, 0
  reset()
end

function game.over() return lives == 0 or left == 0 end

function game.result()
  if left == 0 then return "win" end
  if lives == 0 then return "lose" end
end

local lefts = { left = true, a = true, h = true }
local rights = { right = true, d = true, l = true }

function game.key(_, key)
  if game.over() then return end
  local d = (lefts[key] and -1) or (rights[key] and 1)
  if d then
    dir, held = d, HOLD
  elseif key == "space" or key == "up" or key == "w" then
    if not served then
      served = true
      ball.vx, ball.vy = (math.random() - 0.5) * 0.8, -0.45
    end
  end
end

-- brickAt returns the row and column of the brick covering cell x, y.
local function brickAt(x, y)
  local r = math.floor(y) - TOP + 1
  local c = math.floor(x / BW) + 1
  if r >= 1 and r <= ROWS and bricks[r][c] then return r, c end
  return nil
end

function game.tick()
  if game.over() then return end
  if held > 0 then
    pad = clamp(pad + dir * 1.2, 0, W - PW)
    held = held - 1
  end
  if not served then
    ball.x = pad + PW / 2
    return
  end

  local px, py = ball.x, ball.y
  ball.x, ball.y = ball.x + ball.vx, ball.y + ball.vy
  if ball.x < 0 then ball.x, ball.vx = -ball.x, -ball.vx end
  if ball.x > W - 1 then ball.x, ball.vx = 2 * (W - 1) - ball.x, -ball.vx end
  if ball.y < 0 then ball.y, ball.vy = -ball.y, -ball.vy end

  local r, c = brickAt(ball.x, ball.y)
  if r then
    bricks[r][c] = false
    left = left - 1
    score = score + (ROWS - r + 1)
    -- Bounce off the side the ball came through.
    if math.floor(py) ~= math.floor(ball.y) then
      ball.vy = -ball.vy
    else
      ball.vx = -ball.vx
    end
    ball.x, ball.y = px, py
    return
  end

  if py < H - 1 and ball.y >= H - 1 then
    local off = (ball.x - pad) / PW
    if off >= -0.1 and off <= 1.1 then
      ball.y = H - 1 - (ball.y - (H - 1)) - 0.01
      ball.vy = -math.abs(ball.vy)
      ball.vx = (off - 0.5) * 1.6
    end
  end
  if ball.y >= H then
    lives = lives - 1
    if lives > 0 then reset() end
  end
end

function game.view()
  local rows = {}
  for y = 0, H - 1 do
    local row = {}
    for x = 1, W do row[x] = " " end
    rows[y] = row
  end
  local shades = { "█", "█", "▓", "▓", "▒" }
  for r = 1, ROWS do
    for c = 1, W / BW do
      if bricks[r][c] then
        for i = 0, BW - 2 do rows[TOP + r - 1][(c - 1) * BW + i + 1] = shades[r] end
      end
    end
  end
  local p = math.floor(pad + 0.5)
  for x = p, p + PW - 1 do rows[H - 1][x + 1] = "▀" end
  local bx, by = math.floor(ball.x), math.floor(ball.y)
  if lives > 0 and left > 0 and bx >= 0 and bx < W and by >= 0 and by < H then
    rows[by][bx + 1] = "●"
  end

  local out = { string.rep("─", W) }
  for y = 0, H - 1 do table.insert(out, table.concat(rows[y])) end

  local status
  if left == 0 then
    status = "you cleared the wall · score " .. score
  elseif lives == 0 then
    status = "out of balls · score " .. score
  else
    status = string.format("score %d · balls %d", score, lives)
    if not served then status = status .. " · space serves" end
  end
  return table.concat(out, "\n"), status
end
