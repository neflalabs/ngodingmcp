package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	// stateVersion is the version written to oauth.json.
	//   1: signing key + registered clients.
	//   2: adds refreshTokens and consumedRefreshTokens (hashes only).
	// Older versions still load; missing fields just mean "no tokens".
	stateVersion         = 2
	oldestStateVersion   = 1
	maxRegisteredClients = 200
	maxRedirectURIs      = 8
	maxClientNameLen     = 128
)

// registeredClient is a DCR-registered public client, persisted to oauth.json.
type registeredClient struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	CreatedAt               int64    `json:"created_at"`
	// LastUsedAt is the Unix time of the most recent token issue for this
	// client; 0 means never used. Persisted so eviction decisions survive
	// restarts.
	LastUsedAt int64 `json:"last_used_at,omitempty"`
}

type persistedState struct {
	Version       int                `json:"version"`
	SigningKeyPEM string             `json:"signingKey"`
	SigningKeyID  string             `json:"signingKeyID"`
	Clients       []registeredClient `json:"clients,omitempty"`
	// RefreshTokens and ConsumedRefreshTokens hold hashes only; see
	// refreshstore.go. Absent in version 1 files.
	RefreshTokens         []persistedRefreshToken  `json:"refreshTokens,omitempty"`
	ConsumedRefreshTokens []persistedConsumedToken `json:"consumedRefreshTokens,omitempty"`
}

// defaultStatePath resolves the OAuth state file location, defaulting to
// ~/.config/ngodingmcp/oauth.json.
func defaultStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ngodingmcp", "oauth.json"), nil
}

// loadState reads oauth.json. A missing file is not an error (first run).
func loadState(path string) (*persistedState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("oauth state file %s is not valid JSON: %w", path, err)
	}
	if st.Version < oldestStateVersion || st.Version > stateVersion {
		return nil, fmt.Errorf("oauth state file %s has unsupported version %d", path, st.Version)
	}
	return &st, nil
}

// saveState writes oauth.json with owner-only permissions, mirroring the
// atomic temp+rename pattern used for config.json.
func saveState(path string, st *persistedState) error {
	sort.Slice(st.Clients, func(i, j int) bool { return st.Clients[i].ClientID < st.Clients[j].ClientID })

	dir := filepath.Dir(path)
	// MkdirAll leaves the permissions of already-existing directories
	// untouched. The file inside gets 0600, so an owner-created 0755
	// directory is acceptable; a group/other-WRITABLE one is not, and its
	// permissions are not silently rewritten either.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if !st.IsDir() || st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("oauth state directory %s is group/other-writable (%04o) or not a directory; make it 0700 or set StatePath elsewhere", dir, st.Mode().Perm())
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}

	// os.CreateTemp creates the file with mode 0600.
	tmp, err := os.CreateTemp(dir, ".oauth-*.json")
	if err != nil {
		return errors.New("write oauth state: " + err.Error())
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return errors.New("write oauth state: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return errors.New("write oauth state: " + err.Error())
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return errors.New("write oauth state: " + err.Error())
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return errors.New("write oauth state: " + err.Error())
	}
	return nil
}
