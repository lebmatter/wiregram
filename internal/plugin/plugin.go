// Package plugin runs Lua plugins, each in its own sandboxed VM. A plugin
// declares settings, keeps values in its own slice of wiregram.db and may
// draw a widget on the account chooser.
package plugin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"wiregram/internal/store"
	"wiregram/internal/textfmt"
)

//go:embed builtin/*.lua
var builtin embed.FS

const (
	loadTimeout   = 2 * time.Second
	renderTimeout = 50 * time.Millisecond
	updateTimeout = 20 * time.Second
)

// Setting is one value a plugin lets the user change in Settings.
type Setting struct {
	Key     string
	Label   string
	Kind    string // "string" or "choice"
	Default string
	Options []string // the values a choice cycles through
}

type widget struct {
	height  int
	refresh time.Duration
	update  *lua.LFunction
	render  *lua.LFunction
}

// Plugin is one loaded script with its own Lua VM. mu serialises every call
// into the VM; vmu guards the setting values, which Settings changes while an
// update may hold mu.
type Plugin struct {
	Name     string
	Title    string
	Builtin  bool
	Settings []Setting

	db      *store.DB
	client  *http.Client
	hosts   []string
	startOn bool
	loading bool

	mu     sync.Mutex
	L      *lua.LState
	widget *widget

	vmu     sync.Mutex
	enabled bool
	values  map[string]string
	changes uint64
}

// Manager holds every loaded plugin, built-in ones first, and why any others
// failed to load.
type Manager struct {
	plugins []*Plugin
	Errors  []error
}

// Load reads the built-in plugins, then those in dir, where a file of the
// same name replaces a built-in. A plugin that fails to load is left out and
// reported in Errors; the rest still load.
func Load(db *store.DB, dir string) *Manager {
	sources := map[string]string{}
	builtins := map[string]bool{}
	var names []string
	entries, _ := fs.ReadDir(builtin, "builtin")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".lua")
		src, err := builtin.ReadFile("builtin/" + e.Name())
		if err != nil {
			continue
		}
		sources[name], builtins[name] = string(src), true
		names = append(names, name)
	}
	var errs []error
	user, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("read plugins folder: %w", err))
	}
	for _, e := range user {
		name, path := strings.TrimSuffix(e.Name(), ".lua"), filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			path = filepath.Join(path, "init.lua")
		case !strings.HasSuffix(e.Name(), ".lua"):
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			if !e.IsDir() || !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("plugin %s: %w", name, err))
			}
			continue
		}
		if _, ok := sources[name]; !ok {
			names = append(names, name)
		}
		sources[name], builtins[name] = string(src), false
	}

	client := &http.Client{Timeout: 10 * time.Second}
	m := &Manager{}
	for _, name := range names {
		p := &Plugin{Name: name, Title: name, Builtin: builtins[name], db: db, client: client,
			values: map[string]string{}}
		if err := p.load(sources[name]); err != nil {
			p.L.Close()
			errs = append(errs, fmt.Errorf("plugin %s: %w", name, err))
			continue
		}
		m.plugins = append(m.plugins, p)
	}
	m.Errors = errs
	return m
}

// Plugins lists the loaded plugins in load order.
func (m *Manager) Plugins() []*Plugin { return m.plugins }

func (m *Manager) Close() {
	for _, p := range m.plugins {
		p.mu.Lock()
		p.L.Close()
		p.mu.Unlock()
	}
}

