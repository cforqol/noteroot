local valid = { auto = true, light = true, dark = true }

local function current()
  return kv.get("preference") or "auto"
end

local function apply(name)
  ui.set_panel("theme", { type = "theme", name = name })
end

-- Restore the stored preference at load so a reload keeps the theme.
apply(current())

register_command("set-theme", function(args)
  local name = args and args.theme
  if not name or not valid[name] then
    error("theme must be auto, light, or dark")
  end
  kv.set("preference", name)
  apply(name)
  return { theme = name }
end)

register_command("get-theme", function()
  return { theme = current() }
end)
