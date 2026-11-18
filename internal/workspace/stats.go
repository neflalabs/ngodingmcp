package workspace

import (
	"io"
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
	files, err := w.walkFiltered(w.Root(), nil)
	if err != nil {
		return WorkspaceStats{}, err
	}
	type rec struct {
		ok    bool
		rel   string
		ext   string
		lines int
		sz    int64
		mod   time.Time
	}
	results := mapFiles(files, func(p string) rec {
		data, fi, ok := w.readForBulk(p)
		if !ok {
			return rec{}
		}
		rel := w.relPath(p)
		ext := filepath.Ext(rel)
		if ext == "" {
			ext = "(no ext)"
		}
		return rec{
			ok:    true,
			rel:   rel,
			ext:   ext,
			lines: countLines(string(data)),
			sz:    fi.Size(),
			mod:   fi.ModTime(),
		}
	})

	byExt := make(map[string]ExtStats)
	var allLargest []LargestFile
	var allRecent []RecentFile
	var totalFiles int
	var totalLines int
	var totalBytes int64

	for _, r := range results {
		if !r.ok {
			continue
		}
		totalFiles++
		totalLines += r.lines
		totalBytes += r.sz
		es := byExt[r.ext]
		es.Files++
		es.Lines += r.lines
		es.Bytes += r.sz
		byExt[r.ext] = es
		allLargest = append(allLargest, LargestFile{Path: r.rel, Lines: r.lines})
		allRecent = append(allRecent, RecentFile{Path: r.rel, ModTime: r.mod})
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
	files, err := w.walkFiltered(full, nil)
	if err != nil {
		return TokenCount{}, err
	}
	type rec struct {
		sz    int64
		lines int
	}
	totals := mapFiles(files, func(p string) rec {
		// Sum sizes for all files (binary or not), count lines only for
		// text within the read limit — mirror the original sequential semantics.
		f, err := os.Open(p)
		if err != nil {
			return rec{}
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return rec{}
		}
		sz := fi.Size()
		var lines int
		if sz <= w.MaxRead() {
			data, err := io.ReadAll(f)
			if err == nil && !looksBinary(data) {
				lines = countLines(string(data))
			}
		}
		_ = f.Close()
		return rec{sz: sz, lines: lines}
	})
	for _, r := range totals {
		totalBytes += r.sz
		totalLines += r.lines
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
