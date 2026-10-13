package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	lua "github.com/yuin/gopher-lua"

	"wiregram/internal/store"
)

const (
	maxValue = 64 << 10 // one kv value
	maxKey   = 256
	maxBody  = 1 << 20 // one http.get response
)

// registerAPI installs the wiregram table. Every function closes over p, so a
// plugin can only reach its own settings, store and allowed hosts.
func (p *Plugin) registerAPI() {
	L := p.L
	api := L.NewTable()
	L.SetFuncs(api, map[string]lua.LGFunction{
		"plugin":   p.luaPlugin,
		"settings": p.luaSettings,
		"widget":   p.luaWidget,
		"now":      func(L *lua.LState) int { L.Push(lua.LNumber(time.Now().Unix())); return 1 },
	})
	kv := L.NewTable()
	L.SetFuncs(kv, map[string]lua.LGFunction{
		"get":    p.luaKVGet,
		"set":    p.luaKVSet,
		"delete": p.luaKVDelete,
		"keys":   p.luaKVKeys,
	})
	api.RawSetString("kv", kv)
	httpTable := L.NewTable()
	L.SetFuncs(httpTable, map[string]lua.LGFunction{"get": p.luaHTTPGet})
	api.RawSetString("http", httpTable)
	jsonTable := L.NewTable()
	L.SetFuncs(jsonTable, map[string]lua.LGFunction{"decode": luaJSONDecode})
	api.RawSetString("json", jsonTable)
	L.SetGlobal("wiregram", api)
}

func (p *Plugin) requireLoading(L *lua.LState, fn string) {
	if !p.loading {
		L.RaiseError("wiregram.%s can only be called while the plugin loads", fn)
	}
}

// wiregram.plugin{title=, hosts={…}}: the name Settings shows and the hosts
// http.get may reach.
func (p *Plugin) luaPlugin(L *lua.LState) int {
	p.requireLoading(L, "plugin")
	t := L.CheckTable(1)
	if s, ok := t.RawGetString("title").(lua.LString); ok && s != "" {
		p.Title = string(s)
	}
	if hosts, ok := t.RawGetString("hosts").(*lua.LTable); ok {
		hosts.ForEach(func(_, v lua.LValue) {
			if s, ok := v.(lua.LString); ok {
				p.hosts = append(p.hosts, string(s))
			}
		})
	}
	return 0
}

// wiregram.settings{{key=, label=, type="string"|"choice", default=, options={…}}, …}
// declares the settings in the order Settings lists them, and returns a table
// that reads their live values.
func (p *Plugin) luaSettings(L *lua.LState) int {
	p.requireLoading(L, "settings")
	spec := L.CheckTable(1)
	for i := 1; i <= spec.Len(); i++ {
		t, ok := spec.RawGetInt(i).(*lua.LTable)
		if !ok {
			L.ArgError(1, "each setting must be a table")
		}
		s := Setting{
			Key:     lua.LVAsString(t.RawGetString("key")),
			Label:   lua.LVAsString(t.RawGetString("label")),
			Kind:    lua.LVAsString(t.RawGetString("type")),
			Default: lua.LVAsString(t.RawGetString("default")),
		}
		if opts, ok := t.RawGetString("options").(*lua.LTable); ok {
			for j := 1; j <= opts.Len(); j++ {
				s.Options = append(s.Options, lua.LVAsString(opts.RawGetInt(j)))
			}
		}
		switch {
		case s.Key == "" || s.Key == "enabled":
			L.ArgError(1, fmt.Sprintf("setting %d needs a key other than enabled", i))
		case s.Kind != "string" && s.Kind != "choice":
			L.ArgError(1, fmt.Sprintf("setting %s: type must be string or choice", s.Key))
		case s.Kind == "choice" && len(s.Options) == 0:
			L.ArgError(1, fmt.Sprintf("setting %s: a choice needs options", s.Key))
		}
		if s.Label == "" {
			s.Label = s.Key
		}
		p.Settings = append(p.Settings, s)
	}
	proxy := L.NewTable()
	meta := L.NewTable()
	meta.RawSetString("__index", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(p.Value(L.CheckString(2))))
		return 1
	}))
	meta.RawSetString("__newindex", L.NewFunction(func(L *lua.LState) int {
		L.RaiseError("settings are changed in Settings, not by the plugin")
		return 0
	}))
	L.SetMetatable(proxy, meta)
	L.Push(proxy)
	return 1
}

