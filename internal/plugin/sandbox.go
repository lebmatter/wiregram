package plugin

import lua "github.com/yuin/gopher-lua"

// maxString caps what string.rep builds, so one call cannot take all memory.
const maxString = 1 << 20

// newSandbox makes a VM with only the base, table, string and math libraries:
// no files, processes, modules or debug hooks, and no print, _printregs or
// newproxy, which would write over the screen.
func newSandbox() *lua.LState {
	L := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		CallStackSize:       200,
		RegistrySize:        1024 * 20,
		RegistryMaxSize:     1024 * 80,
		IncludeGoStackTrace: false,
	})
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
	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require",
		"module", "print", "_printregs", "newproxy", "collectgarbage"} {
		L.SetGlobal(name, lua.LNil)
	}
	str := L.GetGlobal(lua.StringLibName).(*lua.LTable)
	rep := str.RawGetString("rep")
	str.RawSetString("rep", L.NewFunction(func(L *lua.LState) int {
		if n := L.OptInt(2, 1); n > 0 && len(L.CheckString(1))*n > maxString {
			L.RaiseError("string.rep would exceed %d KB", maxString>>10)
		}
		L.Push(rep)
		L.Push(L.Get(1))
		L.Push(L.Get(2))
		L.Call(2, 1)
		return 1
	}))
	return L
}
