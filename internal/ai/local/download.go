package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Progress is how far a model's download has got.
type Progress struct {
	Done, Total int64
	Running     bool
	Err         error // why the last download stopped, nil if it finished
}

// download is a model's download in flight.
type download struct {
	cancel context.CancelFunc
	done   int64
}

func (e *Engine) path(m Model) string { return filepath.Join(e.dir, m.File) }

// Installed reports whether m has been downloaded.
func (e *Engine) Installed(m Model) bool {
	info, err := os.Stat(e.path(m))
	return err == nil && info.Size() == m.Size
}

// Progress reports m's download: running, stopped with an error, or none.
func (e *Engine) Progress(m Model) Progress {
	e.dlMu.Lock()
	defer e.dlMu.Unlock()
	if d := e.downloads[m.ID]; d != nil {
		return Progress{Done: d.done, Total: m.Size, Running: true}
	}
	return Progress{Total: m.Size, Err: e.failed[m.ID]}
}

// Download fetches m in the background, resuming a download that stopped
// part way. It runs until it is done, fails, is cancelled or ctx ends.
func (e *Engine) Download(ctx context.Context, m Model) {
	e.dlMu.Lock()
	defer e.dlMu.Unlock()
	if e.downloads[m.ID] != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &download{cancel: cancel}
	e.downloads[m.ID] = d
	delete(e.failed, m.ID)
	go func() {
		defer cancel()
		err := e.fetch(ctx, m, d)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		e.dlMu.Lock()
		delete(e.downloads, m.ID)
		if err != nil {
			e.failed[m.ID] = err
		}
		e.dlMu.Unlock()
	}()
}

// CancelDownload stops m's download, keeping what it fetched so the next
// one resumes.
func (e *Engine) CancelDownload(m Model) {
	e.dlMu.Lock()
	defer e.dlMu.Unlock()
	if d := e.downloads[m.ID]; d != nil {
		d.cancel()
	}
}

// Remove deletes m's file, unloading it first if it is in use.
func (e *Engine) Remove(m Model) error {
	e.mu.Lock()
	if e.loaded == m.ID {
		e.unload()
	}
	e.mu.Unlock()
	err := os.Remove(e.path(m))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	os.Remove(e.path(m) + ".part")
	if err != nil {
		return fmt.Errorf("remove %s: %w", m.Name, err)
	}
	return nil
}

// fetch downloads m to a .part file next to its place, then checks its
// hash and moves it there.
func (e *Engine) fetch(ctx context.Context, m Model, d *download) error {
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		return fmt.Errorf("create models folder: %w", err)
	}
	part := e.path(m) + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open download: %w", err)
	}
	defer f.Close()
	// The hash covers the whole file, so what an earlier download fetched
	// is read through it first.
	hash := sha256.New()
	have, err := io.Copy(hash, f)
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if have > m.Size {
		return e.corrupt(m, part)
	}
	e.setDone(d, have)
	if have < m.Size {
		if err := e.get(ctx, m, f, hash, have, d); err != nil {
			return err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.SHA256 {
		return e.corrupt(m, part)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save %s: %w", m.Name, err)
	}
	if err := os.Rename(part, e.path(m)); err != nil {
		return fmt.Errorf("save %s: %w", m.Name, err)
	}
	return nil
}

// get fetches m from byte have onwards, appending to f.
func (e *Engine) get(ctx context.Context, m Model, f *os.File, hash io.Writer, have int64,
	d *download) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL(), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", m.Name, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
	case resp.StatusCode == http.StatusOK && have == 0:
	default:
		return fmt.Errorf("download %s: Hugging Face answered %s", m.Name, resp.Status)
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		return fmt.Errorf("write download: %w", err)
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if have+int64(n) > m.Size {
				return e.corrupt(m, f.Name())
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write download: %w", werr)
			}
			hash.Write(buf[:n])
			have += int64(n)
			e.setDone(d, have)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("download %s: %w", m.Name, err)
		}
	}
	if have != m.Size {
		return fmt.Errorf("download %s: it ended early; try again to resume", m.Name)
	}
	return nil
}

func (e *Engine) setDone(d *download, n int64) {
	e.dlMu.Lock()
	d.done = n
	e.dlMu.Unlock()
}

// corrupt drops a download that does not match the catalog.
func (e *Engine) corrupt(m Model, part string) error {
	os.Remove(part)
	return fmt.Errorf("the download of %s did not match its checksum and was deleted; "+
		"try again", m.Name)
}
