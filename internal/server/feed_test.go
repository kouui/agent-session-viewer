package server

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-viewer/internal/model"
)

func TestBuildFeedKeepsNewestFlattened(t *testing.T) {
	msgs := []model.Message{
		{ID: "u1", Role: "user", Time: 1, Parts: []model.Part{{ID: "p1", Kind: model.KindText, Text: "hello\n  world"}}},
		{ID: "a1", Role: "assistant", Time: 2, Parts: []model.Part{
			{ID: "p2", Kind: model.KindReasoning, Text: "hidden"},
			{ID: "p3", Kind: model.KindTool, Tool: "bash", Status: "completed", Input: json.RawMessage(`{"command":"ls -la"}`)},
			{ID: "p4", Kind: model.KindText, Text: "done"},
		}},
	}
	got := buildFeed(msgs, 3)
	if len(got) != 3 || got[0].Text != "hello world" || got[1].Tool != "bash" || got[1].Text != "ls -la" || got[2].Kind != "text" {
		t.Fatalf("unexpected feed: %+v", got)
	}
	if got := buildFeed(msgs, 1); len(got) != 1 || got[0].ID != "p4" {
		t.Fatalf("n=1 should keep only the newest item: %+v", got)
	}
}

func TestToolSummaryExtractsCodexExecCommand(t *testing.T) {
	in, _ := json.Marshal(`text(await tools.exec_command({cmd:"echo \"hi\"\nls"}))`)
	if got := toolSummary(model.Part{Input: in}); got != `echo "hi" ls` {
		t.Fatalf("got %q", got)
	}
}

func TestSnippetTruncates(t *testing.T) {
	if got := snippet(strings.Repeat("あ", 300), 10); len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}
