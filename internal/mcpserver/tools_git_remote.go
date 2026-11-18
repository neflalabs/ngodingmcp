package mcpserver

// Git remote tools, always registered with the other git tools:
// git_remote, git_fetch, git_pull, git_push, git_merge and
// git_github_create_repo (internal/gitwrite remote.go and github.go).
//
// push never forces and never pushes tags or deletes remote branches, pull
// only fast-forwards, merge aborts itself on conflict, and network commands
// never prompt for a password: they use the user's own ssh-agent, ssh keys,
// global git credential helper or gh login, and fail fast with a hint
// otherwise.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/gitwrite"
)

type gitRemoteArgs struct {
	Action string `json:"action,omitempty" jsonschema:"list (default), add, set_url or remove"`
	Name   string `json:"name,omitempty" jsonschema:"remote name for add, set_url and remove, e.g. origin"`
	URL    string `json:"url,omitempty" jsonschema:"remote URL for add and set_url: https://host/owner/repo.git (no token in it) or ssh (git@host:owner/repo.git, ssh://git@host/owner/repo.git)"`
}

type gitRemoteOut struct {
	Message string            `json:"message"`
	Remotes []gitwrite.Remote `json:"remotes,omitempty"`
}

type gitFetchArgs struct {
	Remote string `json:"remote,omitempty" jsonschema:"remote to fetch (default: the current branch's upstream remote, else origin)"`
	Prune  bool   `json:"prune,omitempty" jsonschema:"delete remote-tracking branches whose branch is gone on the remote"`
}

type gitPullArgs struct {
	Remote string `json:"remote,omitempty" jsonschema:"remote to pull from (default: upstream remote, else origin)"`
	Branch string `json:"branch,omitempty" jsonschema:"remote branch (default: upstream branch, else the current branch name)"`
}

type gitPushArgs struct {
	Remote       string `json:"remote,omitempty" jsonschema:"remote to push to (default: the branch's upstream remote, else origin)"`
	Branch       string `json:"branch,omitempty" jsonschema:"local branch to push (default: the current branch)"`
	RemoteBranch string `json:"remote_branch,omitempty" jsonschema:"branch name on the remote (default: the upstream branch, else the same name)"`
	SetUpstream  bool   `json:"set_upstream,omitempty" jsonschema:"remember remote/remote_branch as the branch's upstream (use on the first push of a branch)"`
}

type gitMergeArgs struct {
	Ref     string `json:"ref" jsonschema:"branch, remote branch (origin/main, after git_fetch) or commit to merge into the current branch"`
	FFOnly  bool   `json:"ff_only,omitempty" jsonschema:"only fast-forward; refuse to create a merge commit"`
	Message string `json:"message,omitempty" jsonschema:"optional merge commit message"`
}

type gitHubCreateRepoArgs struct {
	Name        string `json:"name" jsonschema:"new repository name, e.g. ngodingmcp"`
	Owner       string `json:"owner,omitempty" jsonschema:"optional organisation; default is the account gh is logged in as"`
	Visibility  string `json:"visibility,omitempty" jsonschema:"private (default), public or internal"`
	Description string `json:"description,omitempty" jsonschema:"optional one-line description"`
	Remote      string `json:"remote,omitempty" jsonschema:"remote name to add for the new repository (default origin)"`
	SkipRemote  bool   `json:"skip_remote,omitempty" jsonschema:"only create the repository on GitHub; do not add a remote"`
}

type gitHubRunsArgs struct {
	Workflow string `json:"workflow,omitempty" jsonschema:"optional workflow file name or name, e.g. ci.yml"`
	Branch   string `json:"branch,omitempty" jsonschema:"optional branch name to filter runs"`
	Status   string `json:"status,omitempty" jsonschema:"optional status or conclusion filter: queued, in_progress, completed, success, failure, cancelled"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max runs to return (default 10, max 50)"`
}

