package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/term"
)

type attachment struct {
	location tg.InputFileLocationClass
	dc       int
	audio    bool
}

func mediaAttachment(message *tg.Message) (attachment, error) {
	switch m := message.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return attachment{}, fmt.Errorf("this photo is no longer available")
		}
		size, area := "", 0
		for _, value := range photo.Sizes {
			var kind string
			var w, h int
			switch s := value.(type) {
			case *tg.PhotoSize:
				kind, w, h = s.Type, s.W, s.H
			case *tg.PhotoSizeProgressive:
				kind, w, h = s.Type, s.W, s.H
			case *tg.PhotoCachedSize:
				kind, w, h = s.Type, s.W, s.H
			}
			if w*h > area {
				size, area = kind, w*h
			}
		}
		if size == "" {
			return attachment{}, fmt.Errorf("this photo has no downloadable size")
		}
		return attachment{location: &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size}, dc: photo.DCID}, nil
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok {
			return attachment{}, fmt.Errorf("this attachment is no longer available")
		}
		audio := strings.HasPrefix(d.MimeType, "audio/")
		playable := audio || strings.HasPrefix(d.MimeType, "image/") || strings.HasPrefix(d.MimeType, "video/")
		for _, attr := range d.Attributes {
			switch attr.(type) {
			case *tg.DocumentAttributeAudio:
				audio, playable = true, true
			case *tg.DocumentAttributeVideo:
				playable = true
			}
		}
		if !playable {
			return attachment{}, fmt.Errorf("this attachment format cannot be viewed or played inside the terminal")
		}
		return attachment{location: &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, dc: d.DCID, audio: audio}, nil
	default:
		return attachment{}, fmt.Errorf("this message has no playable attachment")
	}
}

// The player never reads the terminal. Only the chat loop handles keyboard input.
type mediaOutput struct{ terminal *terminal }

func (w mediaOutput) Write(data []byte) (int, error) {
	w.terminal.mu.Lock()
	defer w.terminal.mu.Unlock()
	if w.terminal.locked || !w.terminal.viewing {
		return len(data), nil
	}
	return os.Stdout.Write(data)
}

func (t *terminal) mediaScreen(label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.viewing = true
	fmt.Fprintf(os.Stdout, "\033[2J\033[H%s\r\nEsc: return to chat. Inactivity stops media after 10 seconds.\r\n", plain(label))
}

func (t *terminal) clearMedia() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.viewing = false
	// Terminate any interrupted graphics packet, then delete image data as well as placements.
	fmt.Fprint(os.Stdout, "\033\\\033_Ga=d,d=A,q=2;\033\\\033[?25h")
}

func (c *client) playMedia(ctx context.Context, t *terminal, a attachment, player string) (result error) {
	dir, err := os.MkdirTemp("", "wiregram-media-")
	if err != nil {
		return fmt.Errorf("create private media directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			result = fmt.Errorf("remove temporary media: %w", err)
		}
	}()
	path := filepath.Join(dir, "attachment")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create temporary media file: %w", err)
	}
	err = c.downloadMedia(ctx, a, file)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("download media: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	args := []string{"--no-config", "--load-scripts=no", "--input-terminal=no", "--input-default-bindings=no", "--input-media-keys=no", "--osc=no", "--osd-level=0", "--really-quiet", "--audio-display=no", "--autoload-files=no", "--image-display-duration=inf"}
	if a.audio {
		args = append(args, "--vid=no", "--vo=null")
	} else {
		cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || cols < 1 || rows < 4 {
			cols, rows = 80, 24
		}
		args = append(args, "--profile=sw-fast", "--vo=kitty", "--vo-kitty-alt-screen=no", "--vo-kitty-use-shm=no", "--vo-kitty-config-clear=no", "--vo-kitty-top=3", "--vo-kitty-left=1", fmt.Sprintf("--vo-kitty-cols=%d", cols), fmt.Sprintf("--vo-kitty-rows=%d", rows-2), fmt.Sprintf("--vo-kitty-width=%d", cols*8), fmt.Sprintf("--vo-kitty-height=%d", (rows-2)*16))
	}
	args = append(args, "--", path)
	cmd := exec.CommandContext(ctx, player, args...)
	cmd.Stdout = mediaOutput{t}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("media playback failed; check that mpv supports this format and the Kitty video output: %w", err)
	}
	return nil
}

type downloadable struct {
	location tg.InputFileLocationClass
	dc       int
	filename string
}

func mediaDownloadable(message *tg.Message) (downloadable, error) {
	switch m := message.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return downloadable{}, fmt.Errorf("this photo is no longer available")
		}
		size, area := "", 0
		for _, value := range photo.Sizes {
			var kind string
			var w, h int
			switch s := value.(type) {
			case *tg.PhotoSize:
				kind, w, h = s.Type, s.W, s.H
			case *tg.PhotoSizeProgressive:
				kind, w, h = s.Type, s.W, s.H
			case *tg.PhotoCachedSize:
				kind, w, h = s.Type, s.W, s.H
			}
			if w*h > area {
				size, area = kind, w*h
			}
		}
		if size == "" {
			return downloadable{}, fmt.Errorf("this photo has no downloadable size")
		}
		return downloadable{
			location: &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size},
			dc:       photo.DCID,
			filename: fmt.Sprintf("photo_%d.jpg", photo.ID),
		}, nil
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok {
			return downloadable{}, fmt.Errorf("this attachment is no longer available")
		}
		name := "document"
		for _, attr := range d.Attributes {
			if f, ok := attr.(*tg.DocumentAttributeFilename); ok {
				name = f.FileName
			}
		}
		return downloadable{
			location: &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference},
			dc:       d.DCID,
			filename: name,
		}, nil
	default:
		return downloadable{}, fmt.Errorf("this message has no downloadable attachment")
	}
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return path
}

func (c *client) saveMedia(ctx context.Context, dl downloadable, path string) error {
	path = uniquePath(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	a := attachment{location: dl.location, dc: dl.dc}
	err = c.downloadMedia(ctx, a, file)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		os.Remove(path)
		return fmt.Errorf("download: %w", err)
	}
	return nil
}

func (c *client) acknowledgeMedia(ctx context.Context, message *tg.Message) {
	if expiringMediaLabel(message.Media) == "" {
		return
	}
	_, err := c.api.MessagesReadMessageContents(ctx, []int{message.ID})
	if err != nil {
		c.report(fmt.Errorf("acknowledge viewed media: %w", err))
	}
}

func (c *client) downloadMedia(ctx context.Context, a attachment, file *os.File) error {
	var pool telegram.CloseInvoker
	var err error
	if a.dc == c.telegram.Config().ThisDC {
		// Exporting authorization to the current DC is invalid; reuse its session.
		pool, err = c.telegram.Pool(1)
	} else {
		pool, err = c.telegram.DC(ctx, a.dc, 1)
	}
	if err != nil {
		return fmt.Errorf("connect to media server: %w", err)
	}
	defer pool.Close()
	rpc := telegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		for {
			err := pool.Invoke(ctx, input, output)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			delay, flood := tgerr.AsFloodWait(err)
			if !flood && !networkError(err) {
				return err
			}
			if flood {
				c.report(fmt.Errorf("Rate limited. Retry in %ds.", int(delay.Seconds())))
			} else {
				delay = time.Second
				c.report(fmt.Errorf("Media connection interrupted. Retrying: %w", err))
			}
			if err := waitFor(ctx, delay); err != nil {
				return err
			}
		}
	})
	_, err = downloader.NewDownloader().Download(tg.NewClient(rpc), a.location).Stream(ctx, file)
	return err
}
