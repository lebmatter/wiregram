-- Calendar: the next events of a Google Calendar, read from its secret iCal
-- address. A click lists the upcoming events and the recent ones by day.

wiregram.plugin {
  title = "Calendar",
  hosts = { "calendar.google.com" },
}

local cfg = wiregram.settings {
  { key = "url", label = "Secret iCal URL", type = "string", default = "", secret = true },
  { key = "ahead", label = "Upcoming (days)", type = "choice", options = { "7", "14", "30" },
    default = "14" },
  { key = "behind", label = "Recent (days)", type = "choice", options = { "1", "3", "7" },
    default = "3" },
  { key = "refresh", label = "Refresh (minutes)", type = "choice",
    options = { "15", "30", "60" }, default = "15" },
}

local DAY = 86400
local MAX_EVENTS = 300

-- Times are kept as "wall seconds": a local date and time counted as if it
-- were UTC, so calendar arithmetic needs no time zone.

-- days counts the days from 1970-01-01 to a Gregorian date.
local function days(y, m, d)
  if m <= 2 then y = y - 1 end
  local era = math.floor(y / 400)
  local yoe = y - era * 400
  local doy = math.floor((153 * ((m + 9) % 12) + 2) / 5) + d - 1
  return era * 146097 + yoe * 365 + math.floor(yoe / 4) - math.floor(yoe / 100) + doy - 719468
end

-- civil is days' inverse: the year, month and day of day z.
local function civil(z)
  z = z + 719468
  local era = math.floor(z / 146097)
  local doe = z - era * 146097
  local yoe = math.floor((doe - math.floor(doe / 1460) + math.floor(doe / 36524) -
    math.floor(doe / 146096)) / 365)
  local doy = doe - (365 * yoe + math.floor(yoe / 4) - math.floor(yoe / 100))
  local mp = math.floor((5 * doy + 2) / 153)
  local d = doy - math.floor((153 * mp + 2) / 5) + 1
  local m = mp < 10 and mp + 3 or mp - 9
  local y = yoe + era * 400
  if m <= 2 then y = y + 1 end
  return y, m, d
end

local function weekday(z) return (z + 4) % 7 end -- 0 is Sunday

local function monthLen(y, m)
  if m == 12 then return 31 end
  return days(y, m + 1, 1) - days(y, m, 1)
end

local weekdays = { SU = 0, MO = 1, TU = 2, WE = 3, TH = 4, FR = 5, SA = 6 }

-- nthWeekday is the day of the n-th weekday wd in a month, counting from its
-- end when n is negative, or nil when the month has none.
local function nthWeekday(y, m, n, wd)
  local first, len = days(y, m, 1), monthLen(y, m)
  local z
  if n > 0 then
    z = first + (wd - weekday(first)) % 7 + (n - 1) * 7
  else
    local last = first + len - 1
    z = last - (weekday(last) - wd) % 7 + (n + 1) * 7
  end
  if z < first or z >= first + len then return nil end
  return z
end

-- stamp reads an iCal date or date-time as wall seconds, and whether it was in
-- UTC (a trailing Z) and whether it was a bare date.
local function stamp(v)
  local y, m, d = v:match("^(%d%d%d%d)(%d%d)(%d%d)")
  if not y then return nil end
  local t = days(tonumber(y), tonumber(m), tonumber(d)) * DAY
  local hh, mm, ss = v:match("^%d+T(%d%d)(%d%d)(%d%d)")
  if not hh then return t, false, true end
  return t + tonumber(hh) * 3600 + tonumber(mm) * 60 + tonumber(ss), v:sub(-1) == "Z", false
end

local function utcOffset(s)
  local sign, h, m = (s or ""):match("^([+-])(%d%d)(%d%d)")
  if not sign then return 0 end
  local o = tonumber(h) * 3600 + tonumber(m) * 60
  return sign == "-" and -o or o
end

local function duration(s)
  local units = { W = 7 * DAY, D = DAY, H = 3600, M = 60, S = 1 }
  local t = 0
  for n, u in s:gmatch("(%d+)([WDHMS])") do t = t + tonumber(n) * units[u] end
  return t
end

