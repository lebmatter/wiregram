package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type apiProvider struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`
}

type apiAgent struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Prompt        string `json:"prompt"`
	AutoReply     int    `json:"autoreply"`
	ProviderID    int64  `json:"provider_id"`
	RetentionDays int    `json:"retention_days"`
	Enabled       bool   `json:"enabled"`
}

func (ws *webServer) handleListProviders(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []apiProvider{})
	}
	providers, err := ws.agents.db.listProviders()
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to list providers"})
	}
	result := make([]apiProvider, len(providers))
	for i, p := range providers {
		result[i] = apiProvider{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL,
			APIKeyEnv: p.APIKeyEnv, Model: p.Model,
		}
	}
	return c.JSON(http.StatusOK, result)
}

func (ws *webServer) handleCreateProvider(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	var req apiProvider
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" || req.BaseURL == "" || req.Model == "" {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "name, base_url, and model are required"})
	}
	req.BaseURL = strings.TrimRight(req.BaseURL, "/")
	id, err := ws.agents.db.createProvider(agentProvider{
		Name: req.Name, BaseURL: req.BaseURL,
		APIKeyEnv: req.APIKeyEnv, Model: req.Model,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to create provider"})
	}
	req.ID = id
	return c.JSON(http.StatusCreated, req)
}

func (ws *webServer) handleUpdateProvider(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid provider id"})
	}
	var req apiProvider
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" || req.BaseURL == "" || req.Model == "" {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "name, base_url, and model are required"})
	}
	req.BaseURL = strings.TrimRight(req.BaseURL, "/")
	if err := ws.agents.db.updateProvider(agentProvider{
		ID: id, Name: req.Name, BaseURL: req.BaseURL,
		APIKeyEnv: req.APIKeyEnv, Model: req.Model,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to update provider"})
	}
	ws.agents.reload()
	req.ID = id
	return c.JSON(http.StatusOK, req)
}

func (ws *webServer) handleDeleteProvider(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid provider id"})
	}
	if err := ws.agents.db.deleteProvider(id); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to delete provider"})
	}
	ws.agents.reload()
	return c.NoContent(http.StatusNoContent)
}

func (ws *webServer) handleListAgents(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []apiAgent{})
	}
	agents, err := ws.agents.db.listAgents()
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to list agents"})
	}
	result := make([]apiAgent, len(agents))
	for i, a := range agents {
		result[i] = apiAgent{
			ID: a.ID, Name: a.Name, Prompt: a.Prompt,
			AutoReply: a.AutoReply, ProviderID: a.ProviderID,
			RetentionDays: a.RetentionDays, Enabled: a.Enabled,
		}
	}
	return c.JSON(http.StatusOK, result)
}

func (ws *webServer) handleCreateAgent(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	var req apiAgent
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "name is required"})
	}
	id, err := ws.agents.db.createAgent(agentRecord{
		Name: req.Name, Prompt: req.Prompt, AutoReply: req.AutoReply,
		ProviderID: req.ProviderID, RetentionDays: req.RetentionDays,
		Enabled: req.Enabled,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to create agent"})
	}
	ws.agents.reload()
	req.ID = id
	return c.JSON(http.StatusCreated, req)
}

func (ws *webServer) handleUpdateAgent(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	var req apiAgent
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "name is required"})
	}
	if err := ws.agents.db.updateAgent(agentRecord{
		ID: id, Name: req.Name, Prompt: req.Prompt,
		AutoReply: req.AutoReply, ProviderID: req.ProviderID,
		RetentionDays: req.RetentionDays, Enabled: req.Enabled,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to update agent"})
	}
	ws.agents.reload()
	req.ID = id
	return c.JSON(http.StatusOK, req)
}

func (ws *webServer) handleDeleteAgent(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	if err := ws.agents.db.deleteAgent(id); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to delete agent"})
	}
	ws.agents.reload()
	return c.NoContent(http.StatusNoContent)
}

func (ws *webServer) handleAgentKnowledge(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []string{})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	paths, err := ws.agents.db.agentKnowledge(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to load knowledge"})
	}
	return c.JSON(http.StatusOK, paths)
}

type apiKnowledgeRequest struct {
	Paths []string `json:"paths"`
}

func (ws *webServer) handleSetAgentKnowledge(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	var req apiKnowledgeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if err := ws.agents.db.setAgentKnowledge(id, req.Paths); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to set knowledge"})
	}
	ws.agents.reload()
	return c.JSON(http.StatusOK, req.Paths)
}

func (ws *webServer) handleAgentChats(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []agentChatAssignment{})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	chats, err := ws.agents.db.agentChats(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to load chat assignments"})
	}
	return c.JSON(http.StatusOK, chats)
}

type apiAgentChatsRequest struct {
	Chats []agentChatAssignment `json:"chats"`
}

func (ws *webServer) handleSetAgentChats(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusServiceUnavailable,
			map[string]string{"error": "agents not available"})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	var req apiAgentChatsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid request body"})
	}
	if err := ws.agents.db.setAgentChats(id, req.Chats); err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to set chat assignments"})
	}
	ws.agents.reload()
	return c.JSON(http.StatusOK, req.Chats)
}

func (ws *webServer) handleAgentThreads(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []agentThread{})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	threads, err := ws.agents.db.agentThreads(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to load threads"})
	}
	return c.JSON(http.StatusOK, threads)
}

func (ws *webServer) handleAgentThreadMessages(c echo.Context) error {
	if ws.agents == nil || ws.agents.db == nil {
		return c.JSON(http.StatusOK, []agentLoggedMessage{})
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			map[string]string{"error": "invalid agent id"})
	}
	backendType := c.Param("backend")
	accountID := c.Param("account")
	chatID := c.Param("chat")
	msgs, err := ws.agents.db.chatHistory(id, backendType, accountID, chatID, 100)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			map[string]string{"error": "failed to load thread messages"})
	}
	return c.JSON(http.StatusOK, msgs)
}
