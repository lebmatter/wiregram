-- Chess. Seat 1 (the challenger) plays white and moves first; each seat sees
-- its own pieces at the bottom. No draw by repetition or the fifty-move rule.

game = {
  info = { id = "chess", name = "Chess", version = 1,
           desc = "The full game, castling and all", players = 2,
           help = "arrows move · enter picks a piece, then its square",
           min_w = 26, min_h = 11, tick_ms = 0,
           -- Mid-tone squares, so both white and black pieces stand out on them.
           palette = { light = "on #a68c64", dark = "on #80633f", cur = "on #9c8c20",
                       last = "on #7d8a4a", sel = "on #4f8a8b", cap = "on #8a6a9a",
                       check = "on #b8443a", w = "#ffffff", b = "#000000",
                       hint = "#e8f0c0", coord = "dim" },
           icon = "♜ ♞ ♝ ♛ ♚ ♝ ♞ ♜\n♟ ♟ ♟ ♟ ♟ ♟ ♟ ♟\n♙ ♙ ♙ ♙ ♙ ♙ ♙ ♙" },
}

local KNIGHT = { {2, 1}, {1, 2}, {-1, 2}, {-2, 1}, {-2, -1}, {-1, -2}, {1, -2}, {2, -1} }
local KING = { {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1} }
local ROOK = { {1, 0}, {-1, 0}, {0, 1}, {0, -1} }
local BISHOP = { {1, 1}, {1, -1}, {-1, 1}, {-1, -1} }
local GLYPH = { k = "♚", q = "♛", r = "♜", b = "♝", n = "♞", p = "♟" }
local FILES = "abcdefgh"

-- corners loses a side's castling right when anything moves from or to it.
local corners = { [1] = {1, "q"}, [8] = {1, "k"}, [57] = {2, "q"}, [64] = {2, "k"} }

local board, names, turn, castle, ep, legal, check, last, captured
local cursor, picked, promoting, winner, stalemate

local function sq(r, f) return (r - 1) * 8 + f end
local function rf(s) return math.floor((s - 1) / 8) + 1, (s - 1) % 8 + 1 end
local function on(r, f) return r >= 1 and r <= 8 and f >= 1 and f <= 8 end
local function name(s) local r, f = rf(s) return FILES:sub(f, f) .. r end

-- attacked reports whether side by attacks square s on board b.
local function attacked(b, s, by)
  local r, f = rf(s)
  local back = by == 1 and -1 or 1
  for _, df in ipairs({ -1, 1 }) do
    if on(r + back, f + df) then
      local p = b[sq(r + back, f + df)]
      if p and p.side == by and p.kind == "p" then return true end
    end
  end
  for kind, steps in pairs({ n = KNIGHT, k = KING }) do
    for _, d in ipairs(steps) do
      if on(r + d[1], f + d[2]) then
        local p = b[sq(r + d[1], f + d[2])]
        if p and p.side == by and p.kind == kind then return true end
      end
    end
  end
  for _, ray in ipairs({ { ROOK, "r" }, { BISHOP, "b" } }) do
    for _, d in ipairs(ray[1]) do
      local rr, ff = r + d[1], f + d[2]
      while on(rr, ff) do
        local p = b[sq(rr, ff)]
        if p then
          if p.side == by and (p.kind == ray[2] or p.kind == "q") then return true end
          break
        end
        rr, ff = rr + d[1], ff + d[2]
      end
    end
  end
  return false
end

local function kingOf(b, side)
  for s = 1, 64 do
    local p = b[s]
    if p and p.side == side and p.kind == "k" then return s end
  end
end

-- moved is board b after move m; promotions become promo, or a queen.
local function moved(b, m, promo)
  local n = {}
  for s = 1, 64 do n[s] = b[s] end
  local p = n[m.from]
  n[m.from] = nil
  if m.ep then n[m.ep] = nil end
  if m.rook then n[m.rook[2]], n[m.rook[1]] = n[m.rook[1]], nil end
  n[m.to] = m.promo and { side = p.side, kind = promo or "q" } or p
  return n
end

