package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

type pathCompleter struct{}

func (pathCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos != len(line) {
		return nil, 0
	}
	typed := string(line)
	expanded := expandHome(typed)
	dir, base := filepath.Dir(expanded), filepath.Base(expanded)
	if typed == "" {
		dir, base = ".", ""
	} else if strings.HasSuffix(typed, string(os.PathSeparator)) {
		dir, base = expanded, ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	var candidates [][]rune
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) || (base == "" && strings.HasPrefix(name, ".")) {
			continue
		}
		suffix := strings.TrimPrefix(name, base)
		if entry.IsDir() {
			suffix += string(os.PathSeparator)
		}
		candidates = append(candidates, []rune(suffix))
	}
	return candidates, utf8.RuneCountInString(base)
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

func uploadPath(value string) (string, error) {
	path := expandHome(strings.TrimSpace(value))
	if path == "" {
		return "", fmt.Errorf("enter a file path or press Esc to cancel")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve upload path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("open upload file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upload path must be a regular file")
	}
	return path, nil
}

type uploadKind byte

const (
	uploadFile uploadKind = iota
	uploadPhoto
	uploadVideo
	uploadAudio
	uploadGIF
)

func classifyUpload(path string) (uploadKind, string) {
	ext := strings.ToLower(filepath.Ext(path))
	mimeType := mime.TypeByExtension(ext)
	if comma := strings.IndexByte(mimeType, ';'); comma >= 0 {
		mimeType = mimeType[:comma]
	}
	switch ext {
	case ".jpg", ".jpeg", ".png":
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return uploadPhoto, mimeType
	case ".mp4", ".m4v", ".mov", ".webm", ".mkv":
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		return uploadVideo, mimeType
	case ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus", ".wav":
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		return uploadAudio, mimeType
	case ".gif":
		return uploadGIF, "image/gif"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return uploadFile, mimeType
}

func uploadedMedia(file tg.InputFileClass, path string) tg.InputMediaClass {
	kind, mimeType := classifyUpload(path)
	if kind == uploadPhoto {
		return &tg.InputMediaUploadedPhoto{File: file}
	}
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
	}
	switch kind {
	case uploadVideo:
		attributes = append(attributes, &tg.DocumentAttributeVideo{SupportsStreaming: true})
	case uploadAudio:
		attributes = append(attributes, &tg.DocumentAttributeAudio{})
	case uploadGIF:
		attributes = append(attributes, &tg.DocumentAttributeAnimated{})
	}
	return &tg.InputMediaUploadedDocument{
		File: file, MimeType: mimeType, Attributes: attributes, ForceFile: kind == uploadFile,
	}
}

func uploadPlaceholder(path, caption string, size int64) *tg.Message {
	kind, mimeType := classifyUpload(path)
	message := &tg.Message{Out: true, Message: caption, Date: int(time.Now().Unix())}
	if kind == uploadPhoto {
		message.Media = &tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}}
		return message
	}
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
	}
	switch kind {
	case uploadVideo:
		attributes = append(attributes, &tg.DocumentAttributeVideo{})
	case uploadAudio:
		attributes = append(attributes, &tg.DocumentAttributeAudio{})
	case uploadGIF:
		attributes = append(attributes, &tg.DocumentAttributeAnimated{})
	}
	message.Media = &tg.MessageMediaDocument{Document: &tg.Document{
		MimeType: mimeType, Size: size, Attributes: attributes,
	}}
	return message
}

func (c *client) sendUpload(
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
		file, err = uploader.NewUploader(c.api).FromPath(ctx, path)
		if err == nil {
			break
		}
		if !networkError(err) {
			return nil, fmt.Errorf("upload media: %w", err)
		}
		c.report(fmt.Errorf("Connection interrupted. Retrying upload: %w", err))
		if err := waitFor(ctx, time.Second); err != nil {
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
		result, err = c.api.MessagesSendMedia(ctx, request)
		if err == nil {
			break
		}
		if !networkError(err) {
			return nil, fmt.Errorf("send media: %w", err)
		}
		c.report(fmt.Errorf("Connection interrupted. Retrying media: %w", err))
		if err := waitFor(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	placeholder := uploadPlaceholder(path, caption, info.Size())
	return c.sentMessage(result, entry, randomID, replyTo, placeholder), nil
}
