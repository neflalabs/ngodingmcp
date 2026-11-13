package gitwrite

// Remote operations: list/add/set-url/remove remotes, fetch, fast-forward
// pull, non-force push and merge.
//
// They use the system git binary like the rest of this package, so the
// user's own credentials work unchanged: ssh keys, ssh-agent, ~/.ssh/config
// and the credential helpers in the user's global or system git config
// (for example `gh auth setup-git`). go-git would need its own credential
// plumbing and does not run git credential helpers.
//
// Safety rules:
//   - push never forces: the refspec never starts with '+', no --force,
//     --force-with-lease, --mirror, --delete or --tags; a rejected
//     non-fast-forward push is reported, never retried with force.
//   - pull only fast-forwards; diverged branches are refused before any
//     change. merge aborts itself on conflict, so the repository is never
//     left half-merged, and it requires a clean tracked working tree.
//   - network commands never prompt and never hang: GIT_TERMINAL_PROMPT=0,
//     no askpass programs, ssh BatchMode, no controlling terminal and a
//     timeout that kills git together with ssh and credential helpers.
//   - only https and ssh transports are allowed (GIT_ALLOW_PROTOCOL);
//     file://, local paths, git://, http:// and ext:: are refused.
//   - programs and redirects from the repository's own .git/config are
//     never trusted: when it sets a credential helper, ssh command, askpass,
//     url rewrite, upload/receive-pack, filter, merge driver or include,
//     remote and merge tools refuse to run.
//   - credentials in URLs (user:pass@ or a token as https user) are scrubbed
//     from every output and error, and new https URLs may not carry any.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RemoteTimeout bounds one network git command (fetch, pull, push). It is
// longer than DefaultTimeout for real pushes and fetches, and still makes
// sure a stuck connection or a helper waiting for input never blocks long.
const RemoteTimeout = 2 * time.Minute

// ErrDetached is returned when an operation needs a checked-out branch.
var ErrDetached = errors.New("HEAD is detached; switch to a branch first with git_switch_branch")

// Remote is one configured remote. URLs have credentials scrubbed.
type Remote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetch_url"`
	PushURL  string `json:"push_url,omitempty"`
}

// Remotes lists the configured remotes.
func (g *Git) Remotes(ctx context.Context) ([]Remote, error) {
	if err := g.requireRoot(ctx); err != nil {
		return nil, err
	}
	out, err := g.git(ctx, "", "remote", "-v")
	if err != nil {
		return nil, err
	}
	list := []Remote{}
	idx := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var push bool
		switch {
		case strings.HasSuffix(rest, " (fetch)"):
			rest = strings.TrimSuffix(rest, " (fetch)")
		case strings.HasSuffix(rest, " (push)"):
			rest, push = strings.TrimSuffix(rest, " (push)"), true
		default:
			continue
		}
		u := g.ws.ScrubPaths(ScrubCredentials(rest))
		i, seen := idx[name]
		if !seen {
			i = len(list)
			idx[name] = i
			list = append(list, Remote{Name: name})
		}
		if push {
			list[i].PushURL = u
		} else {
			list[i].FetchURL = u
		}
	}
	for i := range list {
		if list[i].PushURL == list[i].FetchURL {
			list[i].PushURL = ""
		}
	}
	return list, nil
}

// AddRemote adds a remote. Nothing is fetched.
func (g *Git) AddRemote(ctx context.Context, name, rawURL string) (string, error) {
	if err := ValidRemoteName(name); err != nil {
		return "", err
	}
	u, err := g.validRemoteURL(rawURL)
	if err != nil {
		return "", err
	}
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	exists, err := g.remoteExists(ctx, name)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("remote %q already exists; use action=set_url to change its URL", name)
	}
	if _, err := g.git(ctx, "", "remote", "add", name, u); err != nil {
		return "", err
	}
	return fmt.Sprintf("Added remote %s (%s). Nothing was fetched yet; run git_fetch.", name, ScrubCredentials(u)), nil
}

// SetRemoteURL changes the URL of an existing remote.
func (g *Git) SetRemoteURL(ctx context.Context, name, rawURL string) (string, error) {
	if err := ValidRemoteName(name); err != nil {
		return "", err
	}
	u, err := g.validRemoteURL(rawURL)
	if err != nil {
		return "", err
	}
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	if err := g.requireRemote(ctx, name); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, "", "remote", "set-url", name, u); err != nil {
		return "", err
	}
	return fmt.Sprintf("Remote %s now points to %s.", name, ScrubCredentials(u)), nil
}

