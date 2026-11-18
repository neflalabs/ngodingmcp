package gitwrite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

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
