package workspace

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// walkFiltered walks base like filepath.WalkDir with the standard bulk-walker
// rules (blocked entries, heavy dirs, .git, symlink escapes) and returns the
// regular files in lexical order — the order WalkDir yields. pred, when set,
// sees each file's slash-separated path relative to the root and lets the
// caller apply extra filters (e.g. an anchored glob).
func (w *Workspace) walkFiltered(base string, pred func(relp string) bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(base, p, d); skip {
			return err
		}
		if skipHeavy(base, p, d) {
			return filepath.SkipDir
		}
		if !d.IsDir() && !w.walkFileSafe(p, d) {
			return nil // symlink escaping the root (or broken link)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if pred != nil && !pred(w.relPath(p)) {
			return nil
		}
		files = append(files, p)
		return nil
	})
	return files, err
}

// readForBulk opens path exactly once and returns its contents plus the
// FileInfo from that same handle, so callers can enforce MaxRead and detect
// binary files without a second stat. ok=false means skip: the file is
// unreadable, larger than MaxRead, or binary.
func (w *Workspace) readForBulk(path string) ([]byte, fs.FileInfo, bool) {
	// Walkers see paths directly and never call resolve. Stat first so a
	// FIFO cannot block the open while search or stats scans the tree.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, false
	}
	if fi.Size() > w.MaxRead() {
		_ = f.Close()
		return nil, nil, false
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return nil, nil, false
	}
	if looksBinary(data) {
		return nil, nil, false
	}
	return data, fi, true
}

// chunkRanges splits n items into contiguous ranges for a worker pool of at
// most 16 workers. Ranges are filled in input order, so workers see and
// produce output in input order.
func chunkRanges(n int) [][2]int {
	if n == 0 {
		return nil
	}
	workers := runtime.NumCPU()
	if workers > n {
		workers = n
	}
	if workers > 16 {
		workers = 16
	}
	chunk := (n + workers - 1) / workers
	var out [][2]int
	for start := 0; start < n; start += chunk {
		end := start + chunk
		if end > n {
			end = n
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// mapFiles runs fn on each file concurrently and returns the results in the
// same order as files, using statically partitioned chunks so no locks are
// needed.
func mapFiles[T any](files []string, fn func(fullPath string) T) []T {
	results := make([]T, len(files))
	var wg sync.WaitGroup
	for _, r := range chunkRanges(len(files)) {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := r[0]; i < r[1]; i++ {
				results[i] = fn(files[i])
			}
		}()
	}
	wg.Wait()
	return results
}

// mapFilesUntil is mapFiles with a per-chunk budget: fn receives the
// remaining budget and returns both its result and how many budget units it
// consumed (e.g. matches found, or 1 when the file matched). A chunk stops
// once its budget is exhausted; files not reached keep the zero value. Bounds
// both wasted work and produced values; output order is deterministic.
func mapFilesUntil[T any](files []string, budget int, fn func(fullPath string, budget int) (T, int)) []T {
	results := make([]T, len(files))
	if budget <= 0 {
		return results
	}
	var wg sync.WaitGroup
	for _, r := range chunkRanges(len(files)) {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := budget
			for i := r[0]; i < r[1] && local > 0; i++ {
				v, used := fn(files[i], local)
				results[i] = v
				local -= used
			}
		}()
	}
	wg.Wait()
	return results
}
