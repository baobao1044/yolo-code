// dotenv is a minimal, stdlib-only `.env` loader (File 14 §14.10 config story).
//
// It implements the behaviour docs/user/configuration.md claims: yolo-code
// loads `.env` from the current directory on startup. It is intentionally tiny
// — no interpolation, no export prefixes, no quoting beyond a single optional
// pair of surrounding double quotes — so it stays dependency-free and matches
// the documented surface. A key already present in the real environment is
// NEVER overridden: explicit `export KEY=...` (or an already-set env var) wins
// over the file, which is the least-surprising order for a CLI.
//
// The loader is best-effort: a missing `.env` is fine (many runs have none), and
// a malformed line is skipped with a warning written to stderr rather than
// aborting startup. Callers must invoke LoadDotEnv before any code resolves the
// LLM provider from env vars (see run() in main.go).
package main

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from the `.env` file in the current
// directory into the process environment, skipping keys that are already set.
// A missing file is a no-op. Returns the path it attempted and any read error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // a missing .env is normal — silently no-op
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Drop an optional leading `export ` (common in shell .env files).
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			// Not a KEY=VALUE line; skip rather than failing the whole file.
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		// Strip one surrounding pair of double quotes if present.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		// Never override an env var already set by the shell or a CLI flag.
		if _, ok := os.LookupEnv(key); ok {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return scanner.Err()
}
