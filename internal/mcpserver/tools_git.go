package mcpserver

// Git tools, always registered. Read-only: git_status, git_diff, git_log
// (internal/gitread). Write: git_init, git_add, git_unstage, git_commit,
// git_create_branch, git_switch_branch (internal/gitwrite). Remote tools
// (remotes, fetch, pull, push, merge, GitHub repo creation) live in
// tools_git_remote.go.
//
// The write tools in this file cover the local, non-destructive part of a
// git workflow. Nothing can discard work: there is no reset --hard, clean,
// checkout of files, branch delete, amend or force option.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/gitread"
	"ngodingmcp/internal/gitwrite"
)

// addGitTools registers the read-only git tools, plus the write and remote
// tools unless their module is disabled.
func addGitTools(s *mcp.Server, ws Workspace, on moduleSet) {
	addGitReadTools(s, gitread.New(ws))
	gw := gitwrite.New(ws)
	if on.enabled(ModuleGitWrite) {
		addGitWriteTools(s, gw)
	}
	if on.enabled(ModuleGitRemote) {
		addGitRemoteTools(s, gw)
	}
}

// ===========================================================================
// Read-only git
// ===========================================================================

const notRepoMessage = "The workspace is not a git repository, so there is no git status, diff or log to show. " +
	"Call git_init to create a repository at the workspace root, then git_status, git_diff and git_log will work."

