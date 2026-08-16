package prompt

import (
	stdctx "context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// compilePkg builds a small ContextPackage via the real Layer 4 Engine over a
// fixture repo, then runs it through the Prompt Compiler. This mirrors the
// end-to-end L4→L5 path and is the basis for both the L5-001 pipeline tests and
// the L5-002 wire-format round-trip.
func compilePkg(t *testing.T, window, softBudget int, goal string, openFiles []string) (*Compiler, []Message) {
	t.Helper()
	root := fixtureRepo(t)
	eng := context.New(context.Deps{
		Bus:        event.New(),
		Repo:       root,
		Open:       openFiles,
		SoftBudget: softBudget,
		Window:     window,
	})
	req := buildReq(t, root, goal)
	pkg, err := eng.Build(stdctx.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Build: %v", err)
	}
	comp := New(nil, nil) // default whitespace counter, no bus
	msgs := comp.CompilePackage(pkg)
	return comp, msgs
}

// buildReq creates a ContextRequest carrying a real task with the given goal.
func buildReq(t *testing.T, repo, goal string) context.ContextRequest {
	t.Helper()
	bus := event.New()
	store := session.NewFileStore(filepath.Join(repo, ".store"))
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, _ := smgr.OpenSession(stdctx.Background(), "proj", "demo")
	tid, _ := smgr.StartTask(stdctx.Background(), sid, goal)
	// Resume before reading the live task pointer (see engine_test.go newReq:
	// Resume rehydrates the task; reading first returns a stale pointer).
	sess, _, _ := smgr.Resume(stdctx.Background(), sid)
	task := smgr.LoadTaskPublic(tid)
	return context.ContextRequest{Task: task, Session: sess}
}

// fixtureRepo writes a tiny repo layout into a temp dir and returns its path.
// Kept local so this package's tests are self-contained.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"AGENTS.md":     "# Conventions\nUse table-driven tests.\n",
		"main.go":       "package main\n\nfunc main() {}\n",
		"auth/login.go": "package auth\n\nfunc Login(user string) error { return nil }\n",
	}
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return root
}

