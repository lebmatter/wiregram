package game

import (
	"embed"
	"fmt"
	"path"
	"time"

	lua "github.com/yuin/gopher-lua"
)

//go:embed scripts/*.lua
var scriptFS embed.FS

// Info is a script's game.info table. Bump Version on any rule change: only
// players with the same id and version can be matched.
type Info struct {
	ID      string
	Name    string
	Desc    string
	Help    string // one line of in-game keys
	Version int
	Players int
	MinW    int
	MinH    int
	TickMS  int
}

func (i Info) Ref() GameRef { return GameRef{ID: i.ID, Version: i.Version} }

// TickRate is how often Tick runs; zero for turn-based games.
func (i Info) TickRate() time.Duration { return time.Duration(i.TickMS) * time.Millisecond }

// Script is one embedded game.
type Script struct {
	Info Info
	Src  string
}

// Catalog loads every embedded script. A broken script is left out and
// reported in err; the rest are still returned.
func Catalog() (scripts []Script, err error) {
	entries, readErr := scriptFS.ReadDir("scripts")
	if readErr != nil {
		return nil, fmt.Errorf("read games: %w", readErr)
	}
	for _, e := range entries {
		src, readErr := scriptFS.ReadFile(path.Join("scripts", e.Name()))
		if readErr != nil {
			err = fmt.Errorf("read game %s: %w", e.Name(), readErr)
			continue
		}
		info, infoErr := readInfo(string(src))
		if infoErr != nil {
			err = fmt.Errorf("game %s: %w", e.Name(), infoErr)
			continue
		}
		scripts = append(scripts, Script{Info: info, Src: string(src)})
	}
	return scripts, err
}

// Find returns the script matching ref.
func Find(scripts []Script, ref GameRef) (Script, bool) {
	for _, s := range scripts {
		if s.Info.Ref() == ref {
			return s, true
		}
	}
	return Script{}, false
}

func readInfo(src string) (Info, error) {
	L, g, err := load(src)
	if err != nil {
		return Info{}, err
	}
	defer L.Close()
	t, ok := g.RawGetString("info").(*lua.LTable)
	if !ok {
		return Info{}, fmt.Errorf("game.info is missing")
	}
	str := func(k string) string { return lua.LVAsString(t.RawGetString(k)) }
	num := func(k string) int { return int(lua.LVAsNumber(t.RawGetString(k))) }
	info := Info{
		ID: str("id"), Name: str("name"), Desc: str("desc"), Help: str("help"),
		Version: num("version"),
		Players: num("players"), MinW: num("min_w"), MinH: num("min_h"), TickMS: num("tick_ms"),
	}
	switch {
	case info.ID == "" || info.Name == "":
		return Info{}, fmt.Errorf("game.info needs id and name")
	case info.Version < 1:
		return Info{}, fmt.Errorf("game.info.version must be 1 or more")
	case info.Players != 2:
		return Info{}, fmt.Errorf("game.info.players must be 2")
	case info.TickMS < 0 || (info.TickMS > 0 && info.TickMS < 16):
		return Info{}, fmt.Errorf("game.info.tick_ms must be 0 or at least 16")
	}
	need := []string{"init", "key", "view", "over"}
	if info.TickMS > 0 {
		need = append(need, "tick")
	}
	for _, fn := range need {
		if _, ok := g.RawGetString(fn).(*lua.LFunction); !ok {
			return Info{}, fmt.Errorf("game.%s is missing", fn)
		}
	}
	return info, nil
}
