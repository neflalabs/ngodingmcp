package config

import (
	"encoding/json"
	"errors"
	"os"
)

// SavedWorkspace is one entry of the Web UI workspace selector. Settings that
// are left empty (nil or zero) inherit the global defaults the server was
// started with.
type SavedWorkspace struct {
	// Name is the unique label shown in the selector.
	Name string `json:"name"`
	// Path is the absolute workspace root.
	Path string `json:"path"`
	// AllowSensitive overrides the sensitive-file guard for this workspace:
	// nil inherits the global setting, true turns the guard off, false on.
	AllowSensitive *bool `json:"allowSensitive,omitempty"`
	// MaxFileBytes overrides the per-file read/write limit; 0 inherits.
	MaxFileBytes int64 `json:"maxFileBytes,omitempty"`
}

// Update loads the config, applies fn and saves it, so fields other code
// owns (token, toggles, saved workspaces) are kept. Unlike Load it refuses to
// continue when the file exists but cannot be parsed, so a damaged file is
// never silently replaced by an almost empty one.
func Update(fn func(*Config)) error {
	p, err := Path()
	if err != nil {
		return err
	}
	var c Config
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &c); err != nil {
			return errors.New("config file is not valid JSON; fix or remove it first")
		}
	case errors.Is(err, os.ErrNotExist):
		// Start from an empty config.
	default:
		return errors.New("read config: " + err.Error())
	}
	fn(&c)
	return Save(c)
}
