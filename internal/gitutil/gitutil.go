// Package gitutil holds the safety helpers shared by the gitread and
// gitwrite packages: the repository-local config guard, the environment
// denylist and the revision-name validator.
package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// UntrustedLocalKeys matches .git/config keys that could run a program,
// redirect a remote or pull in another config file. The user's global and
// system config are trusted; .git/config can be written by anything in the
// workspace. Git lowercases section and variable names in --list output.
var UntrustedLocalKeys = []*regexp.Regexp{
	regexp.MustCompile(`^core\.(sshcommand|askpass|gitproxy|fsmonitor)$`),
	regexp.MustCompile(`^credential\.(.+\.)?helper$`),
	regexp.MustCompile(`^url\..+\.(insteadof|pushinsteadof)$`),
	regexp.MustCompile(`^remote\..+\.(uploadpack|receivepack)$`),
	regexp.MustCompile(`^filter\..+\.(clean|smudge|process)$`),
	regexp.MustCompile(`^diff\.(external|.+\.(textconv|command))$`),
	regexp.MustCompile(`^merge\..+\.driver$`),
	regexp.MustCompile(`^include\.`),
	regexp.MustCompile(`^includeif\.`),
}

// CheckLocalConfig refuses to run when the repository's own config sets a
// key from UntrustedLocalKeys. Without this guard, filter drivers listed in
// .gitattributes would execute programs during ordinary index/worktree
// operations. Keys belonging to a plain git-lfs install are refused by the
// same rule on purpose: the key names cannot be told apart from a hostile
// `filter.lfs.*` written by anything in the workspace. Operators who need
// git-lfs in a workspace should move the filter config to the global git
// config, which is trusted, as the message below explains.
func CheckLocalConfig(ctx context.Context, root string) error {
	bin, err := exec.LookPath("git")
	if err != nil {
		// The caller surfaces a missing git binary by itself; do not
		// block on the guard failing here.
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, "config", "--local", "--name-only", "--list")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// Not a repository yet (Init), no local values at all, or an
		// unreadable config: in all three cases every guarded command
		// re-validates the repository itself and git refuses to run on a
		// config it cannot parse. The guard only adds a refusal; it is
		// not the hard boundary.
		return nil
	}
	var bad []string
	for _, key := range strings.Split(string(out), "\n") {
		key = strings.TrimSpace(key)
		low := strings.ToLower(key)
		if low == "" {
			continue
		}
		for _, re := range UntrustedLocalKeys {
			if re.MatchString(low) {
				bad = append(bad, key)
				break
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to run: the repository's own config (.git/config) sets %s, which could run a program, redirect credentials or pull in more config. Put such settings in your global git config (~/.gitconfig) instead and remove them here with `git config --local --unset <key>`", strings.Join(bad, ", "))
}

// envDenylist names environment variables that can redirect git to a
// different repository or inject configuration (equivalent to passing -c
// flags) without writing .git/config.
var envDenylist = map[string]bool{
	"GIT_DIR":                        true,
	"GIT_WORK_TREE":                  true,
	"GIT_INDEX_FILE":                 true,
	"GIT_COMMON_DIR":                 true,
	"GIT_CEILING_DIRECTORIES":        true,
	"GIT_OBJECT_DIRECTORY":           true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_EXTERNAL_DIFF":              true,
	"GIT_EXEC_PATH":                  true,
	"GIT_TEMPLATE_DIR":               true,
	"GIT_SSH":                        true,
	"GIT_SSH_COMMAND":                true,
	"GIT_SSH_VARIANT":                true,
	"GIT_ASKPASS":                    true,
	"GIT_CONFIG_PARAMETERS":          true,
	"GIT_CONFIG_GLOBAL":              true,
	"GIT_CONFIG_SYSTEM":              true,
	"GIT_CONFIG_NOSYSTEM":            true,
	"GIT_CONFIG_COUNT":               true,
}

// SanitizeEnv returns env with git steering variables removed. GIT_CONFIG_
// KEY_n and GIT_CONFIG_VALUE_n use numbered suffixes and are dropped as a
// group.
func SanitizeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if envDenylist[key] {
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ValidRef accepts ordinary revision names only: letters, digits and
// . _ / - ~ ^ @ with no leading dash and no ".." anywhere (so a revision
// range such as "main..origin/main" can never be smuggled in).
func ValidRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || len(ref) > 100 ||
		strings.Contains(ref, "..") {
		return fmt.Errorf("invalid ref %q", ref)
	}
	for _, c := range ref {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("._/-~^@", c)
		if !ok {
			return fmt.Errorf("invalid ref %q", ref)
		}
	}
	return nil
}