// RemoveRemote removes a remote and its remote-tracking branches. Local
// branches and commits are not touched.
func (g *Git) RemoveRemote(ctx context.Context, name string) (string, error) {
	if err := ValidRemoteName(name); err != nil {
		return "", err
	}
	if err := g.requireRoot(ctx); err != nil {
		return "", err
	}
	if err := g.requireRemote(ctx, name); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, "", "remote", "remove", name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed remote %s and its remote-tracking branches. Local branches and commits are untouched.", name), nil
}

// FetchOptions selects what git_fetch downloads.
type FetchOptions struct {
	// Remote defaults to the current branch's upstream remote, else origin.
	Remote string
	// Prune deletes remote-tracking branches whose remote branch is gone.
	Prune bool
}

// FetchResult reports one fetch.
type FetchResult struct {
	Remote   string   `json:"remote"`
	UpToDate bool     `json:"up_to_date"`
	Updates  []string `json:"updates,omitempty"`
}

// Fetch downloads new commits and branches from a remote into its
// remote-tracking branches (refs/remotes/<remote>/*). Local branches and the
// working tree are not changed.
func (g *Git) Fetch(ctx context.Context, o FetchOptions) (FetchResult, error) {
	if err := g.requireRoot(ctx); err != nil {
		return FetchResult{}, err
	}
	remote := strings.TrimSpace(o.Remote)
	if remote == "" {
		cur, _ := g.CurrentBranch(ctx)
		remote, _ = g.upstreamOf(ctx, cur)
		if remote == "" {
			remote = "origin"
		}
	}
	if err := g.prepareNetwork(ctx, remote); err != nil {
		return FetchResult{Remote: remote}, err
	}
	updates, err := g.fetch(ctx, remote, o.Prune)
	if err != nil {
		return FetchResult{Remote: remote}, err
	}
	return FetchResult{Remote: remote, UpToDate: len(updates) == 0, Updates: updates}, nil
}

// fetch runs the network fetch with an explicit refspec into the remote's
// own tracking namespace. --refmap= keeps a remote.<name>.fetch setting in
// .git/config from writing into local branches.
func (g *Git) fetch(ctx context.Context, remote string, prune bool) ([]string, error) {
	args := []string{"fetch", "--no-recurse-submodules", "--refmap="}
	if prune {
		args = append(args, "--prune")
	}
	args = append(args, remote, "+refs/heads/*:refs/remotes/"+remote+"/*")
	_, errOut, err := g.run(ctx, runOpts{network: true}, args...)
	if err != nil {
		return nil, withHint(err)
	}
	var updates []string
	for _, line := range strings.Split(errOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "->") || strings.Contains(line, "[deleted]") {
			updates = append(updates, line)
		}
	}
	return updates, nil
}

// PullOptions selects what git_pull merges.
type PullOptions struct {
	// Remote defaults to the current branch's upstream remote, else origin.
	Remote string
	// Branch is the remote branch; it defaults to the upstream branch, else
	// the current branch name.
	Branch string
}

