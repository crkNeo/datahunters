package api

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// uploadDir is where all uploaded images live (asset proofs, article images,
// logo, QR). Served read-only at /uploads/ (see Routes).
const uploadDir = "uploads"

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// saveUpload stores an uploaded file under uploads/<sub>/ and returns its URL
// path ("/uploads/<sub>/<file>"). The name is sanitised + nanosecond-stamped so
// it can't collide or be trivially guessed.
func saveUpload(sub, base, filename string, src io.Reader) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
	default:
		ext = ".png"
	}
	dir := filepath.Join(uploadDir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := unsafeName.ReplaceAllString(base, "")
	if safe == "" {
		safe = "f"
	}
	fname := fmt.Sprintf("%s-%d%s", safe, time.Now().UnixNano(), ext)
	dst, err := os.Create(filepath.Join(dir, fname))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return "/" + uploadDir + "/" + sub + "/" + fname, nil
}
