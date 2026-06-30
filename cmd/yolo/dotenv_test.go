package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDotEnv_BasicParse covers the documented surface: KEY=VALUE lines are
// loaded, comments and blanks skipped, surrounding quotes stripped, and an
// `export ` prefix tolerated.
func TestLoadDotEnv_BasicParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# a comment\n\nKEY1=value1\nKEY2=\"quoted value\"\nexport KEY3=value3\nNOEQ line should be skipped\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	// Ensure none of these leak from the shell.
	for _, k := range []string{"KEY1", "KEY2", "KEY3"} {
		os.Unsetenv(k)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("KEY1"); got != "value1" {
		t.Errorf("KEY1 = %q, want %q", got, "value1")
	}
	if got := os.Getenv("KEY2"); got != "quoted value" {
		t.Errorf("KEY2 = %q, want %q", got, "quoted value")
	}
	if got := os.Getenv("KEY3"); got != "value3" {
		t.Errorf("KEY3 = %q, want %q", got, "value3")
	}
}

// TestLoadDotEnv_NeverOverridesEnv verifies the precedence guarantee: a key
// already present in the environment is NOT overwritten by the .env file. This
// is the behaviour configuration.md relies on (shell env beats .env).
func TestLoadDotEnv_NeverOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("YOLO_MODEL=from-file\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("YOLO_MODEL", "from-shell")
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("YOLO_MODEL"); got != "from-shell" {
		t.Errorf("YOLO_MODEL = %q, want %q (env must win over .env)", got, "from-shell")
	}
}

// TestLoadDotEnv_MissingFileIsNoOp confirms a missing .env is silently ignored
// (many runs have none), returning nil and changing no env vars.
func TestLoadDotEnv_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YOLO_MODEL", "unchanged")
	if err := LoadDotEnv(filepath.Join(dir, "does-not-exist.env")); err != nil {
		t.Fatalf("LoadDotEnv on missing file: %v", err)
	}
	if got := os.Getenv("YOLO_MODEL"); got != "unchanged" {
		t.Errorf("YOLO_MODEL = %q, want %q", got, "unchanged")
	}
}
