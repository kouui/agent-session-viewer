package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-viewer/internal/model"
)

func noAgent(sid, agent string) string { return "" }

func TestConvertLineAssistantToolUseAndResultLink(t *testing.T) {
	d := &model.Delta{}
	convertLine(d, 0, []byte(`{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"id":"m1","role":"assistant","model":"claude-x","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"Agent","input":{"description":"d"}}]}}`+"\n"), "S", noAgent)
	if len(d.Messages) != 1 || d.Messages[0].Role != "assistant" || d.Messages[0].Parts[0].ID != "tool:toolu_1" || d.Messages[0].Parts[0].Status != "running" {
		t.Fatalf("tool_use: %+v", d.Messages)
	}
	agent := func(sid, a string) string { return sid + "~agent-" + a }
	convertLine(d, 10, []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},"toolUseResult":{"agentId":"abc","status":"async_launched"}}`+"\n"), "S", agent)
	last := d.Messages[len(d.Messages)-1]
	p := last.Parts[0]
	if p.ID != "tool:toolu_1" || p.Output != "ok" || p.Status != "completed" || p.Link == nil || p.Link.SessionID != "S~agent-abc" {
		t.Fatalf("tool_result: %+v", p)
	}
	convertLine(d, 20, []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"started"}]},"toolUseResult":{"runId":"wf_1","taskId":"t"}}`+"\n"), "S", agent)
	if p := d.Messages[len(d.Messages)-1].Parts[0]; p.Link == nil || p.Link.Group != "wf_1" {
		t.Fatalf("workflow link: %+v", p.Link)
	}
}

func TestConvertLineSkipsMetaAndEmptyThinking(t *testing.T) {
	d := &model.Delta{}
	convertLine(d, 0, []byte(`{"type":"user","isMeta":true,"message":{"role":"user","content":"hidden"}}`+"\n"), "S", noAgent)
	convertLine(d, 5, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"x"}]}}`+"\n"), "S", noAgent)
	convertLine(d, 9, []byte(`{"type":"user","message":{"role":"user","content":"hi there"}}`+"\n"), "S", noAgent)
	if len(d.Messages) != 1 || d.Messages[0].Parts[0].Text != "hi there" {
		t.Fatalf("got %+v", d.Messages)
	}
}

func TestTailOpen(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, lines ...string) (string, int64) {
		p := filepath.Join(dir, name)
		b := []byte(strings.Join(lines, "\n") + "\n")
		os.WriteFile(p, b, 0o644)
		return p, int64(len(b))
	}
	user := `{"type":"user","message":{"role":"user","content":"go"}}`
	toolUse := `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[]}}`
	endTurn := `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[]}}`
	dur := `{"type":"system","subtype":"turn_duration"}`
	blob := `{"type":"api-request-blob","x":"` + strings.Repeat("y", 400) + `"}`
	for _, c := range []struct {
		name  string
		lines []string
		want  bool
	}{
		{"prompt", []string{endTurn, dur, user}, true},
		{"tool", []string{user, toolUse, blob}, true},
		{"done", []string{user, toolUse, endTurn, dur, blob}, false},
		{"subagent done", []string{user, endTurn}, false},
	} {
		p, n := write(c.name, c.lines...)
		if got := tailOpen(p, n); got != c.want {
			t.Errorf("%s: open=%v want %v", c.name, got, c.want)
		}
	}
}
