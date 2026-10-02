package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

//go:embed web/index.html web/static/*
var webFS embed.FS

type webServer struct {
	mgr       *accountManager
	agents    *agentRuntime
	port      int
	token     string
	tokenUsed bool
	sessions  map[string]*webSession
	mu        sync.Mutex
	sseConnID int64

	photoDir      string
	photoRefs     map[string]message
	photoInflight map[string]chan struct{}
}

type webSession struct {
	csrf    string
	created time.Time
}

func startWeb(ctx context.Context, mgr *accountManager, port int, token string) error {
	photoDir, err := os.MkdirTemp("", "wiregram-photos-")
	if err != nil {
		return fmt.Errorf("create photo cache: %w", err)
	}
	defer os.RemoveAll(photoDir)

	ws := &webServer{
		mgr:           mgr,
		agents:        mgr.agents,
		port:          port,
		token:         token,
		sessions:      make(map[string]*webSession),
		photoDir:      photoDir,
		photoRefs:     make(map[string]message),
		photoInflight: make(map[string]chan struct{}),
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(ws.loopbackGuard)
	e.Use(ws.hostAllowlist)
	e.Use(ws.authMiddleware)
	e.Use(ws.csrfMiddleware)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("prepare embedded filesystem: %w", err)
	}
	staticSub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return fmt.Errorf("prepare static filesystem: %w", err)
	}
	e.GET("/", echo.WrapHandler(http.FileServer(http.FS(sub))))
	e.GET("/static/*", echo.WrapHandler(
		http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))),
	))

	api := e.Group("/api")
	api.GET("/session", ws.handleSession)
	api.GET("/accounts", ws.handleListAccounts)
	api.GET("/accounts/:idx/chats", ws.handleListChats)
	api.GET("/accounts/:idx/chats/:id/messages", ws.handleListMessages)
	api.POST("/accounts/:idx/chats/:id/messages", ws.handleSendMessage)
	api.GET("/accounts/:idx/chats/:id/photos/:msg", ws.handlePhoto)
	api.GET("/events", ws.handleSSE)

	api.GET("/providers", ws.handleListProviders)
	api.POST("/providers", ws.handleCreateProvider)
	api.PUT("/providers/:id", ws.handleUpdateProvider)
	api.DELETE("/providers/:id", ws.handleDeleteProvider)

	api.GET("/agents", ws.handleListAgents)
	api.POST("/agents", ws.handleCreateAgent)
	api.PUT("/agents/:id", ws.handleUpdateAgent)
	api.DELETE("/agents/:id", ws.handleDeleteAgent)
	api.GET("/agents/:id/knowledge", ws.handleAgentKnowledge)
	api.PUT("/agents/:id/knowledge", ws.handleSetAgentKnowledge)
	api.GET("/agents/:id/chats", ws.handleAgentChats)
	api.PUT("/agents/:id/chats", ws.handleSetAgentChats)
	api.GET("/agents/:id/threads", ws.handleAgentThreads)
	api.GET("/agents/:id/threads/:backend/:account/:chat",
		ws.handleAgentThreadMessages)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.Shutdown(shutCtx)
	}()
	if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("web server: %w", err)
	}
	return nil
}

func (ws *webServer) loopbackGuard(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ip := c.RealIP()
		if ip != "127.0.0.1" && ip != "::1" {
			return c.String(http.StatusForbidden, "loopback only")
		}
		return next(c)
	}
}

func (ws *webServer) hostAllowlist(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		host := c.Request().Host
		h, _, err := net.SplitHostPort(host)
		if err != nil {
			h = host
		}
		if h != "127.0.0.1" && h != "localhost" {
			return c.String(http.StatusForbidden, "host not allowed")
		}
		return next(c)
	}
}

func (ws *webServer) authMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		if strings.HasPrefix(path, "/static/") {
			return next(c)
		}

		cookie, err := c.Cookie("wiregram_session")
		if err == nil {
			ws.mu.Lock()
			sess, ok := ws.sessions[cookie.Value]
			ws.mu.Unlock()
			if ok {
				c.Set("session", sess)
				return next(c)
			}
		}

		tok := c.QueryParam("token")
		if tok != "" {
			ws.mu.Lock()
			used := ws.tokenUsed
			ws.mu.Unlock()
			if !used && subtle.ConstantTimeCompare([]byte(tok), []byte(ws.token)) == 1 {
				sess := ws.createSession()
				ws.mu.Lock()
				ws.tokenUsed = true
				ws.mu.Unlock()
				c.SetCookie(&http.Cookie{
					Name:     "wiregram_session",
					Value:    sess.id,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				return c.Redirect(http.StatusFound, "/")
			}
		}

		return c.String(http.StatusUnauthorized, "unauthorized")
	}
}

type sessionWithID struct {
	id   string
	sess *webSession
}

func (ws *webServer) createSession() sessionWithID {
	var idBytes [16]byte
	rand.Read(idBytes[:])
	id := hex.EncodeToString(idBytes[:])

	var csrfBytes [16]byte
	rand.Read(csrfBytes[:])
	csrf := hex.EncodeToString(csrfBytes[:])

	sess := &webSession{csrf: csrf, created: time.Now()}
	ws.mu.Lock()
	ws.sessions[id] = sess
	ws.mu.Unlock()
	return sessionWithID{id: id, sess: sess}
}

func (ws *webServer) csrfMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		method := c.Request().Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return next(c)
		}

		sess, ok := c.Get("session").(*webSession)
		if !ok {
			return c.String(http.StatusForbidden, "no session")
		}

		csrfHeader := c.Request().Header.Get("X-Wiregram-CSRF")
		if subtle.ConstantTimeCompare([]byte(csrfHeader), []byte(sess.csrf)) != 1 {
			return c.String(http.StatusForbidden, "csrf mismatch")
		}

		origin := c.Request().Header.Get("Origin")
		if origin != "" {
			allowed := fmt.Sprintf("http://127.0.0.1:%d", ws.port)
			allowedLocalhost := fmt.Sprintf("http://localhost:%d", ws.port)
			if origin != allowed && origin != allowedLocalhost {
				return c.String(http.StatusForbidden, "origin not allowed")
			}
		}

		return next(c)
	}
}

func (ws *webServer) handleSession(c echo.Context) error {
	sess, ok := c.Get("session").(*webSession)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "no session"})
	}
	return c.JSON(http.StatusOK, map[string]string{"csrf": sess.csrf})
}
