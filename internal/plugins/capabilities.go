package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/yuin/gopher-lua"

	"noteroot/internal/platform"
)

// installAPI injects the plugin API into a fresh sandbox. Everything
// a plugin can do goes through these closures — permission checks
// happen here, at call time, against the manifest's grants.
func (p *Plugin) installAPI(L *lua.LState) {
	L.SetGlobal("print", L.NewFunction(p.luaPrint))
	L.SetGlobal("register_command", L.NewFunction(p.luaRegisterCommand))
	L.SetGlobal("on", L.NewFunction(p.luaOn))

	note := L.NewTable()
	note.RawSetString("read", L.NewFunction(p.luaNoteRead))
	note.RawSetString("write", L.NewFunction(p.luaNoteWrite))
	L.SetGlobal("note", note)

	kv := L.NewTable()
	kv.RawSetString("get", L.NewFunction(p.luaKVGet))
	kv.RawSetString("set", L.NewFunction(p.luaKVSet))
	kv.RawSetString("delete", L.NewFunction(p.luaKVDelete))
	L.SetGlobal("kv", kv)

	ui := L.NewTable()
	ui.RawSetString("set_panel", L.NewFunction(p.luaSetPanel))
	L.SetGlobal("ui", ui)

	fm := L.NewTable()
	fm.RawSetString("parse", L.NewFunction(p.luaFMParse))
	fm.RawSetString("build", L.NewFunction(p.luaFMBuild))
	L.SetGlobal("fm", fm)
}

func newSandbox(p *Plugin) (*lua.LState, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, lib := range []struct {
		name string
		f    lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.StringLibName, lua.OpenString},
		{lua.TabLibName, lua.OpenTable},
		{lua.MathLibName, lua.OpenMath},
	} {
		// Same call pattern as lua.OpenLibs: push the opener, push the
		// lib name, call it. Openers cannot fail for built-in libs.
		L.Push(L.NewFunction(lib.f))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	// Strip everything that reaches the host or loads more code: no
	// io/os/package/debug/coroutine were ever opened, and base's own
	// escape hatches go too.
	for _, name := range []string{
		"dofile", "loadfile", "load", "loadstring",
		"getfenv", "setfenv", "require", "module", "newproxy",
	} {
		L.SetGlobal(name, lua.LNil)
	}
	p.installAPI(L)
	return L, nil
}

// capCtx derives a capability's context from the current invocation's
// context (set by invokeLocked while it holds p.mu): a blocking Go
// call therefore inherits the hook/command deadline and dies with the
// shutdown signal, instead of running detached for up to
// capabilityTimeout.
func (p *Plugin) capCtx() (context.Context, context.CancelFunc) {
	base := p.invCtx
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, capabilityTimeout)
}

// require grants permission or raises a Lua error (catchable by the
// plugin, fatal to nothing). Qualifier narrows like API tokens: a
// grant without one matches everything, a grant with one only matches
// that path/key/name.
func (p *Plugin) require(L *lua.LState, resource, action, qualifier string) {
	if !platform.GrantsAllow(p.Grants, resource, action, qualifier) {
		L.RaiseError("permission denied: %s:%s", resource, action)
	}
}

func (p *Plugin) luaPrint(L *lua.LState) int {
	parts := make([]string, 0, L.GetTop())
	for i := 1; i <= L.GetTop(); i++ {
		parts = append(parts, L.Get(i).String())
	}
	p.log.Info("print", "msg", strings.Join(parts, "\t"))
	return 0
}

func (p *Plugin) luaRegisterCommand(L *lua.LState) int {
	name := L.CheckString(1)
	fn := L.CheckFunction(2)
	if !nameRe.MatchString(name) {
		L.RaiseError("bad command name %q: want lower-case ident", name)
		return 0
	}
	p.commands[name] = fn
	return 0
}

func (p *Plugin) luaOn(L *lua.LState) int {
	evType := L.CheckString(1)
	fn := L.CheckFunction(2)
	if evType == "" {
		L.RaiseError("on() needs an event type")
		return 0
	}
	// Clone-and-swap: Enqueue reads hooks from the bus goroutine with
	// no lock, so the stored map is never mutated in place.
	next := map[string][]*lua.LFunction{}
	if cur := p.hooks.Load(); cur != nil {
		for k, v := range *cur {
			next[k] = v
		}
	}
	next[evType] = append(append([]*lua.LFunction{}, next[evType]...), fn)
	p.hooks.Store(&next)
	return 0
}

func (p *Plugin) luaNoteRead(L *lua.LState) int {
	rel := L.CheckString(1)
	if err := checkPath(rel); err != nil {
		L.RaiseError("%s", err.Error())
		return 0
	}
	p.require(L, "note", "read", rel)
	ctx, cancel := p.capCtx()
	defer cancel()
	text, err := p.store.Read(ctx, rel)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			L.Push(lua.LNil)
			L.Push(lua.LString("not found"))
			return 2
		}
		L.RaiseError("note.read: %s", err.Error())
		return 0
	}
	L.Push(noteTable(L, text))
	return 1
}

