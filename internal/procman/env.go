package procman

import (
	"fmt"
	"sort"
	"strings"
)

func normalizeEnv(env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if !allowedEnvKeys[key] {
			return nil, fmt.Errorf("%w: %s", ErrInvalidEnv, key)
		}
		if strings.ContainsRune(v, 0) || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: %s contains control characters", ErrInvalidEnv, key)
		}
		out[key] = v
	}
	return out, nil
}

func envPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