// PullResult reports one pull.
type PullResult struct {
	Branch       string   `json:"branch"`
	Remote       string   `json:"remote"`
	RemoteBranch string   `json:"remote_branch"`
	UpToDate     bool     `json:"up_to_date"`
	Ahead        int      `json:"ahead,omitempty"`
	From         string   `json:"from,omitempty"`
	To           string   `json:"to,omitempty"`
	Commits      int      `json:"commits,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Fetched      []string `json:"fetched,omitempty"`
}

// Pull fetches the remote branch and fast-forwards the current branch to
// it. When the branches have diverged nothing is changed and an error
// explains how to merge instead. There is no rebase.
func (g *Git) Pull(ctx context.Context, o PullOptions) (PullResult, error) {
	if err := g.requireRoot(ctx); err != nil {
		return PullResult{}, err
	}
	cur, err := g.CurrentBranch(ctx)
	if err != nil {
		return PullResult{}, err
	}
	if cur == "" {
		return PullResult{}, ErrDetached
	}
	upR, upB := g.upstreamOf(ctx, cur)
	remote := strings.TrimSpace(o.Remote)
	if remote == "" {
		remote = upR
		if remote == "" {
			remote = "origin"
		}
	}
	rb := strings.TrimSpace(o.Branch)
	if rb == "" {
		rb = cur
		if remote == upR && upB != "" {
			rb = upB
		}
	}
	res := PullResult{Branch: cur, Remote: remote, RemoteBranch: rb}
	if err := ValidBranch(rb); err != nil {
		return res, err
	}
	if err := g.prepareNetwork(ctx, remote); err != nil {
		return res, err
	}
	fetched, err := g.fetch(ctx, remote, false)
	if err != nil {
		return res, err
	}
	res.Fetched = fetched
	ref := "refs/remotes/" + remote + "/" + rb
	if !g.commitExists(ctx, ref) {
		return res, fmt.Errorf("branch %q does not exist on remote %s", rb, remote)
	}
	ahead, behind, err := g.aheadBehind(ctx, ref)
	if err != nil {
		return res, err
	}
	res.Ahead = ahead
	if behind == 0 {
		res.UpToDate = true
		return res, nil
	}
	if ahead > 0 {
		return res, fmt.Errorf("cannot pull: local branch %s and %s/%s have diverged (%d local commit(s) not on the remote, %d remote commit(s) not here). git_pull only fast-forwards and changed nothing. Merge with git_merge ref=%s/%s (a conflict is aborted automatically), then git_push", cur, remote, rb, ahead, behind, remote, rb)
	}
	res.From = g.short(ctx, "HEAD")
	if _, err := g.git(ctx, "", "merge", "--ff-only", "--no-edit", ref); err != nil {
		return res, localChangesHint(err)
	}
	res.To = g.short(ctx, "HEAD")
	res.Commits = behind
	if res.From != "" {
		out, _ := g.git(ctx, "", "diff", "--shortstat", res.From, res.To)
		res.Summary = strings.TrimSpace(out)
	}
	return res, nil
}

// PushOptions selects what git_push sends.
type PushOptions struct {
	// Remote defaults to the branch's upstream remote, else origin.
	Remote string
	// Branch is the local branch; it defaults to the current branch.
	Branch string
	// RemoteBranch defaults to the upstream branch, else the same name.
	RemoteBranch string
	// SetUpstream records remote/RemoteBranch as the branch's upstream.
	SetUpstream bool
}

// PushResult reports one push.
type PushResult struct {
	Remote       string `json:"remote"`
	Branch       string `json:"branch"`
	RemoteBranch string `json:"remote_branch"`
	Status       string `json:"status"`
	Summary      string `json:"summary,omitempty"`
	UpstreamSet  bool   `json:"upstream_set,omitempty"`
}

// Push sends one local branch to one remote branch, fast-forward only. It
// never forces, never deletes remote branches and never pushes tags.
func (g *Git) Push(ctx context.Context, o PushOptions) (PushResult, error) {
	if err := g.requireRoot(ctx); err != nil {
		return PushResult{}, err
	}
	branch := strings.TrimSpace(o.Branch)
	if branch == "" {
		cur, err := g.CurrentBranch(ctx)
		if err != nil {
			return PushResult{}, err
		}
		if cur == "" {
			return PushResult{}, ErrDetached
		}
		branch = cur
	}
	if err := ValidBranch(branch); err != nil {
		return PushResult{}, err
	}
	if !g.branchExists(ctx, branch) {
		return PushResult{}, fmt.Errorf("local branch %q does not exist or has no commits yet", branch)
	}
	upR, upB := g.upstreamOf(ctx, branch)
	remote := strings.TrimSpace(o.Remote)
	if remote == "" {
		remote = upR
		if remote == "" {
			remote = "origin"
		}
	}
	rb := strings.TrimSpace(o.RemoteBranch)
	if rb == "" {
		rb = branch
		if remote == upR && upB != "" {
			rb = upB
		}
	}
	res := PushResult{Remote: remote, Branch: branch, RemoteBranch: rb}
	if err := ValidBranch(rb); err != nil {
		return res, err
	}
	if err := g.prepareNetwork(ctx, remote); err != nil {
		return res, err
	}
	args := []string{"-c", "remote." + remote + ".mirror=false",
		"push", "--porcelain", "--no-recurse-submodules", "--no-follow-tags"}
	if o.SetUpstream {
		args = append(args, "--set-upstream")
	}
	// No leading '+': git refuses anything that is not a fast-forward.
	args = append(args, remote, "refs/heads/"+branch+":refs/heads/"+rb)
	out, _, err := g.run(ctx, runOpts{network: true}, args...)
	flag, summary := pushStatus(out)
	if err != nil {
		low := strings.ToLower(out + "\n" + err.Error())
		if strings.Contains(low, "non-fast-forward") || strings.Contains(low, "fetch first") {
			return res, fmt.Errorf("push rejected: %s/%s has commits that local %s does not have (non-fast-forward). Run git_pull (or git_fetch and git_merge), then push again. Force push is not available", remote, rb, branch)
		}
		if flag == "!" && summary != "" {
			return res, withHint(fmt.Errorf("push rejected by %s: %s: %w", remote, summary, err))
		}
		return res, withHint(err)
	}
	switch flag {
	case "=":
		res.Status = "up to date"
	case "*":
		res.Status = "created remote branch"
	default:
		res.Status = "pushed"
	}
	res.Summary = summary
	res.UpstreamSet = o.SetUpstream
	return res, nil
}

// pushStatus reads the first ref line of `git push --porcelain` output:
// "<flag>\t<from>:<to>\t<summary>".
func pushStatus(out string) (flag, summary string) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 || len(parts[0]) != 1 {
			continue
		}
		return parts[0], strings.TrimSpace(parts[2])
	}
	return "", ""
}

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
	out, err := g.git(ctx, "", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
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

// upstreamOf returns the remote and remote branch name branch tracks, or
// empty strings. A local upstream (remote ".") counts as none.
func (g *Git) upstreamOf(ctx context.Context, branch string) (remote, rbranch string) {
	if branch == "" || ValidBranch(branch) != nil {
		return "", ""
	}
	r, err := g.git(ctx, "", "config", "--get", "branch."+branch+".remote")
	if err != nil {
		return "", ""
	}
	r = strings.TrimSpace(r)
	if r == "." || ValidRemoteName(r) != nil {
		return "", ""
	}
	m, _ := g.git(ctx, "", "config", "--get", "branch."+branch+".merge")
	m = strings.TrimPrefix(strings.TrimSpace(m), "refs/heads/")
	if ValidBranch(m) != nil {
		m = ""
	}
	return r, m
}

// aheadBehind counts commits in HEAD but not ref (ahead) and in ref but not
// HEAD (behind). An unborn HEAD is behind by every commit of ref.
func (g *Git) aheadBehind(ctx context.Context, ref string) (ahead, behind int, err error) {
	if !g.commitExists(ctx, "HEAD") {
		out, err := g.git(ctx, "", "rev-list", "--count", ref)
		if err != nil {
			return 0, 0, err
		}
		n, _ := strconv.Atoi(strings.TrimSpace(out))
		return 0, n, nil
	}
	out, err := g.git(ctx, "", "rev-list", "--left-right", "--count", "HEAD..."+ref)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", strings.TrimSpace(out))
	}
	ahead, _ = strconv.Atoi(f[0])
	behind, _ = strconv.Atoi(f[1])
	return ahead, behind, nil
}

func (g *Git) commitExists(ctx context.Context, ref string) bool {
	_, err := g.git(ctx, "", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func (g *Git) short(ctx context.Context, rev string) string {
	out, err := g.git(ctx, "", "rev-parse", "--short", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (g *Git) remoteExists(ctx context.Context, name string) (bool, error) {
	out, err := g.git(ctx, "", "remote")
	if err != nil {
		return false, err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == name {
			return true, nil
		}
	}
	return false, nil
}

func (g *Git) requireRemote(ctx context.Context, name string) error {
	ok, err := g.remoteExists(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("remote %q does not exist; add it with git_remote action=add", name)
	}
	return nil
}

// prepareNetwork runs the checks every network command needs.
func (g *Git) prepareNetwork(ctx context.Context, remote string) error {
	if err := ValidRemoteName(remote); err != nil {
		return err
	}
	if err := g.requireRemote(ctx, remote); err != nil {
		return err
	}
	return g.checkLocalConfig(ctx)
}

// untrustedLocalKeys match .git/config keys that could run a program,
// redirect a remote or pull in another config file during fetch, pull, push
// or merge. Git lowercases section and variable names in --list output.
var untrustedLocalKeys = []*regexp.Regexp{
	regexp.MustCompile(`^core\.(sshcommand|askpass|gitproxy)$`),
	regexp.MustCompile(`^credential\.(.+\.)?helper$`),
	regexp.MustCompile(`^url\..+\.(insteadof|pushinsteadof)$`),
	regexp.MustCompile(`^remote\..+\.(uploadpack|receivepack)$`),
	regexp.MustCompile(`^filter\..+\.(clean|smudge|process)$`),
	regexp.MustCompile(`^merge\..+\.driver$`),
	regexp.MustCompile(`^include\.`),
	regexp.MustCompile(`^includeif\.`),
}

// checkLocalConfig refuses to run when the repository's own config sets a
// key from untrustedLocalKeys. The user's global and system config are
// trusted; .git/config can be written by anything in the workspace.
func (g *Git) checkLocalConfig(ctx context.Context) error {
	out, err := g.git(ctx, "", "config", "--local", "--name-only", "--list")
	if err != nil {
		return err
	}
	var bad []string
	for _, key := range strings.Split(out, "\n") {
		key = strings.TrimSpace(key)
		low := strings.ToLower(key)
		for _, re := range untrustedLocalKeys {
			if re.MatchString(low) {
				bad = append(bad, key)
				break
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to run: the repository's own config (.git/config) sets %s, which could run a program or redirect credentials during remote or merge operations. Put such settings in your global git config (~/.gitconfig) instead and remove them here with `git config --local --unset <key>`", strings.Join(bad, ", "))
}

// networkSetup returns the extra -c options and environment for a command
// that talks to a remote.
func (g *Git) networkSetup(ctx context.Context) (config, env []string) {
	config = []string{
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "push.recurseSubmodules=no",
		"-c", "push.followTags=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "http.lowSpeedLimit=1000",
		"-c", "http.lowSpeedTime=30",
		"-c", "credential.interactive=false",
	}
	protocols := "https:ssh"
	if g.allowLocal {
		protocols += ":file"
	}
	env = []string{
		// Overrides every protocol.* setting, including .git/config.
		"GIT_ALLOW_PROTOCOL=" + protocols,
		"GIT_PROTOCOL_FROM_USER=0",
		// An empty GIT_ASKPASS stops git from falling back to core.askPass
		// or SSH_ASKPASS, so no password dialog can block the call.
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=never",
	}
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		custom, _ := g.git(ctx, "", "config", "--get", "core.sshCommand")
		if strings.TrimSpace(custom) == "" {
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=20")
		}
	}
	return config, env
}

// ValidRemoteName accepts letters, digits, '.', '_' and '-' (no leading dash
// or dot, no "..", at most 50 characters).
func ValidRemoteName(name string) error {
	bad := fmt.Errorf("invalid remote name %q (use letters, digits, '.', '_' or '-')", name)
	if name == "" || len(name) > 50 || name[0] == '-' || name[0] == '.' ||
		strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") {
		return bad
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)
		if !ok {
			return bad
		}
	}
	return nil
}

var (
	transportPrefix = regexp.MustCompile(`^[A-Za-z0-9+.-]+::`)
	scpLike         = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9.-]*:[^:\s].*$`)
	sshUser         = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hostName        = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)
)

