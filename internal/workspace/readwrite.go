package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadResult is the result of ReadFile.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Size       int64  `json:"size"`
	// Hash is a short digest of the whole file, independent of the
	// offset/limit slice. read_file returns it so a later call can pass
	// if_changed_since and skip unchanged content.
	Hash string `json:"hash"`
}

// contentHash is the first 16 hex characters of the file's SHA-256. That is
// plenty for change detection and much cheaper to repeat in a header than the
// full digest.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// ReadFile reads a file, optionally slicing by 1-based line offset/limit.
func (w *Workspace) ReadFile(rel string, offset, limit int) (ReadResult, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ReadResult{}, err
	}
	total := countLines(content)
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return ReadResult{
			Path:       w.relPath(full),
			Content:    "",
			StartLine:  offset,
			EndLine:    offset - 1,
			TotalLines: total,
			Size:       info.Size(),
			Hash:       contentHash(content),
		}, nil
	}
	endLine := total
	if limit > 0 && offset+limit-1 < total {
		endLine = offset + limit - 1
	}
	start := lineStartOffset(content, offset)
	end := lineStartOffset(content, endLine+1)
	if end == -1 {
		end = len(content)
	}
	return ReadResult{
		Path:       w.relPath(full),
		Content:    content[start:end],
		StartLine:  offset,
		EndLine:    endLine,
		TotalLines: total,
		Size:       info.Size(),
		Hash:       contentHash(content),
	}, nil
}

// WriteFile writes content to rel atomically, creating parent directories.
// The whole operation is serialized with other writes and edits.
func (w *Workspace) WriteFile(rel, content string) (string, int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.writeFileLocked(rel, content)
}

// writeFileLocked is WriteFile without taking writeMu. Callers that already
// hold writeMu must use this to avoid a self-deadlock.
func (w *Workspace) writeFileLocked(rel, content string) (string, int, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return "", 0, err
	}
	target, err := w.writeTarget(full)
	if err != nil {
		return "", 0, err
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return "", 0, ErrIsDir
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".ngodingmcp-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	// Keep the mode of an existing file; only new files get 0644.
	chmod := os.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		chmod = st.Mode().Perm()
	}
	if err := os.Chmod(tmpName, chmod); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", 0, err
	}
	relp := w.relPath(full)
	w.hub.notify("write", relp, "")
	return relp, len(content), nil
}

// ReplaceText replaces occurrences of old with new inside rel.
func (w *Workspace) ReplaceText(rel, old, new string, all bool) (string, int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if old == "" {
		return "", 0, errors.New("old text must not be empty")
	}
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	count := strings.Count(content, old)
	if count == 0 {
		return "", 0, errors.New("old text not found")
	}
	n := 1
	if all {
		n = count
	} else if count > 1 {
		return "", 0, fmt.Errorf("old text occurs %d times; provide more context or set replace_all=true", count)
	}
	updated := strings.Replace(content, old, new, n)
	if _, _, err := w.writeFileLocked(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), n, nil
}

// InsertText inserts text before the given 1-based line. line may be
// totalLines+1 to append at the end.
func (w *Workspace) InsertText(rel string, line int, text string) (string, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", err
	}
	total := countLines(content)
	if line < 1 || line > total+1 {
		return "", fmt.Errorf("line %d out of range (1-%d)", line, total+1)
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	switch {
	case total == 0:
		content = text
	case line == total+1:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += text
	default:
		off := lineStartOffset(content, line)
		content = content[:off] + text + content[off:]
	}
	if _, _, err := w.writeFileLocked(w.relPath(full), content); err != nil {
		return "", err
	}
	return w.relPath(full), nil
}

// DeleteLines deletes the inclusive 1-based line range [from, to].
func (w *Workspace) DeleteLines(rel string, from, to int) (string, int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	total := countLines(content)
	if total == 0 {
		return "", 0, errors.New("file is empty")
	}
	if from < 1 || to < from || to > total {
		return "", 0, fmt.Errorf("invalid line range %d-%d (file has %d lines)", from, to, total)
	}
	start := lineStartOffset(content, from)
	end := lineStartOffset(content, to+1)
	if end == -1 {
		end = len(content)
	}
	updated := content[:start] + content[end:]
	if _, _, err := w.writeFileLocked(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), countLines(updated), nil
}

// ReplaceLines replaces the inclusive 1-based line range [from, to] with
// newContent, writing the result atomically via WriteFile.
// Returns the relative path and new total line count.
func (w *Workspace) ReplaceLines(rel string, from, to int, newContent string) (string, int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	full, content, err := w.readAll(rel)
	if err != nil {
		return "", 0, err
	}
	total := countLines(content)
	if total == 0 {
		return "", 0, errors.New("file is empty")
	}
	if from < 1 || to < from || to > total {
		return "", 0, fmt.Errorf("invalid line range %d-%d (file has %d lines)", from, to, total)
	}
	if newContent != "" && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	start := lineStartOffset(content, from)
	end := lineStartOffset(content, to+1)
	if end == -1 {
		end = len(content)
	}
	updated := content[:start] + newContent + content[end:]
	if _, _, err := w.writeFileLocked(w.relPath(full), updated); err != nil {
		return "", 0, err
	}
	return w.relPath(full), countLines(updated), nil
}

// NumberLines formats content with right-aligned line numbers, starting at
// startLine (1-based).
func NumberLines(content string, startLine int) string {
	if content == "" {
		return ""
	}
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", startLine+i, line)
	}
	return b.String()
}
