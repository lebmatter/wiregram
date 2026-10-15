package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"wiregram/internal/backend"
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
			// The sender picks this name, so keep only its last element.
			if f, ok := attr.(*tg.DocumentAttributeFilename); ok {
				name = backend.SafeFilename(f.FileName, name)
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

func (c *Client) saveMedia(ctx context.Context, dl downloadable, path string) error {
	file, err := backend.CreateUnique(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	a := attachment{location: dl.location, dc: dl.dc}
	err = c.downloadMedia(ctx, a, file)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("download: %w", err)
	}
	return nil
}

func (c *Client) acknowledgeMedia(ctx context.Context, message *tg.Message) {
	if expiringMediaLabel(message.Media) == "" {
		return
	}
	_, err := c.Api.MessagesReadMessageContents(ctx, []int{message.ID})
	if err != nil {
		c.Report(fmt.Errorf("acknowledge viewed media: %w", err))
	}
}

func (c *Client) downloadMedia(ctx context.Context, a attachment, file *os.File) error {
	var pool telegram.CloseInvoker
	var err error
	if a.dc == c.Telegram.Config().ThisDC {
		// Exporting authorization to the current DC is invalid; reuse its session.
		pool, err = c.Telegram.Pool(1)
	} else {
		pool, err = c.Telegram.DC(ctx, a.dc, 1)
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
			if !flood && !backend.NetworkError(err) {
				return err
			}
			if flood {
				c.Report(fmt.Errorf("Rate limited. Retry in %ds.", int(delay.Seconds())))
			} else {
				delay = time.Second
				c.Report(fmt.Errorf("Media connection interrupted. Retrying: %w", err))
			}
			if err := backend.WaitFor(ctx, delay); err != nil {
				return err
			}
		}
	})
	_, err = downloader.NewDownloader().Download(tg.NewClient(rpc), a.location).Stream(ctx, file)
	return err
}
