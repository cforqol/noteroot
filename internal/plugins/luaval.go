package plugins

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/yuin/gopher-lua"
	"gopkg.in/yaml.v3"

	"noteroot/internal/frontmatter"
	"noteroot/internal/platform"
)

// valueToLua converts JSON/yaml-shaped data (map, slice, string,
// bool, numbers, nil) into a Lua value. Unknown types are stringified
// so data never fails to arrive.
func valueToLua(L *lua.LState, v any) lua.LValue {
	switch t := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(t)
	case bool:
		return lua.LBool(t)
	case float64:
		return lua.LNumber(t)
	case float32:
		return lua.LNumber(t)
	case int:
		return lua.LNumber(t)
	case int64:
		return lua.LNumber(t)
	case json.Number:
		f, _ := t.Float64()
		return lua.LNumber(f)
	case []any:
		tbl := L.NewTable()
		for _, item := range t {
			tbl.Append(valueToLua(L, item))
		}
		return tbl
	case map[string]any:
		tbl := L.NewTable()
		for k, item := range t {
			tbl.RawSetString(k, valueToLua(L, item))
		}
		return tbl
	default:
		return lua.LString(fmt.Sprintf("%v", t))
	}
}

// luaToValue converts a Lua value to JSON-marshalable data. Functions,
// threads and userdata cannot cross the boundary — that is an error,
// not a silent drop, so plugin authors see the problem immediately.
func luaToValue(L *lua.LState, v lua.LValue) (any, error) {
	switch t := v.(type) {
	case *lua.LNilType:
		return nil, nil
	case lua.LString:
		return string(t), nil
	case lua.LBool:
		return bool(t), nil
	case lua.LNumber:
		return float64(t), nil
	case *lua.LTable:
		return tableToValue(L, t)
	default:
		return nil, fmt.Errorf("cannot marshal %s across the plugin boundary", v.Type().String())
	}
}

// tableToValue maps a Lua table to []any when its keys are exactly
// the sequence 1..n, otherwise to map[string]any.
func tableToValue(L *lua.LState, t *lua.LTable) (any, error) {
	n := t.Len()
	seq := make([]any, 0, n)
	hash := 0
	var firstErr error
	t.ForEach(func(k, v lua.LValue) {
		if firstErr != nil {
			return
		}
		if kn, ok := k.(lua.LNumber); ok {
			if f := float64(kn); f == float64(int(f)) && f >= 1 && f <= float64(n) {
				val, err := luaToValue(L, t.RawGetInt(int(f)))
				if err != nil {
					firstErr = err
					return
				}
				if len(seq) < int(f) {
					for len(seq) < int(f)-1 {
						seq = append(seq, nil)
					}
					seq = append(seq, val)
				}
				return
			}
		}
		hash++
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if hash == 0 && len(seq) == n && n > 0 {
		return seq, nil
	}
	out := map[string]any{}
	t.ForEach(func(k, v lua.LValue) {
		if firstErr != nil {
			return
		}
		key, err := luaKeyString(k)
		if err != nil {
			firstErr = err
			return
		}
		val, err := luaToValue(L, v)
		if err != nil {
			firstErr = err
			return
		}
		out[key] = val
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func luaKeyString(k lua.LValue) (string, error) {
	switch t := k.(type) {
	case lua.LString:
		return string(t), nil
	case lua.LNumber:
		return strconv.FormatFloat(float64(t), 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("table key of type %s cannot cross the plugin boundary", k.Type().String())
	}
}

// eventTable renders a bus event for a hook handler.
func eventTable(L *lua.LState, ev platform.Event) *lua.LTable {
	tbl := L.NewTable()
	tbl.RawSetString("type", lua.LString(ev.Type))
	tbl.RawSetString("path", lua.LString(ev.Path))
	tbl.RawSetString("slug", lua.LString(ev.Slug))
	tbl.RawSetString("actor", lua.LString(ev.Actor))
	tbl.RawSetString("origin", lua.LString(ev.Origin))
	tbl.RawSetString("depth", lua.LNumber(ev.Depth))
	tbl.RawSetString("count", lua.LNumber(ev.Count))
	data := L.NewTable()
	for k, v := range ev.Data {
		data.RawSetString(k, valueToLua(L, v))
	}
	tbl.RawSetString("data", data)
	return tbl
}

// noteTable renders a note for the read/fm capabilities.
func noteTable(L *lua.LState, text string) *lua.LTable {
	doc := frontmatter.Parse([]byte(text))
	tbl := L.NewTable()
	tbl.RawSetString("text", lua.LString(text))
	tbl.RawSetString("content", lua.LString(string(doc.Body)))
	tbl.RawSetString("title", lua.LString(doc.Title))
	meta := L.NewTable()
	for k, v := range doc.Meta {
		meta.RawSetString(k, valueToLua(L, v))
	}
	tbl.RawSetString("meta", meta)
	return tbl
}

// buildNote assembles note text from {content=..., meta={...}}.
func buildNote(v any) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("expected a table with content and meta")
	}
	content, _ := m["content"].(string)
	meta, _ := m["meta"].(map[string]any)
	if len(meta) == 0 {
		return content, nil
	}
	block, err := yaml.Marshal(meta)
	if err != nil {
		return "", err
	}
	out := "---\n" + string(block) + "---\n"
	if len(content) > 0 && content[0] != '\n' {
		out += "\n"
	}
	return out + content, nil
}

// fmTable renders parse(text) for Lua.
func fmTable(L *lua.LState, text string) *lua.LTable {
	doc := frontmatter.Parse([]byte(text))
	tbl := L.NewTable()
	tbl.RawSetString("content", lua.LString(string(doc.Body)))
	meta := L.NewTable()
	for k, v := range doc.Meta {
		meta.RawSetString(k, valueToLua(L, v))
	}
	tbl.RawSetString("meta", meta)
	return tbl
}

// checkPath keeps capability paths vault-relative: clean, relative,
// no escape. The vault re-checks at IO time; this exists so plugins
// get a Lua-level error instead of a Go one.
func checkPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("missing path")
	}
	if strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("bad path %q", rel)
	}
	clean := path.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("bad path %q", rel)
	}
	return nil
}
