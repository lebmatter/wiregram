package game

import "encoding/json"

// GameRef names a game and the rules version a client has. Two clients can
// play a game only when their refs are equal.
type GameRef struct {
	ID      string `json:"id"`
	Version int    `json:"v"`
}

// Player is one entry in the lobby.
type Player struct {
	Name  string    `json:"name"`
	Games []GameRef `json:"games"`
	Busy  bool      `json:"busy,omitempty"`
}

// Has reports whether the player can play ref.
func (p Player) Has(ref GameRef) bool {
	for _, g := range p.Games {
		if g == ref {
			return true
		}
	}
	return false
}

// Envelope is the one message type on the base websocket, one per text frame.
type Envelope struct {
	T       string          `json:"t"`
	Name    string          `json:"name,omitempty"`
	Token   string          `json:"token,omitempty"`
	Games   []GameRef       `json:"games,omitempty"`
	Players []Player        `json:"players,omitempty"`
	To      string          `json:"to,omitempty"`
	From    string          `json:"from,omitempty"`
	ID      string          `json:"id,omitempty"`
	Game    *GameRef        `json:"game,omitempty"`
	Seat    int             `json:"seat,omitempty"`
	Names   []string        `json:"names,omitempty"`
	Payload json.RawMessage `json:"p,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Envelope types.
const (
	THello     = "hello"
	TLobby     = "lobby"
	TChallenge = "challenge"
	TAccept    = "accept"
	TDecline   = "decline"
	TMatched   = "matched"
	TDeclined  = "declined"
	TGame      = "game"
	TLeave     = "leave"
	TPeerLeft  = "peer-left"
	TError     = "error"
)

// ReadLimit caps one websocket message on both sides.
const ReadLimit = 64 << 10

// KeyMsg is a guest's key press, sent to the host.
type KeyMsg struct {
	Key string `json:"k"`
}

// FrameMsg is the host's view for one guest. Lines holds only the lines that
// changed since the previous frame, keyed by index; Height trims the rest.
type FrameMsg struct {
	Lines  map[int]string `json:"l,omitempty"`
	Height int            `json:"h"`
	Status string         `json:"s"`
	Over   bool           `json:"o,omitempty"`
}
