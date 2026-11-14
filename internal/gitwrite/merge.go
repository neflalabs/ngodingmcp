package gitwrite

import (
	"fmt"
	"errors"
	"strings"
	"context"
)

// MergeOptions selects what git_merge merges into the current branch.
type MergeOptions struct {
	// Ref is a local branch, a remote-tracking branch (origin/main) or a
	// commit.
	Ref string
	// FFOnly refuses to create a merge commit.
	FFOnly bool
	// Message replaces the default merge commit message.
	Message string
}

// MergeResult reports one merge.
type MergeResult struct {
	Branch      string `json:"branch"`
	Ref         string `json:"ref"`
	UpToDate    bool   `json:"up_to_date"`
	FastForward bool   `json:"fast_forward,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// MergeConflictError is returned when a merge stopped on conflicts. The
// merge has already been aborted, so the branch is unchanged.
type MergeConflictError struct {
	Branch string
	Ref    string
	Files  []string
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merging %s into %s hit conflicts in: %s. The merge was aborted automatically and %s is unchanged. Resolve by editing the branches so they no longer conflict, or ask the user to merge by hand",
		e.Ref, e.Branch, strings.Join(e.Files, ", "), e.Branch)
}

// Merge merges ref into the current branch. It needs a clean tracked
// working tree, and on any failure an unfinished merge is aborted so the
// repository is never left half-merged.
func (g *Git) Merge(ctx context.Context, o MergeOptions) (MergeResult, error) {
	ref := strings.TrimSpace(o.Ref)
	if ref == "" {
		return MergeResult{}, errors.New("ref is required: a branch (feature-x), a remote branch (origin/main) or a commit")
	}
	if err := validRef(ref); err != nil {
		return MergeResult{}, err
	}
	msg := strings.TrimSpace(o.Message)
	if len(msg) > 2000 || strings.ContainsRune(msg, 0) {
		return MergeResult{}, errors.New("message must be at most 2000 characters")
	}
	if err := g.requireRoot(ctx); err != nil {
		return MergeResult{}, err
	}
	cur, err := g.CurrentBranch(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	if cur == "" {
		return MergeResult{}, ErrDetached
	}
	res := MergeResult{Branch: cur, Ref: ref}
	if !g.commitExists(ctx, "HEAD") {
		return res, fmt.Errorf("branch %s has no commits yet; commit first", cur)
	}
	if !g.commitExists(ctx, ref) {
		return res, fmt.Errorf("unknown branch or commit %q (for a remote branch run git_fetch first and use remote/branch, e.g. origin/main)", ref)
	}
	if err := g.checkLocalConfig(ctx); err != nil {
		return res, err
	}
	dirty, err := g.git(ctx, "", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(dirty) != "" {
		return res, errors.New("the working tree has uncommitted changes to tracked files; commit them first (git_add + git_commit). Merge needs a clean tree so a conflict can be aborted without touching your work")
	}
	ahead, behind, err := g.aheadBehind(ctx, ref)
	if err != nil {
		return res, err
	}
	if behind == 0 {
		res.UpToDate = true
		return res, nil
	}
	if o.FFOnly && ahead > 0 {
		return res, fmt.Errorf("cannot fast-forward: %s has %d commit(s) that %s does not have; run without ff_only to create a merge commit", cur, ahead, ref)
	}
	res.From = g.short(ctx, "HEAD")
	args := []string{"merge", "--no-edit"}
	if o.FFOnly {
		args = append(args, "--ff-only")
	}
	if msg != "" {
		args = append(args, "-m", msg)
	}
	args = append(args, ref)
	if _, err := g.git(ctx, "", args...); err != nil {
		files := g.conflictFiles(ctx)
		aborted := g.abortMerge(ctx)
		if len(files) > 0 {
			return res, &MergeConflictError{Branch: cur, Ref: ref, Files: files}
		}
		if aborted {
			return res, fmt.Errorf("%w (the merge was aborted; %s is unchanged)", err, cur)
		}
		return res, err
	}
	res.To = g.short(ctx, "HEAD")
	res.FastForward = ahead == 0
	out, _ := g.git(ctx, "", "diff", "--shortstat", res.From, res.To)
	res.Summary = strings.TrimSpace(out)
	return res, nil
}

func (g *Git) conflictFiles(ctx context.Context) []string {
	out, err := g.git(ctx, "", "diff", "-z", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files
}

// abortMerge aborts an unfinished merge, if any, and reports whether it did.
func (g *Git) abortMerge(ctx context.Context) bool {
	if !g.commitExists(ctx, "MERGE_HEAD") {
		return false
	}
	_, err := g.git(ctx, "", "merge", "--abort")
	return err == nil
}
