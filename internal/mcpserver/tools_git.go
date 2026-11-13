package mcpserver

// Git tools, always registered. Read-only: git_status, git_diff, git_log
// (internal/gitread). Write: git_init, git_add, git_unstage, git_commit,
// git_create_branch, git_switch_branch (internal/gitwrite).
//
// The write tools cover only the local, non-destructive part of a git
// workflow. Nothing here talks to a remote or can discard work: there is no
// push, pull, fetch, reset --hard, clean, checkout of files, branch delete,
// amend or force option.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/gitread"
	"ngodingmcp/internal/gitwrite"
	"ngodingmcp/internal/workspace"
)

// addGitTools registers the read-only and the write git tools.
func addGitTools(s *mcp.Server, ws *workspace.Workspace) {
	addGitReadTools(s, gitread.New(ws))
	addGitWriteTools(s, gitwrite.New(ws))
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
	Path   string `json:"path,omitempty" jsonschema:"optional file or directory, relative to the workspace root"`
	Staged bool   `json:"staged,omitempty" jsonschema:"show staged changes (index vs HEAD) instead of unstaged ones"`
	Stat   bool   `json:"stat,omitempty" jsonschema:"only show a per-file summary"`
	Ref    string `json:"ref,omitempty" jsonschema:"optional commit or branch to compare against, e.g. HEAD~1"`
}

type gitLogArgs struct {
	Path  string `json:"path,omitempty" jsonschema:"optional file or directory, relative to the workspace root"`
	Limit int    `json:"limit,omitempty" jsonschema:"number of commits to show (default 20, max 200)"`
}

func addGitReadTools(s *mcp.Server, g *gitread.Git) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_status",
		Description: "Show `git status --short --branch` for the workspace. Read-only; sensitive files are hidden.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ gitStatusArgs) (*mcp.CallToolResult, gitOut, error) {
		res, err := g.Status(ctx)
		return gitResult(res, err, "(clean)")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_diff",
		Description: "Show `git diff` for the workspace or one path, optionally staged, as a summary (stat), or against a ref. Read-only; external diff and textconv programs never run, and sensitive files are excluded.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitDiffArgs) (*mcp.CallToolResult, gitOut, error) {
		res, err := g.Diff(ctx, gitread.DiffOptions{Path: in.Path, Staged: in.Staged, Stat: in.Stat, Ref: in.Ref})
		return gitResult(res, err, "(no differences)")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_log",
		Description: "Show recent commits, one line each (hash, date, author, subject), for the workspace or one path. Read-only.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitLogArgs) (*mcp.CallToolResult, gitOut, error) {
		res, err := g.Log(ctx, gitread.LogOptions{Path: in.Path, Limit: in.Limit})
		return gitResult(res, err, "(no commits)")
	})
}

// gitResult turns a gitread result into a tool result. A missing repository
// is not an error: it returns a friendly explanation instead.
func gitResult(res gitread.Result, err error, empty string) (*mcp.CallToolResult, gitOut, error) {
	if errors.Is(err, gitread.ErrNotRepo) {
		return textResult(notRepoMessage), gitOut{Repository: false}, nil
	}
	if err != nil {
		return nil, gitOut{}, err
	}
	text := res.Output
	if strings.TrimSpace(text) == "" {
		text = empty
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
	Paths []string `json:"paths,omitempty" jsonschema:"files or directories to stage, relative to the workspace root"`
	All   bool     `json:"all,omitempty" jsonschema:"stage every change in the workspace when paths is empty"`
}

type gitUnstageArgs struct {
	Paths []string `json:"paths,omitempty" jsonschema:"files or directories to unstage, relative to the workspace root"`
	All   bool     `json:"all,omitempty" jsonschema:"unstage everything when paths is empty"`
}

type gitCommitArgs struct {
	Message     string `json:"message" jsonschema:"commit message: a subject line, optionally a blank line and a body"`
	AuthorName  string `json:"author_name,omitempty" jsonschema:"optional author name; defaults to git config user.name"`
	AuthorEmail string `json:"author_email,omitempty" jsonschema:"optional author email; defaults to git config user.email"`
}

type gitCreateBranchArgs struct {
	Name   string `json:"name" jsonschema:"new branch name, e.g. feature/git-write"`
	Start  string `json:"start,omitempty" jsonschema:"optional commit or branch to start from (default HEAD)"`
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
	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_init",
		Description: "Create a git repository at the workspace root (initial branch main unless given). Refuses when the workspace already is a repository.",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitInitArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.Init(ctx, in.Branch))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_add",
		Description: "Stage new, modified and deleted files under the given paths, or the whole workspace with all=true. " +
			"Ignored files are never staged (no force option) and sensitive files such as .env or private keys are skipped and listed.",
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_unstage",
		Description: "Remove paths (or everything with all=true) from the staging area. The files in the workspace are not changed.",
		Annotations: additiveIdempotent(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitUnstageArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult("unstaged; workspace files unchanged", g.Unstage(ctx, in.Paths, in.All))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_commit",
		Description: "Commit the staged changes with the given message. Never amends, never runs repository hooks and never signs. " +
			"Refuses when nothing is staged or when a sensitive file is staged. Check git_diff with staged=true first.",
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "git_create_branch",
		Description: "Create a new local branch at HEAD or at start, optionally switching to it. Existing branches are never overwritten.",
		Annotations: additive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitCreateBranchArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.CreateBranch(ctx, in.Name, in.Start, in.Switch))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "git_switch_branch",
		Description: "Switch the workspace to an existing local branch. Git refuses when uncommitted changes would be overwritten; " +
			"there is no force option, so no work is lost. Non-conflicting uncommitted changes are carried over.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gitSwitchArgs) (*mcp.CallToolResult, gitWriteOut, error) {
		return gitMessageResult(g.Switch(ctx, in.Name))
	})
}
