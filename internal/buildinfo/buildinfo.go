// Package buildinfo reports the version of the running ngodingmcp binary.
//
// The version string has the form:
//
//	v2026.11.12+4deb6fa         clean build of commit 4deb6fa
//	v2026.11.12+4deb6fa.dirty   built with uncommitted changes
//	v2026.11.12+dev             no git information (go run, go test, source copy)
//
// The CalVer part (variable version below) is bumped by hand when a release
// is cut. The commit part comes from the VCS stamp that
// `go build` embeds automatically inside a git checkout, so a plain
// `go build -o ngodingmcp .` is enough. `make build` without an exact git
// tag or an explicit VERSION injects a development stamp (v<date>-dev)
// derived from today's date, so builds never share a stale CalVer line;
// `make release VERSION=v2026.12.0` overrides it. Any part can be
// overridden at link time:
//
//	go build -ldflags "-X ngodingmcp/internal/buildinfo.version=v2026.12.0"
//
// The build time comes from buildTime when it is set (the Makefile sets it),
// otherwise from the modification time of the executable, which go build writes.
// That is why the build time changes on every rebuild even when the commit does not.
package buildinfo

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Link-time overrides (-ldflags "-X ngodingmcp/internal/buildinfo.<name>=<value>").
var (
	// version is the CalVer release line.
	version = "v2026.11.13"
	// commit overrides the VCS revision, for builds outside a git checkout.
	commit = ""
	// modified overrides the VCS dirty flag ("true"/"false"); used together with commit.
	modified = ""
	// buildTime is the build time in RFC 3339; empty means "use the executable's mtime".
	buildTime = ""
)

// timeLayout is how times are shown to people, in the local time zone.
const timeLayout = "2006-01-02 15:04 MST"

// Info describes the running binary.
type Info struct {
	Base       string    // CalVer release line, e.g. v2026.11.12
	Commit     string    // short commit hash; empty when unknown
	Dirty      bool      // built with uncommitted changes
	CommitTime time.Time // zero when unknown
	BuildTime  time.Time // zero when unknown
	GoVersion  string    // e.g. go1.26.10
	Platform   string    // GOOS/GOARCH
}

var get = sync.OnceValue(func() Info {
	var settings map[string]string
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = make(map[string]string, len(bi.Settings))
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
	}
	return resolve(overrides{version: version, commit: commit, modified: modified, buildTime: buildTime}, settings, executableModTime)
})

// Get returns the version information of the running binary, computed once.
func Get() Info { return get() }

// Version is shorthand for Get().Version().
func Version() string { return Get().Version() }

type overrides struct {
	version, commit, modified, buildTime string
}

// resolve merges link-time overrides with the VCS settings embedded by go build.
// exeTime is the fallback for the build time and may be nil.
func resolve(o overrides, settings map[string]string, exeTime func() time.Time) Info {
	info := Info{
		Base:      strings.TrimSpace(o.version),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if info.Base == "" {
		info.Base = "v0.0.0"
	}

	if c := strings.TrimSpace(o.commit); c != "" {
		info.Commit = shortHash(c)
		info.Dirty, _ = strconv.ParseBool(strings.TrimSpace(o.modified))
	} else if rev := strings.TrimSpace(settings["vcs.revision"]); rev != "" {
		info.Commit = shortHash(rev)
		info.Dirty = settings["vcs.modified"] == "true"
		if t, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
			info.CommitTime = t
		}
	}

	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(o.buildTime)); err == nil {
		info.BuildTime = t
	} else if exeTime != nil {
		info.BuildTime = exeTime()
	}
	return info
}

// shortHash keeps the first 7 characters of a commit hash, like `git log --oneline`.
func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// executableModTime returns the modification time of the running binary, or
// the zero time when it cannot be read.
func executableModTime() time.Time {
	exe, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	st, err := os.Stat(exe)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Version returns the compact version string, e.g. "v2026.11.12+4deb6fa.dirty".
// It contains no spaces, so it is safe for the MCP Implementation.Version field.
func (i Info) Version() string {
	if i.Commit == "" {
		return i.Base + "+dev"
	}
	v := i.Base + "+" + i.Commit
	if i.Dirty {
		v += ".dirty"
	}
	return v
}

// Built returns the build time in local time ("2026-11-13 17:45 WIT"), or ""
// when it is unknown.
func (i Info) Built() string {
	if i.BuildTime.IsZero() {
		return ""
	}
	return i.BuildTime.Local().Format(timeLayout)
}

// Details returns the multi-line text printed by `ngodingmcp version`. The
// first line is always "ngodingmcp <version>".
func (i Info) Details() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ngodingmcp %s\n", i.Version())
	switch {
	case i.Commit == "":
		b.WriteString("  commit:    unknown (built without git information)\n")
	case i.Dirty:
		fmt.Fprintf(&b, "  commit:    %s (with uncommitted changes)\n", i.Commit)
	default:
		fmt.Fprintf(&b, "  commit:    %s\n", i.Commit)
	}
	if !i.CommitTime.IsZero() {
		fmt.Fprintf(&b, "  committed: %s\n", i.CommitTime.Local().Format(timeLayout))
	}
	if built := i.Built(); built != "" {
		fmt.Fprintf(&b, "  built:     %s\n", built)
	}
	fmt.Fprintf(&b, "  go:        %s %s\n", i.GoVersion, i.Platform)
	return b.String()
}