func (p *Plugin) load(src string) error {
	p.L = newSandbox()
	p.registerAPI()
	fn, err := p.L.Load(strings.NewReader(src), p.Name+".lua")
	if err != nil {
		return luaError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	p.loading = true
	defer func() { p.loading = false }()
	_, err = p.call(ctx, fn, 0)
	for _, s := range p.Settings {
		if v, err := p.db.Setting(p.settingKey(s.Key)); err == nil {
			p.values[s.Key] = v
		}
	}
	p.enabled = !p.Builtin || p.startOn
	if v, err := p.db.Setting(p.settingKey("enabled")); err == nil {
		p.enabled = v == "1"
	}
	return err
}

func (p *Plugin) settingKey(key string) string { return "plugin." + p.Name + "." + key }

// Enabled reports whether the plugin runs. Built-in plugins start off, since
// they reach the network, unless they declare on=true; plugins the user
// installed start on.
func (p *Plugin) Enabled() bool {
	p.vmu.Lock()
	defer p.vmu.Unlock()
	return p.enabled
}

func (p *Plugin) SetEnabled(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if err := p.db.SetSetting(p.settingKey("enabled"), v); err != nil {
		return err
	}
	p.vmu.Lock()
	p.enabled = on
	p.changes++
	p.vmu.Unlock()
	return nil
}

// Value is a setting's current value, or its default.
func (p *Plugin) Value(key string) string {
	p.vmu.Lock()
	defer p.vmu.Unlock()
	if v, ok := p.values[key]; ok {
		return v
	}
	for _, s := range p.Settings {
		if s.Key == key {
			return s.Default
		}
	}
	return ""
}

func (p *Plugin) SetValue(key, value string) error {
	if err := p.db.SetSetting(p.settingKey(key), value); err != nil {
		return err
	}
	p.vmu.Lock()
	p.values[key] = value
	p.vmu.Unlock()
	p.changed()
	return nil
}

func (p *Plugin) changed() {
	p.vmu.Lock()
	p.changes++
	p.vmu.Unlock()
}

// Changes counts setting changes, so a widget knows to update at once.
func (p *Plugin) Changes() uint64 {
	p.vmu.Lock()
	defer p.vmu.Unlock()
	return p.changes
}

// HasWidget reports whether the plugin draws a widget.
func (p *Plugin) HasWidget() bool { return p.widget != nil }

// WidgetHeight is the widget's lines inside its card.
func (p *Plugin) WidgetHeight() int { return p.widget.height }

// Refresh is how often the widget's update runs.
func (p *Plugin) Refresh() time.Duration { return p.widget.refresh }

// Render draws the widget's lines for one animation frame. It gives up when
// an update holds the VM, and ok is false so the caller keeps the last frame.
func (p *Plugin) Render(width, frame int) (lines []string, ok bool, err error) {
	if !p.mu.TryLock() {
		return nil, false, nil
	}
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	ret, err := p.call(ctx, p.widget.render, 1, lua.LNumber(width), lua.LNumber(frame))
	if err != nil {
		return nil, true, err
	}
	t, isTable := ret[0].(*lua.LTable)
	if !isTable {
		return nil, true, errors.New("render must return a list of lines")
	}
	for i := 1; i <= t.Len() && i <= p.widget.height; i++ {
		lines = append(lines, textfmt.Plain(lua.LVAsString(t.RawGetInt(i))))
	}
	return lines, true, nil
}

// Update runs the widget's update, which may fetch over the network.
func (p *Plugin) Update(ctx context.Context) error {
	if p.widget.update == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	_, err := p.call(ctx, p.widget.update, 0)
	return err
}

// call runs fn under ctx, which cancels a script that runs too long, and
// returns its results.
func (p *Plugin) call(ctx context.Context, fn *lua.LFunction, nret int,
	args ...lua.LValue) ([]lua.LValue, error) {
	L := p.L
	L.SetContext(ctx)
	defer L.RemoveContext()
	top := L.GetTop()
	if err := L.CallByParam(lua.P{Fn: fn, NRet: nret, Protect: true}, args...); err != nil {
		L.SetTop(top)
		if ctx.Err() != nil {
			return nil, errors.New("took too long and was stopped")
		}
		return nil, luaError(err)
	}
	ret := make([]lua.LValue, nret)
	for i := range ret {
		ret[i] = L.Get(top + 1 + i)
	}
	L.SetTop(top)
	return ret, nil
}

// luaError keeps the one-line message of a Lua error, without the traceback.
func luaError(err error) error {
	var api *lua.ApiError
	if errors.As(err, &api) && api.Object != nil {
		msg, _, _ := strings.Cut(api.Object.String(), "\n")
		return errors.New(msg)
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return errors.New(msg)
}

func (p *Plugin) allowed(host string) bool { return slices.Contains(p.hosts, host) }
