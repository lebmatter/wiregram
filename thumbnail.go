package main

import (
	"bytes"
	"strings"
	"sync"

	termimg "github.com/blacktop/go-termimg"
)

var (
	thumbProtocol termimg.Protocol
	thumbEnabled  bool
)

func initThumbnails() {
	thumbProtocol = termimg.DetectProtocol()
	thumbEnabled = thumbProtocol != termimg.Unsupported
}

type thumbCache struct {
	mu      sync.Mutex
	entries map[string]string
	order   []string
	limit   int
}

func newThumbCache(limit int) *thumbCache {
	return &thumbCache{
		entries: make(map[string]string, limit),
		limit:   limit,
	}
}

func (tc *thumbCache) get(key string) (string, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	v, ok := tc.entries[key]
	return v, ok
}

func (tc *thumbCache) put(key string, rendered string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if _, ok := tc.entries[key]; ok {
		return
	}
	if len(tc.order) >= tc.limit {
		evict := tc.order[0]
		tc.order = tc.order[1:]
		delete(tc.entries, evict)
	}
	tc.entries[key] = rendered
	tc.order = append(tc.order, key)
}

func renderThumbnail(data []byte, maxCols, maxRows int) []string {
	if !thumbEnabled || len(data) == 0 {
		return nil
	}
	img, err := termimg.From(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	rendered, err := img.
		Width(maxCols).
		Height(maxRows).
		Scale(termimg.ScaleFit).
		Protocol(thumbProtocol).
		Render()
	if err != nil || rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}