// wiregram.widget{height=, refresh=, update=fn, render=fn(width, frame)}:
// a card on the account chooser. render returns up to height lines and runs
// about four times a second; update runs every refresh seconds and when a
// setting changes.
func (p *Plugin) luaWidget(L *lua.LState) int {
	p.requireLoading(L, "widget")
	t := L.CheckTable(1)
	w := &widget{height: 4, refresh: 10 * time.Minute}
	if n, ok := t.RawGetString("height").(lua.LNumber); ok {
		w.height = max(1, min(12, int(n)))
	}
	if n, ok := t.RawGetString("refresh").(lua.LNumber); ok {
		w.refresh = max(10*time.Second, time.Duration(float64(n)*float64(time.Second)))
	}
	w.update, _ = t.RawGetString("update").(*lua.LFunction)
	render, ok := t.RawGetString("render").(*lua.LFunction)
	if !ok {
		L.ArgError(1, "a widget needs a render function")
	}
	w.render = render
	p.widget = w
	return 0
}

func (p *Plugin) luaKVGet(L *lua.LState) int {
	v, err := p.db.PluginGet(p.Name, L.CheckString(1))
	switch {
	case errors.Is(err, store.ErrNotFound):
		L.Push(lua.LNil)
		return 1
	case err != nil:
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(v))
	return 1
}

func (p *Plugin) luaKVSet(L *lua.LState) int {
	k, v := L.CheckString(1), L.CheckString(2)
	var err error
	switch {
	case len(k) == 0 || len(k) > maxKey:
		err = fmt.Errorf("kv keys are 1 to %d bytes", maxKey)
	case len(v) > maxValue:
		err = fmt.Errorf("kv values are at most %d KB", maxValue>>10)
	default:
		err = p.db.PluginPut(p.Name, k, v)
	}
	return pushResult(L, err)
}

func (p *Plugin) luaKVDelete(L *lua.LState) int {
	return pushResult(L, p.db.PluginDelete(p.Name, L.CheckString(1)))
}

func (p *Plugin) luaKVKeys(L *lua.LState) int {
	keys, err := p.db.PluginKeys(p.Name)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	t := L.NewTable()
	for _, k := range keys {
		t.Append(lua.LString(k))
	}
	L.Push(t)
	return 1
}

// pushResult returns true, or nil and the error, Lua's usual pair.
func pushResult(L *lua.LState, err error) int {
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LTrue)
	return 1
}

// wiregram.http.get(url) returns the body, or nil and an error. Only https to
// the hosts the plugin declared, redirects included.
func (p *Plugin) luaHTTPGet(L *lua.LState) int {
	body, err := p.httpGet(L, L.CheckString(1))
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(body))
	return 1
}

func (p *Plugin) httpGet(L *lua.LState, raw string) (string, error) {
	if p.loading {
		return "", errors.New("http.get is not available while the plugin loads")
	}
	if err := p.checkURL(raw); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(L.Context(), http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("bad request: %w", err)
	}
	req.Header.Set("User-Agent", "wiregram")
	client := *p.client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return p.checkURL(r.URL.String())
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", req.URL.Host, err)
	}
	if len(body) > maxBody {
		return "", fmt.Errorf("%s sent more than %d MB", req.URL.Host, maxBody>>20)
	}
	return string(body), nil
}

func (p *Plugin) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad url: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("only https urls are allowed")
	}
	if !p.allowed(u.Hostname()) {
		return fmt.Errorf("%s is not in the plugin's hosts", u.Hostname())
	}
	return nil
}

// wiregram.json.decode(s) returns a table, or nil and an error.
func luaJSONDecode(L *lua.LState) int {
	var v any
	if err := json.Unmarshal([]byte(L.CheckString(1)), &v); err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString("bad json: " + err.Error()))
		return 2
	}
	L.Push(toLua(L, v))
	return 1
}

func toLua(L *lua.LState, v any) lua.LValue {
	switch v := v.(type) {
	case map[string]any:
		t := L.NewTable()
		for k, e := range v {
			t.RawSetString(k, toLua(L, e))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, e := range v {
			t.Append(toLua(L, e))
		}
		return t
	case string:
		return lua.LString(v)
	case float64:
		return lua.LNumber(v)
	case bool:
		return lua.LBool(v)
	}
	return lua.LNil
}
