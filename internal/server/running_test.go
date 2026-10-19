package server

import (
	"testing"

	"agent-viewer/internal/model"
)

func TestPropagateRunningMarksAncestors(t *testing.T) {
	list := []model.Session{
		{ID: "top"},
		{ID: "mid", ParentID: "top"},
		{ID: "leaf", ParentID: "mid", Running: true},
		{ID: "other"},
		{ID: "idleChild", ParentID: "other"},
		{ID: "orphan", ParentID: "missing", Running: true},
	}
	propagateRunning(list)
	got := map[string]bool{}
	for _, s := range list {
		got[s.ID] = s.Running
	}
	for id, want := range map[string]bool{"top": true, "mid": true, "leaf": true, "other": false, "idleChild": false, "orphan": true} {
		if got[id] != want {
			t.Errorf("%s: running=%v want %v", id, got[id], want)
		}
	}
}