// TestCompileEmitsOrderedMessages pins the §6.6.2 ordering: system, project,
// user (goal), then retrieved context (files/graph/diagnostics), then
// conversation. With a generous budget so nothing is trimmed, the message
// sequence must be exactly that order.
func TestCompileEmitsOrderedMessages(t *testing.T) {
	_, msgs := compilePkg(t, 500_000, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	if len(msgs) < 3 {
		t.Fatalf("Compile produced %d messages, want at least 3 (system+project+user)", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("msgs[0].Role = %q, want system (ordered first, §6.6.2)", msgs[0].Role)
	}
	if msgs[1].Role != "system" {
		t.Errorf("msgs[1].Role = %q, want system (project rules second, §6.6.2)", msgs[1].Role)
	}
	// System message must carry the role text from Layer 4's System group.
	if msgs[0].Content == "" {
		t.Error("msgs[0].Content empty; system message must render the System group")
	}
	// The current request (goal) is ordered third as a user message.
	if msgs[2].Role != "user" {
		t.Errorf("msgs[2].Role = %q, want user (current request ordered third, §6.6.2)", msgs[2].Role)
	}
	if msgs[2].Content == "" {
		t.Error("msgs[2].Content empty; user message must carry the task goal")
	}
}

// TestCompileIncludesRetrievedFilesContext pins that the ranked retrieved
// context (open files) appears as a user message in the compiled prompt, so the
// model sees real file contents — the Sprint 2 exit bar (S6).
func TestCompileIncludesRetrievedFilesContext(t *testing.T) {
	_, msgs := compilePkg(t, 500_000, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	found := false
	for _, m := range msgs {
		if contains(m.Content, "func Login") {
			found = true
			break
		}
	}
	if !found {
		t.Error("no compiled message contains the open file body; the model must see real file contents")
	}
}

// TestCompileStaysWithinTokenBudget is the L5-001 exit criterion: the compiled
// prompt's total token count does not exceed the budget window. With a small
// window and oversized file content, applyBudget must trim so the total fits.
// The min-message guard ensures a broken (empty) pipeline can't spuriously
// pass the budget check. Window=300 accommodates the system prompt (which
// carries the tool schema + rules + coding strategy — ~210 tokens) plus a
// small file body, while still forcing trimming of larger inputs.
func TestCompileStaysWithinTokenBudget(t *testing.T) {
	comp, msgs := compilePkg(t, 300, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	if len(msgs) < 2 {
		t.Fatalf("Compile produced %d messages, want >= 2 (broken/empty pipeline can't satisfy budget by being empty)", len(msgs))
	}
	total := 0
	for _, m := range msgs {
		total += comp.counter.Count(m.Content)
	}
	if total > 300 {
		t.Errorf("compiled prompt token total = %d, want <= window 300 (applyBudget must trim over-budget)", total)
	}
}

// TestCompileTrimsOversizedConversationToBudget directly pressures the trimming
// path: a package whose conversation alone exceeds the window must be cut down
// to fit. This is the test that actually exercises applyBudget's conversation
// hard-cut (§6.7.2 pass 4); the window=200 case above doesn't reach trimming.
func TestCompileTrimsOversizedConversationToBudget(t *testing.T) {
	// Build a package directly: a tiny system + user (never-trimmed) and a huge
	// conversation that far exceeds a 40-token window.
	pkg := context.ContextPackage{
		System:       []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		User:         []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "do thing"}},
		Conversation: make([]context.Part, 20),
		Budget:       allocateBudget(40),
	}
	for i := range pkg.Conversation {
		pkg.Conversation[i] = context.Part{
			Kind: context.KindConversation, Source: "turn#" + itoa(i),
			// Distinct text per turn so dedup doesn't collapse them; each ~8 tokens.
			Text: "turn " + itoa(i) + " alpha beta gamma delta epsilon zeta eta",
		}
	}
	comp := New(nil, nil)
	msgs := comp.CompilePackage(&pkg)

	total := 0
	for _, m := range msgs {
		total += comp.counter.Count(m.Content)
	}
	if total > 40 {
		t.Errorf("oversized conversation trimmed to %d tokens, want <= window 40 (applyBudget pass 4 hard-cut failed)", total)
	}
	// Never-trimmed system + user must survive even after the hard cut (§6.7.3).
	hasSystem, hasUser := false, false
	for _, m := range msgs {
		if m.Role == "system" && m.Content != "" {
			hasSystem = true
		}
		if m.Role == "user" && contains(m.Content, "do thing") {
			hasUser = true
		}
	}
	if !hasSystem {
		t.Error("system prompt dropped; §6.7.3 marks it never-trimmed")
	}
	if !hasUser {
		t.Error("current user message dropped; §6.7.3 marks it never-trimmed")
	}
}

// TestCompileSmallWindowKeepsRetrievedContext pins the §6.6.1 waterfall under a
// window small enough to reach the trimming path. Layer 4's allocate() applies
// a 1024-token reserve floor *before* subtracting, so for any window below that
// floor avail = 0 and every group cap comes back 0 — Files, RAG, graph and
// diagnostics are then trimmed to nothing on the first pass.
//
// This test used to document that degradation as the contract ("all group caps
// are 0 … everything else is trimmed") and assert only that system + user
// survived, which is the bug written down as an expectation. What must actually
// be true: a small-window model still sees the retrieved context it asked for.
// A budget cannot reserve more than the whole window.
func TestCompileSmallWindowKeepsRetrievedContext(t *testing.T) {
	// window=200 is under the 1024 reserve floor AND under the compiled size of
	// this fixture (~273 tokens), so applyBudget really does trim here.
	_, msgs := compilePkg(t, 200, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	hasSystem, hasGoal, hasFile := false, false, false
	for _, m := range msgs {
		if m.Role == "system" && m.Content != "" {
			hasSystem = true
		}
		if m.Role == "user" && contains(m.Content, "fix the Login function") {
			hasGoal = true
		}
		if contains(m.Content, "func Login(user") {
			hasFile = true
		}
	}
	if !hasSystem {
		t.Error("system prompt missing; §6.7.3 marks it never-trimmed")
	}
	if !hasGoal {
		t.Error("current user message (goal) missing; §6.7.3 marks it never-trimmed")
	}
	if !hasFile {
		t.Error("retrieved file body missing: a window under allocate()'s 1024 reserve floor zeroed every group cap, so 100% of the retrieved context was dropped")
	}
}

// TestCompileSignalsOmittedRetrievedContext pins that trimming is never silent.
// When the budget forces retrieved files out of the prompt, the <files> section
// must say so — otherwise the model cannot tell "nothing was retrieved" from
// "what was retrieved did not fit", and neither can anyone reading the wire.
// (There is no TokenBudgetEvent to carry the fact out-of-band; see types.go.)
func TestCompileSignalsOmittedRetrievedContext(t *testing.T) {
	pkg := context.ContextPackage{
		System: []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		User:   []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "do thing"}},
		Budget: allocateBudget(2000),
	}
	for i := 0; i < 5; i++ {
		pkg.Files = append(pkg.Files, context.Part{
			Kind: context.KindFile, Source: "f" + itoa(i) + ".go",
			Text: "package f" + itoa(i) + " " + repeatWords("alpha beta gamma delta ", 150),
		})
	}
	comp := New(nil, nil)
	msgs := comp.CompilePackage(&pkg)

	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !contains(joined, "omitted") {
		t.Errorf("files were trimmed away with no notice in the prompt; the model sees a <files> section that silently lost content.\ncompiled:\n%s", joined)
	}
}

// TestCompileCountsPreferencesInTokenBudget: the <preferences> group is emitted
// by order() and shipped to the model, so it must be counted by applyBudget.
// It was in neither the total nor the `remaining` subtraction — never counted,
// never trimmed. cmd/yolo's memory adapter returns *all* stored preferences,
// one Part per key, with no top-K, so the group grows without bound across
// sessions.
func TestCompileCountsPreferencesInTokenBudget(t *testing.T) {
	pkg := context.ContextPackage{
		System: []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		User:   []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "do thing"}},
		Budget: allocateBudget(2000),
	}
	for i := 0; i < 400; i++ {
		pkg.Preferences = append(pkg.Preferences, context.Part{
			Kind: context.KindPreferences, Source: "pref:" + itoa(i),
			Text: "pref " + itoa(i) + " " + repeatWords("alpha beta gamma delta ", 6),
		})
	}
	comp := New(nil, nil)
	msgs := comp.CompilePackage(&pkg)

	total := 0
	for _, m := range msgs {
		total += comp.counter.Count(m.Content)
	}
	if total > 2000 {
		t.Errorf("compiled prompt = %d tokens, want <= window 2000 (%.1fx over): the Preferences group is shipped but not budgeted", total, float64(total)/2000)
	}
	// Trimmed, not dropped: at least one recalled preference must still reach
	// the model, or the recall was pointless.
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !contains(joined, "<preferences>") {
		t.Error("the whole Preferences group vanished; trimming must keep the highest-ranked preference (§6.7 admits the first part of a group)")
	}
}

