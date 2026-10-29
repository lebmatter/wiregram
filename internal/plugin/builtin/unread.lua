-- Unread: the unread messages of every online account, in private chats and
-- elsewhere. A click opens a catch-up summary of them, by chat, when an AI
-- provider is set up, else their newest messages.

wiregram.plugin {
  title = "Unread",
  permissions = { "messages", "aitasks" },
  on = true,
}

local perChat = 3 -- messages shown per chat in the modal
local linesPerMessage = 4

-- counts holds, per account, {account=, dm=, other=, err=}: unread messages in
-- private chats and in the rest, from the last update; it is kept in memory
-- only, never in the kv store.
local counts

local function update()
  local out = {}
  for _, a in ipairs(wiregram.accounts()) do
    local chats, err = wiregram.unread(a.key, 0)
    local row = { account = a, dm = 0, other = 0, err = err }
    for _, c in ipairs(chats or {}) do
      if c.dm then
        row.dm = row.dm + c.count
      else
        row.other = row.other + c.count
      end
    end
    out[#out + 1] = row
  end
  counts = out
end

-- width counts characters, not bytes.
local function width(s)
  local _, n = s:gsub("[^\128-\191]", "")
  return n
end

-- take splits s after its first n characters.
local function take(s, n)
  local i = 1
  for _ = 1, n do
    local c = s:byte(i)
    if not c then break end
    i = i + (c < 0x80 and 1 or c < 0xE0 and 2 or c < 0xF0 and 3 or 4)
  end
  return s:sub(1, i - 1), s:sub(i)
end

local function cut(s, n)
  if width(s) <= n then return s end
  return (take(s, n - 1)) .. "…"
end

-- spread puts left and right at the two ends of a line n cells wide. left is
-- plain text, escaped once cut to fit; right may hold markup and shows rw cells.
local function spread(left, right, n, rw)
  rw = rw or width(right)
  left = cut(left, math.max(1, n - rw - 2))
  return wiregram.escape(left) .. string.rep(" ", math.max(2, n - width(left) - rw)) .. right
end

local function render(w)
  if not counts then return { "Counting unread messages…" } end
  if #counts == 0 then return { "No accounts online" } end
  local lines, any = {}, false
  for _, row in ipairs(counts) do
    local name = row.account.tag .. " " .. row.account.label
    local right, rw = "nothing unread", nil
    if row.err then
      right = "couldn't load"
    elseif row.dm + row.other > 0 then
      -- DMs sit on the accent fill: they are the ones meant for the user.
      local dm = "*" .. row.dm .. "*"
      if row.dm > 0 then
        any = true
        dm = "__ " .. row.dm .. " __"
      end
      right = "*" .. row.other .. "* · DMs " .. dm
      rw = width((right:gsub("__", ""):gsub("%*", "")))
    end
    lines[#lines + 1] = spread(name, right, w, rw)
  end
  return lines, any and "accent" or nil
end

-- wrap breaks text into lines of at most n characters, at spaces when it can.
-- It stops at max lines, ending the last with "…": render has 50 ms for every
-- message, so it reads only what max lines can hold and measures each word once.
local function wrap(text, n, max)
  local out = {}
  local function full() return #out >= max end
  local rest
  text, rest = take(text, 2 * n * max)
  for para in (text .. "\n"):gmatch("(.-)\n") do
    local line, lw = "", 0
    for word in para:gmatch("%S+") do
      local ww = width(word)
      if line == "" then
        line, lw = word, ww
      elseif lw + 1 + ww <= n then
        line, lw = line .. " " .. word, lw + 1 + ww
      else
        out[#out + 1] = line
        if full() then break end
        line, lw = word, ww
      end
      while lw > n and not full() do
        local head, rest = take(line, n)
        out[#out + 1] = head
        line, lw = rest, lw - n
      end
      if full() then break end
    end
    if full() then
      out[max] = cut(out[max], n - 1) .. "…"
      return out
    end
    out[#out + 1] = line
  end
  if rest ~= "" then out[#out] = cut(out[#out], n - 1) .. "…" end
  return out
end

-- detail is what the modal shows: per account, its chats and their messages.
local detail

-- lines lays detail out w cells wide.
local function lines(w)
  local out = {}
  for i, d in ipairs(detail) do
    if i > 1 then out[#out + 1] = "" end
    out[#out + 1] = "*" .. wiregram.escape(d.account.tag .. " " .. d.account.label) .. "*"
    if d.err then
      out[#out + 1] = "  couldn't load: " .. wiregram.escape(d.err)
    elseif #d.chats == 0 then
      out[#out + 1] = "  nothing unread"
    end
    for _, c in ipairs(d.chats) do
      local count = c.count .. " unread"
      local title = (c.dm and "@" or "#") .. c.title
      out[#out + 1] = "  " .. spread(title, "*" .. count .. "*", w - 2, width(count))
      for _, m in ipairs(c.messages) do
        local prefix = "    [" .. m.clock .. "] "
        local text = m.text
        if not c.dm and m.sender ~= "" then text = m.sender .. ": " .. text end
        local body = wrap(text, math.max(10, w - width(prefix)), linesPerMessage)
        for j, l in ipairs(body) do
          out[#out + 1] = (j == 1 and prefix or string.rep(" ", width(prefix))) ..
            wiregram.escape(l)
        end
      end
      if c.count > #c.messages and #c.messages > 0 then
        out[#out + 1] = "    … " .. (c.count - #c.messages) .. " more"
      end
    end
  end
  return out
end

local modalWidth = 80

-- words splits a line of the summary into words, each marked bold when it
-- sits between the model's **double asterisks**.
local function words(line)
  local out, bold = {}, false
  for seg in (line .. "**"):gmatch("(.-)%*%*") do
    for word in seg:gmatch("%S+") do out[#out + 1] = { text = word, bold = bold } end
    bold = not bold
  end
  return out
end

-- styled appends one line of the summary to out, laid out n cells wide: "## "
-- lines are chat headings, bullets wrap under their text, and bold words are
-- marked up one by one, so a line break never splits markup.
local function styled(line, n, out)
  local heading = line:match("^##%s*(.*)")
  if heading then
    if #out > 0 then out[#out + 1] = "" end
    out[#out + 1] = "*" .. wiregram.escape(cut((heading:gsub("%*", "")), n)) .. "*"
    return
  end
  local first, rest = "  ", "  "
  local bullet = line:match("^%s*[-*]%s+(.*)") or line:match("^%s*•%s*(.*)")
  if bullet then line, first, rest = bullet, "  • ", "    " end
  local text, tw, empty = first, width(first), true
  for _, w in ipairs(words(line)) do
    local ww = width(w.text)
    if not empty and tw + 1 + ww > n then
      out[#out + 1] = text
      text, tw, empty = rest, width(rest), true
    end
    if not empty then text, tw = text .. " ", tw + 1 end
    local s = wiregram.escape(w.text)
    if w.bold then s = "*" .. s .. "*" end
    text, tw, empty = text .. s, tw + ww, false
  end
  if not empty then out[#out + 1] = text end
end

-- summaryLines lays the catch-up summary so far out w cells wide.
local function summaryLines(text, done, err, w)
  local out = {}
  for line in (text .. "\n"):gmatch("(.-)\n") do
    if line:match("%S") then styled(line, w, out) end
  end
  if err then
    if #out > 0 then out[#out + 1] = "" end
    out[#out + 1] = "Couldn't catch up: " .. wiregram.escape(err)
  elseif not done then
    out[#out + 1] = #out == 0 and "Reading unread messages…" or "…"
  end
  return out
end

-- catchup opens a modal the summary streams into, laid out again only when
-- more of it came or the width changed, since render runs every frame.
local function catchup()
  local task = wiregram.aitasks.catchup()
  local shown = {}
  wiregram.modal { title = "Catch up", width = modalWidth, height = 30,
    render = function(w)
      local text, done, err = task.poll()
      if shown.w ~= w or shown.n ~= #text or shown.done ~= done then
        shown = { w = w, n = #text, done = done,
          lines = summaryLines(text, done, err, w) }
      end
      return shown.lines
    end }
end

local function click()
  if wiregram.aitasks.available() then return catchup() end
  detail = {}
  for _, a in ipairs(wiregram.accounts()) do
    local chats, err = wiregram.unread(a.key, perChat)
    detail[#detail + 1] = { account = a, chats = chats or {}, err = err }
  end
  -- Private chats first, then the newest.
  local sorted = function(x, y)
    if x.dm ~= y.dm then return x.dm end
    return x.last > y.last
  end
  for _, d in ipairs(detail) do table.sort(d.chats, sorted) end
  -- Laying out many messages can outlast render's 50 ms, and render runs every
  -- frame, so the lines are made here at the modal's width and kept until the
  -- width changes, as on a terminal narrower than the modal.
  local shown = { w = modalWidth, lines = lines(modalWidth) }
  wiregram.modal { title = "Unread messages", width = modalWidth, height = 30,
    render = function(w)
      if shown.w ~= w then shown = { w = w, lines = lines(w) } end
      return shown.lines
    end }
end

wiregram.widget { height = 4, refresh = 30, update = update, render = render, click = click }
