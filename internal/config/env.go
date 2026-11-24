package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults shared by `ngodingmcp serve` and the TUI.
const (
	DefaultAddr           = "127.0.0.1:8080"
	DefaultTrustedProxies = "loopback"
	DefaultRunTimeout     = time.Minute
	// DefaultTransferMaxBytes caps one download_file call and one Web UI
	// upload.
	DefaultTransferMaxBytes int64 = 20 << 20
)

// DefaultRoot is the workspace used when neither a flag, the environment nor
// config.json names one: ~/ngodingmcp-workspace.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, "ngodingmcp-workspace")
}

// Settings are the server settings before command-line flags are applied.
// Each one comes from its NGODINGMCP_* environment variable, else from
// config.json, else from the built-in default. Flags (serve and the TUI
// alike) then override them, so the full order is flag > env > config.json >
// default.
type Settings struct {
	Root  string
	Addr  string
	Token string

	AllowRun   bool
	RunAllow   string // comma-separated prefixes; empty = built-in policy
	RunExact   string // comma-separated exact commands; empty = built-in policy
	RunTimeout time.Duration

	// AllowSensitive follows the same order as every other setting:
	// NGODINGMCP_ALLOW_SENSITIVE, then config.json, then false. A saved
	// toggle therefore survives a restart of `serve` or a service restart.
	AllowSensitive bool

	AllowDownload    bool
	DownloadHosts    string // comma-separated; empty = any public host
	DownloadMaxBytes int64
	UploadMaxBytes   int64

	PublicURL      string
	OAuth          bool
	TrustedProxies string // comma-separated

	// DisableTools lists core tool modules to leave out (comma-separated);
	// the names are validated where the server is built (app.New).
	DisableTools string
}

// Resolve reads the environment on top of saved. Malformed values are
// errors that name the variable (for example NGODINGMCP_OAUTH=yes), never a
// silent fallback; all of them are reported together.
func Resolve(saved Config) (Settings, error) {
	var errs []error
	boolean := func(key string, fallback bool) bool {
		b, err := EnvBool(key, fallback)
		errs = append(errs, err)
		return b
	}
	bytes := func(key string) int64 {
		n, err := EnvInt64(key, DefaultTransferMaxBytes)
		errs = append(errs, err)
		return n
	}
	oauth := true
	if saved.OAuth != nil {
		oauth = *saved.OAuth
	}

	s := Settings{
		Root:  EnvString("NGODINGMCP_ROOT", FirstNonEmpty(saved.Root, DefaultRoot())),
		Addr:  EnvString("NGODINGMCP_ADDR", FirstNonEmpty(saved.Addr, DefaultAddr)),
		Token: EnvString("NGODINGMCP_TOKEN", saved.Token),

		AllowRun: boolean("NGODINGMCP_ALLOW_RUN", saved.Run.Enabled),
		RunAllow: os.Getenv("NGODINGMCP_RUN_ALLOWLIST"),
		RunExact: os.Getenv("NGODINGMCP_RUN_ALLOWLIST_EXACT"),

		AllowSensitive: boolean("NGODINGMCP_ALLOW_SENSITIVE", saved.AllowSensitive),

		AllowDownload:    boolean("NGODINGMCP_ALLOW_DOWNLOAD", saved.AllowDownload),
		DownloadHosts:    os.Getenv("NGODINGMCP_DOWNLOAD_ALLOW_HOSTS"),
		DownloadMaxBytes: bytes("NGODINGMCP_DOWNLOAD_MAX_BYTES"),
		UploadMaxBytes:   bytes("NGODINGMCP_UPLOAD_MAX_BYTES"),

		PublicURL:      EnvString("NGODINGMCP_PUBLIC_URL", saved.PublicURL),
		OAuth:          boolean("NGODINGMCP_OAUTH", oauth),
		TrustedProxies: EnvString("NGODINGMCP_TRUSTED_PROXIES", FirstNonEmpty(strings.Join(saved.TrustedProxies, ","), DefaultTrustedProxies)),

		DisableTools: EnvString("NGODINGMCP_DISABLE_TOOLS", strings.Join(saved.DisableTools, ",")),
	}
	var err error
	s.RunTimeout, err = EnvDuration("NGODINGMCP_RUN_TIMEOUT", DefaultRunTimeout)
	errs = append(errs, err)
	return s, errors.Join(errs...)
}

// EnvString returns the trimmed value of key, or fallback when it is unset
// or blank.
func EnvString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// EnvBool parses key strictly as a boolean; unset or blank means fallback.
func EnvBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback, fmt.Errorf("%s: invalid boolean %q (use 1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False)", key, v)
	}
	return b, nil
}

// EnvDuration parses key as a positive duration such as 30s or 5m; unset or
// blank means fallback.
func EnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback, fmt.Errorf("%s: invalid duration %q (examples: 30s, 5m)", key, v)
	}
	if d <= 0 {
		return fallback, fmt.Errorf("%s: duration must be positive, got %q", key, v)
	}
	return d, nil
}

// EnvInt64 parses key as a positive integer; unset or blank means fallback.
func EnvInt64(key string, fallback int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s: invalid integer %q", key, v)
	}
	if n <= 0 {
		return fallback, fmt.Errorf("%s: value must be positive, got %q", key, v)
	}
	return n, nil
}
