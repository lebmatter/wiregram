-- Hacker News: the top stories on the front page, from the Algolia HN API,
-- which serves the whole front page in one request, highest points first.

wiregram.plugin {
  title = "Hacker News",
  hosts = { "hn.algolia.com" },
}

local kv = wiregram.kv
local count = 5
local every = 600

-- render runs four times a second, so it reads the store only after update
-- wrote to it; both run in this plugin's VM, so they share shown.
local shown

local function update()
  local age = wiregram.now() - (tonumber(kv.get("fetched_at")) or 0)
  if kv.get("1") and age < every then return end

  local body, err = wiregram.http.get(
    "https://hn.algolia.com/api/v1/search?tags=front_page&hitsPerPage=" .. count)
  if not body then error(err, 0) end
  local data, jerr = wiregram.json.decode(body)
  if not data then error(jerr, 0) end
  if not data.hits or #data.hits == 0 then error("Hacker News sent no stories", 0) end

  for i = 1, count do
    local hit = data.hits[i]
    if hit then
      local points = tostring(tonumber(hit.points) or 0)
      kv.set(tostring(i), string.rep(" ", 4 - #points) .. "*" .. points .. "*  " ..
        wiregram.escape(hit.title or ""))
    else
      kv.delete(tostring(i))
    end
  end
  kv.set("fetched_at", tostring(wiregram.now()))
  shown = nil
end

local function render()
  if not shown then
    shown = {}
    for i = 1, count do shown[#shown + 1] = kv.get(tostring(i)) end
  end
  if #shown == 0 then return { "", "  Fetching the front page…" } end
  return shown
end

wiregram.widget { height = count, refresh = every, update = update, render = render }
