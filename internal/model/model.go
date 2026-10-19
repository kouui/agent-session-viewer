package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- Normalized contract ----------------------------------------------------
// Every backend (opencode, codex, ...) converts its native history into these
// types. The HTTP API and the frontend only ever see this contract.

// Session is one conversation, with its project already resolved.
type Session struct {
	Source      string `json:"source"` // Source.Label, set by the server
	Kind        string `json:"kind"`   // backend kind, e.g. "opencode", "codex"
	ID          string `json:"id"`
	Title       string `json:"title"`
	Directory   string `json:"directory"`
	ParentID    string `json:"parentId,omitempty"`
	TimeCreated int64  `json:"timeCreated"` // unix ms
	MTime       int64  `json:"mtime"`       // unix ms, last activity
	Archived    bool   `json:"archived,omitempty"`
	// Running: the agent is working on this session right now (heuristic, see backends).
	Running bool `json:"running,omitempty"`

	ProjectKey  string `json:"-"` // grouping key (normalized path / id)
	ProjectName string `json:"-"`
	ProjectPath string `json:"-"`
}

type Usage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	Reasoning  int64 `json:"reasoning,omitempty"`
	CacheRead  int64 `json:"cacheRead,omitempty"`
	CacheWrite int64 `json:"cacheWrite,omitempty"`
}

// Stats are session-level totals shown in the view header.
// Usage.Input is NEW (uncached) input only: total input = Input + CacheRead +
// CacheWrite, so backends whose native "input" already includes cache (codex)
// must subtract it. Cost is nil when the backend cannot know the price.
type Stats struct {
	Usage Usage    `json:"usage"`
	Cost  *float64 `json:"cost,omitempty"`
	// Context is the prompt size of the latest request; Window is 0 when unknown.
	ContextUsed   int64  `json:"contextUsed,omitempty"`
	ContextWindow int64  `json:"contextWindow,omitempty"`
	FirstModel    string `json:"firstModel,omitempty"`
	LastModel     string `json:"lastModel,omitempty"`
}

// UsageRow is token usage and cost of one session+model within one UTC hour.
// Cost is nil when the backend does not record price.
type UsageRow struct {
	Session string   `json:"sid"`
	Model   string   `json:"model"`
	Hour    int64    `json:"hour"` // unix ms, start of the UTC hour
	Usage   Usage    `json:"usage"`
	Cost    *float64 `json:"cost,omitempty"`
}

// Part kinds.
const (
	KindText      = "text"
	KindReasoning = "reasoning"
	KindTool      = "tool"
	KindPatch     = "patch"
	KindStep      = "step" // per-step usage footer
	KindOther     = "other"
)

// Part is one block inside a message. The frontend upserts parts by ID and
// merges fields shallowly: a field that is absent keeps its previous value.
// This lets a backend emit "tool output arrived" without repeating the input.
type Part struct {
	ID         string          `json:"id"`
	MessageID  string          `json:"messageId,omitempty"`
	Kind       string          `json:"kind"`
	Text       string          `json:"text,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Title      string          `json:"title,omitempty"`
	Status     string          `json:"status,omitempty"` // pending|running|completed|error
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
	Files      []string        `json:"files,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	Cost       float64         `json:"cost,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// Message is a turn entry. An empty Role means "parts update only".
type Message struct {
	ID     string `json:"id"`
	Role   string `json:"role,omitempty"` // user|assistant
	Time   int64  `json:"time,omitempty"` // unix ms; ordering key
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Finish string `json:"finish,omitempty"`
	Error  string `json:"error,omitempty"`
	Parts  []Part `json:"parts"`
}

// Delta is an incremental update. Cursor is opaque to the client: it passes
// back whatever the previous Delta returned ("" for the first call).
type Delta struct {
	Messages []Message `json:"messages"`
	Cursor   string    `json:"cursor"`
	More     bool      `json:"more,omitempty"`  // call again right away
	Reset    bool      `json:"reset,omitempty"` // discard client state first
}

// Backend reads one agent's history, read-only.
type Backend interface {
	Kind() string
	Path() string
	Sessions() ([]Session, error)
	Delta(sessionID, cursor string) (*Delta, error)
	// Recent returns roughly the newest `limit` messages (with parts), oldest first. Cheap tail read for the bar.
	Recent(sessionID string, limit int) ([]Message, error)
	Stats(sessionID string) (*Stats, error)
	// Usage returns all-time hourly usage rows; backends cache and update incrementally.
	Usage() ([]UsageRow, error)
	// Fingerprint is a cheap change detector (file stats only, no DB reads).
	Fingerprint() string
	// Status reports degraded state: stale = recent writes not visible.
	Status() (stale bool, errMsg string)
}

type Source struct {
	Label string
	B     Backend
}

// ---- helpers ----------------------------------------------------------------

// BaseName handles both / and \ separators regardless of host OS.
func BaseName(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// NormPath makes project keys comparable across backends.
func NormPath(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if len(p) >= 2 && p[1] == ':' { // windows drive: case-insensitive
		p = strings.ToLower(p)
	}
	return p
}

// GitRoot walks up from dir looking for .git on the local filesystem.
// Returns dir unchanged when dir does not exist locally.
func GitRoot(dir string) string {
	d := dir
	for i := 0; i < 16 && d != "" && d != "."; i++ {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}

func RawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func RawOrString(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return RawString(s)
}

// RunningWindow: an unfinished turn only counts as "running" while the history
// was touched this recently (a crashed or killed agent leaves turns open forever).
// Long enough to cover a slow tool call or a main session waiting on subagents.
const RunningWindow = 10 * time.Minute
