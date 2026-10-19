package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// FeedItem is one compact line of the bar's live feed: a user prompt, an
// assistant text snippet, or a one-line tool call.
type FeedItem struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // user | text | tool
	Time   int64  `json:"time"`
	Text   string `json:"text"`
	Tool   string `json:"tool,omitempty"`
	Status string `json:"status,omitempty"` // tools: pending|running|completed|error
}

const feedTextMax = 240

var jsCmdRe = regexp.MustCompile(`cmd:\s*"((?:[^"\\]|\\.)*)"`)

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// toolSummary picks the most telling argument of a tool call.
func toolSummary(p Part) string {
	if p.Title != "" {
		return snippet(p.Title, 100)
	}
	var in map[string]any
	if json.Unmarshal(p.Input, &in) != nil {
		var src string // codex "exec" tools take JS source: pull out the shell command
		if json.Unmarshal(p.Input, &src) != nil {
			return ""
		}
		if m := jsCmdRe.FindStringSubmatch(src); m != nil {
			var cmd string
			if json.Unmarshal([]byte(`"`+m[1]+`"`), &cmd) == nil {
				return snippet(cmd, 100)
			}
		}
		return snippet(src, 100)
	}
	for _, k := range []string{"description", "command", "cmd", "filePath", "file_path", "path", "pattern", "query", "url", "prompt"} {
		switch v := in[k].(type) {
		case string:
			if v != "" {
				return snippet(v, 100)
			}
		case []any:
			var parts []string
			for _, e := range v {
				if s, ok := e.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return snippet(strings.Join(parts, " "), 100)
			}
		}
	}
	return ""
}

// buildFeed flattens messages into feed items and keeps the newest n.
func buildFeed(msgs []Message, n int) []FeedItem {
	var items []FeedItem
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch {
			case p.Kind == KindText && m.Role == "user":
				if t := snippet(p.Text, feedTextMax); t != "" {
					items = append(items, FeedItem{ID: p.ID, Kind: "user", Time: m.Time, Text: t})
				}
			case p.Kind == KindText:
				if t := snippet(p.Text, feedTextMax); t != "" {
					items = append(items, FeedItem{ID: p.ID, Kind: "text", Time: m.Time, Text: t})
				}
			case p.Kind == KindTool:
				items = append(items, FeedItem{ID: p.ID, Kind: "tool", Time: m.Time, Tool: p.Tool, Text: toolSummary(p), Status: p.Status})
			}
		}
	}
	if len(items) > n {
		items = items[len(items)-n:]
	}
	if items == nil {
		items = []FeedItem{}
	}
	return items
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := s.byLabel[q.Get("src")]
	if src == nil || q.Get("id") == "" {
		http.Error(w, "bad src/id", http.StatusBadRequest)
		return
	}
	n, _ := strconv.Atoi(q.Get("n"))
	if n < 1 || n > 20 {
		n = 4
	}
	msgs, err := src.B.Recent(q.Get("id"), 6*n+12)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": buildFeed(msgs, n)})
}
