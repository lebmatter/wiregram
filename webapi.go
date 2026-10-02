package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type apiAccount struct {
	Index       int    `json:"index"`
	Label       string `json:"label"`
	BackendType string `json:"backend_type"`
	Status      string `json:"status"`
	NeedsLogin  bool   `json:"needs_login"`
}

type apiChat struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Unread  int       `json:"unread"`
	LastAt  time.Time `json:"last_at"`
	IsDM    bool      `json:"is_dm"`
	IsGroup bool      `json:"is_group"`
}

type apiMessage struct {
	ID         string               `json:"id"`
	Sender     string               `json:"sender"`
	SenderID   string               `json:"sender_id"`
	Text       string               `json:"text"`
	Timestamp  time.Time            `json:"timestamp"`
	Outgoing   bool                 `json:"outgoing"`
	Pinned     bool                 `json:"pinned"`
	ReplyToID  string               `json:"reply_to_id,omitempty"`
	Forwarded  bool                 `json:"forwarded"`
	MediaLabel string               `json:"media_label,omitempty"`
	Reactions  []apiMessageReaction `json:"reactions,omitempty"`
}

type apiMessageReaction struct {
	Emoji  string `json:"emoji"`
	Count  int    `json:"count"`
	Chosen bool   `json:"chosen"`
}

func (ws *webServer) accountBackend(idx int) (*account, backend, error) {
	if idx < 0 || idx >= len(ws.mgr.accounts) {
		return nil, nil, fmt.Errorf("invalid account index")
	}
	a := ws.mgr.accounts[idx]
	s := a.snapshot()
	if s.backend == nil {
		return nil, nil, fmt.Errorf("account not connected")
	}
	return a, s.backend, nil
}

func (ws *webServer) findChat(c echo.Context, b backend, chatIDStr string) (chatEntry, error) {
	archived := c.QueryParam("archived") == "true"
	entries, _, err := b.Dialogs(c.Request().Context(), archived)
	if err != nil {
		return chatEntry{}, fmt.Errorf("load dialogs: %w", err)
	}
	for _, e := range entries {
		if e.ID.id == chatIDStr {
			return e, nil
		}
	}
	return chatEntry{}, fmt.Errorf("chat not found")
}

func (ws *webServer) handleListAccounts(c echo.Context) error {
	result := make([]apiAccount, 0, len(ws.mgr.accounts))
	for i, a := range ws.mgr.accounts {
		s := a.snapshot()
		result = append(result, apiAccount{
			Index:       i,
			Label:       a.label(),
			BackendType: a.backendType,
			Status:      s.status,
			NeedsLogin:  s.needsLogin,
		})
	}
	return c.JSON(http.StatusOK, result)
}

func (ws *webServer) handleListChats(c echo.Context) error {
	idx, err := strconv.Atoi(c.Param("idx"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid account index"})
	}
	_, b, err := ws.accountBackend(idx)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	archived := c.QueryParam("archived") == "true"
	entries, _, err := b.Dialogs(c.Request().Context(), archived)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load chats"})
	}
	result := make([]apiChat, 0, len(entries))
	for _, e := range entries {
		result = append(result, apiChat{
			ID:      e.ID.id,
			Title:   e.Title,
			Unread:  e.Unread,
			LastAt:  e.LastAt,
			IsDM:    e.IsDM,
			IsGroup: e.IsGroup,
		})
	}
	return c.JSON(http.StatusOK, result)
}

func (ws *webServer) handleListMessages(c echo.Context) error {
	idx, err := strconv.Atoi(c.Param("idx"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid account index"})
	}
	_, b, err := ws.accountBackend(idx)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	chatIDStr := c.Param("id")
	entry, err := ws.findChat(c, b, chatIDStr)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	before := c.QueryParam("before")
	var msgs []message
	if before != "" {
		msgs, err = b.OlderHistory(c.Request().Context(), entry, before)
	} else {
		msgs, err = b.History(c.Request().Context(), entry)
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load messages"})
	}

	result := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, messageToAPI(m))
	}
	return c.JSON(http.StatusOK, result)
}

type apiSendRequest struct {
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to"`
}

func (ws *webServer) handleSendMessage(c echo.Context) error {
	idx, err := strconv.Atoi(c.Param("idx"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid account index"})
	}
	_, b, err := ws.accountBackend(idx)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	chatIDStr := c.Param("id")
	entry, err := ws.findChat(c, b, chatIDStr)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	var req apiSendRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Text == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "text is required"})
	}
	msg, err := b.Send(c.Request().Context(), entry, req.Text, req.ReplyTo)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to send message"})
	}
	return c.JSON(http.StatusOK, messageToAPI(msg))
}

func messageToAPI(m message) apiMessage {
	am := apiMessage{
		ID:         m.ID,
		Sender:     m.Sender,
		SenderID:   m.SenderID,
		Text:       m.Text,
		Timestamp:  m.Timestamp,
		Outgoing:   m.Outgoing,
		Pinned:     m.Pinned,
		ReplyToID:  m.ReplyToID,
		Forwarded:  m.Forwarded,
		MediaLabel: m.MediaLabel,
	}
	if len(m.Reactions) > 0 {
		am.Reactions = make([]apiMessageReaction, len(m.Reactions))
		for i, r := range m.Reactions {
			am.Reactions[i] = apiMessageReaction{
				Emoji:  r.Emoji,
				Count:  r.Count,
				Chosen: r.Chosen,
			}
		}
	}
	return am
}
