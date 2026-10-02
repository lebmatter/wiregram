package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
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

	type subEntry struct {
		accountIdx int
		ch         <-chan backendEvent
	}
	var subs []subEntry

	for i, a := range ws.mgr.accounts {
		s := a.snapshot()
		if s.backend == nil {
			continue
		}
		ch := s.backend.AddSubscriber(subID, nil, 128)
		subs = append(subs, subEntry{accountIdx: i, ch: ch})
	}
	defer func() {
		for _, a := range ws.mgr.accounts {
			s := a.snapshot()
			if s.backend != nil {
				s.backend.RemoveSubscriber(subID)
			}
		}
	}()

	events := make(chan sseEvent, 64)

	for _, sub := range subs {
		sub := sub
		go func() {
			for {
				select {
				case ev, ok := <-sub.ch:
					if !ok {
						return
					}
					sseEv := backendEventToSSE(sub.accountIdx, ev)
					if sseEv.Name != "" {
						select {
						case events <- sseEv:
						default:
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		for {
			select {
			case <-ws.mgr.changed:
				accts := make([]apiAccount, 0, len(ws.mgr.accounts))
				for i, a := range ws.mgr.accounts {
					s := a.snapshot()
					accts = append(accts, apiAccount{
						Index:       i,
						Label:       a.label(),
						BackendType: a.backendType,
						Status:      s.status,
						NeedsLogin:  s.needsLogin,
					})
				}
				select {
				case events <- sseEvent{Name: "accounts", Data: accts}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev := <-events:
			if err := writeSSE(w, ev.Name, ev.Data); err != nil {
				return nil
			}
		case <-ping.C:
			if err := writeSSE(w, "ping", nil); err != nil {
				return nil
			}
		case <-ctx.Done():
			return nil
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

func backendEventToSSE(accountIdx int, ev backendEvent) sseEvent {
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
				Message: messageToAPI(*ev.Message),
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
				Message: messageToAPI(*ev.Message),
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
