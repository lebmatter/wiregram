package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labstack/echo/v4"
)

// inlinePhotoBackend is implemented by backends whose photos the web UI
// can display inline.
type inlinePhotoBackend interface {
	InlinePhoto(msg message) (w, h int, ok bool)
	DownloadPhoto(ctx context.Context, msg message, file *os.File) error
}

var (
	_ inlinePhotoBackend = (*telegramBackend)(nil)
	_ inlinePhotoBackend = (*mmBackend)(nil)
	_ inlinePhotoBackend = (*mxBackend)(nil)
	_ inlinePhotoBackend = (*waBackend)(nil)
)

// maxPhotoRefs bounds the in-memory map of photo messages seen by the web
// UI. Already-downloaded photos stay servable from disk after a reset.
const maxPhotoRefs = 5000

func photoKey(accountIdx int, chatID, msgID string) string {
	return strconv.Itoa(accountIdx) + "/" + chatID + "/" + msgID
}

// apiMessageFor converts m and remembers it if it carries an inline photo,
// so the photo route can download it later without refetching history.
func (ws *webServer) apiMessageFor(accountIdx int, chatID string, b backend, m message) apiMessage {
	am := messageToAPI(m)
	pb, ok := b.(inlinePhotoBackend)
	if !ok {
		return am
	}
	w, h, ok := pb.InlinePhoto(m)
	if !ok {
		return am
	}
	am.Photo, am.PhotoW, am.PhotoH = true, w, h
	ws.mu.Lock()
	if len(ws.photoRefs) >= maxPhotoRefs {
		ws.photoRefs = make(map[string]message)
	}
	ws.photoRefs[photoKey(accountIdx, chatID, m.ID)] = m
	ws.mu.Unlock()
	return am
}

var servablePhotoTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

func (ws *webServer) handlePhoto(c echo.Context) error {
	idx, err := strconv.Atoi(c.Param("idx"))
	if err != nil {
		return c.String(http.StatusBadRequest, "invalid account index")
	}
	key := photoKey(idx, c.Param("id"), c.Param("msg"))
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(ws.photoDir, hex.EncodeToString(sum[:]))

	if err := ws.ensurePhoto(c.Request().Context(), idx, key, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c.String(http.StatusNotFound, "photo not found")
		}
		return c.String(http.StatusBadGateway, "could not download photo")
	}

	f, err := os.Open(path)
	if err != nil {
		return c.String(http.StatusNotFound, "photo not found")
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ctype := http.DetectContentType(head[:n])
	// Only image types are served so a hostile attachment can never be
	// rendered as HTML on the control panel's origin.
	if !servablePhotoTypes[ctype] {
		return c.String(http.StatusUnsupportedMediaType, "not an image")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return c.String(http.StatusInternalServerError, "read photo")
	}
	stat, err := f.Stat()
	if err != nil {
		return c.String(http.StatusInternalServerError, "read photo")
	}
	h := c.Response().Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(c.Response(), c.Request(), "", stat.ModTime(), f)
	return nil
}

// ensurePhoto downloads the photo for key into path unless it is already
// cached. Concurrent requests for the same photo share one download.
func (ws *webServer) ensurePhoto(ctx context.Context, idx int, key, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	ws.mu.Lock()
	wait, busy := ws.photoInflight[key]
	if !busy {
		done := make(chan struct{})
		ws.photoInflight[key] = done
		msg, known := ws.photoRefs[key]
		ws.mu.Unlock()
		err := ws.downloadPhoto(ctx, idx, msg, known, path)
		ws.mu.Lock()
		delete(ws.photoInflight, key)
		ws.mu.Unlock()
		close(done)
		return err
	}
	ws.mu.Unlock()
	select {
	case <-wait:
		// The shared download may have failed; only the file says.
		if _, err := os.Stat(path); err != nil {
			return errors.New("photo download failed")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ws *webServer) downloadPhoto(ctx context.Context, idx int, msg message, known bool, path string) error {
	if !known {
		return os.ErrNotExist
	}
	_, b, err := ws.accountBackend(idx)
	if err != nil {
		return err
	}
	pb, ok := b.(inlinePhotoBackend)
	if !ok {
		return os.ErrNotExist
	}
	tmp, err := os.CreateTemp(ws.photoDir, "dl-*")
	if err != nil {
		return err
	}
	dlErr := pb.DownloadPhoto(ctx, msg, tmp)
	closeErr := tmp.Close()
	if err := errors.Join(dlErr, closeErr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