local function numbers(v)
  local list = {}
  for n in v:gmatch("[+-]?%d+") do list[#list + 1] = tonumber((n:gsub("^%+", ""))) end
  return list
end

local function parseRule(s)
  local r = { interval = 1 }
  for k, v in s:gmatch("([^;=]+)=([^;]*)") do
    if k == "FREQ" then r.freq = v
    elseif k == "INTERVAL" then r.interval = math.max(1, tonumber(v) or 1)
    elseif k == "COUNT" then r.count = tonumber(v)
    elseif k == "UNTIL" then r.stop = v
    elseif k == "WKST" then r.wkst = weekdays[v]
    elseif k == "BYMONTH" then r.bymonth = numbers(v)
    elseif k == "BYMONTHDAY" then r.bymonthday = numbers(v)
    elseif k == "BYSETPOS" then r.bysetpos = numbers(v)
    elseif k == "BYDAY" then
      r.byday = {}
      for n, wd in v:gmatch("([+-]?%d*)(%u%u)") do
        if weekdays[wd] then
          r.byday[#r.byday + 1] = { n = tonumber((n:gsub("^%+", ""))), wd = weekdays[wd] }
        end
      end
    end
  end
  return r
end

-- A zone is the list of a VTIMEZONE's observances: {start=, from=, to=, rule=}.
local zones, calZone = {}, nil

-- onset is when an observance begins in year y, in wall seconds.
local function onset(o, y)
  if not o.rule then return o.start end
  local sy, sm, sd = civil(math.floor(o.start / DAY))
  if y < sy then return nil end
  local r = o.rule
  local m = r.bymonth and r.bymonth[1] or sm
  local b = r.byday and r.byday[1]
  local z
  if b and b.n then
    z = nthWeekday(y, m, b.n, b.wd)
  else
    z = days(y, m, math.min(sd, monthLen(y, m)))
  end
  if not z then return nil end
  local at = z * DAY + o.start % DAY
  local stop, utc = stamp(r.stop or "")
  if stop and (utc and at - o.from or at) > stop then return nil end
  return at
end

-- offsetAt is a zone's UTC offset at a wall time: that of the observance that
-- began last.
local function offsetAt(zone, wall)
  if not zone or #zone == 0 then return 0 end
  local y = civil(math.floor(wall / DAY))
  local best, bestAt
  for _, o in ipairs(zone) do
    for _, yy in ipairs({ y, y - 1 }) do
      local at = onset(o, yy)
      if at and at <= wall and (not bestAt or at > bestAt) then best, bestAt = o, at end
    end
  end
  if not best then return zone[1].from end
  return best.to
end

local function toUTC(wall, tzid)
  return wall - offsetAt(zones[tzid] or calZone, wall)
end

local function toLocal(utc)
  return utc + offsetAt(calZone, utc + offsetAt(calZone, utc))
end

-- A time is {wall=, utc=, allday=, tz=}.
local function utcOf(t)
  if t.utc then return t.wall end
  return toUTC(t.wall, t.tz)
end

local function unescape(s)
  return (s:gsub("\\(.)", function(c)
    if c == "n" or c == "N" then return " " end
    return c
  end))
end

-- parse reads the calendar's time zones into zones and calZone, and returns its events.
local function parse(text)
  -- Lines are unfolded one by one: gopher-lua's gsub slows to seconds on a
  -- large calendar's many line breaks.
  local unfolded = {}
  for l in text:gmatch("[^\r\n]+") do
    local c = l:sub(1, 1)
    if (c == " " or c == "\t") and #unfolded > 0 then
      unfolded[#unfolded] = unfolded[#unfolded] .. l:sub(2)
    else
      unfolded[#unfolded + 1] = l
    end
  end
  zones, calZone = {}, nil
  local calTZ
  local events = {}
  local ev, zone, obs, alarm
  for _, line in ipairs(unfolded) do
    local head, value = line:match("^([^:]*):(.*)$")
    if head then
      local name = (head:match("^[^;]+") or ""):upper()
      local tzid = head:match(";TZID=\"?([^;\"]+)")
      local function at(v)
        local wall, utc, allday = stamp(v)
        if wall then return { wall = wall, utc = utc, allday = allday, tz = tzid } end
      end
      if name == "BEGIN" then
        if value == "VEVENT" then ev = { exdates = {} }
        elseif value == "VALARM" then alarm = true
        elseif value == "VTIMEZONE" then zone = { list = {} }
        elseif zone and (value == "STANDARD" or value == "DAYLIGHT") then obs = {}
        end
      elseif name == "END" then
        if value == "VEVENT" and ev then
          if ev.start then events[#events + 1] = ev end
          ev = nil
        elseif value == "VALARM" then alarm = false
        elseif value == "VTIMEZONE" and zone then
          if zone.id then zones[zone.id] = zone.list end
          zone = nil
        elseif obs and zone then
          if obs.start then zone.list[#zone.list + 1] = obs end
          obs = nil
        end
      elseif obs then
        if name == "TZOFFSETFROM" then obs.from = utcOffset(value)
        elseif name == "TZOFFSETTO" then obs.to = utcOffset(value)
        elseif name == "DTSTART" then obs.start = stamp(value)
        elseif name == "RRULE" then obs.rule = parseRule(value)
        end
      elseif zone then
        if name == "TZID" then zone.id = value end
      elseif ev and not alarm then
        if name == "SUMMARY" then ev.title = unescape(value)
        elseif name == "LOCATION" then ev.where = unescape(value)
        elseif name == "UID" then ev.uid = value
        elseif name == "STATUS" then ev.status = value
        elseif name == "RRULE" then ev.rule = parseRule(value)
        elseif name == "DTSTART" then ev.start = at(value)
        elseif name == "DTEND" then ev.finish = at(value)
        elseif name == "DURATION" then ev.length = duration(value)
        elseif name == "RECURRENCE-ID" then ev.recurrence = at(value)
        elseif name == "EXDATE" then
          for v in value:gmatch("[^,]+") do
            local t = at(v)
            if t then ev.exdates[#ev.exdates + 1] = t end
          end
        end
      elseif name == "X-WR-TIMEZONE" then
        calTZ = value
      end
    end
  end
  for _, z in pairs(zones) do
    for _, o in ipairs(z) do o.from, o.to = o.from or 0, o.to or 0 end
  end
  calZone = zones[calTZ]
  return events
end

-- monthDays lists the days of a month a monthly or yearly rule picks.
local function monthDays(y, m, r, d0)
  local out = {}
  local first, len = days(y, m, 1), monthLen(y, m)
  if r.bymonthday then
    for _, d in ipairs(r.bymonthday) do
      if d < 0 then d = len + d + 1 end
      if d >= 1 and d <= len then out[#out + 1] = first + d - 1 end
    end
  elseif r.byday then
    for _, b in ipairs(r.byday) do
      if b.n then
        out[#out + 1] = nthWeekday(y, m, b.n, b.wd)
      else
        for z = first + (b.wd - weekday(first)) % 7, first + len - 1, 7 do out[#out + 1] = z end
      end
    end
  elseif d0 <= len then
    out[1] = first + d0 - 1
  end
  return out
end

-- expand calls emit with the wall start of each occurrence of a recurring
-- event, in order, up to the wall time limit. from lets rules without a count
-- skip the periods that end long before the window.
local function expand(ev, r, from, limit, emit)
  local start = ev.start.wall
  local tod = start % DAY
  local z0 = math.floor(start / DAY)
  local y0, m0, d0 = civil(z0)
  local wkst = r.wkst or 1
  local stop, stopUTC, stopDate = stamp(r.stop or "")
  local function past(wall)
    if not stop then return false end
    if stopUTC then return toUTC(wall, ev.start.tz) > stop end
    if stopDate then return wall >= stop + DAY end
    return wall > stop
  end

  local function period(k)
    local out = {}
    if r.freq == "DAILY" then
      local z = z0 + k * r.interval
      local keep = not r.byday
      for _, b in ipairs(r.byday or {}) do
        if b.wd == weekday(z) then keep = true end
      end
      if keep then out[1] = z end
    elseif r.freq == "WEEKLY" then
      local ws = z0 - (weekday(z0) - wkst) % 7 + k * 7 * r.interval
      for _, b in ipairs(r.byday or { { wd = weekday(z0) } }) do
        out[#out + 1] = ws + (b.wd - wkst) % 7
      end
    elseif r.freq == "MONTHLY" then
      local total = m0 - 1 + k * r.interval
      out = monthDays(y0 + math.floor(total / 12), total % 12 + 1, r, d0)
    elseif r.freq == "YEARLY" then
      for _, m in ipairs(r.bymonth or { m0 }) do
        for _, z in ipairs(monthDays(y0 + k * r.interval, m, r, d0)) do out[#out + 1] = z end
      end
    end
    table.sort(out)
    if r.bysetpos then
      local picked = {}
      for _, p in ipairs(r.bysetpos) do
        local z = out[p > 0 and p or #out + p + 1]
        if z then picked[#picked + 1] = z end
      end
      table.sort(picked)
      out = picked
    end
    return out
  end

  local k, n = 0, 0
  local span = ({ DAILY = DAY, WEEKLY = 7 * DAY })[r.freq]
  if span and not r.count and from > start then
    k = math.max(0, math.floor((from - start) / (span * r.interval)) - 1)
  end
  for _ = 1, 20000 do
    for _, z in ipairs(period(k)) do
      local wall = z * DAY + tod
      if wall >= start then
        if wall > limit or past(wall) then return end
        n = n + 1
        if r.count and n > r.count then return end
        emit(wall)
      end
    end
    k = k + 1
  end
end

local function clean(s, n)
  s = (s or ""):gsub("[|\n]", " ")
  if #s > n then s = s:sub(1, n) end
  return s
end

-- collect lists the occurrences that overlap [from, to) in UTC, sorted by start:
-- {s=, e=, ls=, le=, allday=, title=, where=}, ls and le in the calendar's wall time.
local function collect(events, from, to)
  local moved = {}
  for _, ev in ipairs(events) do
    if ev.recurrence and ev.uid then moved[ev.uid .. "|" .. utcOf(ev.recurrence)] = true end
  end
  local out = {}
  for _, ev in ipairs(events) do
    local s0 = utcOf(ev.start)
    local length = ev.length or (ev.start.allday and DAY or 0)
    if ev.finish then length = utcOf(ev.finish) - s0 end
    local skip = {}
    for _, t in ipairs(ev.exdates) do skip[utcOf(t)] = true end
    local function add(s)
      local e = s + length
      if ev.status == "CANCELLED" or skip[s] or e <= from or s >= to then return end
      if ev.rule and ev.uid and moved[ev.uid .. "|" .. s] then return end
      local ls = toLocal(s)
      out[#out + 1] = { s = s, e = e, ls = ls, le = toLocal(e), allday = ev.start.allday,
        title = clean(ev.title, 120), where = clean(ev.where, 80) }
    end
    if ev.rule and not ev.recurrence then
      local wallFrom = ev.start.wall + (from - s0) - 2 * DAY
      local limit = ev.start.wall + (to - s0) + 2 * DAY
      expand(ev, ev.rule, wallFrom, limit, function(wall) add(toUTC(wall, ev.start.tz)) end)
    else
      add(s0)
    end
  end
  table.sort(out, function(a, b)
    if a.s ~= b.s then return a.s < b.s end
    return a.title < b.title
  end)
  return out
end

-- nearest keeps the MAX_EVENTS events that start nearest now, a run of the sorted list.
local function nearest(found, now)
  if #found <= MAX_EVENTS then return found end
  local lo = 1
  while lo <= #found and found[lo].s < now do lo = lo + 1 end
  local hi = lo - 1
  while hi - lo + 1 < MAX_EVENTS do
    local before, after = found[lo - 1], found[hi + 1]
    if after and (not before or after.s - now <= now - before.s) then
      hi = hi + 1
    else
      lo = lo - 1
    end
  end
  local out = {}
  for i = lo, hi do out[#out + 1] = found[i] end
  return out
end

-- update and render run in the same VM, so render reads what update left here.
local list, offset, fetchedAt, fetchedFor

local function update()
  local url = cfg.url
  if url == "" then
    list = nil
    return
  end
  if not url:match("^https://calendar%.google%.com/") then
    error("the iCal URL must start with https://calendar.google.com/", 0)
  end
  local wanted = url .. "|" .. cfg.ahead .. "|" .. cfg.behind
  local now = wiregram.now()
  if fetchedFor == wanted and now - fetchedAt < tonumber(cfg.refresh) * 60 then return end

  local body, err = wiregram.http.get(url)
  if not body then error(err, 0) end
  if not body:find("BEGIN:VCALENDAR", 1, true) then
    error("that URL did not return a calendar", 0)
  end
  local events = parse(body)
  local found = collect(events, now - tonumber(cfg.behind) * DAY, now + tonumber(cfg.ahead) * DAY)
  list, offset = nearest(found, now), toLocal(now) - now
  fetchedAt, fetchedFor = now, wanted
end

local dayNames = { "Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat" }
local monthNames = { "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct",
  "Nov", "Dec" }

local function clock(wall)
  local t = wall % DAY
  return string.format("%02d:%02d", math.floor(t / 3600), math.floor(t / 60) % 60)
end

local function dayLabel(z)
  local _, m, d = civil(z)
  return dayNames[weekday(z) + 1] .. " " .. d .. " " .. monthNames[m]
end

-- pad counts characters, not bytes, so – and → take one cell each.
local function pad(s, n)
  local _, chars = s:gsub("[^\128-\191]", "")
  return s .. string.rep(" ", n - chars)
end

local function what(ev)
  local s = ev.title ~= "" and ev.title or "(no title)"
  if ev.allday and ev.e - ev.s > DAY then
    s = s .. " (" .. math.floor((ev.e - ev.s) / DAY + 0.5) .. " days)"
  end
  return s
end

local function render(width, frame)
  if cfg.url == "" then
    return { "", "  Set your Google Calendar's secret iCal", "  address in Settings → Plugins." }
  end
  if not list then return { "", "  Fetching your calendar…" } end
  local now = wiregram.now()
  local today = math.floor((now + offset) / DAY)
  local lines, border, shown = {}, nil, 0
  for _, ev in ipairs(list) do
    if ev.e > now then
      shown = shown + 1
      if shown <= 3 then
        local z = math.floor(ev.ls / DAY)
        local day, time
        if ev.s <= now then
          day, time = ev.allday and "Today" or "Now", ev.allday and "" or "→" .. clock(ev.le)
          if not ev.allday then border = "green" end
        else
          day = z == today and "Today" or z == today + 1 and "Tmrw" or
            dayNames[weekday(z) + 1] .. " " .. select(3, civil(z))
          time = ev.allday and "" or clock(ev.ls)
          if not ev.allday and not border and ev.s - now <= 15 * 60 then border = "yellow" end
        end
        lines[#lines + 1] = "  " .. pad(day, 7) .. pad(time, 7) .. what(ev)
      end
    end
  end
  if shown == 0 then
    lines = { "", "  Nothing in the next " .. cfg.ahead .. " days." }
  end
  while #lines < 3 do lines[#lines + 1] = "" end
  local mins = math.floor((now - fetchedAt) / 60)
  local ago = mins < 1 and "just now" or (mins .. " min ago")
  local more = shown > 3 and (shown - 3) .. " more · " or ""
  lines[4] = "  " .. more .. ago .. " · click for all"
  return lines, border
end

-- section draws events grouped under a header for each day.
local function section(lines, events, now)
  local lastDay
  for _, ev in ipairs(events) do
    local z = math.floor(ev.ls / DAY)
    if z ~= lastDay then
      lines[#lines + 1] = "  " .. dayLabel(z)
      lastDay = z
    end
    local mark = (ev.s <= now and ev.e > now) and "› " or "  "
    local time = ev.allday and "all day" or clock(ev.ls) .. "–" .. clock(ev.le)
    local line = "  " .. mark .. pad(time, 13) .. what(ev)
    if ev.where ~= "" then line = line .. " · " .. ev.where end
    lines[#lines + 1] = line
  end
end

local function agenda(width, height)
  if not list then return { "", "  Fetching your calendar…" } end
  local now = wiregram.now()
  local upcoming, recent = {}, {}
  for _, ev in ipairs(list) do
    if ev.e > now then upcoming[#upcoming + 1] = ev else table.insert(recent, 1, ev) end
  end
  local lines = { "── Upcoming · next " .. cfg.ahead .. " days" }
  if #upcoming == 0 then lines[#lines + 1] = "  Nothing coming up." end
  section(lines, upcoming, now)
  lines[#lines + 1] = ""
  lines[#lines + 1] = "── Recent · last " .. cfg.behind .. " days"
  if #recent == 0 then lines[#lines + 1] = "  Nothing recent." end
  section(lines, recent, now)
  return lines
end

local function click()
  if cfg.url == "" then error("set the secret iCal URL in Settings → Plugins", 0) end
  wiregram.modal { title = "Calendar", width = 72, height = 22, render = agenda }
end

wiregram.widget { height = 4, refresh = 60, update = update, render = render, click = click }
