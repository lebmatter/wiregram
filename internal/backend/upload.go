package backend

import (
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type PathCompleter struct{}

func (PathCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos != len(line) {
		return nil, 0
	}
	typed := string(line)
	expanded := ExpandHome(typed)
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

func ExpandHome(path string) string {
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

type UploadKind byte

const (
	UploadFile UploadKind = iota
	UploadPhoto
	UploadVideo
	UploadAudio
	UploadGIF
)

func ClassifyUpload(path string) (UploadKind, string) {
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
		return UploadPhoto, mimeType
	case ".mp4", ".m4v", ".mov", ".webm", ".mkv":
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		return UploadVideo, mimeType
	case ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus", ".wav":
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		return UploadAudio, mimeType
	case ".gif":
		return UploadGIF, "image/gif"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return UploadFile, mimeType
}