// TestApplyBudgetWindowZeroMeansNoCap pins the load-bearing zero-value guard at
// the top of applyBudget: Window == 0 means "unbudgeted", not "exhausted". A
// zero limit silently meaning the wrong thing is this codebase's recurring
// defect, and this guard was mutation-tested green — it could be inverted with
// no test noticing. Nothing may be trimmed when no window is set.
func TestApplyBudgetWindowZeroMeansNoCap(t *testing.T) {
	pkg := context.ContextPackage{
		System: []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		User:   []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "do thing"}},
		Files: []context.Part{
			{Kind: context.KindFile, Source: "big.go", Text: repeatWords("alpha beta gamma ", 500)},
		},
		RAG:    []context.Part{{Kind: context.KindRAG, Source: "chunk", Text: repeatWords("delta epsilon ", 500)}},
		Budget: context.Budget{}, // Window 0: no budget to enforce
	}
	for i := 0; i < 30; i++ {
		pkg.Conversation = append(pkg.Conversation, context.Part{
			Kind: context.KindConversation, Source: "turn#" + itoa(i),
			Text: "turn " + itoa(i) + " " + repeatWords("zeta eta theta ", 20),
		})
	}
	comp := New(nil, nil)
	out, _ := comp.applyBudget(pkg)

	// Check the surviving part is the original, not the "[N … omitted]" marker
	// applyBudget substitutes for trimmed content — a group of length 1 could
	// otherwise be an entirely dropped group wearing a notice.
	if len(out.Files) != 1 || out.Files[0].Source != "big.go" {
		t.Errorf("Window==0 trimmed Files to %+v, want the original big.go part (unbudgeted means no cap, not exhausted)", out.Files)
	}
	if len(out.RAG) != 1 || out.RAG[0].Source != "chunk" {
		t.Errorf("Window==0 trimmed RAG to %+v, want the original chunk part (unbudgeted means no cap, not exhausted)", out.RAG)
	}
	if len(out.Conversation) != 30 {
		t.Errorf("Window==0 trimmed Conversation to %d parts, want 30 (unbudgeted means no cap, not exhausted)", len(out.Conversation))
	}
}

