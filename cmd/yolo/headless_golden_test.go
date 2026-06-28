//go:build golden

// Golden-transcript determinism (Sprint 11 H-004). Replaying the same headless
// input through the stub runtime must produce a byte-identical event transcript
// every run (S5). The expected SHA256 hash lives in testdata so the test is
// self-contained and fails loudly on drift.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestGoldenHeadlessTranscript(t *testing.T) {
	const input = "hello world"

	got, err := runHeadless(strings.NewReader(input), 1)
	if err != nil {
		t.Fatalf("runHeadless = %v, want nil", err)
	}

	sum := sha256.Sum256([]byte(got))
	gotHex := hex.EncodeToString(sum[:])

	want, err := os.ReadFile("testdata/golden_headless_transcript.txt")
	if err != nil {
		t.Fatalf("read golden hash: %v", err)
	}
	wantHex := strings.TrimSpace(string(want))
	if gotHex != wantHex {
		t.Fatalf("transcript hash mismatch\ngot:  %s\nwant: %s\n\ntranscript:\n%s", gotHex, wantHex, got)
	}
}