// validRemoteURL accepts https://host/path without credentials,
// ssh://[user@]host[:port]/path and scp-like [user@]host:path. Everything
// else (file://, local paths, git://, http://, ext:: and other transport
// helpers) is refused. It returns the trimmed URL.
func (g *Git) validRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	refused := fmt.Errorf("remote URL %q is not allowed: use https://host/owner/repo.git or ssh (git@host:owner/repo.git, ssh://git@host/owner/repo.git); local paths, file://, git://, http:// and transport helpers such as ext:: are refused", ScrubCredentials(raw))
	if raw == "" {
		return "", errors.New("url is required")
	}
	if len(raw) > 500 || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n\x00\\") {
		return "", refused
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return "", refused
		}
	}
	if g.allowLocal && filepath.IsAbs(raw) {
		return raw, nil
	}
	if transportPrefix.MatchString(raw) {
		return "", refused
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || strings.HasPrefix(u.Hostname(), "-") || !hostName.MatchString(u.Host) ||
			strings.Trim(u.Path, "/") == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", refused
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if u.User != nil {
				return "", errors.New("https remote URLs must not contain a username or token; drop the user@ part and let your git credential helper (for example `gh auth setup-git`) supply credentials")
			}
		case "ssh":
			if u.User != nil {
				if _, hasPass := u.User.Password(); hasPass || !sshUser.MatchString(u.User.Username()) {
					return "", errors.New("ssh remote URLs must not contain a password; use an ssh key")
				}
			}
		default:
			return "", refused
		}
		return raw, nil
	}
	if scpLike.MatchString(raw) {
		_, path, _ := strings.Cut(raw, ":")
		if strings.HasPrefix(path, "-") {
			return "", refused
		}
		return raw, nil
	}
	return "", refused
}

var urlUserinfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*)://([^/\s@'"]+)@`)

// ScrubCredentials hides credentials embedded in URLs inside s: any
// user:password@ part, and the user part of http(s) URLs, where it is often
// a token. A plain ssh user such as git@ is kept.
func ScrubCredentials(s string) string {
	return urlUserinfo.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlUserinfo.FindStringSubmatch(m)
		scheme, user := strings.ToLower(sub[1]), sub[2]
		if strings.Contains(user, ":") || scheme == "http" || scheme == "https" {
			return sub[1] + "://***@"
		}
		return m
	})
}

// hintError adds a how-to-fix line to a git error.
type hintError struct {
	err  error
	hint string
}

func (e *hintError) Error() string { return e.err.Error() + "\nhint: " + e.hint }
func (e *hintError) Unwrap() error { return e.err }

// withHint attaches an explanation for common network and auth failures.
func withHint(err error) error {
	if err == nil {
		return nil
	}
	if h := remoteHint(err.Error()); h != "" {
		return &hintError{err: err, hint: h}
	}
	return err
}

func remoteHint(msg string) string {
	low := strings.ToLower(msg)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(low, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("timed out after"):
		return "the remote did not finish in time. A slow network, or ssh or a credential helper waiting for input that cannot be given here (ngodingmcp never prompts), can cause this. Check that `git fetch` works in a terminal without typing anything; see docs/tools.md, section Git remote."
	case has("host key verification failed"):
		return "ssh does not know this server's host key yet. Run `ssh -T git@github.com` (or the right host) once in a terminal as the same user and accept the key; it is saved in ~/.ssh/known_hosts."
	case has("permission denied (publickey", "could not open a connection to your authentication agent",
		"no such identity", "sign_and_send_pubkey", "agent refused", "passphrase"):
		return "ssh could not log in and ngodingmcp never asks for a passphrase. Load your key into ssh-agent (ssh-add) or use a key without a passphrase. For the background service (ngodingmcp -d), add SSH_AUTH_SOCK=<output of `echo $SSH_AUTH_SOCK`> to ~/.config/ngodingmcp/env and run `ngodingmcp restart`."
	case has("could not read username", "could not read password", "terminal prompts disabled",
		"unable to get password", "unable to get username",
		"authentication failed", "invalid username or password", "returned error: 401", "returned error: 403"):
		return "HTTPS needs a credential helper in your global git config, because ngodingmcp never prompts. For GitHub run `gh auth login` and then `gh auth setup-git`. Do not put a token in the remote URL. The background service reads the same ~/.gitconfig; a helper that needs the desktop keyring may not work there, gh does."
	case has("not allowed") && has("transport", "protocol"):
		return "only https and ssh remotes are allowed; change the URL with git_remote action=set_url."
	case has("could not resolve host", "connection refused", "network is unreachable", "connection timed out"):
		return "the server could not reach the remote host; check the network or the remote URL."
	}
	return ""
}

// localChangesHint explains a fast-forward blocked by uncommitted changes.
func localChangesHint(err error) error {
	if err != nil && strings.Contains(err.Error(), "would be overwritten") {
		return &hintError{err: err, hint: "commit your changes first (git_add + git_commit), then pull again."}
	}
	return err
}
