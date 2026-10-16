local rules = {
  { prefix = "projects/", tag = "project" },
  { prefix = "journal/",  tag = "journal" },
  { prefix = "meetings/", tag = "meeting" },
  { prefix = "inbox/",    tag = "inbox" },
}

local function tags_for(path)
  local out = {}
  for _, r in ipairs(rules) do
    if string.sub(path, 1, #r.prefix) == r.prefix then
      table.insert(out, r.tag)
    end
  end
  return out
end

local function same(a, b)
  if #a ~= #b then
    return false
  end
  for i = 1, #a do
    if a[i] ~= b[i] then
      return false
    end
  end
  return true
end

on("note.write", function(ev)
  if not ev.path or ev.path == "" then
    return
  end
  local want = tags_for(ev.path)
  if #want == 0 then
    return
  end
  local n = note.read(ev.path)
  if not n then
    return
  end
  local cur = n.meta.tags
  if type(cur) ~= "table" then
    cur = {}
  end
  if same(cur, want) then
    return
  end
  n.meta.tags = want
  note.write(ev.path, { content = n.content, meta = n.meta })
end)

register_command("tags", function(args)
  local path = args and args.path
  if not path then
    error("tags requires {path=...}")
  end
  local n = note.read(path)
  if not n then
    error("not found: " .. path)
  end
  local tags = n.meta.tags
  if type(tags) ~= "table" then
    tags = {}
  end
  return { path = path, tags = tags }
end)
