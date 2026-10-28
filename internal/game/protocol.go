package game

import "encoding/json"

// Envelope is the one message type between host and guest, one per text frame.
type Envelope struct {
	T     string          `json:"t"`
	Token string          `json:"token,omitempty"`
	Name  string          `json:"name,omitempty"`
	Names []string        `json:"names,omitempty"`
	Game  *Info           `json:"game,omitempty"`
	P     json.RawMessage `json:"p,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Envelope types.
const (
	THello = "hello" // guest → host: Token, Name
	TStart = "start" // host → guest: Game, Names
	TKey   = "key"   // guest → host: P is a KeyMsg
	TFrame = "frame" // host → guest: P is a FrameMsg
	TSize  = "size"  // guest → host: P is a SizeMsg
	TLeave = "leave"
	TError = "error"
)

const (
	// ReadLimit caps one websocket message on both sides.
	ReadLimit = 64 << 10
	// MaxBoardW and MaxBoardH bound a board, so a frame stays under ReadLimit.
	MaxBoardW, MaxBoardH = 120, 60
	// MaxFrameLines bounds the lines a guest keeps from a frame.
	MaxFrameLines = MaxBoardH
)

// KeyMsg is a guest's key press, sent to the host.
type KeyMsg struct {
	Key string `json:"k"`
}

// SizeMsg is the area the guest has for the board, sent when the game starts
// and whenever it changes.
type SizeMsg struct {
	W int `json:"w"`
	H int `json:"h"`
}

// FrameMsg is the host's view for the guest. Lines holds only the lines that
// changed since the previous frame, keyed by index; Height trims the rest.
// Result is the guest's game.result once Over.
type FrameMsg struct {
	Lines  map[int]string `json:"l,omitempty"`
	Height int            `json:"h"`
	Status string         `json:"s"`
	Over   bool           `json:"o,omitempty"`
	Result string         `json:"r,omitempty"`
}

// Payload encodes v for an envelope's P.
func Payload(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