type gitHubRunViewArgs struct {
	RunID      int64  `json:"run_id,omitempty" jsonschema:"workflow run database ID from git_github_runs; omit or 0 to inspect the latest matching run"`
	Workflow   string `json:"workflow,omitempty" jsonschema:"optional workflow file name or name when run_id is omitted"`
	Branch     string `json:"branch,omitempty" jsonschema:"optional branch filter when run_id is omitted"`
	FailedLogs bool   `json:"failed_logs,omitempty" jsonschema:"include logs from failed steps (--log-failed, truncated to last 32 KiB)"`
}

type gitHubRunRerunArgs struct {
	RunID      int64 `json:"run_id" jsonschema:"workflow run database ID from git_github_runs"`
	FailedOnly bool  `json:"failed_only,omitempty" jsonschema:"only re-run failed jobs instead of the entire workflow run"`
}

type gitHubRunRerunOut struct {
	Message string `json:"message"`
}

// network marks a tool that talks to a remote server.
func network(a *mcp.ToolAnnotations) *mcp.ToolAnnotations {
	a.OpenWorldHint = boolPtr(true)
	return a
}

func addGitRemoteTools(s *mcp.Server, g *gitwrite.Git) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "git_remote",
		Description: "List, add, change (set_url) or remove git remotes. Only https and ssh URLs are accepted; local paths, file://, git://, http:// and ext:: are refused, " +
			"and https URLs may not contain a username or token (credentials come from the user's git credential helper or ssh key). " +
			"remove deletes only the remote and its remote-tracking branches, never local branches or commits.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitRemoteArgs) (*mcp.CallToolResult, gitRemoteOut, error) {
		switch strings.ToLower(strings.TrimSpace(in.Action)) {
		case "", "list":
			list, err := g.Remotes(ctx)
			if err != nil {
				return nil, gitRemoteOut{}, err
			}
			if len(list) == 0 {
				msg := "no remotes configured; add one with action=add, or create a GitHub repository with git_github_create_repo"
				return textResult(msg), gitRemoteOut{Message: msg}, nil
			}
			var b strings.Builder
			for _, r := range list {
				fmt.Fprintf(&b, "%s\t%s\n", r.Name, r.FetchURL)
				if r.PushURL != "" {
					fmt.Fprintf(&b, "%s\t%s (push)\n", r.Name, r.PushURL)
				}
			}
			msg := fmt.Sprintf("%d remote(s)", len(list))
			return textResult(b.String()), gitRemoteOut{Message: msg, Remotes: list}, nil
		case "add":
			msg, err := g.AddRemote(ctx, in.Name, in.URL)
			return remoteMessage(msg, err)
		case "set_url", "set-url":
			msg, err := g.SetRemoteURL(ctx, in.Name, in.URL)
			return remoteMessage(msg, err)
		case "remove", "rm":
			msg, err := g.RemoveRemote(ctx, in.Name)
			return remoteMessage(msg, err)
		default:
			return nil, gitRemoteOut{}, fmt.Errorf("unknown action %q; use list, add, set_url or remove", in.Action)
		}
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_fetch",
		Description: "Download new commits and branches from a remote into remote-tracking branches (origin/main etc.). " +
			"Local branches and workspace files are not changed. Never prompts for a password; see the hint in an auth error.",
		Annotations: network(additiveIdempotent()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitFetchArgs) (*mcp.CallToolResult, gitwrite.FetchResult, error) {
		res, err := g.Fetch(ctx, gitwrite.FetchOptions{Remote: in.Remote, Prune: in.Prune})
		if err != nil {
			return nil, gitwrite.FetchResult{}, err
		}
		if res.UpToDate {
			return textResult("fetched " + res.Remote + ": already up to date"), res, nil
		}
		return textResult(fmt.Sprintf("fetched %s:\n  %s", res.Remote, strings.Join(res.Updates, "\n  "))), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_pull",
		Description: "Fetch the remote branch and fast-forward the current branch to it. Fast-forward only: when local and remote have diverged nothing is changed " +
			"and the error says to use git_merge with the remote branch. No rebase. Refuses when uncommitted changes would be overwritten.",
		Annotations: network(additive()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitPullArgs) (*mcp.CallToolResult, gitwrite.PullResult, error) {
		res, err := g.Pull(ctx, gitwrite.PullOptions{Remote: in.Remote, Branch: in.Branch})
		if err != nil {
			return nil, gitwrite.PullResult{}, err
		}
		src := res.Remote + "/" + res.RemoteBranch
		if res.UpToDate {
			text := fmt.Sprintf("%s is up to date with %s", res.Branch, src)
			if res.Ahead > 0 {
				text += fmt.Sprintf(" (%d local commit(s) not pushed yet)", res.Ahead)
			}
			return textResult(text), res, nil
		}
		from := res.From
		if from == "" {
			from = "(empty)"
		}
		text := fmt.Sprintf("fast-forwarded %s %s..%s from %s (%d commit(s))", res.Branch, from, res.To, src, res.Commits)
		if res.Summary != "" {
			text += "\n" + res.Summary
		}
		return textResult(text), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_push",
		Description: "Push one local branch (default: the current one) to a remote. Never forces, never deletes remote branches and never pushes tags; " +
			"a rejected non-fast-forward push is reported (pull or merge first). Use set_upstream=true on a branch's first push. Never prompts for a password.",
		Annotations: network(additive()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitPushArgs) (*mcp.CallToolResult, gitwrite.PushResult, error) {
		res, err := g.Push(ctx, gitwrite.PushOptions{Remote: in.Remote, Branch: in.Branch, RemoteBranch: in.RemoteBranch, SetUpstream: in.SetUpstream})
		if err != nil {
			return nil, gitwrite.PushResult{}, err
		}
		text := fmt.Sprintf("%s: %s -> %s/%s", res.Status, res.Branch, res.Remote, res.RemoteBranch)
		if res.Summary != "" && res.Summary != "[up to date]" {
			text += " (" + res.Summary + ")"
		}
		if res.UpstreamSet {
			text += fmt.Sprintf("\nupstream of %s is now %s/%s", res.Branch, res.Remote, res.RemoteBranch)
		}
		return textResult(text), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_merge",
		Description: "Merge a branch, remote branch (origin/main) or commit into the current branch. Needs a clean working tree (commit first). " +
			"On a conflict the merge is aborted automatically, the branch is left unchanged and the conflicting files are listed. Never runs hooks.",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitMergeArgs) (*mcp.CallToolResult, gitwrite.MergeResult, error) {
		res, err := g.Merge(ctx, gitwrite.MergeOptions{Ref: in.Ref, FFOnly: in.FFOnly, Message: in.Message})
		if err != nil {
			var conflict *gitwrite.MergeConflictError
			if errors.As(err, &conflict) {
				return nil, gitwrite.MergeResult{}, conflict
			}
			return nil, gitwrite.MergeResult{}, err
		}
		if res.UpToDate {
			return textResult(fmt.Sprintf("%s already contains %s", res.Branch, res.Ref)), res, nil
		}
		kind := "merged"
		if res.FastForward {
			kind = "fast-forwarded"
		}
		text := fmt.Sprintf("%s %s into %s (%s..%s)", kind, res.Ref, res.Branch, res.From, res.To)
		if res.Summary != "" {
			text += "\n" + res.Summary
		}
		return textResult(text), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_github_create_repo",
		Description: "Create a new empty repository on GitHub with the GitHub CLI (gh), private unless visibility says otherwise, and add it as remote origin " +
			"(or the given remote). Nothing is pushed: call git_push with set_upstream=true next. Needs gh installed and logged in (`gh auth login`); never prompts.",
		Annotations: network(additive()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitHubCreateRepoArgs) (*mcp.CallToolResult, gitwrite.GitHubRepoResult, error) {
		res, err := g.CreateGitHubRepo(ctx, gitwrite.GitHubRepoOptions{
			Name: in.Name, Owner: in.Owner, Visibility: in.Visibility, Description: in.Description,
			Remote: in.Remote, SkipRemote: in.SkipRemote,
		})
		if err != nil {
			return nil, gitwrite.GitHubRepoResult{}, err
		}
		text := fmt.Sprintf("created %s repository %s\n%s", res.Visibility, res.URL, res.Note)
		return textResult(text), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_github_runs",
		Description: "List recent GitHub Actions workflow runs for this repository using the GitHub CLI (gh). " +
			"Filter optionally by workflow (e.g. ci.yml), branch, or status/conclusion. Read-only; never prompts.",
		Annotations: network(readOnly()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitHubRunsArgs) (*mcp.CallToolResult, gitwrite.GitHubRunsResult, error) {
		res, err := g.ListGitHubRuns(ctx, gitwrite.GitHubRunsOptions{
			Workflow: in.Workflow, Branch: in.Branch, Status: in.Status, Limit: in.Limit,
		})
		if err != nil {
			return nil, gitwrite.GitHubRunsResult{}, err
		}
		if len(res.Runs) == 0 {
			return textResult("no GitHub Actions workflow runs found"), res, nil
		}
		var b strings.Builder
		for _, r := range res.Runs {
			state := r.Status
			if r.Conclusion != "" {
				state = r.Conclusion
			}
			fmt.Fprintf(&b, "#%d (id: %d) [%s] %s — %s (%s @ %s) %s\n",
				r.Number, r.DatabaseID, state, r.WorkflowName, r.DisplayTitle, r.HeadBranch, r.HeadSha, r.CreatedAt)
		}
		return textResult(b.String()), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_github_run_view",
		Description: "Inspect a GitHub Actions workflow run: show jobs, steps, status/conclusion, and optionally failed step logs (failed_logs=true). " +
			"Omit run_id (or pass 0) to inspect the latest run. Read-only; never prompts.",
		Annotations: network(readOnly()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitHubRunViewArgs) (*mcp.CallToolResult, gitwrite.GitHubRunDetail, error) {
		res, err := g.ViewGitHubRun(ctx, gitwrite.GitHubRunViewOptions{
			RunID: in.RunID, Workflow: in.Workflow, Branch: in.Branch, FailedLogs: in.FailedLogs,
		})
		if err != nil {
			return nil, gitwrite.GitHubRunDetail{}, err
		}
		state := res.Status
		if res.Conclusion != "" {
			state = res.Conclusion
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s #%d (id: %d) — %s\n", res.WorkflowName, res.Number, res.DatabaseID, state)
		fmt.Fprintf(&b, "Title: %s (%s @ %s, event: %s)\n", res.DisplayTitle, res.HeadBranch, res.HeadSha, res.Event)
		fmt.Fprintf(&b, "URL: %s\n", res.URL)
		for _, j := range res.Jobs {
			jState := j.Status
			if j.Conclusion != "" {
				jState = j.Conclusion
			}
			fmt.Fprintf(&b, "\nJob: %s [%s]\n", j.Name, jState)
			for _, st := range j.Steps {
				sState := st.Status
				if st.Conclusion != "" {
					sState = st.Conclusion
				}
				fmt.Fprintf(&b, "  %d. %s [%s]\n", st.Number, st.Name, sState)
			}
		}
		if res.FailedLogs != "" {
			b.WriteString("\n--- Failed Logs ---\n")
			b.WriteString(res.FailedLogs)
			if res.LogsTruncated {
				b.WriteString("\n[logs truncated to last 32 KiB]")
			}
		}
		return textResult(b.String()), res, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_github_run_rerun",
		Description: "Re-run a GitHub Actions workflow run (all jobs, or only failed jobs with failed_only=true) via the GitHub CLI (gh). " +
			"Requires write access to the repository; never prompts.",
		Annotations: network(additive()),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitHubRunRerunArgs) (*mcp.CallToolResult, gitHubRunRerunOut, error) {
		msg, err := g.RerunGitHubRun(ctx, in.RunID, in.FailedOnly)
		if err != nil {
			return nil, gitHubRunRerunOut{}, err
		}
		return textResult(msg), gitHubRunRerunOut{Message: msg}, nil
	})
}
func remoteMessage(msg string, err error) (*mcp.CallToolResult, gitRemoteOut, error) {
	if err != nil {
		return nil, gitRemoteOut{}, err
	}
	return textResult(msg), gitRemoteOut{Message: msg}, nil
}
