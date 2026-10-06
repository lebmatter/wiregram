package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"wiregram/internal/backend"
)

func uploadedMedia(file tg.InputFileClass, path string) tg.InputMediaClass {
	kind, mimeType := backend.ClassifyUpload(path)
	if kind == backend.UploadPhoto {
		return &tg.InputMediaUploadedPhoto{File: file}
	}
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
	}
	switch kind {
	case backend.UploadVideo:
		attributes = append(attributes, &tg.DocumentAttributeVideo{SupportsStreaming: true})
	case backend.UploadAudio:
		attributes = append(attributes, &tg.DocumentAttributeAudio{})
	case backend.UploadGIF:
		attributes = append(attributes, &tg.DocumentAttributeAnimated{})
	}
	return &tg.InputMediaUploadedDocument{
		File: file, MimeType: mimeType, Attributes: attributes, ForceFile: kind == backend.UploadFile,
	}
}

func uploadPlaceholder(path, caption string, size int64) *tg.Message {
	kind, mimeType := backend.ClassifyUpload(path)
	message := &tg.Message{Out: true, Message: caption, Date: int(time.Now().Unix())}
	if kind == backend.UploadPhoto {
		message.Media = &tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}}
		return message
	}
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
	}
	switch kind {
	case backend.UploadVideo:
		attributes = append(attributes, &tg.DocumentAttributeVideo{})
	case backend.UploadAudio:
		attributes = append(attributes, &tg.DocumentAttributeAudio{})
	case backend.UploadGIF:
		attributes = append(attributes, &tg.DocumentAttributeAnimated{})
	}
	message.Media = &tg.MessageMediaDocument{Document: &tg.Document{
		MimeType: mimeType, Size: size, Attributes: attributes,
	}}
	return message
}

func (c *Client) sendUpload(
	ctx context.Context,
	entry chat,
	path string,
	caption string,
	replyTo int,
) (*tg.Message, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open upload file: %w", err)
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("create message ID: %w", err)
	}
	randomID := int64(binary.LittleEndian.Uint64(random[:]))

	var file tg.InputFileClass
	for {
		file, err = uploader.NewUploader(c.Api).FromPath(ctx, path)
		if err == nil {
			break
		}
		if !backend.NetworkError(err) {
			return nil, fmt.Errorf("upload media: %w", err)
		}
		c.Report(fmt.Errorf("Connection interrupted. Retrying upload: %w", err))
		if err := backend.WaitFor(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	request := &tg.MessagesSendMediaRequest{
		Peer: entry.peer, Media: uploadedMedia(file, path), Message: caption, RandomID: randomID,
	}
	if replyTo > 0 {
		request.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	var result tg.UpdatesClass
	for {
		result, err = c.Api.MessagesSendMedia(ctx, request)
		if err == nil {
			break
		}
		if !backend.NetworkError(err) {
			return nil, fmt.Errorf("send media: %w", err)
		}
		c.Report(fmt.Errorf("Connection interrupted. Retrying media: %w", err))
		if err := backend.WaitFor(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	placeholder := uploadPlaceholder(path, caption, info.Size())
	return c.sentMessage(result, entry, randomID, replyTo, placeholder), nil
}
