//go:build unix

package gitwrite

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestGitHubActionsRunsAndView(t *testing.T) {
	g, _ := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}

	log := fakeGH(t, `
case "$1 $2" in
  "run list")
    cat <<'JSON'
[{"databaseId":987654321,"number":42,"workflowName":"CI","displayTitle":"feat: add actions","event":"push","headBranch":"main","headSha":"abcdef1234567890","status":"completed","conclusion":"failure","url":"https://example.invalid/actions/runs/987654321","createdAt":"2026-11-19T00:00:00Z","updatedAt":"2026-11-19T00:01:00Z"}]
JSON
    ;;
  "run view")
    if [ "$4" = "--log-failed" ]; then
      echo "FAIL: TestBroken (0.01s)"
    else
      cat <<'JSON'
{"databaseId":987654321,"number":42,"workflowName":"CI","displayTitle":"feat: add actions","event":"push","headBranch":"main","headSha":"abcdef1234567890","status":"completed","conclusion":"failure","url":"https://example.invalid/actions/runs/987654321","createdAt":"2026-11-19T00:00:00Z","updatedAt":"2026-11-19T00:01:00Z","jobs":[{"databaseId":111,"name":"check","status":"completed","conclusion":"failure","startedAt":"2026-11-19T00:00:05Z","completedAt":"2026-11-19T00:00:55Z","steps":[{"number":1,"name":"Checkout","status":"completed","conclusion":"success"},{"number":2,"name":"test","status":"completed","conclusion":"failure"}]}]}
JSON
    fi
    ;;
  "run rerun")
    echo "Requested rerun"
    ;;
  *)
    echo "unexpected args: $@" >&2
    exit 1
    ;;
esac
`)

	// 1. List runs with filters
	list, err := g.ListGitHubRuns(ctx, GitHubRunsOptions{
		Workflow: "ci.yml",
		Branch:   "main",
		Status:   "failure",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("ListGitHubRuns: %v", err)
	}
	if len(list.Runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(list.Runs))
	}
	r := list.Runs[0]
	if r.DatabaseID != 987654321 || r.Number != 42 || r.HeadSha != "abcdef1" || r.Conclusion != "failure" {
		t.Fatalf("run = %+v", r)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "--workflow\nci.yml\n--branch\nmain\n--status\nfailure\n") {
		t.Fatalf("gh list args = %q", b)
	}

	// 2. View latest run (runID == 0 resolves latest run ID first) + failed logs
	detail, err := g.ViewGitHubRun(ctx, GitHubRunViewOptions{
		Workflow:   "ci.yml",
		Branch:     "main",
		FailedLogs: true,
	})
	if err != nil {
		t.Fatalf("ViewGitHubRun: %v", err)
	}
	if detail.DatabaseID != 987654321 || len(detail.Jobs) != 1 || len(detail.Jobs[0].Steps) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if !strings.Contains(detail.FailedLogs, "FAIL: TestBroken") {
		t.Fatalf("failed logs = %q", detail.FailedLogs)
	}

	// 3. Rerun failed jobs
	msg, err := g.RerunGitHubRun(ctx, 987654321, true)
	if err != nil {
		t.Fatalf("RerunGitHubRun: %v", err)
	}
	if !strings.Contains(msg, "failed jobs") || !strings.Contains(msg, "987654321") {
		t.Fatalf("rerun msg = %q", msg)
	}
	b, _ = os.ReadFile(log)
	if !strings.HasPrefix(string(b), "run\nrerun\n987654321\n--failed\n") {
		t.Fatalf("gh rerun args = %q", b)
	}
}

func TestGitHubActionsValidation(t *testing.T) {
	log := fakeGH(t, `echo "[]"`)
	g, _ := newGit(t)
	ctx := context.Background()
	if _, err := g.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}

	if _, err := g.ListGitHubRuns(ctx, GitHubRunsOptions{Workflow: "../evil.yml"}); err == nil {
		t.Error("invalid workflow accepted")
	}
	if _, err := g.ListGitHubRuns(ctx, GitHubRunsOptions{Branch: "--flag"}); err == nil {
		t.Error("invalid branch accepted")
	}
	if _, err := g.ListGitHubRuns(ctx, GitHubRunsOptions{Status: "bogus"}); err == nil {
		t.Error("invalid status accepted")
	}
	if _, err := g.ViewGitHubRun(ctx, GitHubRunViewOptions{RunID: -5}); err == nil {
		t.Error("negative run_id accepted")
	}
	if _, err := g.RerunGitHubRun(ctx, 0, false); err == nil {
		t.Error("zero run_id accepted for rerun")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("gh executed despite invalid inputs")
	}
}
