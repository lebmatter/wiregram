-- Weather: the current conditions for one city, from Open-Meteo, with a
-- small animation of the sky. A click opens today's forecast hour by hour.

wiregram.plugin {
  title = "Weather",
  hosts = { "geocoding-api.open-meteo.com", "api.open-meteo.com" },
  on = true,
}

local cfg = wiregram.settings {
  { key = "city", label = "City", type = "string", default = "Bengaluru" },
  { key = "units", label = "Units", type = "choice", options = { "celsius", "fahrenheit" },
    default = "celsius" },
  { key = "refresh", label = "Refresh (minutes)", type = "choice",
    options = { "15", "30", "60" }, default = "30" },
}

local kv = wiregram.kv

-- Each sky is a list of frames of four lines; frames alternate every half second.
local skies = {
  sun = {
    { [[   \  |  /]], [[    .---.]], [[ --(     )--]], [[    `---']] },
    { [[    \ | /]], [[ \  .---.  /]], [[  -(     )-]], [[ /  `---'  \]] },
  },
  moon = {
    { [[    .--.   *]], [[   /  _.']], [[  |  (    +]], [[   \  `-.]] },
    { [[    .--.   +]], [[   /  _.'  *]], [[  |  (]], [[   \  `-. *]] },
  },
  partly = {
    { [[  \  /]], [[_ /"".-.]], [[  \_(   ).]], [[  /(___(__)]] },
    { [[  \  / ]], [[_ /"" .-.]], [[  \_ (   ).]], [[  / (___(__)]] },
  },
  cloudy = {
    { [[     .--.]], [[  .-(    ).]], [[ (___.__)__)]], [[]] },
    { [[      .--.]], [[   .-(    ).]], [[  (___.__)__)]], [[]] },
  },
  fog = {
    { [[ _ - _ - _ -]], [[  _ - _ - _]], [[ _ - _ - _ -]], [[  _ - _ - _]] },
    { [[  _ - _ - _]], [[ _ - _ - _ -]], [[  _ - _ - _]], [[ _ - _ - _ -]] },
  },
  rain = {
    { [[    .-.--.]], [[   (___(__)]], [[   ' ' ' ']], [[  ' ' ' ']] },
    { [[    .-.--.]], [[   (___(__)]], [[  ' ' ' ']], [[   ' ' ' ']] },
  },
  snow = {
    { [[    .-.--.]], [[   (___(__)]], [[   *  *  *]], [[  .  .  .]] },
    { [[    .-.--.]], [[   (___(__)]], [[   .  .  .]], [[  *  *  *]] },
  },
  thunder = {
    { [[    .-.--.]], [[   (___(__)]], [[   ' /_ ']], [[  '  /  ']] },
    { [[    .-.--.]], [[   (___(__)]], [[  ' ' ' ']], [[   ' ' ' ']] },
  },
}

-- WMO weather codes, as Open-Meteo reports them.
local conditions = {
  [0] = { "Clear", "sun" }, [1] = { "Mainly clear", "sun" },
  [2] = { "Partly cloudy", "partly" }, [3] = { "Overcast", "cloudy" },
  [45] = { "Fog", "fog" }, [48] = { "Rime fog", "fog" },
  [51] = { "Light drizzle", "rain" }, [53] = { "Drizzle", "rain" },
  [55] = { "Heavy drizzle", "rain" }, [56] = { "Freezing drizzle", "rain" },
  [57] = { "Freezing drizzle", "rain" }, [61] = { "Light rain", "rain" },
  [63] = { "Rain", "rain" }, [65] = { "Heavy rain", "rain" },
  [66] = { "Freezing rain", "rain" }, [67] = { "Freezing rain", "rain" },
  [71] = { "Light snow", "snow" }, [73] = { "Snow", "snow" },
  [75] = { "Heavy snow", "snow" }, [77] = { "Snow grains", "snow" },
  [80] = { "Showers", "rain" }, [81] = { "Showers", "rain" },
  [82] = { "Heavy showers", "rain" }, [85] = { "Snow showers", "snow" },
  [86] = { "Snow showers", "snow" }, [95] = { "Thunderstorm", "thunder" },
  [96] = { "Thunderstorm, hail", "thunder" }, [99] = { "Thunderstorm, hail", "thunder" },
}

local function escape(s)
  return (s:gsub("[^%w%-%._~]", function(c) return string.format("%%%02X", c:byte()) end))
end

local function fetch(url)
  local body, err = wiregram.http.get(url)
  if not body then error(err, 0) end
  local data, jerr = wiregram.json.decode(body)
  if not data then error(jerr, 0) end
  return data
end

-- place returns the name, latitude and longitude of the city, looked up once.
local function place(city)
  local key = "geo:" .. city:lower()
  local cached = kv.get(key)
  if cached then
    local name, lat, lon = cached:match("^(.*)|(.-)|(.-)$")
    if name then return name, lat, lon end
  end
  local data = fetch("https://geocoding-api.open-meteo.com/v1/search?count=1&name=" .. escape(city))
  local hit = data.results and data.results[1]
  if not hit then error("no place called " .. city, 0) end
  local lat, lon = string.format("%.4f", hit.latitude), string.format("%.4f", hit.longitude)
  kv.set(key, hit.name .. "|" .. lat .. "|" .. lon)
  return hit.name, lat, lon
end

-- render runs four times a second, so it reads the store only after update
-- wrote to it; both run in this plugin's VM, so they share shown.
local shown

local fields = { "place", "temp", "code", "is_day", "wind", "high", "low", "hourly", "offset" }

local function update()
  local city = cfg.city
  if city == "" then error("set a city in Settings", 0) end
  local wanted = city .. "|" .. cfg.units
  local age = wiregram.now() - (tonumber(kv.get("fetched_at")) or 0)
  if kv.get("for") == wanted and kv.get("hourly") and age < tonumber(cfg.refresh) * 60 then
    return
  end

  local name, lat, lon = place(city)
  local url = "https://api.open-meteo.com/v1/forecast?latitude=" .. lat .. "&longitude=" .. lon ..
    "&current=temperature_2m,weather_code,is_day,wind_speed_10m" ..
    "&hourly=temperature_2m,weather_code,precipitation_probability,wind_speed_10m" ..
    "&daily=temperature_2m_max,temperature_2m_min&forecast_days=1&timezone=auto"
  if cfg.units == "fahrenheit" then
    url = url .. "&temperature_unit=fahrenheit&wind_speed_unit=mph"
  end
  local data = fetch(url)
  local now, daily, hourly = data.current, data.daily, data.hourly
  if not now or not daily or not hourly then error("Open-Meteo sent no forecast", 0) end
  -- One "temp,code,rain,wind" entry per hour from midnight, joined by ";".
  local hours = {}
  for i = 1, #hourly.temperature_2m do
    hours[i] = table.concat({ tostring(hourly.temperature_2m[i]), tostring(hourly.weather_code[i]),
      tostring(hourly.precipitation_probability[i]), tostring(hourly.wind_speed_10m[i]) }, ",")
  end
  local values = {
    place = name, temp = now.temperature_2m, code = now.weather_code, is_day = now.is_day,
    wind = now.wind_speed_10m, high = daily.temperature_2m_max[1],
    low = daily.temperature_2m_min[1], hourly = table.concat(hours, ";"),
    offset = data.utc_offset_seconds or 0,
  }
  for _, f in ipairs(fields) do kv.set(f, tostring(values[f])) end
  kv.set("for", wanted)
  kv.set("fetched_at", tostring(wiregram.now()))
  shown = nil
end

local function round(n) return string.format("%d", math.floor((tonumber(n) or 0) + 0.5)) end

-- pad counts characters, not bytes, so ° and ▇ take one cell each.
local function pad(s, n)
  local _, chars = s:gsub("[^\128-\191]", "")
  return s .. string.rep(" ", n - chars)
end

local function current()
  if not shown then
    shown = {}
    for _, f in ipairs(fields) do shown[f] = kv.get(f) end
    shown["for"], shown.fetched_at = kv.get("for"), kv.get("fetched_at")
  end
  return shown
end

local function render(width, frame)
  local w = current()
  if not w.temp then
    return { "", "  Fetching the weather for " .. cfg.city .. "…" }
  end
  local code = tonumber(w.code) or 0
  local cond = conditions[code] or { "Unknown", "cloudy" }
  local sky = cond[2]
  if sky == "sun" and w.is_day == "0" then sky = "moon" end
  local frames = skies[sky]
  local art = frames[math.floor(frame / 2) % #frames + 1]

  local fahrenheit = w["for"] and w["for"]:match("|fahrenheit$")
  local unit, speed = "°C", "km/h"
  if fahrenheit then unit, speed = "°F", "mph" end
  local mins = math.floor((wiregram.now() - (tonumber(w.fetched_at) or 0)) / 60)
  local ago = mins < 1 and "just now" or (mins .. " min ago")
  local text = {
    w.place,
    round(w.temp) .. unit .. "  " .. cond[1],
    "H " .. round(w.high) .. "°  L " .. round(w.low) .. "°  wind " .. round(w.wind) .. " " .. speed,
    ago .. " · click for hourly",
  }
  local lines = {}
  for i = 1, 4 do
    if width >= 34 then
      lines[i] = pad(art[i], 14) .. text[i]
    else
      lines[i] = text[i]
    end
  end
  return lines
end

local function hours(w)
  local out = {}
  for entry in (w.hourly or ""):gmatch("[^;]+") do
    local temp, code, rain, wind = entry:match("^(.-),(.-),(.-),(.-)$")
    if temp then
      out[#out + 1] = { temp = tonumber(temp) or 0, code = tonumber(code), rain = tonumber(rain),
        wind = tonumber(wind) }
    end
  end
  return out
end

-- forecast draws today hour by hour: a temperature bar, the sky, the chance of
-- rain and the wind, the current hour marked, scrolled to keep it in view.
local function forecast(width, height)
  local w = current()
  local list = hours(w)
  if #list == 0 then return { "", "  Fetching the forecast for " .. cfg.city .. "…" } end
  local fahrenheit = w["for"] and w["for"]:match("|fahrenheit$")
  local speed = fahrenheit and "mph" or "km/h"
  local lo, hi = math.huge, -math.huge
  for _, h in ipairs(list) do
    lo, hi = math.min(lo, h.temp), math.max(hi, h.temp)
  end
  local now = math.floor(((wiregram.now() + (tonumber(w.offset) or 0)) % 86400) / 3600)
  local showSky = width >= 56
  local fixed = 2 + 5 + 2 + 4 + 2 + 2 + 4 + 3 + 7 + (showSky and 18 or 0)
  local barW = math.max(3, math.min(16, width - fixed))

  local lines = {
    pad("  hour   temp", 17 + barW) .. (showSky and pad("sky", 18) or "") .. "rain   wind",
  }
  local rows = height - #lines
  local first = math.max(0, math.min(now - 1, #list - rows))
  for i = first + 1, math.min(#list, first + rows) do
    local h = list[i]
    local cond = conditions[h.code or -1] or { "Unknown" }
    local fill = hi > lo and math.floor((h.temp - lo) / (hi - lo) * (barW - 1) + 1.5) or barW
    local mark = (i - 1 == now) and "› " or "  "
    local line = mark .. string.format("%02d:00", i - 1) .. "  " .. pad(round(h.temp) .. "°", 4) ..
      "  " .. pad(string.rep("▇", fill), barW) .. "  "
    if showSky then line = line .. pad(cond[1], 18) end
    line = line .. pad(round(h.rain) .. "%", 4) .. "   " .. round(h.wind) .. " " .. speed
    lines[#lines + 1] = line
  end
  return lines
end

local function click()
  wiregram.modal { title = "Today in " .. cfg.city, width = 70, height = 25,
    render = forecast }
end

wiregram.widget { height = 4, refresh = 60, update = update, render = render, click = click }
