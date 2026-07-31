package build

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubIndex struct{ text string }

func (s stubIndex) Index(int) string { return s.text }

type stubCompleter struct{ reply string }

func (s stubCompleter) Complete(context.Context, string, string) (string, error) {
	return s.reply, nil
}

func knowledgeConfig(t *testing.T, mutate func(*config.Config)) config.Config {
	t.Helper()
	data := t.TempDir()
	cfg := config.Config{
		Paths:  config.PathConfig{DataDir: data, SessionDir: filepath.Join(data, "sessions"), Workspace: t.TempDir()},
		Skills: config.SkillConfig{Enabled: true, Dir: filepath.Join(data, "skills"), MaxIndexBytes: 4096},
		Memory: config.MemoryConfig{
			Dir: filepath.Join(data, "memory"), TopK: 4, MaxItems: 16, MaxItemBytes: 2048,
			MaxInjectedBytes: 4096, HalfLife: time.Hour, CandidateTTL: time.Hour,
		},
		Prompt: config.PromptConfig{ReservedContextTokens: 256, MaxInjectedBytes: 4096, MaxMemoryItems: 4},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

func TestBuildKnowledgeDisabledIsUsableNotNil(t *testing.T) {
	components, err := BuildKnowledge(KnowledgeInput{
		Config: knowledgeConfig(t, nil), Skills: stubIndex{text: "demo | 1 | does things"}, Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The point of the disabled implementations: every seam is callable, so the
	// request path never branches on configuration.
	if components.Store == nil || components.Candidates == nil || components.Extractor == nil || components.Assembler == nil {
		t.Fatalf("components = %#v", components)
	}
	if items, err := components.Store.List(context.Background(), memory.Query{WorkspaceID: components.WorkspaceID}); err != nil || len(items) != 0 {
		t.Fatalf("disabled list = %#v, %v", items, err)
	}
	if _, err := components.Store.Save(context.Background(), memory.Memory{}); err == nil {
		t.Fatal("a disabled store accepted a write")
	}
	// Skills still reach the prompt: the two capabilities are independent.
	snapshot, err := components.Assembler.Prepare(context.Background(), prompt.ContextQuery{
		Text: "how do I reset it", WorkspaceID: components.WorkspaceID, TopK: 4, Now: time.Unix(1000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SkillIndex == "" {
		t.Fatal("skill index is empty with memory disabled")
	}
	if len(snapshot.Memories) != 0 {
		t.Fatalf("memories = %#v with memory disabled", snapshot.Memories)
	}
}

func TestBuildKnowledgeEnabledSearchesWhatItStores(t *testing.T) {
	cfg := knowledgeConfig(t, func(c *config.Config) { c.Memory.Enabled = true })
	components, err := BuildKnowledge(KnowledgeInput{
		Config: cfg, Skills: stubIndex{}, Logger: testLogger(t), Now: func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{WorkspaceID: components.WorkspaceID, Visibility: memory.VisibilityWorkspace}
	if _, err := components.Store.Save(context.Background(), memory.Memory{
		Kind: memory.KindFact, Scope: scope, Content: "the reset register is at offset 0x10", Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := components.Assembler.Prepare(context.Background(), prompt.ContextQuery{
		Text: "reset register offset", WorkspaceID: components.WorkspaceID, TopK: 4, Now: time.Unix(1000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Memories) != 1 {
		t.Fatalf("memories = %#v; the assembler is not reading the store it was built with", snapshot.Memories)
	}
	// Auto-extract is off, so the queue is still the disabled one: enabling
	// recall must not silently enable proposals.
	if _, err := components.Candidates.Add(context.Background(), memory.Candidate{Kind: memory.KindFact, Scope: scope, Content: "x"}); err == nil {
		t.Fatal("the candidate queue accepted a write with auto-extract off")
	}
}

func TestBuildKnowledgeAutoExtractNeedsACompleter(t *testing.T) {
	cfg := knowledgeConfig(t, func(c *config.Config) { c.Memory.Enabled, c.Memory.AutoExtract = true, true })
	if _, err := BuildKnowledge(KnowledgeInput{Config: cfg, Skills: stubIndex{}, Logger: testLogger(t)}); err == nil {
		t.Fatal("auto-extract built without an extraction model")
	}
	components, err := BuildKnowledge(KnowledgeInput{
		Config: cfg, Skills: stubIndex{}, Logger: testLogger(t),
		Completer: stubCompleter{reply: `[{"kind":"fact","content":"the board boots from NOR flash"}]`},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{WorkspaceID: components.WorkspaceID, Visibility: memory.VisibilityWorkspace}
	proposals, err := components.Extractor.Extract(context.Background(), memory.Turn{User: "how does it boot", Assistant: "from NOR flash"}, scope)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals = %#v, %v", proposals, err)
	}
	stored, err := components.Candidates.Add(context.Background(), proposals[0])
	if err != nil {
		t.Fatal(err)
	}
	// A proposal is pending, never a memory: only approval can move it across.
	if stored.Status != memory.CandidatePending {
		t.Fatalf("status = %q", stored.Status)
	}
	items, err := components.Store.List(context.Background(), memory.Query{WorkspaceID: components.WorkspaceID})
	if err != nil || len(items) != 0 {
		t.Fatalf("extraction wrote straight into the store: %#v, %v", items, err)
	}
}

func TestBuildKnowledgeRejectsBadInput(t *testing.T) {
	cfg := knowledgeConfig(t, nil)
	if _, err := BuildKnowledge(KnowledgeInput{Config: cfg, Logger: testLogger(t)}); err == nil {
		t.Fatal("built without a skill index source")
	}
	if _, err := BuildKnowledge(KnowledgeInput{Config: cfg, Skills: stubIndex{}}); err == nil {
		t.Fatal("built without a logger")
	}
	relative := knowledgeConfig(t, func(c *config.Config) { c.Paths.Workspace = "relative/path" })
	if _, err := BuildKnowledge(KnowledgeInput{Config: relative, Skills: stubIndex{}, Logger: testLogger(t)}); err == nil {
		t.Fatal("built from a relative workspace path")
	}
}

func TestWorkspaceIDIsStableAndOpaque(t *testing.T) {
	path := "/Users/someone/work/secret-project"
	first, err := WorkspaceID(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkspaceID(path + "/")
	if err != nil {
		t.Fatal(err)
	}
	// Same directory, same id: otherwise yesterday's memories become invisible
	// because of a trailing slash.
	if first != second {
		t.Fatalf("%q != %q", first, second)
	}
	other, err := WorkspaceID("/Users/someone/work/other-project")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two workspaces share one id")
	}
	// The id is stored in memory scopes and rendered in listings, so it must not
	// carry the operator's directory layout.
	for _, fragment := range []string{"Users", "someone", "secret-project", "/"} {
		if strings.Contains(first, fragment) {
			t.Fatalf("workspace id %q leaks %q", first, fragment)
		}
	}
	if !strings.HasPrefix(first, "ws-") {
		t.Fatalf("workspace id = %q", first)
	}
	if _, err := WorkspaceID("   "); err == nil {
		t.Fatal("empty workspace accepted")
	}
}