// TestTrimGroupSlotZeroDropsEverything pins the other zero-value guard, which
// is also load-bearing and was also mutation-green: trimGroup's slot of 0 means
// "this group has no allocation — drop it", and applyBudget relies on exactly
// that when it trims graph/diagnostics, which have no slot of their own. A
// negative slot means the same. Any positive slot admits at least the first,
// highest-ranked part even when that part alone overflows the slot.
func TestTrimGroupSlotZeroDropsEverything(t *testing.T) {
	comp := New(nil, nil)
	parts := []context.Part{
		{Kind: context.KindFile, Source: "a.go", Text: "alpha beta gamma"},
		{Kind: context.KindFile, Source: "b.go", Text: "delta epsilon zeta"},
	}
	if got := comp.trimmer.trimGroup(parts, 0); got != nil {
		t.Errorf("trimGroup(slot=0) kept %d parts, want 0 (a zero slot is no allocation, not 'no cap')", len(got))
	}
	if got := comp.trimmer.trimGroup(parts, -1); got != nil {
		t.Errorf("trimGroup(slot=-1) kept %d parts, want 0", len(got))
	}
	if got := comp.trimmer.trimGroup(parts, 1); len(got) != 1 {
		t.Errorf("trimGroup(slot=1) kept %d parts, want 1 (the first part is admitted even when it alone exceeds the slot)", len(got))
	}
	if got := comp.trimmer.trimGroup(parts, 1000); len(got) != 2 {
		t.Errorf("trimGroup(slot=1000) kept %d parts, want 2 (both fit)", len(got))
	}
}

// TestDedupDoesNotMutateCallerPackage pins that Compile is a pure function of
// its input. dedup filtered in place through pkg.Conversation[:0], which writes
// through the backing array the caller still owns: the caller kept a slice of
// the original length whose first element was gone and whose last was a
// duplicate. Compile takes the package by value, and CompilePackage takes a
// pointer — both advertise that the caller's package survives the call.
func TestDedupDoesNotMutateCallerPackage(t *testing.T) {
	pkg := context.ContextPackage{
		Files: []context.Part{{Kind: context.KindFile, Source: "a.go", Text: "package a"}},
		Conversation: []context.Part{
			// Dropped by the cross-group dedup (same source as a kept File).
			{Kind: context.KindConversation, Source: "a.go", Text: "TOOL RESULT for a.go"},
			{Kind: context.KindConversation, Source: "turn#1", Text: "assistant said ONE"},
			{Kind: context.KindConversation, Source: "turn#2", Text: "assistant said TWO"},
		},
		User: []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "do thing"}},
	}
	want := make([]string, len(pkg.Conversation))
	for i, p := range pkg.Conversation {
		want[i] = p.Text
	}

	New(nil, nil).CompilePackage(&pkg)

	got := make([]string, len(pkg.Conversation))
	for i, p := range pkg.Conversation {
		got[i] = p.Text
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CompilePackage corrupted the caller's Conversation\n before: %v\n after:  %v", want, got)
			break
		}
	}
}

// repeatWords returns w repeated n times — a cheap way to build a part with a
// known, large token count under the whitespace counter.
func repeatWords(w string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += w
	}
	return out
}

// TestCompileIsDeterministic pins S5 §5.5: two compiles over identical input
// produce byte-identical message sequences. The min-message guard ensures an
// empty (broken) pipeline can't pass by being trivially equal.
func TestCompileIsDeterministic(t *testing.T) {
	_, first := compilePkg(t, 50_000, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})
	_, second := compilePkg(t, 50_000, 1<<20, "fix the Login function in @auth/login.go", []string{"auth/login.go"})

	if len(first) < 3 {
		t.Fatalf("first compile produced %d messages, want >= 3 (broken/empty pipeline can't be 'deterministic' by being empty)", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("nondeterministic: first has %d msgs, second %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Role != second[i].Role || first[i].Content != second[i].Content {
			t.Errorf("nondeterministic at msg %d: (%q/%q...) vs (%q/%q...)",
				i, first[i].Role, first[i].Content, second[i].Role, second[i].Content)
		}
	}
}

