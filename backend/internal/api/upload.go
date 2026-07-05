package api

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// uploadDir is where all uploaded images live (asset proofs, article images,
// logo, QR). Served read-only at /uploads/ (see Routes; directory listing off).
const uploadDir = "uploads"

// maxUploadBytes caps every image upload (register proof + admin uploads).
const maxUploadBytes = 3 << 20 // 3 MB

// imageExt whitelists common web images plus iPhone photos (HEIC/HEIF).
var imageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".heic": true, ".heif": true,
}

var errBadImageType = errors.New("僅接受圖片檔(png / jpg / jpeg / webp / gif / heic / heif)")
var errImageTooLarge = errors.New("圖片過大,上限 3MB")

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// saveUpload stores an uploaded file under uploads/<sub>/ and returns its URL
// path ("/uploads/<sub>/<file>"). The name is sanitised + nanosecond-stamped so
// it can't collide or be trivially guessed. Rejects non-image extensions and
// enforces the 3MB cap while copying (removing the partial file on overflow).
func saveUpload(sub, base, filename string, src io.Reader) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if !imageExt[ext] {
		return "", errBadImageType
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
	full := filepath.Join(dir, fname)
	dst, err := os.Create(full)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	n, err := io.Copy(dst, io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		os.Remove(full)
		return "", err
	}
	if n > maxUploadBytes {
		os.Remove(full)
		return "", errImageTooLarge
	}
	return "/" + uploadDir + "/" + sub + "/" + fname, nil
}
