package gitwrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GitHubRunsOptions filters `gh run list`.
type GitHubRunsOptions struct {
	Workflow string // optional workflow file name or name, e.g. "ci.yml"
	Branch   string // optional branch filter
	Status   string // optional status/conclusion filter: queued, in_progress, completed, success, failure, cancelled
	Limit    int    // max runs to return (default 10, max 50)
}

// GitHubRun summarizes one GitHub Actions workflow run.
type GitHubRun struct {
	DatabaseID   int64  `json:"database_id"`
	Number       int    `json:"number"`
	WorkflowName string `json:"workflow_name"`
	DisplayTitle string `json:"display_title"`
	Event        string `json:"event"`
	HeadBranch   string `json:"head_branch"`
	HeadSha      string `json:"head_sha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion,omitempty"`
	URL          string `json:"url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// GitHubRunsResult reports recent workflow runs.
type GitHubRunsResult struct {
	Runs []GitHubRun `json:"runs"`
}

// GitHubRunViewOptions selects one run to inspect.
type GitHubRunViewOptions struct {
	RunID      int64 // optional run database ID; when <= 0, uses the latest run (optionally filtered by Branch/Workflow)
	Branch     string
	Workflow   string
	FailedLogs bool // include failed step logs (--log-failed)
}

// GitHubJobStep summarizes one step inside a GitHub Actions job.
type GitHubJobStep struct {
	Number     int    `json:"number"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

// GitHubRunJob summarizes one job inside a workflow run.
type GitHubRunJob struct {
	DatabaseID  int64           `json:"database_id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion,omitempty"`
	StartedAt   string          `json:"started_at,omitempty"`
	CompletedAt string          `json:"completed_at,omitempty"`
	Steps       []GitHubJobStep `json:"steps,omitempty"`
}

// GitHubRunDetail reports detailed status, jobs, steps, and optional failed logs for one run.
type GitHubRunDetail struct {
	DatabaseID    int64          `json:"database_id"`
	Number        int            `json:"number"`
	WorkflowName  string         `json:"workflow_name"`
	DisplayTitle  string         `json:"display_title"`
	Event         string         `json:"event"`
	HeadBranch    string         `json:"head_branch"`
	HeadSha       string         `json:"head_sha"`
	Status        string         `json:"status"`
	Conclusion    string         `json:"conclusion,omitempty"`
	URL           string         `json:"url"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	Jobs          []GitHubRunJob `json:"jobs,omitempty"`
	FailedLogs    string         `json:"failed_logs,omitempty"`
	LogsTruncated bool           `json:"logs_truncated,omitempty"`
}

var (
	ghWorkflowName   = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,100}$`)
	allowedRunStatus = map[string]bool{
		"queued":          true,
		"in_progress":     true,
		"completed":       true,
		"requested":       true,
		"waiting":         true,
		"pending":         true,
		"action_required": true,
		"cancelled":       true,
		"failure":         true,
		"neutral":         true,
		"skipped":         true,
		"stale":           true,
		"startup_failure": true,
		"success":         true,
		"timed_out":       true,
	}
)

const maxFailedLogBytes = 32 << 10 // 32 KiB cap so huge build logs don't blow up context

