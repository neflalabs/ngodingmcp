package gitwrite

// GitHub repository creation through the GitHub CLI (gh). gh keeps the
// user's own login (`gh auth login`, or GH_TOKEN in the environment), so
// ngodingmcp never sees or stores a token. Only `gh repo create` runs, with
// fixed arguments: no clone, no push, no template, no deletion.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ErrNoGH is returned when the gh binary is not installed.
var ErrNoGH = errors.New("the GitHub CLI (gh) is not installed or not on PATH; install it (on Fedora: sudo dnf install gh), run `gh auth login`, then restart ngodingmcp")

// GitHubRepoOptions selects the repository git_github_create_repo creates.
type GitHubRepoOptions struct {
	// Name is the repository name; Owner is an optional organisation (empty
	// means the logged-in user).
	Name  string
	Owner string
	// Visibility is private (default), public or internal.
	Visibility  string
	Description string
	// Remote is the remote name added for the new repository (default
	// origin). SkipRemote creates the repository only.
	Remote     string
	SkipRemote bool
}

// GitHubRepoResult reports one created repository.
type GitHubRepoResult struct {
	URL         string `json:"url"`
	Visibility  string `json:"visibility"`
	Remote      string `json:"remote,omitempty"`
	RemoteURL   string `json:"remote_url,omitempty"`
	RemoteAdded bool   `json:"remote_added"`
	Note        string `json:"note,omitempty"`
}

var (
	ghRepoName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	ghOwnerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	ghRepoURL   = regexp.MustCompile(`https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)`)
)

// CreateGitHubRepo creates an empty repository on GitHub with gh and, unless
// SkipRemote is set, adds it as a remote of the workspace repository. It
// never pushes: call Push afterwards.
func (g *Git) CreateGitHubRepo(ctx context.Context, o GitHubRepoOptions) (GitHubRepoResult, error) {
	name := strings.TrimSpace(o.Name)
	owner := strings.TrimSpace(o.Owner)
	if !ghRepoName.MatchString(name) || name == "." || name == ".." || strings.HasPrefix(name, "-") {
		return GitHubRepoResult{}, fmt.Errorf("invalid repository name %q (use letters, digits, '.', '_' or '-', at most 100)", name)
	}
	if owner != "" && !ghOwnerName.MatchString(owner) {
		return GitHubRepoResult{}, fmt.Errorf("invalid owner %q (a GitHub user or organisation name)", owner)
	}
	vis := strings.ToLower(strings.TrimSpace(o.Visibility))
	if vis == "" {
		vis = "private"
	}
	if vis != "private" && vis != "public" && vis != "internal" {
		return GitHubRepoResult{}, fmt.Errorf("visibility must be private, public or internal, not %q", o.Visibility)
	}
	desc := strings.TrimSpace(o.Description)
	if len(desc) > 350 || strings.ContainsAny(desc, "\r\n\x00") {
		return GitHubRepoResult{}, errors.New("description must be one line of at most 350 characters")
	}
	remote := strings.TrimSpace(o.Remote)
	if remote == "" {
		remote = "origin"
	}
	if !o.SkipRemote {
		if err := ValidRemoteName(remote); err != nil {
			return GitHubRepoResult{}, err
		}
		if err := g.requireRoot(ctx); err != nil {
			return GitHubRepoResult{}, err
		}
		exists, err := g.remoteExists(ctx, remote)
		if err != nil {
			return GitHubRepoResult{}, err
		}
		if exists {
			return GitHubRepoResult{}, fmt.Errorf("remote %q already exists; pass another remote name, or skip_remote=true to only create the repository", remote)
		}
	}

	full := name
	if owner != "" {
		full = owner + "/" + name
	}
	args := []string{"repo", "create", full, "--" + vis}
	if desc != "" {
		args = append(args, "--description", desc)
	}
	out, err := g.gh(ctx, args...)
	if err != nil {
		return GitHubRepoResult{}, err
	}
	m := ghRepoURL.FindStringSubmatch(out)
	if m == nil {
		return GitHubRepoResult{}, fmt.Errorf("gh created the repository but printed no GitHub URL (output: %q); add the remote by hand with git_remote action=add", strings.TrimSpace(out))
	}
	res := GitHubRepoResult{URL: "https://github.com/" + m[1] + "/" + m[2], Visibility: vis}
	if o.SkipRemote {
		res.Note = "Repository created; no remote was added."
		return res, nil
	}
	res.Remote = remote
	res.RemoteURL = res.URL + ".git"
	if _, err := g.AddRemote(ctx, remote, res.RemoteURL); err != nil {
		res.Note = "Repository created on GitHub, but adding the remote failed: " + err.Error()
		return res, nil
	}
	res.RemoteAdded = true
	res.Note = fmt.Sprintf("Repository created and added as remote %s. Nothing was pushed yet: run git_push with set_upstream=true.", remote)
	return res, nil
}

// gh runs one gh command detached from any terminal, with prompts, pagers,
// colours and the update check turned off, and returns stdout.
func (g *Git) gh(ctx context.Context, args ...string) (string, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return "", ErrNoGH
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, g.remoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = g.ws.Root()
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_PAGER=cat",
		"NO_COLOR=1", "CLICOLOR=0", "GH_SPINNER_DISABLED=1", "BROWSER=false")
	detach(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("gh %s timed out after %s: %w", firstCommand(args), g.remoteTimeout, context.DeadlineExceeded)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		sub := "gh"
		if len(args) >= 2 && !strings.HasPrefix(args[0], "-") && !strings.HasPrefix(args[1], "-") {
			sub = "gh " + args[0] + " " + args[1]
		} else if len(args) >= 1 && !strings.HasPrefix(args[0], "-") {
			sub = "gh " + args[0]
		}
		e := g.ws.SafeError(&scrubbedError{msg: ScrubCredentials(sub + ": " + msg), err: err})
		if h := ghHint(msg); h != "" {
			return "", &hintError{err: e, hint: h}
		}
		return "", e
	}
	return g.ws.ScrubPaths(ScrubCredentials(stdout.String())), nil
}

func ghHint(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "gh auth login") || strings.Contains(low, "not logged") ||
		strings.Contains(low, "authentication") || strings.Contains(low, "401") || strings.Contains(low, "bad credentials"):
		return "gh is not logged in. Run `gh auth login` once in a terminal as the same user. For the background service (ngodingmcp -d), if gh stores its token in the desktop keyring the service may not reach it: run `gh auth login --insecure-storage`, or put GH_TOKEN=<token> in ~/.config/ngodingmcp/env and run `ngodingmcp restart`."
	case strings.Contains(low, "name already exists"):
		return "a repository with this name already exists on that account; pick another name, or add the existing one with git_remote action=add."
	case strings.Contains(low, "could not find any workflows") || strings.Contains(low, "no workflow runs"):
		return "no GitHub Actions workflow runs exist on this repository yet, or the workflow file name did not match."
	case strings.Contains(low, "403") || strings.Contains(low, "resource not accessible"):
		return "the gh token may lack permission (scopes repo and workflow are needed to inspect or re-run GitHub Actions)."
	}
	return ""
}
