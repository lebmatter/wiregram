-- Unread: the unread private chats of every online account. A click opens
-- their newest messages.

wiregram.plugin {
  title = "Unread",
  permissions = { "messages" },
  on = true,
}

local perChat = 3 -- messages shown per chat in the modal

-- counts holds, per account, {account=, dms=, unread=, err=}, from the last
-- update; it is kept in memory only, never in the kv store.
local counts

local function update()
  local out = {}
  for _, a in ipairs(wiregram.accounts()) do
    local chats, err = wiregram.unread(a.key, 0)
    local row = { account = a, dms = 0, unread = 0, err = err }
    for _, c in ipairs(chats or {}) do
      row.dms, row.unread = row.dms + 1, row.unread + c.count
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

-- spread puts left and right at the two ends of a line n cells wide.
local function spread(left, right, n)
  left = cut(left, math.max(1, n - width(right) - 2))
  return left .. string.rep(" ", math.max(2, n - width(left) - width(right))) .. right
end

local function plural(n, one, many) return n .. " " .. (n == 1 and one or many) end

local function render(w)
  if not counts then return { "Counting unread messages…" } end
  if #counts == 0 then return { "No accounts online" } end
  local lines, any = {}, false
  for _, row in ipairs(counts) do
    local name = row.account.tag .. " " .. row.account.label
    local right = "nothing unread"
    if row.err then
      right = "couldn't load"
    elseif row.unread > 0 then
      any = true
      right = plural(row.dms, "DM", "DMs") .. " · " .. row.unread .. " unread"
    end
    lines[#lines + 1] = spread(name, right, w)
  end
  return lines, any and "accent" or nil
end

-- wrap breaks text into lines of at most n characters, at spaces when it can.
local function wrap(text, n)
  local out = {}
  for para in (text .. "\n"):gmatch("(.-)\n") do
    local line = ""
    for word in para:gmatch("%S+") do
      if line == "" then
        line = word
      elseif width(line) + 1 + width(word) <= n then
        line = line .. " " .. word
      else
        out[#out + 1] = line
        line = word
      end
      while width(line) > n do
        local head, rest = take(line, n)
        out[#out + 1] = head
        line = rest
      end
    end
    out[#out + 1] = line
  end
  return out
end

-- detail is what the modal shows: per account, its chats and their messages.
local detail

local function click()
  detail = {}
  for _, a in ipairs(wiregram.accounts()) do
    local chats, err = wiregram.unread(a.key, perChat)
    detail[#detail + 1] = { account = a, chats = chats or {}, err = err }
  end
  local sorted = function(x, y) return x.last > y.last end
  for _, d in ipairs(detail) do table.sort(d.chats, sorted) end
  wiregram.modal { title = "Unread messages", width = 80, height = 30, render = function(w)
    local lines = {}
    for i, d in ipairs(detail) do
      if i > 1 then lines[#lines + 1] = "" end
      lines[#lines + 1] = d.account.tag .. " " .. d.account.label
      if d.err then
        lines[#lines + 1] = "  couldn't load: " .. d.err
      elseif #d.chats == 0 then
        lines[#lines + 1] = "  nothing unread"
      end
      for _, c in ipairs(d.chats) do
        lines[#lines + 1] = "  " .. spread(c.title, c.count .. " unread", w - 2)
        for _, m in ipairs(c.messages) do
          local prefix = "    [" .. m.clock .. "] "
          local body = wrap(m.text, math.max(10, w - width(prefix)))
          for j, l in ipairs(body) do
            lines[#lines + 1] = (j == 1 and prefix or string.rep(" ", width(prefix))) .. l
          end
        end
        if c.count > #c.messages and #c.messages > 0 then
          lines[#lines + 1] = "    … " .. (c.count - #c.messages) .. " more"
        end
      end
    end
    return lines
  end }
end

wiregram.widget { height = 4, refresh = 30, update = update, render = render, click = click }
