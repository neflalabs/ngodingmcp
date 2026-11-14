package gitwrite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ngodingmcp/internal/gitread"
	"ngodingmcp/internal/gitutil"
	"ngodingmcp/internal/workspace"
)

// TestLocalConfigGuardRefusesFilters builds a hostile repository whose
// .git/config names a program in filter.evil.clean / filter.evil.process and
// whose .gitattributes routes a file through that filter. Every git command
// that touches the index or worktree must refuse to run before executing
// the filter.
func TestLocalConfigGuardRefusesFilters(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()

	write(t, root, "evil.txt", "harmless\n")
	gitOut(t, root, "init", "-q")
	gitOut(t, root, "add", "evil.txt")
	marker := filepath.Join(t.TempDir(), "pwned")
	gitOut(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")
	write(t, root, ".gitattributes", "evil.txt filter=evil\n")
	gitOut(t, root, "config", "filter.evil.clean", "touch "+marker+"; cat")
	gitOut(t, root, "config", "filter.evil.process", "touch "+marker+"; cat")

	if _, err := g.Add(ctx, AddOptions{All: true}); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Add err = %v, want refusal", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("filter executed during Add (marker exists)")
	}

	ws, err := workspace.New(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	// gitread must refuse too.
	r := gitread.New(ws)
	if _, err := r.Status(ctx); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Status err = %v, want refusal", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("filter executed during Status (marker exists)")
	}
	if _, err := r.Diff(ctx, gitread.DiffOptions{Ref: "HEAD"}); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Diff err = %v, want refusal", err)
	}
	if _, err := g.Commit(ctx, CommitOptions{Message: "x", AuthorName: "t", AuthorEmail: "t@t"}); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("Commit err = %v, want refusal", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("filter executed during Commit (marker exists)")
	}
}

func TestValidRefRejectsRanges(t *testing.T) {
	for _, ref := range []string{"main..origin/main", "HEAD...dev", "a..b..c", "../x", "-x", ""} {
		if err := validRef(ref); err == nil {
			t.Fatalf("validRef(%q) accepted", ref)
		}
	}
	for _, ref := range []string{"main", "HEAD~1", "origin/feature-x", "v1.2.3", "a/b/c"} {
		if err := validRef(ref); err != nil {
			t.Fatalf("validRef(%q) rejected: %v", ref, err)
		}
	}
}

func TestSanitizeEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/tester",
		"GIT_DIR=/elsewhere",
		"GIT_WORK_TREE=/elsewhere",
		"GIT_INDEX_FILE=/tmp/index",
		"GIT_CONFIG_PARAMETERS='core.sshCommand=ssh evil'",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.sshCommand",
		"GIT_CONFIG_VALUE_0=ssh evil",
		"GIT_EXTERNAL_DIFF=/tmp/evil",
		"GIT_SSH_COMMAND=ssh evil",
		"GIT_ASKPASS=/tmp/evil",
	}
	got := gitutil.SanitizeEnv(env)
	for _, kv := range got {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_CONFIG") || k == "GIT_DIR" || k == "GIT_WORK_TREE" ||
			k == "GIT_INDEX_FILE" || k == "GIT_EXTERNAL_DIFF" || k == "GIT_SSH_COMMAND" || k == "GIT_ASKPASS" {
			t.Fatalf("SanitizeEnv kept %q", kv)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/tester"} {
		found := false
		for _, kv := range got {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("SanitizeEnv dropped %q", want)
		}
	}
}
