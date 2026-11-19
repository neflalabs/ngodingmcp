package mcpserver

import (
	"fmt"
	"sort"
	"strings"
)

// Module names a group of core tools that a profile can switch off to keep
// tools/list small. run_command, the process tools and download_file are
// not modules: they are already opt-in through Options.
type Module string

const (
	// ModuleInspect: workspace_stats, validate_syntax, count_tokens.
	ModuleInspect Module = "inspect"
	// ModuleGitWrite: git_init, git_add, git_unstage, git_commit,
	// git_create_branch, git_switch_branch.
	ModuleGitWrite Module = "git_write"
	// ModuleGitRemote: git_remote, git_fetch, git_pull, git_push, git_merge
	// and the GitHub tools (git_github_*).
	ModuleGitRemote Module = "git_remote"
)

// modules lists every module that can be disabled.
var modules = []Module{ModuleInspect, ModuleGitWrite, ModuleGitRemote}

// ModuleNames returns the names accepted by ParseModules, sorted.
func ModuleNames() []string {
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = string(m)
	}
	sort.Strings(names)
	return names
}

// ParseModules validates module names (case-insensitive, blanks skipped).
// An unknown name is an error that lists the valid ones, so a typo never
// silently leaves a module on.
func ParseModules(names []string) ([]Module, error) {
	var out []Module
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		m := Module(n)
		if !knownModule(m) {
			return nil, fmt.Errorf("unknown tool module %q (valid: %s)", n, strings.Join(ModuleNames(), ", "))
		}
		out = append(out, m)
	}
	return out, nil
}

func knownModule(m Module) bool {
	for _, k := range modules {
		if k == m {
			return true
		}
	}
	return false
}

// moduleSet answers whether a module is enabled.
type moduleSet map[Module]bool

func newModuleSet(disabled []Module) moduleSet {
	off := make(moduleSet, len(disabled))
	for _, m := range disabled {
		off[m] = true
	}
	return off
}

func (off moduleSet) enabled(m Module) bool { return !off[m] }
