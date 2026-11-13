package gitwrite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noProxy keeps proxy settings of the machine running the tests away from
// the local test servers.
func noProxy(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
}

// commitFile writes rel and commits it with plain git.
func commitFile(t *testing.T, root, rel, content, msg string) {
	t.Helper()
	write(t, root, rel, content)
	gitOut(t, root, "add", "--", rel)
	gitOut(t, root, "commit", "-q", "-m", msg)
}

// newRemoteSetup returns a workspace repo with one commit on main, a bare
// remote added as origin (not pushed yet) and the bare repo path.
func newRemoteSetup(t *testing.T) (*Git, string, string) {
	t.Helper()
	g, root := newGit(t)
	g.allowLocal = true
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, root, "a.txt", "one\n", "first")
	bare := t.TempDir()
	gitOut(t, bare, "init", "-q", "--bare")
	gitOut(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, err := g.AddRemote(ctx, "origin", bare); err != nil {
		t.Fatal(err)
	}
	return g, root, bare
}

// cloneOther clones bare into a second working copy ("another developer").
func cloneOther(t *testing.T, bare string) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "clone", "-q", bare, "other")
	return filepath.Join(dir, "other")
}

func TestRemoteManage(t *testing.T) {
	g, _ := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddRemote(ctx, "origin", "https://github.com/neflalabs/ngodingmcp.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddRemote(ctx, "origin", "https://github.com/a/b.git"); err == nil {
		t.Fatal("adding an existing remote should fail")
	}
	if _, err := g.AddRemote(ctx, "mirror", "git@github.com:neflalabs/ngodingmcp.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetRemoteURL(ctx, "mirror", "ssh://git@github.com:22/neflalabs/ngodingmcp.git"); err != nil {
		t.Fatal(err)
	}
	list, err := g.Remotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "mirror" || list[0].FetchURL != "ssh://git@github.com:22/neflalabs/ngodingmcp.git" ||
		list[1].Name != "origin" || list[1].FetchURL != "https://github.com/neflalabs/ngodingmcp.git" {
		t.Fatalf("remotes = %+v", list)
	}
	if _, err := g.RemoveRemote(ctx, "mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RemoveRemote(ctx, "mirror"); err == nil {
		t.Fatal("removing a missing remote should fail")
	}
	if _, err := g.SetRemoteURL(ctx, "nope", "https://github.com/a/b.git"); err == nil {
		t.Fatal("set_url on a missing remote should fail")
	}
	for _, name := range []string{"", "-x", ".x", "a b", "a/b", "../x", "a..b", "x.lock"} {
		if _, err := g.AddRemote(ctx, name, "https://github.com/a/b.git"); err == nil {
			t.Errorf("remote name %q accepted", name)
		}
	}
}

func TestRemoteURLRefused(t *testing.T) {
	g, _ := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"file:///tmp/repo.git",
		"/tmp/repo.git",
		"./repo",
		"../repo",
		"ext::sh -c touch% /tmp/pwned",
		"ext::sh",
		"fd::3",
		"git://github.com/a/b.git",
		"http://github.com/a/b.git",
		"ftp://example.com/a.git",
		"https://user:secret@github.com/a/b.git",
		"https://ghp_token123@github.com/a/b.git",
		"ssh://user:pw@github.com/a/b.git",
		"ssh://-oProxyCommand=touch%20x/a.git",
		"-oProxyCommand=touch x",
		"git@github.com:-oProxyCommand",
		"https://github.com/",
		"https://github.com/a/b.git?x=1",
		"git@github.com:a/b.git\nfoo",
		"",
	}
	for _, u := range bad {
		_, err := g.AddRemote(ctx, "r", u)
		if err == nil {
			t.Errorf("url %q accepted", u)
			continue
		}
		for _, secret := range []string{"secret", "ghp_token123", ":pw@"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error for %q leaks credentials: %v", u, err)
			}
		}
	}
	list, err := g.Remotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("refused URLs left remotes behind: %+v", list)
	}
}

