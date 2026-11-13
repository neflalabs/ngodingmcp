package workspace

import (
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ErrSensitive is returned when a path points at a secret-bearing file or
// directory (for example .env, private keys, or .git internals).
var ErrSensitive = errors.New("access to sensitive path is blocked (restart with --allow-sensitive to permit)")

// sensitiveDirs are directory names whose whole subtree is blocked.
var sensitiveDirs = map[string]bool{
	".git":   true,
	".ssh":   true,
	".aws":   true,
	".gnupg": true,
}

// sensitiveNames are exact file names (case-insensitive) that are blocked.
var sensitiveNames = map[string]bool{
	".env":             true,
	".npmrc":           true,
	".pypirc":          true,
	".netrc":           true,
	".git-credentials": true,
	".htpasswd":        true,
	"id_rsa":           true,
	"id_dsa":           true,
	"id_ecdsa":         true,
	"id_ed25519":       true,
}

// sensitiveExts are file extensions (case-insensitive) that are blocked.
var sensitiveExts = map[string]bool{
	".pem":      true,
	".key":      true,
	".p12":      true,
	".pfx":      true,
	".jks":      true,
	".keystore": true,
}

// envTemplates are .env.* variants meant to be committed, so they stay
// readable.
var envTemplates = map[string]bool{
	".env.example":  true,
	".env.sample":   true,
	".env.template": true,
	".env.dist":     true,
}

// IsSensitive reports whether the slash-separated relative path rel names a
// secret-bearing file or lives inside a blocked directory.
func IsSensitive(rel string) bool {
	rel = strings.Trim(path.Clean(filepath.ToSlash(rel)), "/")
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, dir := range parts[:len(parts)-1] {
		if sensitiveDirs[strings.ToLower(dir)] {
			return true
		}
	}
	return sensitiveName(parts[len(parts)-1])
}

func sensitiveName(name string) bool {
	name = strings.ToLower(name)
	if sensitiveDirs[name] || sensitiveNames[name] {
		return true
	}
	if strings.HasPrefix(name, ".env.") {
		return !envTemplates[name]
	}
	return sensitiveExts[path.Ext(name)]
}

// SensitivePatterns returns git pathspec exclusions matching the blocked
// names, so read-only git tools never print their contents.
func SensitivePatterns() []string {
	var out []string
	for d := range sensitiveDirs {
		out = append(out, ":(exclude,glob)**/"+d+"/**")
	}
	for n := range sensitiveNames {
		out = append(out, ":(exclude,glob)**/"+n)
	}
	for e := range sensitiveExts {
		out = append(out, ":(exclude,glob)**/*"+e)
	}
	// .env.example and friends are hidden from git output too; exclude magic
	// cannot re-include them, and hiding a template is the safe direction.
	out = append(out, ":(exclude,glob)**/.env.*")
	sort.Strings(out)
	return out
}

// SetAllowSensitive disables (true) or enables (false) sensitive-path
// blocking. Blocking is on by default.
func (w *Workspace) SetAllowSensitive(allow bool) { w.allowSensitive = allow }

// AllowSensitive reports whether sensitive paths are accessible.
func (w *Workspace) AllowSensitive() bool { return w.allowSensitive }

// blocked reports whether the absolute path full must be hidden.
func (w *Workspace) blocked(full string) bool {
	return !w.allowSensitive && IsSensitive(w.relPath(full))
}

// safeError keeps the original error for errors.Is/As but hides host paths.
type safeError struct {
	msg string
	err error
}

func (e *safeError) Error() string { return e.msg }
func (e *safeError) Unwrap() error { return e.err }

// SafeError rewrites absolute workspace paths in err's message into
// workspace-relative ones, so clients never learn the host directory layout.
// errors.Is and errors.As still see the original error.
func (w *Workspace) SafeError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, w.root) {
		return err
	}
	return &safeError{msg: w.ScrubPaths(msg), err: err}
}

// ScrubPaths replaces the absolute workspace root in s with workspace-relative
// paths ("<root>/a/b" becomes "a/b", a bare "<root>" becomes ".").
func (w *Workspace) ScrubPaths(s string) string {
	if w.root == "" || !strings.Contains(s, w.root) {
		return s
	}
	s = strings.ReplaceAll(s, w.root+string(filepath.Separator), "")
	return strings.ReplaceAll(s, w.root, ".")
}
