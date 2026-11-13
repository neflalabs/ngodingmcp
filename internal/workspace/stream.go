package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrExists is returned when a streamed write targets an existing file
	// and overwrite was not requested.
	ErrExists = errors.New("destination already exists (set overwrite to replace it)")
	// ErrStreamTooLarge is returned when a streamed write passes its size limit.
	ErrStreamTooLarge = errors.New("file exceeds the size limit")
)

// StreamOptions controls WriteStream.
type StreamOptions struct {
	// MaxBytes caps the number of bytes written; more input aborts the write.
	MaxBytes int64
	// Overwrite allows replacing an existing file.
	Overwrite bool
	// Verify, when set, receives the hex SHA-256 of the complete data before
	// the file is put in place; a non-nil error aborts the write.
	Verify func(sum string) error
}

// StreamResult describes a completed streamed write.
type StreamResult struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// CheckWritable validates rel as a destination file for WriteStream (inside
// the root, not sensitive, not a directory, not the root itself) and, unless
// overwrite is set, that nothing exists there yet. It returns the clean
// relative path. Callers use it to fail fast before fetching any data.
func (w *Workspace) CheckWritable(rel string, overwrite bool) (string, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	if full == w.root {
		return "", ErrIsDir
	}
	info, err := os.Stat(full)
	switch {
	case err == nil && info.IsDir():
		return "", ErrIsDir
	case err == nil && !overwrite:
		return "", ErrExists
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	return w.relPath(full), nil
}

// WriteStream copies r into rel atomically: the data goes to a temp file in
// the destination directory, which is renamed into place only after the whole
// stream was read within opts.MaxBytes and passed opts.Verify. On any failure
// the temp file is removed, so no partial file is ever left behind. Content
// length headers are never trusted: the limit is enforced on the bytes read.
func (w *Workspace) WriteStream(rel string, r io.Reader, opts StreamOptions) (StreamResult, error) {
	if opts.MaxBytes <= 0 {
		return StreamResult{}, errors.New("stream size limit must be positive")
	}
	relp, err := w.CheckWritable(rel, opts.Overwrite)
	if err != nil {
		return StreamResult{}, err
	}
	full := filepath.Join(w.root, filepath.FromSlash(relp))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return StreamResult{}, err
	}
	tmp, err := os.CreateTemp(dir, ".ngodingmcp-*")
	if err != nil {
		return StreamResult{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, opts.MaxBytes+1))
	if err != nil {
		tmp.Close()
		return StreamResult{}, err
	}
	if n > opts.MaxBytes {
		tmp.Close()
		return StreamResult{}, fmt.Errorf("%w of %d bytes", ErrStreamTooLarge, opts.MaxBytes)
	}
	if err := tmp.Close(); err != nil {
		return StreamResult{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if opts.Verify != nil {
		if err := opts.Verify(sum); err != nil {
			return StreamResult{}, err
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return StreamResult{}, err
	}
	// Re-check right before the rename: the destination may have appeared
	// while the data was streaming.
	if _, err := w.CheckWritable(relp, opts.Overwrite); err != nil {
		return StreamResult{}, err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return StreamResult{}, err
	}
	return StreamResult{Path: relp, Bytes: n, SHA256: sum}, nil
}
