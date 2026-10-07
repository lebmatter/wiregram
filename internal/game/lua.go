package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// callBudget bounds every call into a script so a bug cannot freeze the UI.
const callBudget = 50 * time.Millisecond

// Session is one running match of a script. The host drives every seat. It is
// not safe for concurrent use; the TUI calls it only from Update.
type Session struct {
	L    *lua.LState
	game *lua.LTable
	info Info
}

// newState is a VM with only the pure libraries: no io, os, debug, package,
// and no way to load more code.
func newState() *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true, CallStackSize: 128})
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring",
		"require", "module", "print", "collectgarbage", "getfenv", "setfenv"} {
		L.SetGlobal(name, lua.LNil)
	}
	return L
}

// load runs src in a fresh sandbox and returns the VM and its game table.
func load(src string) (*lua.LState, *lua.LTable, error) {
	L := newState()
	ctx, cancel := context.WithTimeout(context.Background(), callBudget)
	defer cancel()
	L.SetContext(ctx)
	err := L.DoString(src)
	L.RemoveContext()
	if err != nil {
		L.Close()
		return nil, nil, fmt.Errorf("load script: %w", err)
	}
	g, ok := L.GetGlobal("game").(*lua.LTable)
	if !ok {
		L.Close()
		return nil, nil, errors.New("script defines no game table")
	}
	return L, g, nil
}

// NewSession starts a match. names are in seat order; seat 1 is the host.
func NewSession(s Script, names []string) (*Session, error) {
	L, g, err := load(s.Src)
	if err != nil {
		return nil, err
	}
	sess := &Session{L: L, game: g, info: s.Info}
	t := L.NewTable()
	for _, n := range names {
		t.Append(lua.LString(n))
	}
	if _, err := sess.call("init", 0, t); err != nil {
		L.Close()
		return nil, err
	}
	return sess, nil
}

func (s *Session) Info() Info { return s.info }

// Key passes a key press from seat (1-based) to the script.
func (s *Session) Key(seat int, key string) error {
	_, err := s.call("key", 0, lua.LNumber(seat), lua.LString(key))
	return err
}

func (s *Session) Tick() error {
	_, err := s.call("tick", 0)
	return err
}

// View returns what seat sees in a w×h area: the board and one status line.
func (s *Session) View(seat, w, h int) (board, status string, err error) {
	ret, err := s.call("view", 2, lua.LNumber(seat), lua.LNumber(w), lua.LNumber(h))
	if err != nil {
		return "", "", err
	}
	return lua.LVAsString(ret[0]), lua.LVAsString(ret[1]), nil
}

func (s *Session) Over() (bool, error) {
	ret, err := s.call("over", 1)
	if err != nil {
		return false, err
	}
	return lua.LVAsBool(ret[0]), nil
}

func (s *Session) Close() { s.L.Close() }

func (s *Session) call(name string, nret int, args ...lua.LValue) ([]lua.LValue, error) {
	fn, ok := s.game.RawGetString(name).(*lua.LFunction)
	if !ok {
		return nil, fmt.Errorf("%s: game.%s is missing", s.info.Name, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callBudget)
	defer cancel()
	s.L.SetContext(ctx)
	defer s.L.RemoveContext()
	if err := s.L.CallByParam(lua.P{Fn: fn, NRet: nret, Protect: true}, args...); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: game script took too long", s.info.Name)
		}
		return nil, fmt.Errorf("%s: %s", s.info.Name, firstLine(err.Error()))
	}
	ret := make([]lua.LValue, nret)
	for i := range nret {
		ret[i] = s.L.Get(i - nret)
	}
	s.L.Pop(nret)
	return ret, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
