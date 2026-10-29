// Package ai is the provider-neutral model layer: every feature that asks a
// model goes through an Engine, whatever serves it.
package ai

import (
	"context"
	"errors"
)

// Engine is one way of reaching models: a remote API, a server on this machine
// or a model run in-process.
type Engine interface {
	// Chat answers the conversation. With Request.OnText set it streams the
	// reply's text as it arrives and still returns the whole reply.
	Chat(ctx context.Context, req Request) (Reply, error)
}

// Roles of a Message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

type Message struct {
	Role   string
	Text   string
	Images []Image // only on user messages
}

// Image is an attached picture, sent to models that can see.
type Image struct {
	MIME string
	Data []byte
}

type Request struct {
	Model     string
	Messages  []Message
	MaxTokens int
	// OnText receives each piece of the reply as it arrives, on the engine's
	// goroutine; nil waits for the whole reply.
	OnText func(string)
}

type Reply struct {
	Text string
}

// ErrUnauthorized is returned when the provider rejects the API key, or
// wants one and none was given.
var ErrUnauthorized = errors.New("provider refused the API key")
