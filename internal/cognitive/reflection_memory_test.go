package cognitive

import (
	"reflect"
	"strings"
	"testing"
)

// TestReflectionMemoryAddAndRetrieve pins the accumulator contract (§7.3.3):
// AddLesson/AddFact append in insertion order without dedup, and Lessons/Facts
// return the slice as stored. A repeated lesson is preserved (it is signal, not
// noise the memory should silently collapse).
func TestReflectionMemoryAddAndRetrieve(t *testing.T) {
	cases := []struct {
		name        string
		lessons     []string
		facts       []string
		wantLessons []string
		wantFacts   []string
	}{
		{
			name:        "empty memory",
			wantLessons: nil,
			wantFacts:   nil,
		},
		{
			name:        "append without dedup keeps repeats",
			lessons:     []string{"a", "a", "b"},
			facts:       []string{"x", "x"},
			wantLessons: []string{"a", "a", "b"},
			wantFacts:   []string{"x", "x"},
		},
		{
			name:        "lessons and facts are independent",
			lessons:     []string{"only lesson"},
			facts:       []string{"only fact"},
			wantLessons: []string{"only lesson"},
			wantFacts:   []string{"only fact"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewReflectionMemory()
			for _, l := range tc.lessons {
				m.AddLesson(l)
			}
			for _, f := range tc.facts {
				m.AddFact(f)
			}
			if got := m.Lessons(); !reflect.DeepEqual(got, tc.wantLessons) {
				t.Errorf("Lessons() = %v, want %v", got, tc.wantLessons)
			}
			if got := m.Facts(); !reflect.DeepEqual(got, tc.wantFacts) {
				t.Errorf("Facts() = %v, want %v", got, tc.wantFacts)
			}
		})
	}
}

// TestReflectionMemoryPromptPrefix pins the prefix contract: "" when the memory
// is empty (a fresh task adds no noise to the prompt) and a deterministic,
// non-empty summary that names and carries each lesson and fact when populated.
func TestReflectionMemoryPromptPrefix(t *testing.T) {
	cases := []struct {
		name         string
		lessons      []string
		facts        []string
		wantEmpty    bool
		wantContains []string // substrings the prefix must carry
	}{
		{
			name:      "empty memory → empty prefix",
			wantEmpty: true,
		},
		{
			name:         "lessons only",
			lessons:      []string{"check nil before indexing"},
			wantContains: []string{"Prior lessons", "check nil before indexing"},
		},
		{
			name:         "facts only",
			facts:        []string{"the API returns 200 for healthy"},
			wantContains: []string{"Established facts", "the API returns 200 for healthy"},
		},
		{
			name:         "both lessons and facts",
			lessons:      []string{"L1", "L2"},
			facts:        []string{"F1"},
			wantContains: []string{"Prior lessons", "Established facts", "L1", "L2", "F1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewReflectionMemory()
			for _, l := range tc.lessons {
				m.AddLesson(l)
			}
			for _, f := range tc.facts {
				m.AddFact(f)
			}
			got := m.PromptPrefix()
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("PromptPrefix() = %q, want empty", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("PromptPrefix() = empty, want a non-empty summary")
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("PromptPrefix() = %q, want it to contain %q", got, sub)
				}
			}
		})
	}
}

// TestReflectionMemoryPromptPrefixIsDeterministic pins S5: the same lessons and
// facts produce the identical prefix across calls, so a golden transcript is
// stable.
func TestReflectionMemoryPromptPrefixIsDeterministic(t *testing.T) {
	build := func() string {
		m := NewReflectionMemory()
		m.AddLesson("sign the request before sending")
		m.AddFact("the server rejects unsigned requests")
		return m.PromptPrefix()
	}
	first := build()
	second := build()
	if first == "" {
		t.Fatal("prefix empty, want a non-empty summary")
	}
	if first != second {
		t.Errorf("prefix not deterministic:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