func TestScrubCredentials(t *testing.T) {
	cases := map[string]string{
		"https://user:secret@github.com/a/b.git":             "https://***@github.com/a/b.git",
		"fatal: unable to access 'https://tok@host/x/': 403": "fatal: unable to access 'https://***@host/x/': 403",
		"ssh://git@github.com/a/b.git":                       "ssh://git@github.com/a/b.git",
		"ssh://git:pw@github.com/a/b.git":                    "ssh://***@github.com/a/b.git",
		"git@github.com:a/b.git":                             "git@github.com:a/b.git",
		"https://github.com/a/b.git":                         "https://github.com/a/b.git",
		"HTTP://u:p@x.y/z and https://t@x.y/w":               "HTTP://***@x.y/z and https://***@x.y/w",
	}
	for in, want := range cases {
		if got := ScrubCredentials(in); got != want {
			t.Errorf("ScrubCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemotesScrubsExistingCredentials(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "remote", "add", "origin", "https://user:s3cr3t@example.com/r.git")
	list, err := g.Remotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || strings.Contains(list[0].FetchURL, "s3cr3t") || list[0].FetchURL != "https://***@example.com/r.git" {
		t.Fatalf("remotes = %+v", list)
	}
}

func TestFetchPullPush(t *testing.T) {
	g, root, bare := newRemoteSetup(t)
	ctx := context.Background()

	res, err := g.Push(ctx, PushOptions{SetUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "created remote branch" || res.Remote != "origin" || res.RemoteBranch != "main" || !res.UpstreamSet {
		t.Fatalf("first push = %+v", res)
	}
	if got := gitOut(t, root, "config", "branch.main.remote"); got != "origin" {
		t.Fatalf("upstream remote = %q", got)
	}
	if gitOut(t, bare, "rev-parse", "main") != gitOut(t, root, "rev-parse", "HEAD") {
		t.Fatal("remote main does not match local HEAD after push")
	}
	if res, err = g.Push(ctx, PushOptions{}); err != nil || res.Status != "up to date" {
		t.Fatalf("second push = %+v, %v", res, err)
	}

	other := cloneOther(t, bare)
	commitFile(t, other, "b.txt", "from other\n", "other work")
	gitOut(t, other, "push", "-q", "origin", "main")

	fr, err := g.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fr.UpToDate || fr.Remote != "origin" || len(fr.Updates) == 0 {
		t.Fatalf("fetch = %+v", fr)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("fetch must not touch the working tree")
	}

	pr, err := g.Pull(ctx, PullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.UpToDate || pr.Commits != 1 || pr.From == "" || pr.To == "" || pr.From == pr.To {
		t.Fatalf("pull = %+v", pr)
	}
	if b, err := os.ReadFile(filepath.Join(root, "b.txt")); err != nil || string(b) != "from other\n" {
		t.Fatalf("b.txt after pull = %q, %v", b, err)
	}
	if pr, err = g.Pull(ctx, PullOptions{}); err != nil || !pr.UpToDate {
		t.Fatalf("second pull = %+v, %v", pr, err)
	}

	// A new branch pushed under another remote name.
	gitOut(t, root, "switch", "-q", "-c", "feature")
	commitFile(t, root, "c.txt", "c\n", "feature work")
	res, err = g.Push(ctx, PushOptions{RemoteBranch: "review/feature"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "created remote branch" || res.RemoteBranch != "review/feature" || res.UpstreamSet {
		t.Fatalf("feature push = %+v", res)
	}
	if gitOut(t, bare, "rev-parse", "review/feature") != gitOut(t, root, "rev-parse", "HEAD") {
		t.Fatal("review/feature does not match local feature")
	}
	// Tags are never pushed.
	gitOut(t, root, "tag", "v1")
	if _, err := g.Push(ctx, PushOptions{RemoteBranch: "review/feature"}); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, bare, "tag", "--list"); out != "" {
		t.Fatalf("tags reached the remote: %q", out)
	}
}

func TestPushNeverForcesAndPullOnlyFastForwards(t *testing.T) {
	g, root, bare := newRemoteSetup(t)
	ctx := context.Background()
	if _, err := g.Push(ctx, PushOptions{SetUpstream: true}); err != nil {
		t.Fatal(err)
	}
	other := cloneOther(t, bare)
	commitFile(t, other, "b.txt", "other\n", "other work")
	gitOut(t, other, "push", "-q", "origin", "main")
	remoteHead := gitOut(t, bare, "rev-parse", "main")

	commitFile(t, root, "c.txt", "local\n", "local work")
	localHead := gitOut(t, root, "rev-parse", "HEAD")

	_, err := g.Push(ctx, PushOptions{})
	if err == nil || !strings.Contains(err.Error(), "non-fast-forward") || !strings.Contains(err.Error(), "Force push is not available") {
		t.Fatalf("diverged push err = %v", err)
	}
	if gitOut(t, bare, "rev-parse", "main") != remoteHead {
		t.Fatal("rejected push changed the remote")
	}

	_, err = g.Pull(ctx, PullOptions{})
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("diverged pull err = %v", err)
	}
	if gitOut(t, root, "rev-parse", "HEAD") != localHead {
		t.Fatal("refused pull moved HEAD")
	}

	// The documented way out: merge the remote branch, then push.
	mr, err := g.Merge(ctx, MergeOptions{Ref: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if mr.UpToDate || mr.FastForward {
		t.Fatalf("merge = %+v", mr)
	}
	if res, err := g.Push(ctx, PushOptions{}); err != nil || res.Status != "pushed" || res.Summary == "" {
		t.Fatalf("push after merge = %+v, %v", res, err)
	}
	if gitOut(t, bare, "rev-parse", "main") != gitOut(t, root, "rev-parse", "HEAD") {
		t.Fatal("remote main does not match local HEAD after merge + push")
	}
}

func TestPullRefusesWhenLocalChangesWouldBeOverwritten(t *testing.T) {
	g, root, bare := newRemoteSetup(t)
	ctx := context.Background()
	if _, err := g.Push(ctx, PushOptions{SetUpstream: true}); err != nil {
		t.Fatal(err)
	}
	other := cloneOther(t, bare)
	commitFile(t, other, "a.txt", "two\n", "edit a")
	gitOut(t, other, "push", "-q", "origin", "main")
	write(t, root, "a.txt", "my edit\n")
	_, err := g.Pull(ctx, PullOptions{})
	if err == nil || !strings.Contains(err.Error(), "commit your changes first") {
		t.Fatalf("pull over local edit err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "my edit\n" {
		t.Fatalf("local edit lost: %q", b)
	}
}

func TestMerge(t *testing.T) {
	g, root := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, root, "a.txt", "one\n", "first")
	gitOut(t, root, "switch", "-q", "-c", "feature")
	commitFile(t, root, "a.txt", "feature\n", "feature edit")
	gitOut(t, root, "switch", "-q", "main")
	commitFile(t, root, "a.txt", "main\n", "main edit")
	head := gitOut(t, root, "rev-parse", "HEAD")

	_, err := g.Merge(ctx, MergeOptions{Ref: "feature"})
	var conflict *MergeConflictError
	if !errors.As(err, &conflict) || len(conflict.Files) != 1 || conflict.Files[0] != "a.txt" {
		t.Fatalf("conflicting merge err = %v", err)
	}
	if exec.Command("git", "-C", root, "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil {
		t.Fatal("merge was left half-done (MERGE_HEAD exists)")
	}
	if gitOut(t, root, "rev-parse", "HEAD") != head || gitOut(t, root, "status", "--porcelain") != "" {
		t.Fatal("aborted merge changed the branch or the working tree")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "main\n" {
		t.Fatalf("a.txt after aborted merge = %q", b)
	}

	if _, err := g.Merge(ctx, MergeOptions{Ref: "feature", FFOnly: true}); err == nil || !strings.Contains(err.Error(), "cannot fast-forward") {
		t.Fatalf("ff_only on diverged err = %v", err)
	}
	write(t, root, "a.txt", "dirty\n")
	if _, err := g.Merge(ctx, MergeOptions{Ref: "feature"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("merge on dirty tree err = %v", err)
	}
	write(t, root, "a.txt", "main\n")
	if _, err := g.Merge(ctx, MergeOptions{Ref: "nope"}); err == nil {
		t.Fatal("merging an unknown ref should fail")
	}
	if _, err := g.Merge(ctx, MergeOptions{Ref: "--abort"}); err == nil {
		t.Fatal("option-like ref accepted")
	}

	// A clean three-way merge with a custom message.
	gitOut(t, root, "switch", "-q", "-c", "side")
	commitFile(t, root, "b.txt", "b\n", "side work")
	gitOut(t, root, "switch", "-q", "main")
	commitFile(t, root, "c.txt", "c\n", "main work")
	mr, err := g.Merge(ctx, MergeOptions{Ref: "side", Message: "Merge side"})
	if err != nil {
		t.Fatal(err)
	}
	if mr.UpToDate || mr.FastForward || gitOut(t, root, "log", "-1", "--format=%s") != "Merge side" {
		t.Fatalf("merge = %+v", mr)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal("merged file missing")
	}
	if mr, err = g.Merge(ctx, MergeOptions{Ref: "side"}); err != nil || !mr.UpToDate {
		t.Fatalf("repeat merge = %+v, %v", mr, err)
	}

	// Fast-forward.
	gitOut(t, root, "switch", "-q", "-c", "ahead")
	commitFile(t, root, "d.txt", "d\n", "ahead work")
	gitOut(t, root, "switch", "-q", "main")
	if mr, err = g.Merge(ctx, MergeOptions{Ref: "ahead", FFOnly: true}); err != nil || !mr.FastForward {
		t.Fatalf("ff merge = %+v, %v", mr, err)
	}
}

func TestRemoteRefusesUntrustedLocalConfig(t *testing.T) {
	cases := [][2]string{
		{"core.sshCommand", "touch pwned"},
		{"credential.helper", "!touch pwned"},
		{"credential.https://github.com.helper", "!touch pwned"},
		{"url.ext::sh -c touch% pwned.insteadOf", "https://github.com/"},
		{"remote.origin.uploadpack", "touch pwned"},
		{"filter.x.smudge", "touch pwned"},
		{"merge.x.driver", "touch pwned"},
		{"include.path", "/tmp/evil.config"},
	}
	for _, c := range cases {
		t.Run(c[0], func(t *testing.T) {
			g, root, _ := newRemoteSetup(t)
			ctx := context.Background()
			gitOut(t, root, "config", "--local", c[0], c[1])
			for name, run := range map[string]func() error{
				"fetch": func() error { _, err := g.Fetch(ctx, FetchOptions{}); return err },
				"pull":  func() error { _, err := g.Pull(ctx, PullOptions{}); return err },
				"push":  func() error { _, err := g.Push(ctx, PushOptions{}); return err },
				"merge": func() error { _, err := g.Merge(ctx, MergeOptions{Ref: "main"}); return err },
			} {
				if err := run(); err == nil || !strings.Contains(err.Error(), "refusing to run") {
					t.Errorf("%s with %s: err = %v", name, c[0], err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "pwned")); err == nil {
				t.Fatal("a program from .git/config ran")
			}
		})
	}
}

func TestLocalTransportBlocked(t *testing.T) {
	g, root, bare := newRemoteSetup(t)
	g.allowLocal = false
	ctx := context.Background()
	before := gitOut(t, root, "rev-parse", "HEAD")
	_, err := g.Fetch(ctx, FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "not allowed") || !strings.Contains(err.Error(), "only https and ssh") {
		t.Fatalf("fetch over file transport err = %v", err)
	}
	if _, err := g.Push(ctx, PushOptions{}); err == nil {
		t.Fatal("push over file transport should fail")
	}
	if out := gitOut(t, bare, "for-each-ref"); out != "" || gitOut(t, root, "rev-parse", "HEAD") != before {
		t.Fatalf("blocked transport still changed something: %q", out)
	}
}

func TestHTTPSAuthFailsFastWithoutPromptAndScrubs(t *testing.T) {
	noProxy(t)
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	g, root := newGit(t)
	g.remoteTimeout = 20 * time.Second
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, root, "a.txt", "one\n", "first")
	u := strings.Replace(srv.URL, "https://", "https://tok3n@", 1) + "/r.git"
	gitOut(t, root, "remote", "add", "origin", u)

	start := time.Now()
	_, err := g.Fetch(ctx, FetchOptions{})
	if err == nil {
		t.Fatal("fetch from a server that wants a password should fail")
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("fetch took %s; it should fail fast without prompting", time.Since(start))
	}
	if strings.Contains(err.Error(), "tok3n") {
		t.Fatalf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "credential helper") {
		t.Fatalf("error has no auth hint: %v", err)
	}
	if _, err := g.Push(ctx, PushOptions{}); err == nil || strings.Contains(err.Error(), "tok3n") {
		t.Fatalf("push err = %v", err)
	}
}

func TestRemoteTimeoutKillsStuckConnection(t *testing.T) {
	noProxy(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() { close(done); ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and never answer.
			go func() { <-done; c.Close() }()
		}
	}()

	g, root := newGit(t)
	g.remoteTimeout = 2 * time.Second
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "remote", "add", "origin", "https://"+ln.Addr().String()+"/r.git")
	start := time.Now()
	_, err = g.Fetch(ctx, FetchOptions{})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not finish in time") {
		t.Fatalf("stuck fetch err = %v", err)
	}
	if time.Since(start) > 12*time.Second {
		t.Fatalf("stuck fetch returned after %s", time.Since(start))
	}
}

func TestRemoteHint(t *testing.T) {
	cases := map[string]string{
		"git fetch: fatal: could not read Username for 'https://github.com': terminal prompts disabled": "gh auth setup-git",
		"git push: git@github.com: Permission denied (publickey).":                                      "SSH_AUTH_SOCK",
		"git fetch: Host key verification failed.":                                                      "known_hosts",
		"git fetch: fatal: transport 'file' not allowed":                                                "only https and ssh",
		"git push timed out after 2m0s":                                                                 "never prompts",
	}
	for msg, want := range cases {
		if h := remoteHint(msg); !strings.Contains(h, want) {
			t.Errorf("remoteHint(%q) = %q, want it to mention %q", msg, h, want)
		}
	}
}
