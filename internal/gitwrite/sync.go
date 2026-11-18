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
package gitwrite

import (
	"context"
	"errors"
	"fmt"
	"ngodingmcp/internal/gitutil"
	"os"
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
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return 0, 0, fmt.Errorf("unexpected rev-list --count output %q", strings.TrimSpace(out))
		}
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
	ahead, err = strconv.Atoi(f[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected rev-list ahead count %q", f[0])
	}
	behind, err = strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected rev-list behind count %q", f[1])
	}
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

// UntrustedLocalKeys is defined in internal/gitutil so gitread and gitwrite
// share one list.

// checkLocalConfig refuses to run when the repository's own config sets a
// key from UntrustedLocalKeys. The user's global and system config are
// trusted; .git/config can be written by anything in the workspace.
func (g *Git) checkLocalConfig(ctx context.Context) error {
	return gitutil.CheckLocalConfig(ctx, g.ws.Root())
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
