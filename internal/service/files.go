package service

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// renderUnit builds the unit file. exe is the absolute path of the binary;
// envRef is the EnvironmentFile path as written in the unit (it may use the
// %h specifier).
func renderUnit(exe, envRef string) string {
	return `# Managed by "ngodingmcp -d": running it again rewrites this file, for
# example after the binary moved. Put the token and other settings in the
# environment file below, not here.
[Unit]
Description=NgodingMCP (sandboxed MCP server and Web UI)
After=network.target

[Service]
Type=simple
ExecStart=` + quoteExecArg(exe) + ` serve
EnvironmentFile=-` + escapeSpecifiers(envRef) + `
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
}

// quoteExecArg quotes one ExecStart argument: double quotes with \ and "
// escaped, % doubled (specifiers) and $ doubled (variable expansion).
func quoteExecArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

// escapeSpecifiers doubles % except in a leading %h, which systemd expands to
// the home directory.
func escapeSpecifiers(s string) string {
	if rest, ok := strings.CutPrefix(s, "%h/"); ok {
		return "%h/" + strings.ReplaceAll(rest, "%", "%%")
	}
	return strings.ReplaceAll(s, "%", "%%")
}

// envRef is how the unit names the env file: with %h when it sits in the home
// directory, so the unit stays readable; otherwise the absolute path.
func envRef(envPath, home string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, envPath); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "%h/" + filepath.ToSlash(rel)
		}
	}
	return envPath
}

// execPathFromUnit reads the binary path back out of a unit's ExecStart line.
func execPathFromUnit(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, `"`) {
			var b strings.Builder
			for i := 1; i < len(rest); i++ {
				c := rest[i]
				switch {
				case c == '\\' && i+1 < len(rest):
					i++
					b.WriteByte(rest[i])
				case c == '"':
					return unescapeDoubled(b.String())
				default:
					b.WriteByte(c)
				}
			}
			return unescapeDoubled(b.String())
		}
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			rest = rest[:i]
		}
		return unescapeDoubled(rest)
	}
	return ""
}

func unescapeDoubled(s string) string {
	return strings.NewReplacer("%%", "%", "$$", "$").Replace(s)
}

// writeFileAtomic writes data to path through a temporary file and a rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

const envHeader = `# NgodingMCP background service settings (read by systemd, see "ngodingmcp -d").
# After editing, apply with: ngodingmcp restart
#
# NGODINGMCP_TOKEN is the shared token for the Web UI and for MCP clients.
# Other optional settings, for example:
#   NGODINGMCP_ADDR=127.0.0.1:8080
#   NGODINGMCP_ALLOW_RUN=true
# Leave NGODINGMCP_ROOT out: the workspace chosen in the Web UI is remembered
# in config.json, and NGODINGMCP_ROOT here would override it on every restart.
`

var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9._~+/=:@,-]+$`)

// envLine renders KEY=value, quoting the value when systemd would otherwise
// split or reinterpret it.
func envLine(key, value string) string {
	if plainEnvValue.MatchString(value) {
		return key + "=" + value + "\n"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return key + `="` + r.Replace(value) + `"` + "\n"
}

// readEnvFile parses KEY=value lines; comments, blank lines and malformed
// lines are skipped. A missing file is an empty map.
func readEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			q := value[0]
			value = value[1 : len(value)-1]
			if q == '"' {
				value = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(value)
			}
		}
		out[key] = value
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}
