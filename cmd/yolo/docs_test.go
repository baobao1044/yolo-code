//go:build docs

// Doc-coverage test (Sprint 11 H-007). docs/user/SUMMARY.md is the source of
// truth for user-facing documentation; this test fails when a file is missing,
// unlisted, or a link points to nothing.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const docsDir = "../../docs/user"

func TestUserDocsCoverage(t *testing.T) {
	summaryPath := filepath.Join(docsDir, "SUMMARY.md")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read SUMMARY: %v", err)
	}

	linkRe := regexp.MustCompile(`(?m)^\s*-\s+\[[^\]]*\]\(([^)]+)\)\s*$`)
	matches := linkRe.FindAllStringSubmatch(string(data), -1)

	listed := make(map[string]bool)
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" {
			t.Fatalf("empty filename in SUMMARY link")
		}
		if listed[name] {
			t.Fatalf("duplicate SUMMARY entry: %s", name)
		}
		listed[name] = true

		path := filepath.Join(docsDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("SUMMARY links to missing file: %s (%v)", name, err)
		}
	}

	entries, err := os.ReadDir(docsDir)
	if err != nil {
		t.Fatalf("read docs dir: %v", err)
	}
	var extra []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == "SUMMARY.md" {
			continue
		}
		if !listed[e.Name()] {
			extra = append(extra, e.Name())
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Fatalf("docs/user files not listed in SUMMARY.md: %v", extra)
	}
}