// TestCompileEmitsPreferencesGroup is the L10-006 wire gap: the §6.6.2 order()
// must emit pkg.Preferences so a recalled memory (File 11 §11.8, surfaced into
// Layer 4's Preferences group by the context.Memory seam) reaches the model.
// Sprint 2 gathered Preferences via the noop Memory stub (always empty), so
// order() never needed to emit the group — but the group slot exists in the
// ContextPackage (§6.4) and the ranker/compress populate it. With a non-empty
// Preferences group the compiled prompt must carry its text under a
// <preferences> tag, ordered within the system block (persistent guidance,
// like project rules). This is RED until order() emits the group.
func TestCompileEmitsPreferencesGroup(t *testing.T) {
	pkg := context.ContextPackage{
		System: []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		Project: []context.Part{
			{Kind: context.KindProject, Source: "AGENTS.md", Text: "Use table-driven tests."},
		},
		Preferences: []context.Part{
			{Kind: context.KindPreferences, Source: "pref:test-style", Text: "I prefer table-driven tests"},
		},
		User:   []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "write a test"}},
		Budget: allocateBudget(50_000),
	}
	comp := New(nil, nil)
	msgs := comp.CompilePackage(&pkg)

	// The preference text must appear somewhere in the compiled prompt —
	// otherwise a recalled memory never reaches the model (Sprint 7 exit bar).
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !contains(joined, "I prefer table-driven tests") {
		t.Error("compiled prompt dropped the Preferences group; a recalled memory did not surface in the prompt")
	}
	// The group must render under its stable <preferences> section tag (wire
	// format §6.6.2) so the parser round-trips it and the tag set stays stable.
	if !contains(joined, "<preferences>") || !contains(joined, "</preferences>") {
		t.Error("compiled prompt missing <preferences>…</preferences> section tag; the Preferences group must render under its stable tag")
	}
}

