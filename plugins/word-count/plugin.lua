local function words(text)
  local n = 0
  for _ in string.gmatch(text or "", "%S+") do
    n = n + 1
  end
  return n
end

local function publish(path, content)
  ui.set_panel("word-count", {
    text = words(content) .. " words",
    level = "info",
    path = path,
  })
end

on("note.write", function(ev)
  if not ev.path or ev.path == "" then
    return
  end
  local n = note.read(ev.path)
  if n then
    publish(ev.path, n.content)
  end
end)

register_command("count", function(args)
  local path = args and args.path
  if not path then
    error("count requires {path=...}")
  end
  local n = note.read(path)
  if not n then
    error("not found: " .. path)
  end
  publish(path, n.content)
  return { path = path, words = words(n.content) }
end)
