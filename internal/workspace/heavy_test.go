package workspace

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeavyDirsSkippedByBulkWalkers(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "src/a.js", "needle\n")
	mustWrite(t, ws, "dist/b.js", "needle\n") // dist is a normal folder: not skipped
	mustWrite(t, ws, "node_modules/pkg/c.js", "needle\n")
	mustWrite(t, ws, "app/node_modules/pkg/d.js", "needle\n")

	got, err := ws.SearchText("", "needle", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 hits (src, dist), got %+v", got)
	}

	// Pointing straight at a heavy dir still searches it.
	got, err = ws.SearchText("node_modules", "needle", "", 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("explicit node_modules search: %v %+v", err, got)
	}

	st, err := ws.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalFiles != 2 {
		t.Fatalf("stats should count 2 files, got %d", st.TotalFiles)
	}

	res, err := ws.SearchReplace("", "needle", "x", "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("replace should touch 2 files, got %+v", res)
	}
}

func TestSearchTextClipsLongLines(t *testing.T) {
	ws := newTestWorkspace(t)
	long := strings.Repeat("é", 5000) + "NEEDLE" + strings.Repeat("ü", 5000)
	mustWrite(t, ws, "min.js", long+"\n")
	got, err := ws.SearchText("", "NEEDLE", "", 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	txt := got[0].Text
	if len(txt) > MaxMatchTextBytes+8 {
		t.Fatalf("not clipped: %d bytes", len(txt))
	}
	if !strings.Contains(txt, "NEEDLE") || !utf8.ValidString(txt) {
		t.Fatalf("match lost or invalid utf8: %q", txt)
	}
	if !strings.HasPrefix(txt, "…") || !strings.HasSuffix(txt, "…") {
		t.Fatalf("missing ellipses: %q", txt)
	}
}

func TestListFilesRecursiveSkipsHeavyAndCaps(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "src/a.js", "x\n")
	mustWrite(t, ws, "node_modules/pkg/c.js", "x\n")

	// Skip heavy dirs entirely.
	entries, truncated, err := ws.ListFiles("", true)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncation for a small tree")
	}
	for _, e := range entries {
		if strings.Contains(e.Path, "node_modules") {
			t.Fatalf("listing leaked %s", e.Path)
		}
	}

	// Explicit path at a heavy dir still lists it.
	entries, _, err = ws.ListFiles("node_modules", true)
	if err != nil || len(entries) == 0 {
		t.Fatalf("explicit node_modules listing: %v %+v", err, entries)
	}
}

func TestListFilesRecursiveTruncated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the 2000-entry stress test in -short mode")
	}
	ws := newTestWorkspace(t)
	for i := 0; i < maxListFilesEntries+10; i++ {
		mustWrite(t, ws, fmt.Sprintf("f%04d.txt", i), "x\n")
	}
	entries, truncated, err := ws.ListFiles("", true)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected truncated=true past the cap")
	}
	if len(entries) != maxListFilesEntries {
		t.Fatalf("entries = %d, want %d", len(entries), maxListFilesEntries)
	}
}
