// Package game runs two-player Lua games for a chat. The challenger hosts: its
// wiregram runs the script for both seats behind a cloudflared quick tunnel,
// and the guest only sends key presses and draws the frames it gets back.
package game

import (
	"embed"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

//go:embed scripts/*.lua
var scriptFS embed.FS

// Info is a script's game.info table. The host sends it to the guest, which
// needs it only for the title, the help line and the board's size.
type Info struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Desc   string `json:"desc,omitempty"`
	Help   string `json:"help,omitempty"` // one line of in-game keys
	MinW   int    `json:"w"`
	MinH   int    `json:"h"`
	TickMS int    `json:"-"`
	// Icon is a small drawing of the game for the picker: up to IconLines
	// lines of up to IconWidth cells.
	Icon []string `json:"-"`
}

// The largest icon the picker draws.
const IconLines, IconWidth = 3, 15

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

func readInfo(src string) (Info, error) {
	L, g, err := load(src)
	if err != nil {
		return Info{}, err
	}
	defer L.Close()
	t, ok := g.RawGetString("info").(*lua.LTable)
	if !ok {
		return Info{}, errors.New("game.info is missing")
	}
	str := func(k string) string { return lua.LVAsString(t.RawGetString(k)) }
	num := func(k string) int { return int(lua.LVAsNumber(t.RawGetString(k))) }
	info := Info{
		ID: str("id"), Name: str("name"), Desc: str("desc"), Help: str("help"),
		MinW: num("min_w"), MinH: num("min_h"), TickMS: num("tick_ms"),
	}
	if icon := strings.Trim(str("icon"), "\n"); icon != "" {
		info.Icon = strings.Split(icon, "\n")
		if len(info.Icon) > IconLines {
			info.Icon = info.Icon[:IconLines]
		}
	}
	switch {
	case info.ID == "" || info.Name == "":
		return Info{}, errors.New("game.info needs id and name")
	case num("players") != 2:
		return Info{}, errors.New("game.info.players must be 2")
	case info.MinW < 1 || info.MinW > maxBoardW || info.MinH < 1 || info.MinH > maxBoardH:
		return Info{}, fmt.Errorf("game.info.min_w and min_h must be 1 to %d and %d",
			maxBoardW, maxBoardH)
	case info.TickMS < 0 || (info.TickMS > 0 && info.TickMS < 16):
		return Info{}, errors.New("game.info.tick_ms must be 0 or at least 16")
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