func (p *Plugin) luaNoteWrite(L *lua.LState) int {
	rel := L.CheckString(1)
	if err := checkPath(rel); err != nil {
		L.RaiseError("%s", err.Error())
		return 0
	}
	var text string
	switch t := L.Get(2).(type) {
	case lua.LString:
		text = string(t)
	case *lua.LTable:
		v, err := luaToValue(L, t)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		text, err = buildNote(v)
		if err != nil {
			L.RaiseError("note.write: %s", err.Error())
			return 0
		}
	default:
		L.RaiseError("note.write(path, text_or_{content,meta})")
		return 0
	}
	p.require(L, "note", "write", rel)
	ctx, cancel := p.capCtx()
	defer cancel()
	origin := pluginOrigin(p.Meta.Name)
	err := p.store.Write(ctx, WriteRequest{
		Actor:   origin,
		Origin:  origin,
		Depth:   p.depth + 1,
		Path:    rel,
		Content: text,
	})
	if err != nil {
		L.RaiseError("note.write: %s", err.Error())
		return 0
	}
	L.Push(lua.LTrue)
	return 1
}

func (p *Plugin) kvNamespace() string { return "plugin:" + p.Meta.Name }

func (p *Plugin) luaKVGet(L *lua.LState) int {
	key := L.CheckString(1)
	p.require(L, "kv", "read", key)
	ctx, cancel := p.capCtx()
	defer cancel()
	v, ok, err := p.db.KVGet(ctx, p.kvNamespace(), key)
	if err != nil {
		L.RaiseError("kv.get: %s", err.Error())
		return 0
	}
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LString(v))
	return 1
}

func (p *Plugin) luaKVSet(L *lua.LState) int {
	key := L.CheckString(1)
	value := L.CheckString(2)
	p.require(L, "kv", "write", key)
	ctx, cancel := p.capCtx()
	defer cancel()
	if err := p.db.KVSet(ctx, p.kvNamespace(), key, value); err != nil {
		L.RaiseError("kv.set: %s", err.Error())
		return 0
	}
	L.Push(lua.LTrue)
	return 1
}

func (p *Plugin) luaKVDelete(L *lua.LState) int {
	key := L.CheckString(1)
	p.require(L, "kv", "write", key)
	ctx, cancel := p.capCtx()
	defer cancel()
	if err := p.db.KVDelete(ctx, p.kvNamespace(), key); err != nil {
		L.RaiseError("kv.delete: %s", err.Error())
		return 0
	}
	L.Push(lua.LTrue)
	return 1
}

// luaSetPanel publishes a UI panel fragment: name -> JSON spec, stored
// in the notebook's platform db under the ui.panels namespace. The
// frontend polls panels; nothing here runs in a browser.
func (p *Plugin) luaSetPanel(L *lua.LState) int {
	name := L.CheckString(1)
	spec := L.CheckTable(2)
	p.require(L, "ui", "write", name)
	v, err := luaToValue(L, spec)
	if err != nil {
		L.RaiseError("ui.set_panel: %s", err.Error())
		return 0
	}
	data, err := json.Marshal(v)
	if err != nil {
		L.RaiseError("ui.set_panel: %s", err.Error())
		return 0
	}
	ctx, cancel := p.capCtx()
	defer cancel()
	if err := p.db.KVSet(ctx, "ui.panels", name, string(data)); err != nil {
		L.RaiseError("ui.set_panel: %s", err.Error())
		return 0
	}
	L.Push(lua.LTrue)
	return 1
}

// fm.parse/fm.build split and assemble frontmatter without touching
// the vault — pure string helpers so plugins can rewrite metadata as
// tables instead of doing YAML surgery by hand.
func (p *Plugin) luaFMParse(L *lua.LState) int {
	text := L.CheckString(1)
	L.Push(fmTable(L, text))
	return 1
}

func (p *Plugin) luaFMBuild(L *lua.LState) int {
	t := L.CheckTable(1)
	v, err := luaToValue(L, t)
	if err != nil {
		L.RaiseError("fm.build: %s", err.Error())
		return 0
	}
	text, err := buildNote(v)
	if err != nil {
		L.RaiseError("fm.build: %s", err.Error())
		return 0
	}
	L.Push(lua.LString(text))
	return 1
}

// riskOf groups grants for the approval surface: read-only is the
// default and lowest risk, admin grants (everything, or anything that
// can exfiltrate/replace whole notebooks) are the highest.
func riskOf(gs []platform.Grant) string {
	risk := "none"
	for _, g := range gs {
		switch {
		case g.Resource == "*" && g.Action == "*":
			return "admin"
		case g.Resource == "secrets" || g.Resource == "backup" || g.Resource == "import":
			return "admin"
		case g.Action == "write":
			if risk != "admin" {
				risk = "write"
			}
		default:
			if risk == "none" {
				risk = "read"
			}
		}
	}
	return risk
}