-- pseudo lists side's moves without asking whether they leave its king in
-- check; castling already checks the squares the king passes.
local function pseudo(side)
  local out = {}
  local function add(from, to, extra)
    local m = extra or {}
    m.from, m.to = from, to
    table.insert(out, m)
  end
  local enemy = 3 - side
  for s = 1, 64 do
    local p = board[s]
    if p and p.side == side then
      local r, f = rf(s)
      if p.kind == "p" then
        local fwd = side == 1 and 1 or -1
        local promo = r + fwd == (side == 1 and 8 or 1)
        if on(r + fwd, f) and not board[sq(r + fwd, f)] then
          add(s, sq(r + fwd, f), { promo = promo })
          local start = side == 1 and 2 or 7
          if r == start and not board[sq(r + 2 * fwd, f)] then
            add(s, sq(r + 2 * fwd, f), { double = sq(r + fwd, f) })
          end
        end
        for _, df in ipairs({ -1, 1 }) do
          if on(r + fwd, f + df) then
            local t = sq(r + fwd, f + df)
            if board[t] and board[t].side == enemy then
              add(s, t, { promo = promo })
            elseif t == ep then
              add(s, t, { ep = sq(r, f + df) })
            end
          end
        end
      elseif p.kind == "n" or p.kind == "k" then
        for _, d in ipairs(p.kind == "n" and KNIGHT or KING) do
          if on(r + d[1], f + d[2]) then
            local t = sq(r + d[1], f + d[2])
            if not board[t] or board[t].side == enemy then add(s, t) end
          end
        end
      else
        local rays = {}
        if p.kind ~= "b" then for _, d in ipairs(ROOK) do table.insert(rays, d) end end
        if p.kind ~= "r" then for _, d in ipairs(BISHOP) do table.insert(rays, d) end end
        for _, d in ipairs(rays) do
          local rr, ff = r + d[1], f + d[2]
          while on(rr, ff) do
            local t = sq(rr, ff)
            if board[t] then
              if board[t].side == enemy then add(s, t) end
              break
            end
            add(s, t)
            rr, ff = rr + d[1], ff + d[2]
          end
        end
      end
    end
  end
  local home = side == 1 and 1 or 8
  local e = sq(home, 5)
  local kp = board[e]
  if kp and kp.side == side and kp.kind == "k" and not attacked(board, e, enemy) then
    if castle[side].k and not board[sq(home, 6)] and not board[sq(home, 7)]
        and not attacked(board, sq(home, 6), enemy) then
      add(e, sq(home, 7), { rook = { sq(home, 8), sq(home, 6) } })
    end
    if castle[side].q and not board[sq(home, 4)] and not board[sq(home, 3)]
        and not board[sq(home, 2)] and not attacked(board, sq(home, 4), enemy) then
      add(e, sq(home, 3), { rook = { sq(home, 1), sq(home, 4) } })
    end
  end
  return out
end

local function legalMoves(side)
  local out = {}
  for _, m in ipairs(pseudo(side)) do
    local b = moved(board, m)
    if not attacked(b, kingOf(b, side), 3 - side) then table.insert(out, m) end
  end
  return out
end

local function moveTo(from, to)
  for _, m in ipairs(legal) do
    if m.from == from and m.to == to then return m end
  end
end

local function canMove(from)
  for _, m in ipairs(legal) do
    if m.from == from then return true end
  end
  return false
end

local function play(m, promo)
  local p = board[m.from]
  local taken = board[m.to] or (m.ep and board[m.ep])
  if taken then table.insert(captured[p.side], taken.kind) end
  board = moved(board, m, promo)
  if p.kind == "k" then castle[p.side] = {} end
  for _, s in ipairs({ m.from, m.to }) do
    local c = corners[s]
    if c then castle[c[1]][c[2]] = nil end
  end
  ep = m.double
  last = { m.from, m.to }
  turn = 3 - turn
  legal = legalMoves(turn)
  check = attacked(board, kingOf(board, turn), 3 - turn)
  if #legal == 0 then
    if check then winner = 3 - turn else stalemate = true end
  end
end

function game.init(n)
  names = n
  board = {}
  local back = { "r", "n", "b", "q", "k", "b", "n", "r" }
  for f = 1, 8 do
    board[sq(1, f)] = { side = 1, kind = back[f] }
    board[sq(2, f)] = { side = 1, kind = "p" }
    board[sq(7, f)] = { side = 2, kind = "p" }
    board[sq(8, f)] = { side = 2, kind = back[f] }
  end
  turn, castle, ep, check, last = 1, { { k = true, q = true }, { k = true, q = true } }, nil,
    false, nil
  captured = { {}, {} }
  cursor, picked, promoting = { sq(2, 5), sq(7, 5) }, {}, {}
  winner, stalemate = nil, false
  legal = legalMoves(turn)
end

function game.over()
  return winner ~= nil or stalemate
end

function game.result(seat)
  if winner then return winner == seat and "win" or "lose" end
  if stalemate then return "draw" end
end

-- Arrows move on the screen, and black's board is turned around.
local steps = { up = { 1, 0 }, k = { 1, 0 }, down = { -1, 0 }, j = { -1, 0 },
                left = { 0, -1 }, h = { 0, -1 }, right = { 0, 1 }, l = { 0, 1 } }
