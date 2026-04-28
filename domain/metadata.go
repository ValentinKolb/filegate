package domain

import (
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

func detectMimeType(name string) string {
	if v := mime.TypeByExtension(filepath.Ext(name)); v != "" {
		return v
	}
	return "application/octet-stream"
}

func isEXIFCandidate(mimeType, name string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/jpeg") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".tif", ".tiff", ".dng":
		return true
	default:
		return false
	}
}

func readEXIF(absPath, mimeType, name string) map[string]string {
	if !isEXIFCandidate(mimeType, name) {
		return map[string]string{}
	}
	f, err := os.Open(absPath)
	if err != nil {
		return map[string]string{}
	}
	defer f.Close()

	decoded, err := exif.Decode(f)
	if err != nil {
		return map[string]string{}
	}
	w := &exifWalker{fields: make(map[string]string)}
	if err := decoded.Walk(w); err != nil {
		return map[string]string{}
	}
	return w.fields
}

type exifWalker struct {
	fields map[string]string
}

func (w *exifWalker) Walk(name exif.FieldName, tag *tiff.Tag) error {
	if w.fields == nil {
		w.fields = make(map[string]string)
	}
	value := strings.TrimSpace(tag.String())
	if value == "" {
		return nil
	}
	w.fields[string(name)] = value
	return nil
}
