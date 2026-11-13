package buildinfo

import (
	"strings"
	"testing"
	"time"
)

var exeTime = time.Date(2026, 11, 13, 8, 45, 0, 0, time.UTC)

func fixedExeTime() time.Time { return exeTime }

func TestResolve_CleanGitBuild(t *testing.T) {
	info := resolve(overrides{version: "v2026.11.12"}, map[string]string{
		"vcs":          "git",
		"vcs.revision": "4deb6fa0123456789abcdef0123456789abcdef0",
		"vcs.time":     "2026-11-13T08:20:01Z",
		"vcs.modified": "false",
	}, fixedExeTime)

	if got, want := info.Version(), "v2026.11.12+4deb6fa"; got != want {
		t.Fatalf("Version() = %q, want %q", got, want)
	}
	if info.Dirty {
		t.Fatal("clean build reported as dirty")
	}
	if want := time.Date(2026, 11, 13, 8, 20, 1, 0, time.UTC); !info.CommitTime.Equal(want) {
		t.Fatalf("CommitTime = %v, want %v", info.CommitTime, want)
	}
	if !info.BuildTime.Equal(exeTime) {
		t.Fatalf("BuildTime = %v, want executable mtime %v", info.BuildTime, exeTime)
	}
}

func TestResolve_DirtyGitBuild(t *testing.T) {
	info := resolve(overrides{version: "v2026.11.12"}, map[string]string{
		"vcs.revision": "4deb6fa0123456789abcdef0123456789abcdef0",
		"vcs.modified": "true",
	}, fixedExeTime)

	if got, want := info.Version(), "v2026.11.12+4deb6fa.dirty"; got != want {
		t.Fatalf("Version() = %q, want %q", got, want)
	}
	if !strings.Contains(info.Details(), "4deb6fa (with uncommitted changes)") {
		t.Fatalf("Details() does not mention uncommitted changes:\n%s", info.Details())
	}
}

func TestResolve_NoGitInfo(t *testing.T) {
	info := resolve(overrides{version: "v2026.11.12"}, nil, nil)

	if got, want := info.Version(), "v2026.11.12+dev"; got != want {
		t.Fatalf("Version() = %q, want %q", got, want)
	}
	if info.Built() != "" {
		t.Fatalf("Built() = %q, want empty when the build time is unknown", info.Built())
	}
	if !strings.Contains(info.Details(), "unknown (built without git information)") {
		t.Fatalf("Details() should explain the missing commit:\n%s", info.Details())
	}
}

func TestResolve_LdflagsOverrideVCS(t *testing.T) {
	info := resolve(overrides{
		version:   "v2026.12.0",
		commit:    "abcdef1234567",
		modified:  "true",
		buildTime: "2026-12-01T00:00:00Z",
	}, map[string]string{
		"vcs.revision": "4deb6fa0123456789abcdef0123456789abcdef0",
		"vcs.modified": "false",
	}, fixedExeTime)

	if got, want := info.Version(), "v2026.12.0+abcdef1.dirty"; got != want {
		t.Fatalf("Version() = %q, want %q", got, want)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); !info.BuildTime.Equal(want) {
		t.Fatalf("BuildTime = %v, want ldflags value %v", info.BuildTime, want)
	}
}

func TestResolve_InvalidBuildTimeFallsBackToExecutable(t *testing.T) {
	info := resolve(overrides{version: "v2026.11.12", buildTime: "yesterday"}, nil, fixedExeTime)
	if !info.BuildTime.Equal(exeTime) {
		t.Fatalf("BuildTime = %v, want executable mtime %v", info.BuildTime, exeTime)
	}
}

func TestResolve_EmptyBaseVersion(t *testing.T) {
	info := resolve(overrides{version: "  "}, nil, nil)
	if got, want := info.Version(), "v0.0.0+dev"; got != want {
		t.Fatalf("Version() = %q, want %q", got, want)
	}
}

func TestVersionHasNoSpaces(t *testing.T) {
	v := Version()
	if v == "" || strings.ContainsAny(v, " \t\n") {
		t.Fatalf("Version() = %q, want a non-empty string without spaces", v)
	}
	if !strings.HasPrefix(Get().Details(), "ngodingmcp "+v+"\n") {
		t.Fatalf("Details() must start with %q:\n%s", "ngodingmcp "+v, Get().Details())
	}
}
