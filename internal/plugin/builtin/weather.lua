-- Weather: the current conditions for one city, from Open-Meteo, with a
-- small animation of the sky.

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

local fields = { "place", "temp", "code", "is_day", "wind", "high", "low" }

local function update()
  local city = cfg.city
  if city == "" then error("set a city in Settings", 0) end
  local wanted = city .. "|" .. cfg.units
  local age = wiregram.now() - (tonumber(kv.get("fetched_at")) or 0)
  if kv.get("for") == wanted and age < tonumber(cfg.refresh) * 60 then return end

  local name, lat, lon = place(city)
  local url = "https://api.open-meteo.com/v1/forecast?latitude=" .. lat .. "&longitude=" .. lon ..
    "&current=temperature_2m,weather_code,is_day,wind_speed_10m" ..
    "&daily=temperature_2m_max,temperature_2m_min&forecast_days=1&timezone=auto"
  if cfg.units == "fahrenheit" then
    url = url .. "&temperature_unit=fahrenheit&wind_speed_unit=mph"
  end
  local data = fetch(url)
  local now, daily = data.current, data.daily
  if not now or not daily then error("Open-Meteo sent no forecast", 0) end
  local values = {
    place = name, temp = now.temperature_2m, code = now.weather_code, is_day = now.is_day,
    wind = now.wind_speed_10m, high = daily.temperature_2m_max[1],
    low = daily.temperature_2m_min[1],
  }
  for _, f in ipairs(fields) do kv.set(f, tostring(values[f])) end
  kv.set("for", wanted)
  kv.set("fetched_at", tostring(wiregram.now()))
  shown = nil
end

local function round(n) return string.format("%d", math.floor((tonumber(n) or 0) + 0.5)) end

local function pad(s, n) return s .. string.rep(" ", n - #s) end

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
    ago,
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

wiregram.widget { height = 4, refresh = 60, update = update, render = render }