type gitOut struct {
	Repository bool   `json:"repository"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
}

type gitStatusArgs struct{}

type gitDiffArgs struct {
	Path   string `json:"path,omitempty" jsonschema:"path relative to workspace root"`
	Staged bool   `json:"staged,omitempty" jsonschema:"staged changes (index vs HEAD)"`
	Stat   bool   `json:"stat,omitempty" jsonschema:"only show a per-file summary"`
	Ref    string `json:"ref,omitempty" jsonschema:"commit or branch to compare against, e.g. HEAD~1"`
	Full   bool   `json:"full,omitempty" jsonschema:"return the full patch instead of the default summary"`
}

type gitLogArgs struct {
	Path  string `json:"path,omitempty" jsonschema:"path relative to workspace root"`
	Limit int    `json:"limit,omitempty" jsonschema:"number of commits to show (default 10, max 200)"`
	Body  bool   `json:"body,omitempty" jsonschema:"include the commit body below the subject"`
}

func addGitReadTools(s *mcp.Server, g *gitread.Git) {
	addTextTool(s, &mcp.Tool{
		Name:        "git_status",
		Description: "`git status --short --branch`; sensitive files are hidden.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ gitStatusArgs) (*mcp.CallToolResult, gitOut, error) {
		res, err := g.Status(ctx)
		return gitResult(res, err, "(clean)")
	})

	addTextTool(s, &mcp.Tool{
		Name:        "git_diff",
		Description: "Summarize changes with --stat by default; full=true or a path returns the patch. Sensitive files are excluded.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitDiffArgs) (*mcp.CallToolResult, gitOut, error) {
		stat := in.Stat
		summaryOnly := !in.Stat && !in.Full && strings.TrimSpace(in.Path) == ""
		if summaryOnly {
			stat = true
		}
		res, err := g.Diff(ctx, gitread.DiffOptions{Path: in.Path, Staged: in.Staged, Stat: stat, Ref: in.Ref})
		if summaryOnly {
			return gitResult(res, err, "(no differences)", "[patch omitted; set full=true or pass path for file contents]")
		}
		return gitResult(res, err, "(no differences)")
	})

	addTextTool(s, &mcp.Tool{
		Name:        "git_log",
		Description: "Recent commits, one line each (default 10); body=true adds commit bodies. Optionally for one path.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitLogArgs) (*mcp.CallToolResult, gitOut, error) {
		res, err := g.Log(ctx, gitread.LogOptions{Path: in.Path, Limit: in.Limit, Body: in.Body})
		return gitResult(res, err, "(no commits)")
	})
}

// gitResult turns a gitread result into a tool result. A missing repository
// is not an error: it returns a friendly explanation instead. Notes are
// appended only when there is output to explain.
func gitResult(res gitread.Result, err error, empty string, notes ...string) (*mcp.CallToolResult, gitOut, error) {
	if errors.Is(err, gitread.ErrNotRepo) {
		return textResult(notRepoMessage), gitOut{Repository: false}, nil
	}
	if err != nil {
		return nil, gitOut{}, err
	}
	text := res.Output
	if strings.TrimSpace(text) == "" {
		text = empty
	} else {
		for _, note := range notes {
			if note != "" {
				text = strings.TrimRight(text, "\n") + "\n" + note
			}
		}
	}
	if res.Truncated {
		text += "\n[output truncated]"
	}
	return textResult(text), gitOut{Repository: true, Output: res.Output, Truncated: res.Truncated}, nil
}

// ===========================================================================
// Git write
// ===========================================================================

type gitInitArgs struct {
	Branch string `json:"branch,omitempty" jsonschema:"initial branch name (default main)"`
}

type gitAddArgs struct {
	Paths []string `json:"paths,omitempty" jsonschema:"paths to stage"`
	All   bool     `json:"all,omitempty" jsonschema:"stage every change in the workspace when paths is empty"`
}

type gitUnstageArgs struct {
	Paths []string `json:"paths,omitempty" jsonschema:"paths to unstage"`
	All   bool     `json:"all,omitempty" jsonschema:"unstage everything when paths is empty"`
}

type gitCommitArgs struct {
	Message     string `json:"message" jsonschema:"commit message: a subject line, optionally a blank line and a body"`
	AuthorName  string `json:"author_name,omitempty" jsonschema:"author name; defaults to git config user.name"`
	AuthorEmail string `json:"author_email,omitempty" jsonschema:"author email; defaults to git config user.email"`
}

type gitCreateBranchArgs struct {
	Name   string `json:"name" jsonschema:"new branch name, e.g. feature/git-write"`
	Start  string `json:"start,omitempty" jsonschema:"commit or branch to start from (default HEAD)"`
	Switch bool   `json:"switch,omitempty" jsonschema:"switch to the new branch after creating it"`
}

type gitSwitchArgs struct {
	Name string `json:"name" jsonschema:"existing local branch to switch to"`
}

type gitWriteOut struct {
	Message string `json:"message"`
}

// additiveIdempotent marks a tool that only adds to the repository state and
// gives the same result when repeated (staging and unstaging).
func additiveIdempotent() *mcp.ToolAnnotations {
	a := additive()
	a.IdempotentHint = true
	return a
}

// gitMessageResult is the shared result shape for git write tools that only
// report one line.
func gitMessageResult(msg string, err error) (*mcp.CallToolResult, gitWriteOut, error) {
	if err != nil {
		return nil, gitWriteOut{}, err
	}
	return textResult(msg), gitWriteOut{Message: msg}, nil
}

func addGitWriteTools(s *mcp.Server, g *gitwrite.Git) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "git_init",
		Description: "Create a git repository at the workspace root (branch main unless given).",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitInitArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.Init(ctx, in.Branch))
	})

	addUntypedTool(s, &mcp.Tool{
		Name:        "git_add",
		Description: "Stage paths, or everything with all=true. Ignored files are never staged; sensitive files (.env, keys) are skipped and listed.",
		Annotations: additiveIdempotent(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitAddArgs) (*mcp.CallToolResult, gitwrite.AddResult, error) {
		res, err := g.Add(ctx, gitwrite.AddOptions{Paths: in.Paths, All: in.All})
		if err != nil {
			return nil, gitwrite.AddResult{}, err
		}
		var b strings.Builder
		if len(res.Staged) == 0 {
			b.WriteString("nothing to stage\n")
		} else {
			fmt.Fprintf(&b, "staged %d file(s):\n", len(res.Staged))
			for _, p := range res.Staged {
				b.WriteString("  " + p + "\n")
			}
		}
		if len(res.Skipped) > 0 {
			fmt.Fprintf(&b, "skipped %d sensitive file(s) (never staged):\n", len(res.Skipped))
			for _, p := range res.Skipped {
				b.WriteString("  " + p + "\n")
			}
		}
		return textResult(b.String()), res, nil
	})

	addUntypedTool(s, &mcp.Tool{
		Name:        "git_unstage",
		Description: "Unstage paths, or everything with all=true; workspace files are unchanged.",
		Annotations: additiveIdempotent(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitUnstageArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult("unstaged; workspace files unchanged", g.Unstage(ctx, in.Paths, in.All))
	})

	addUntypedTool(s, &mcp.Tool{
		Name:        "git_commit",
		Description: "Commit the staged changes. Never amends, runs hooks or signs; refuses when nothing or a sensitive file is staged.",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitCommitArgs) (*mcp.CallToolResult, gitwrite.CommitResult, error) {
		res, err := g.Commit(ctx, gitwrite.CommitOptions{Message: in.Message, AuthorName: in.AuthorName, AuthorEmail: in.AuthorEmail})
		if err != nil {
			return nil, gitwrite.CommitResult{}, err
		}
		branch := res.Branch
		if branch == "" {
			branch = "detached HEAD"
		}
		text := fmt.Sprintf("[%s %s] %s\n%d file(s) committed", branch, res.Hash, res.Subject, len(res.Files))
		return textResult(text), res, nil
	})

	addUntypedTool(s, &mcp.Tool{
		Name:        "git_create_branch",
		Description: "Create a local branch at HEAD or start, optionally switching to it; never overwrites.",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitCreateBranchArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.CreateBranch(ctx, in.Name, in.Start, in.Switch))
	})

	addUntypedTool(s, &mcp.Tool{
		Name:        "git_switch_branch",
		Description: "Switch to an existing local branch. No force: git refuses when uncommitted changes would be lost.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitSwitchArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.Switch(ctx, in.Name))
	})
}
