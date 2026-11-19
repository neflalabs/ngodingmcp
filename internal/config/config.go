// Package config persists the last-used TUI settings, including the run_command
// policy. The file lives outside the workspace so an agent cannot edit it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the fields remembered between TUI runs.
type Config struct {
	Root  string    `json:"root"`
	Addr  string    `json:"addr"`
	Token string    `json:"token"`
	Run   RunConfig `json:"run,omitempty"`
	// AllowSensitive turns off the sensitive-file guard (.env, private keys,
	// .git internals). False (guarded) when missing from older config files.
	AllowSensitive bool `json:"allowSensitive,omitempty"`
	// AllowDownload enables the opt-in download_file tool. False (off) when
	// missing from older config files.
	AllowDownload bool `json:"allowDownload,omitempty"`
	// Workspaces are the saved workspaces listed in the Web UI selector.
	Workspaces []SavedWorkspace `json:"workspaces,omitempty"`
	// PublicURL is the external base URL of this server (e.g.
	// "https://mcp.example.com"), used as the OAuth issuer/resource
	// identifier. Empty means it is derived per request from the Host header.
	PublicURL string `json:"publicUrl,omitempty"`
	// TrustedProxies lists the peers whose X-Forwarded-Proto header is
	// honored when deriving the OAuth issuer (e.g. "loopback", "private",
	// "none", CIDRs or IPs). Empty means "loopback".
	TrustedProxies []string `json:"trustedProxies,omitempty"`
	// OAuth enables the embedded OAuth authorization server for /mcp.
	// Absent (nil) means enabled.
	OAuth *bool `json:"oauth,omitempty"`
	// DisableTools names core tool modules left out of tools/list (inspect,
	// git_write, git_remote). Empty means every module is on.
	DisableTools []string `json:"disableTools,omitempty"`
}

// RunConfig is the persisted run_command switch. The allowed commands are
// predefined in code (see internal/runner.DefaultPolicy); only the on/off flag
// is user-configurable.
type RunConfig struct {
	Enabled bool `json:"enabled"`
}

// SplitList splits a comma-separated list, trimming blanks.
func SplitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// FirstNonEmpty returns the first non-blank value, or "" if none.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Path returns the config file location.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ngodingmcp", "config.json"), nil
}

// Load reads the config, returning an empty Config when the file is missing.
// A file that exists but cannot be parsed is moved aside to
// config.json.corrupt instead of being silently replaced by an empty config,
// so the user can see and recover their real settings.
func Load() Config {
	p, err := Path()
	if err != nil {
		return Config{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		aside := p + ".corrupt"
		_ = os.Rename(p, aside)
		fmt.Fprintf(os.Stderr, "config: %s is not valid JSON; moved it to %s (%v)\n", p, aside, err)
		return Config{}
	}
	return c
}

// Save writes the config with owner-only permissions because it stores the
// auth token in plaintext: the directory is 0700 and the file 0600. Existing
// files and directories with looser permissions are tightened, and the file is
// replaced atomically so a crash never leaves a half-written token behind.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll keeps the mode of a directory that already exists.
	if err := os.Chmod(dir, 0o700); err != nil {
		return errors.New("secure config dir: " + err.Error())
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	// os.CreateTemp creates the file with mode 0600.
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return errors.New("write config: " + err.Error())
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return errors.New("write config: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return errors.New("write config: " + err.Error())
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return errors.New("write config: " + err.Error())
	}
	if err := os.Rename(tmpName, p); err != nil {
		cleanup()
		return errors.New("write config: " + err.Error())
	}
	return nil
}
