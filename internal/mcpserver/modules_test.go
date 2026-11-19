package mcpserver_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
)

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	slices.Sort(names)
	return names
}

func TestParseModules(t *testing.T) {
	got, err := mcpserver.ParseModules([]string{" Git_Remote ", "", "inspect"})
	if err != nil {
		t.Fatalf("ParseModules: %v", err)
	}
	want := []mcpserver.Module{mcpserver.ModuleGitRemote, mcpserver.ModuleInspect}
	if !slices.Equal(got, want) {
		t.Fatalf("ParseModules = %v, want %v", got, want)
	}

	_, err = mcpserver.ParseModules([]string{"git"})
	if err == nil || !strings.Contains(err.Error(), "git_remote") {
		t.Fatalf("unknown module: err = %v, want an error listing the valid names", err)
	}
}

func TestDisabledModules(t *testing.T) {
	all := toolNames(listCoreTools(t))
	cases := []struct {
		module  mcpserver.Module
		removed []string
	}{
		{mcpserver.ModuleInspect, []string{"count_tokens", "validate_syntax", "workspace_stats"}},
		{mcpserver.ModuleGitWrite, []string{"git_add", "git_commit", "git_create_branch", "git_init", "git_switch_branch", "git_unstage"}},
		{mcpserver.ModuleGitRemote, []string{
			"git_fetch", "git_github_create_repo", "git_github_run_rerun", "git_github_run_view",
			"git_github_runs", "git_merge", "git_pull", "git_push", "git_remote",
		}},
	}
	for _, c := range cases {
		t.Run(string(c.module), func(t *testing.T) {
			got := toolNames(listCoreTools(t, c.module))
			var want []string
			for _, n := range all {
				if !slices.Contains(c.removed, n) {
					want = append(want, n)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("tools with %s disabled:\n got %v\nwant %v", c.module, got, want)
			}
		})
	}

	// The read-only git tools stay in every profile.
	minimal := toolNames(listCoreTools(t, mcpserver.ModuleInspect, mcpserver.ModuleGitWrite, mcpserver.ModuleGitRemote))
	for _, n := range []string{"git_status", "git_diff", "git_log", "read_file", "edit_file"} {
		if !slices.Contains(minimal, n) {
			t.Errorf("%s missing from the minimal profile: %v", n, minimal)
		}
	}
}
