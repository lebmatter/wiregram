package plugin

import lua "github.com/yuin/gopher-lua"

// newSandbox makes a VM with only the base, table, string and math libraries:
// no files, processes, modules or debug hooks, and no print, which would
// write over the screen.
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
		"module", "print", "collectgarbage"} {
		L.SetGlobal(name, lua.LNil)
	}
	return L
}
