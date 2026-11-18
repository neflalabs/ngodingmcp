package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExtStats holds aggregate statistics for files sharing one extension.
type ExtStats struct {
	Files int   `json:"files"`
	Lines int   `json:"lines"`
	Bytes int64 `json:"bytes"`
}

// RecentFile pairs a relative path with its modification time.
type RecentFile struct {
	Path    string    `json:"path"`
	ModTime time.Time `json:"modTime"`
}

// LargestFile pairs a relative path with its line count.
type LargestFile struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
}

// WorkspaceStats holds aggregate information about the whole workspace.
type WorkspaceStats struct {
	TotalFiles   int                 `json:"totalFiles"`
	TotalLines   int                 `json:"totalLines"`
	TotalBytes   int64               `json:"totalBytes"`
	ByExtension  map[string]ExtStats `json:"byExtension"`
	LargestFiles []LargestFile       `json:"largestFiles"`
	RecentFiles  []RecentFile        `json:"recentFiles"`
}

// Stats walks the entire workspace and returns aggregate statistics.
// It skips .git/, binary files, and files exceeding maxRead.
func (w *Workspace) Stats() (WorkspaceStats, error) {
	byExt := make(map[string]ExtStats)
	var allLargest []LargestFile
	var allRecent []RecentFile

	var totalFiles int
	var totalLines int
	var totalBytes int64

	err := filepath.WalkDir(w.Root(), func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(w.Root(), p, d); skip {
			return err
		}
		if skipHeavy(w.Root(), p, d) {
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
		info, err := d.Info()
		if err != nil {
			return nil // skip unreadable
		}
		if info.Size() > w.MaxRead() {
			return nil // too large
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if looksBinary(data) {
			return nil
		}

		relp := w.relPath(p)
		ext := filepath.Ext(d.Name())
		if ext == "" {
			ext = "(no ext)"
		}
		lines := countLines(string(data))
		sz := info.Size()

		totalFiles++
		totalLines += lines
		totalBytes += sz

		es := byExt[ext]
		es.Files++
		es.Lines += lines
		es.Bytes += sz
		byExt[ext] = es

		allLargest = append(allLargest, LargestFile{Path: relp, Lines: lines})
		allRecent = append(allRecent, RecentFile{Path: relp, ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return WorkspaceStats{}, err
	}

	// Sort largest files by lines desc, keep top 5.
	sort.Slice(allLargest, func(i, j int) bool {
		return allLargest[i].Lines > allLargest[j].Lines
	})
	if len(allLargest) > 5 {
		allLargest = allLargest[:5]
	}

	// Sort recent files by modTime desc, keep top 5.
	sort.Slice(allRecent, func(i, j int) bool {
		return allRecent[i].ModTime.After(allRecent[j].ModTime)
	})
	if len(allRecent) > 5 {
		allRecent = allRecent[:5]
	}

	return WorkspaceStats{
		TotalFiles:   totalFiles,
		TotalLines:   totalLines,
		TotalBytes:   totalBytes,
		ByExtension:  byExt,
		LargestFiles: allLargest,
		RecentFiles:  allRecent,
	}, nil
}

// TokenCount holds byte/line/token estimates for a single path.
type TokenCount struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	Lines           int    `json:"lines"`
	EstimatedTokens int    `json:"estimatedTokens"`
}

// CountTokens returns size/token estimates for each path in rels.
// Directories are walked recursively and aggregated into a single entry.
func (w *Workspace) CountTokens(rels []string) ([]TokenCount, error) {
	var results []TokenCount
	for _, rel := range rels {
		tc, err := w.countOne(rel)
		if err != nil {
			return nil, err
		}
		results = append(results, tc)
	}
	return results, nil
}

func (w *Workspace) countOne(rel string) (TokenCount, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return TokenCount{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return TokenCount{}, err
	}
	relp := w.relPath(full)

	if !info.IsDir() {
		return w.countFile(full, relp)
	}

	// Directory: walk recursively and aggregate.
	var totalBytes int64
	var totalLines int
	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skip, err := w.skipBlocked(full, p, d); skip {
			return err
		}
		if skipHeavy(full, p, d) {
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
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		sz := fi.Size()
		totalBytes += sz
		// Count lines for non-binary text files within size limit.
		if sz <= w.MaxRead() {
			if data, err := os.ReadFile(p); err == nil && !looksBinary(data) {
				totalLines += countLines(string(data))
			}
		}
		return nil
	})
	if err != nil {
		return TokenCount{}, err
	}
	// Ensure directory path ends with /
	if relp == "" || relp == "." {
		relp = "./"
	} else if !strings.HasSuffix(relp, "/") {
		relp += "/"
	}
	return TokenCount{
		Path:            relp,
		Bytes:           totalBytes,
		Lines:           totalLines,
		EstimatedTokens: int(totalBytes / 4),
	}, nil
}

func (w *Workspace) countFile(full, relp string) (TokenCount, error) {
	info, err := os.Stat(full)
	if err != nil {
		return TokenCount{}, err
	}
	sz := info.Size()
	var lines int
	if sz <= w.MaxRead() {
		if data, err := os.ReadFile(full); err == nil && !looksBinary(data) {
			lines = countLines(string(data))
		}
	}
	return TokenCount{
		Path:            relp,
		Bytes:           sz,
		Lines:           lines,
		EstimatedTokens: int(sz / 4),
	}, nil
}
