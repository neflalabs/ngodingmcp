//go:build unix

package gitwrite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGH puts a gh script on PATH that records its arguments and
// environment and then runs body.
func fakeGH(t *testing.T, body string) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "args.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + logFile + "'\necho \"PROMPT=$GH_PROMPT_DISABLED\" >> '" + logFile + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func TestCreateGitHubRepo(t *testing.T) {
	log := fakeGH(t, `echo "https://github.com/nefla/demo"`)
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	res, err := g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo", Description: "Demo repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/nefla/demo" || res.Visibility != "private" || !res.RemoteAdded || res.Remote != "origin" {
		t.Fatalf("result = %+v", res)
	}
	if got := gitOut(t, root, "remote", "get-url", "origin"); got != "https://github.com/nefla/demo.git" {
		t.Fatalf("origin = %q", got)
	}
	b, _ := os.ReadFile(log)
	if want := "repo\ncreate\ndemo\n--private\n--description\nDemo repo\nPROMPT=1\n"; string(b) != want {
		t.Fatalf("gh args = %q, want %q", b, want)
	}

	// Existing remote: refused before gh runs.
	os.Remove(log)
	if _, err := g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo2"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing remote err = %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("gh ran although the remote already existed")
	}

	// Organisation, public, no remote.
	res, err = g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo", Owner: "neflalabs", Visibility: "public", SkipRemote: true})
	if err != nil || res.RemoteAdded {
		t.Fatalf("skip_remote = %+v, %v", res, err)
	}
	b, _ = os.ReadFile(log)
	if !strings.HasPrefix(string(b), "repo\ncreate\nneflalabs/demo\n--public\n") {
		t.Fatalf("gh args = %q", b)
	}
}

func TestCreateGitHubRepoValidation(t *testing.T) {
	log := fakeGH(t, `echo "https://github.com/nefla/x"`)
	g, _ := newGit(t)
	ctx := context.Background()
	bad := []GitHubRepoOptions{
		{Name: ""},
		{Name: "-x", SkipRemote: true},
		{Name: "a/b", SkipRemote: true},
		{Name: "..", SkipRemote: true},
		{Name: "ok", Owner: "bad/owner", SkipRemote: true},
		{Name: "ok", Visibility: "secret", SkipRemote: true},
		{Name: "ok", Description: "two\nlines", SkipRemote: true},
		{Name: "ok"}, // workspace is not a repository yet
	}
	for _, o := range bad {
		if _, err := g.CreateGitHubRepo(ctx, o); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("gh ran for invalid options")
	}
}

func TestCreateGitHubRepoErrors(t *testing.T) {
	fakeGH(t, `echo "To get started with GitHub CLI, please run:  gh auth login" >&2; echo "token ghp_abc https://x:ghp_abc@github.com" >&2; exit 4`)
	g, _ := newGit(t)
	ctx := context.Background()
	_, err := g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo", SkipRemote: true})
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") || strings.Contains(err.Error(), "x:ghp_abc@") {
		t.Fatalf("auth err = %v", err)
	}

	fakeGH(t, `sleep 30`)
	g.remoteTimeout = time.Second
	start := time.Now()
	if _, err := g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo", SkipRemote: true}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("stuck gh err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("stuck gh returned after %s", time.Since(start))
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := g.CreateGitHubRepo(ctx, GitHubRepoOptions{Name: "demo", SkipRemote: true}); err != ErrNoGH {
		t.Fatalf("missing gh err = %v", err)
	}
}