// ListGitHubRuns lists recent GitHub Actions workflow runs for the repository.
func (g *Git) ListGitHubRuns(ctx context.Context, o GitHubRunsOptions) (GitHubRunsResult, error) {
	if err := g.requireRoot(ctx); err != nil {
		return GitHubRunsResult{}, err
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	args := []string{
		"run", "list",
		"--limit", strconv.Itoa(limit),
		"--json", "databaseId,number,workflowName,displayTitle,event,headBranch,headSha,status,conclusion,url,createdAt,updatedAt",
	}

	if wf := strings.TrimSpace(o.Workflow); wf != "" {
		if err := validWorkflow(wf); err != nil {
			return GitHubRunsResult{}, err
		}
		args = append(args, "--workflow", wf)
	}
	if br := strings.TrimSpace(o.Branch); br != "" {
		if err := ValidBranch(br); err != nil {
			return GitHubRunsResult{}, err
		}
		args = append(args, "--branch", br)
	}
	if st := strings.ToLower(strings.TrimSpace(o.Status)); st != "" {
		if !allowedRunStatus[st] {
			return GitHubRunsResult{}, fmt.Errorf("invalid status %q (use queued, in_progress, completed, success, failure, or cancelled)", o.Status)
		}
		args = append(args, "--status", st)
	}

	out, err := g.gh(ctx, args...)
	if err != nil {
		return GitHubRunsResult{}, err
	}

	var raw []struct {
		DatabaseID   int64  `json:"databaseId"`
		Number       int    `json:"number"`
		WorkflowName string `json:"workflowName"`
		DisplayTitle string `json:"displayTitle"`
		Event        string `json:"event"`
		HeadBranch   string `json:"headBranch"`
		HeadSha      string `json:"headSha"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		URL          string `json:"url"`
		CreatedAt    string `json:"createdAt"`
		UpdatedAt    string `json:"updatedAt"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return GitHubRunsResult{}, fmt.Errorf("failed to parse gh run list output: %w", err)
	}

	runs := make([]GitHubRun, 0, len(raw))
	for _, r := range raw {
		sha := r.HeadSha
		if len(sha) > 7 {
			sha = sha[:7]
		}
		runs = append(runs, GitHubRun{
			DatabaseID:   r.DatabaseID,
			Number:       r.Number,
			WorkflowName: r.WorkflowName,
			DisplayTitle: r.DisplayTitle,
			Event:        r.Event,
			HeadBranch:   r.HeadBranch,
			HeadSha:      sha,
			Status:       r.Status,
			Conclusion:   r.Conclusion,
			URL:          r.URL,
			CreatedAt:    r.CreatedAt,
			UpdatedAt:    r.UpdatedAt,
		})
	}
	return GitHubRunsResult{Runs: runs}, nil
}

// ViewGitHubRun inspects a specific GitHub Actions run (or the latest matching run when RunID <= 0),
// returning its jobs, steps, and optionally the logs of failed steps.
func (g *Git) ViewGitHubRun(ctx context.Context, o GitHubRunViewOptions) (GitHubRunDetail, error) {
	if err := g.requireRoot(ctx); err != nil {
		return GitHubRunDetail{}, err
	}
	runID := o.RunID
	if runID < 0 {
		return GitHubRunDetail{}, errors.New("run_id must be positive")
	}
	if runID == 0 {
		list, err := g.ListGitHubRuns(ctx, GitHubRunsOptions{
			Workflow: o.Workflow,
			Branch:   o.Branch,
			Limit:    1,
		})
		if err != nil {
			return GitHubRunDetail{}, err
		}
		if len(list.Runs) == 0 {
			return GitHubRunDetail{}, errors.New("no GitHub Actions workflow runs found")
		}
		runID = list.Runs[0].DatabaseID
	}

	idStr := strconv.FormatInt(runID, 10)
	out, err := g.gh(ctx,
		"run", "view", idStr,
		"--json", "databaseId,number,workflowName,displayTitle,event,headBranch,headSha,status,conclusion,url,createdAt,updatedAt,jobs",
	)
	if err != nil {
		return GitHubRunDetail{}, err
	}

	var raw struct {
		DatabaseID   int64  `json:"databaseId"`
		Number       int    `json:"number"`
		WorkflowName string `json:"workflowName"`
		DisplayTitle string `json:"displayTitle"`
		Event        string `json:"event"`
		HeadBranch   string `json:"headBranch"`
		HeadSha      string `json:"headSha"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		URL          string `json:"url"`
		CreatedAt    string `json:"createdAt"`
		UpdatedAt    string `json:"updatedAt"`
		Jobs         []struct {
			DatabaseID  int64  `json:"databaseId"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion"`
			StartedAt   string `json:"startedAt"`
			CompletedAt string `json:"completedAt"`
			Steps       []struct {
				Number     int    `json:"number"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return GitHubRunDetail{}, fmt.Errorf("failed to parse gh run view output: %w", err)
	}

	sha := raw.HeadSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	detail := GitHubRunDetail{
		DatabaseID:   raw.DatabaseID,
		Number:       raw.Number,
		WorkflowName: raw.WorkflowName,
		DisplayTitle: raw.DisplayTitle,
		Event:        raw.Event,
		HeadBranch:   raw.HeadBranch,
		HeadSha:      sha,
		Status:       raw.Status,
		Conclusion:   raw.Conclusion,
		URL:          raw.URL,
		CreatedAt:    raw.CreatedAt,
		UpdatedAt:    raw.UpdatedAt,
		Jobs:         make([]GitHubRunJob, 0, len(raw.Jobs)),
	}
	for _, j := range raw.Jobs {
		job := GitHubRunJob{
			DatabaseID:  j.DatabaseID,
			Name:        j.Name,
			Status:      j.Status,
			Conclusion:  j.Conclusion,
			StartedAt:   j.StartedAt,
			CompletedAt: j.CompletedAt,
			Steps:       make([]GitHubJobStep, 0, len(j.Steps)),
		}
		for _, st := range j.Steps {
			job.Steps = append(job.Steps, GitHubJobStep{
				Number:     st.Number,
				Name:       st.Name,
				Status:     st.Status,
				Conclusion: st.Conclusion,
			})
		}
		detail.Jobs = append(detail.Jobs, job)
	}

	if o.FailedLogs {
		logsOut, logErr := g.gh(ctx, "run", "view", idStr, "--log-failed")
		if logErr != nil {
			// If the run hasn't failed or has no failed logs yet, record note instead of failing whole view.
			detail.FailedLogs = "(no failed step logs available: " + logErr.Error() + ")"
		} else {
			logsOut = strings.TrimSpace(logsOut)
			if len(logsOut) > maxFailedLogBytes {
				// Keep the tail of failed logs because errors are usually at the bottom.
				logsOut = logsOut[len(logsOut)-maxFailedLogBytes:]
				detail.LogsTruncated = true
			}
			detail.FailedLogs = logsOut
		}
	}

	return detail, nil
}

// RerunGitHubRun triggers a re-run of a GitHub Actions workflow run (all jobs or failed jobs only).
func (g *Git) RerunGitHubRun(ctx context.Context, runID int64, failedOnly bool) (string, error) {
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	if runID <= 0 {
		return "", errors.New("run_id must be a positive workflow run ID")
	}
	args := []string{"run", "rerun", strconv.FormatInt(runID, 10)}
	if failedOnly {
		args = append(args, "--failed")
	}
	if _, err := g.gh(ctx, args...); err != nil {
		return "", err
	}
	if failedOnly {
		return fmt.Sprintf("Triggered re-run of failed jobs for workflow run %d.", runID), nil
	}
	return fmt.Sprintf("Triggered re-run of all jobs for workflow run %d.", runID), nil
}

func validWorkflow(name string) error {
	if strings.HasPrefix(name, "-") || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\\r\n\x00") || !ghWorkflowName.MatchString(name) {
		return fmt.Errorf("invalid workflow %q (pass workflow file name like \"ci.yml\" or workflow name)", name)
	}
	return nil
}