// TestCompileEmitsRAGGroup: the RAG group (retrieved code chunks, File 11
// §11.6) renders under a stable <rag> tag, after <files> and before the
// conversation. This is RED until order() emits the group.
func TestCompileEmitsRAGGroup(t *testing.T) {
	pkg := context.ContextPackage{
		System: []context.Part{{Kind: context.KindSystem, Source: "<system>", Text: "role"}},
		Files: []context.Part{
			{Kind: context.KindFile, Source: "main.go", Text: "package main"},
		},
		RAG: []context.Part{
			{Kind: context.KindRAG, Source: "auth/login.go", Text: "func Login(user string) error"},
		},
		User:   []context.Part{{Kind: context.KindSystem, Source: "goal", Text: "fix login"}},
		Budget: allocateBudget(50_000),
	}
	comp := New(nil, nil)
	msgs := comp.CompilePackage(&pkg)

	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !contains(joined, "func Login") {
		t.Error("compiled prompt dropped the RAG group; a retrieved chunk did not surface")
	}
	if !contains(joined, "<rag>") || !contains(joined, "</rag>") {
		t.Error("compiled prompt missing <rag>…</rag> section tag; the RAG group must render under its stable tag")
	}
	// The <rag> tag must come after <files> (retrieved context order).
	if fi, ri := indexOf(joined, "<files>"), indexOf(joined, "<rag>"); fi >= 0 && ri >= 0 && ri < fi {
		t.Error("<rag> rendered before <files>; want after (retrieved-context order)")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// indexOf returns the first index of sub in s, or -1.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// allocateBudget mirrors internal/context.allocate so this package's tests can
// build a §6.6.1 budget without crossing packages to call the unexported one.
func allocateBudget(window int) context.Budget {
	reserve := window * 15 / 100
	if reserve < 1024 {
		reserve = 1024
	}
	avail := window - reserve
	if avail < 0 {
		avail = 0
	}
	sys := avail * 12 / 100
	if sys > 4096 {
		sys = 4096
	}
	proj := avail * 8 / 100
	if proj > 2048 {
		proj = 2048
	}
	conv := avail * 45 / 100
	files := avail * 17 / 100
	rag := avail * 8 / 100
	user := avail - sys - proj - conv - files - rag
	if user < 0 {
		user = 0
	}
	return context.Budget{
		Window: window, Reserve: reserve,
		System: sys, Project: proj, Conversation: conv, Files: files, RAG: rag, User: user,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestEffectiveSlotsMatchTheRetiredFallback pins the claim in effectiveSlots'
// doc comment: the degenerate-Budget fallback it used to carry is now dead code
// for every window, so removing it changed nothing.
//
// The argument for that is a two-sided one — Layer 4 stopped producing all-zero
// budgets, AND the one window where the old guard still chose the fallback
// (window == 1, where reserveFor floors the reserve at 1 so Reserve == Window)
// is a window where the fallback's own percentages truncate to zero regardless.
// Neither half is obvious from reading either file, and an argument that needs
// two files to check is one that rots. So check it against the real allocator
// rather than a reconstruction of it: the budgets here come from a live
// context.Engine, which is the only thing that can tell us what allocate()
// actually returns from this package.
func TestEffectiveSlotsMatchTheRetiredFallback(t *testing.T) {
	// The retired implementation, verbatim. It is fine for this to be the only
	// surviving copy: its job is to be compared against, and a test that pinned
	// the new behaviour to a re-derivation of the new behaviour would pin
	// nothing.
	retired := func(b context.Budget) slots {
		if b.Reserve < b.Window {
			return slots{files: b.Files, rag: b.RAG, pref: b.Project}
		}
		avail := b.Window - b.Window*15/100
		return slots{files: avail * 17 / 100, rag: avail * 8 / 100, pref: avail * 8 / 100}
	}

	root := fixtureRepo(t)
	// 1 and 2 are the boundary the two-sided argument turns on; 1024/1025
	// straddle the old reserve floor; the rest span the range a real provider
	// window falls in. Window <= 0 is absent deliberately — applyBudget
	// short-circuits it before effectiveSlots is ever reached.
	for _, window := range []int{1, 2, 3, 8, 100, 512, 900, 1023, 1024, 1025,
		1500, 2048, 4096, 8000, 16000, 32000, 128000, 200000, 1000000} {
		eng := context.New(context.Deps{Bus: event.New(), Repo: root, Window: window})
		pkg, err := eng.Build(stdctx.Background(), buildReq(t, root, "say hi"))
		if err != nil {
			t.Fatalf("window %d: Engine.Build: %v", window, err)
		}
		if got, want := effectiveSlots(pkg.Budget), retired(pkg.Budget); got != want {
			t.Errorf("window %d: effectiveSlots = %+v, retired fallback = %+v (budget %+v)",
				window, got, want, pkg.Budget)
		}
	}
}

// TestBudgetNeverStarvesAGroupAtASmallWindow is the other half — the reason the
// fallback could go. The fallback existed because Layer 4 handed back a Budget
// whose every cap was 0 for any window at or below the 1024-token reply floor,
// and a 0 slot is slotNoAllocation: drop the group entirely. That is the
// zero-means-its-own-opposite defect this tree keeps hitting, and it is Layer
// 4's to prevent, not Layer 5's to paper over.
//
// So assert the invariant Layer 5 now depends on, from Layer 5, where the
// dependency is. If a future change to allocate() reintroduces a starving
// budget, this fails here rather than silently emptying prompts.
func TestBudgetNeverStarvesAGroupAtASmallWindow(t *testing.T) {
	root := fixtureRepo(t)
	for _, window := range []int{2, 3, 8, 100, 512, 900, 1024, 1025, 2048} {
		eng := context.New(context.Deps{Bus: event.New(), Repo: root, Window: window})
		pkg, err := eng.Build(stdctx.Background(), buildReq(t, root, "say hi"))
		if err != nil {
			t.Fatalf("window %d: Engine.Build: %v", window, err)
		}
		s := effectiveSlots(pkg.Budget)
		if s.files <= slotNoAllocation || s.rag <= slotNoAllocation || s.pref <= slotNoAllocation {
			t.Errorf("window %d: slots %+v — a positive window allocated a group nothing, "+
				"so trimGroup drops it wholesale (budget %+v)", window, s, pkg.Budget)
		}
	}
}
