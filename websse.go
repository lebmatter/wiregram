package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/labstack/echo/v4"
)

type sseEvent struct {
	Name string
	Data any
}

func (ws *webServer) handleSSE(c echo.Context) error {
	sess, ok := c.Get("session").(*webSession)
	if !ok {
		return c.String(http.StatusUnauthorized, "unauthorized")
	}
	csrf := c.QueryParam("csrf")
	if subtle.ConstantTimeCompare([]byte(csrf), []byte(sess.csrf)) != 1 {
		return c.String(http.StatusForbidden, "csrf mismatch")
	}

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	w.Flush()

	ws.mu.Lock()
	ws.sseConnID++
	connID := ws.sseConnID
	ws.mu.Unlock()
	subID := fmt.Sprintf("web-%d", connID)
	ctx := c.Request().Context()

	events := make(chan sseEvent, 64)

	// Accounts connect in the background and may reconnect with a new
	// backend, so subscriptions are reconciled on every tick rather than
	// taken once. ws.mgr.changed is not used: the agent runtime already
	// consumes that single-slot channel.
	subscribed := make(map[int]backend)
	defer func() {
		for _, b := range subscribed {
			b.RemoveSubscriber(subID)
		}
	}()
	var lastAccounts []apiAccount
	reconcileSubs := func() {
		for i, a := range ws.mgr.accounts {
			b := a.snapshot().backend
			if subscribed[i] == b {
				continue
			}
			if old := subscribed[i]; old != nil {
				old.RemoveSubscriber(subID)
				delete(subscribed, i)
			}
			if b == nil {
				continue
			}
			subscribed[i] = b
			go ws.forwardEvents(ctx, i, b, b.AddSubscriber(subID, nil, 128), events)
		}
		accts := ws.accountList()
		if !slices.Equal(accts, lastAccounts) {
			lastAccounts = accts
			select {
			case events <- sseEvent{Name: "accounts", Data: accts}:
			default:
			}
		}
	}
	reconcileSubs()

	reconcile := time.NewTicker(2 * time.Second)
	defer reconcile.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev := <-events:
			if err := writeSSE(w, ev.Name, ev.Data); err != nil {
				return nil
			}
		case <-reconcile.C:
			reconcileSubs()
		case <-ping.C:
			if err := writeSSE(w, "ping", nil); err != nil {
				return nil
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (ws *webServer) forwardEvents(
	ctx context.Context, accountIdx int, b backend, ch <-chan backendEvent, out chan<- sseEvent,
) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			sseEv := ws.backendEventToSSE(accountIdx, b, ev)
			if sseEv.Name == "" {
				continue
			}
			select {
			case out <- sseEv:
			default:
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeSSE(w *echo.Response, event string, data any) error {
	var payload []byte
	if data != nil {
		var err error
		payload, err = json.Marshal(data)
		if err != nil {
			return err
		}
	} else {
		payload = []byte("{}")
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	if err != nil {
		return err
	}
	w.Flush()
	return nil
}

type sseMessageEvent struct {
	Account int        `json:"account"`
	ChatID  string     `json:"chat_id"`
	Message apiMessage `json:"message"`
}

type sseDeleteEvent struct {
	Account   int    `json:"account"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
}

type sseReactionEvent struct {
	Account   int                  `json:"account"`
	ChatID    string               `json:"chat_id"`
	MessageID string               `json:"message_id"`
	Reactions []apiMessageReaction `json:"reactions"`
}

func (ws *webServer) backendEventToSSE(accountIdx int, b backend, ev backendEvent) sseEvent {
	switch ev.Kind {
	case "message":
		if ev.Message == nil {
			return sseEvent{}
		}
		return sseEvent{
			Name: "message",
			Data: sseMessageEvent{
				Account: accountIdx,
				ChatID:  ev.Chat.id,
				Message: ws.apiMessageFor(accountIdx, ev.Chat.id, b, *ev.Message),
			},
		}
	case "edit":
		if ev.Message == nil {
			return sseEvent{}
		}
		return sseEvent{
			Name: "edit",
			Data: sseMessageEvent{
				Account: accountIdx,
				ChatID:  ev.Chat.id,
				Message: ws.apiMessageFor(accountIdx, ev.Chat.id, b, *ev.Message),
			},
		}
	case "delete":
		return sseEvent{
			Name: "delete",
			Data: sseDeleteEvent{
				Account:   accountIdx,
				ChatID:    ev.Chat.id,
				MessageID: ev.DeleteID,
			},
		}
	case "reaction":
		if ev.Reaction == nil {
			return sseEvent{}
		}
		reactions := make([]apiMessageReaction, len(ev.Reaction.Reactions))
		for i, r := range ev.Reaction.Reactions {
			reactions[i] = apiMessageReaction{
				Emoji:  r.Emoji,
				Count:  r.Count,
				Chosen: r.Chosen,
			}
		}
		return sseEvent{
			Name: "reaction",
			Data: sseReactionEvent{
				Account:   accountIdx,
				ChatID:    ev.Chat.id,
				MessageID: ev.Reaction.MessageID,
				Reactions: reactions,
			},
		}
	}
	return sseEvent{}
}