local promos = { q = "q", r = "r", b = "b", n = "n", enter = "q", space = "q" }

function game.key(seat, key)
  if game.over() then return end
  if promoting[seat] then
    if promos[key] then
      play(promoting[seat], promos[key])
      promoting[seat] = nil
    end
    return
  end
  local d = steps[key]
  if d then
    local flip = seat == 1 and 1 or -1
    local r, f = rf(cursor[seat])
    if on(r + d[1] * flip, f + d[2] * flip) then
      cursor[seat] = sq(r + d[1] * flip, f + d[2] * flip)
    end
    return
  end
  if key == "backspace" then
    picked[seat] = nil
    return
  end
  if (key ~= "enter" and key ~= "space") or seat ~= turn then return end
  local c = cursor[seat]
  if picked[seat] == c then
    picked[seat] = nil
  elseif board[c] and board[c].side == seat then
    picked[seat] = canMove(c) and c or nil
  elseif picked[seat] then
    local m = moveTo(picked[seat], c)
    if not m then return end
    picked[seat] = nil
    if m.promo then promoting[seat] = m else play(m) end
  end
end

local function material(side)
  local order = { q = 1, r = 2, b = 3, n = 4, p = 5 }
  local t = {}
  for _, k in ipairs(captured[side]) do table.insert(t, k) end
  table.sort(t, function(a, b) return order[a] < order[b] end)
  local out = {}
  for _, k in ipairs(t) do table.insert(out, GLYPH[k]) end
  return "  {coord}" .. table.concat(out) .. "{/}"
end

-- squareSize is the largest square that fits a board in w×h: cw cells wide and
-- ch rows tall, cw about twice ch since a cell is twice as tall as it is wide.
-- A piece is one cell, so squares stop at 5×2, past which it looks lost.
local function squareSize(w, h)
  local ch = 1
  while ch < 2 and 2 + 8 * (2 * ch + 3) <= w and 8 * (ch + 1) + 3 <= h do ch = ch + 1 end
  return 2 * ch + 1, ch
end

function game.view(seat, w, h)
  local cw, ch = squareSize(w, h)
  local pad, mid = string.rep(" ", (cw - 1) / 2), math.ceil(ch / 2)
  local live = not game.over()
  local from = picked[seat]
  local targets = {}
  if from then
    for _, m in ipairs(legal) do
      if m.from == from then targets[m.to] = true end
    end
  end
  local inCheck = check and kingOf(board, turn)
  local ranks, files = { 8, 7, 6, 5, 4, 3, 2, 1 }, { 1, 2, 3, 4, 5, 6, 7, 8 }
  if seat == 2 then ranks, files = { 1, 2, 3, 4, 5, 6, 7, 8 }, { 8, 7, 6, 5, 4, 3, 2, 1 } end
  local rows = { material(3 - seat) }
  for _, r in ipairs(ranks) do
    local bgs, mids = {}, {}
    for _, f in ipairs(files) do
      local s = sq(r, f)
      local p = board[s]
      local bg = (r + f) % 2 == 0 and "dark" or "light"
      if last and (s == last[1] or s == last[2]) then bg = "last" end
      if s == inCheck then bg = "check" end
      if targets[s] and p then bg = "cap" end
      if s == from then bg = "sel" end
      if live and s == cursor[seat] then bg = "cur" end
      local piece = " "
      if p then
        piece = (p.side == 1 and "{w}" or "{b}") .. GLYPH[p.kind]
      elseif targets[s] then
        piece = "{hint}•"
      end
      table.insert(bgs, bg)
      table.insert(mids, piece)
    end
    for line = 1, ch do
      local cells = { line == mid and "{coord}" .. r .. "{/} " or "  " }
      for i, bg in ipairs(bgs) do
        local piece = line == mid and mids[i] or " "
        table.insert(cells, "{" .. bg .. "}" .. pad .. piece .. pad .. "{/}")
      end
      table.insert(rows, table.concat(cells))
    end
  end
  local head = { "  " }
  for _, f in ipairs(files) do table.insert(head, pad .. FILES:sub(f, f) .. pad) end
  table.insert(rows, "{coord}" .. table.concat(head) .. "{/}")
  table.insert(rows, material(seat))

  local status
  if winner then
    status = "checkmate · " .. (winner == seat and "you won" or names[winner] .. " won")
  elseif stalemate then
    status = "stalemate · draw"
  elseif promoting[seat] then
    status = "promote to q r b n (enter: queen)"
  elseif turn == seat then
    status = check and "check · your move" or "your move"
  else
    status = names[turn] .. " to move"
  end
  if last and live then status = status .. " · " .. name(last[1]) .. "–" .. name(last[2]) end
  return table.concat(rows, "\n"), status
end
