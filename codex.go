package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Codex reads ~/.codex/sessions/**/rollout-*.jsonl (append-only JSONL).
// The delta cursor is a byte offset into the file.
type Codex struct {
	root string // codex home, e.g. ~/.codex

	mu       sync.Mutex
	files    map[string]*cxFile // by absolute path
	byID     map[string]string  // session id -> path
	lastScan time.Time
	fp       string
	roots    map[string]string // gitRoot cache
	usage    cxUsageCache
}

type cxFile struct {
	path     string
	size     int64
	mtime    time.Time
	archived bool
	metaOK   bool
	id       string
	cwd      string
	title    string
	created  int64
	userEvt  bool // session records user input as event_msg (else as response_item)

	// incremental stats scan state
	statOff   int64
	openSize  int64
	openMTime time.Time
	open      bool // newest turn not completed (cached for openSize/openMTime)
	stats     Stats
}

func NewCodex(root string) *Codex {
	return &Codex{root: root, files: map[string]*cxFile{}, byID: map[string]string{}, roots: map[string]string{}}
}

func (c *Codex) Kind() string           { return "codex" }
func (c *Codex) Path() string           { return c.root }
func (c *Codex) Status() (bool, string) { return false, "" }
func (c *Codex) Fingerprint() string    { c.mu.Lock(); defer c.mu.Unlock(); c.scan(); return c.fp }

// scan refreshes the file index (throttled to once per second). Caller holds mu.
func (c *Codex) scan() {
	if time.Since(c.lastScan) < time.Second {
		return
	}
	c.lastScan = time.Now()
	next := map[string]*cxFile{}
	var count, size, newest int64
	for _, sub := range []struct {
		dir      string
		archived bool
	}{{"sessions", false}, {"archived_sessions", true}} {
		filepath.WalkDir(filepath.Join(c.root, sub.dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			f := c.files[p]
			if f == nil {
				f = &cxFile{path: p}
			}
			if f.size != info.Size() || !f.mtime.Equal(info.ModTime()) {
				f.size, f.mtime = info.Size(), info.ModTime()
				if f.title == "" || f.cwd == "" {
					f.metaOK = false // head may have been incomplete; re-read
				}
			}
			f.archived = sub.archived
			next[p] = f
			count++
			size += info.Size()
			newest = max(newest, info.ModTime().UnixNano())
			return nil
		})
	}
	c.files = next
	c.byID = map[string]string{}
	for p, f := range c.files {
		if !f.metaOK {
			parseCodexMeta(f)
		}
		c.byID[f.id] = p
	}
	c.fp = fmt.Sprintf("%d:%d:%d", count, size, newest)
}

type cxLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type cxPayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Status    string          `json:"status"`
	Message   string          `json:"message"`
	Output    json.RawMessage `json:"output"`
	Action    json.RawMessage `json:"action"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

func parseTS(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func filenameID(path string) string {
	b := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(b) >= 36 {
		return b[len(b)-36:]
	}
	return b
}

// parseCodexMeta reads only the head of a rollout: session_meta and first user message.
func parseCodexMeta(f *cxFile) {
	f.metaOK = true
	f.id = filenameID(f.path)
	f.created = f.mtime.UnixMilli()
	fh, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 64<<10)
	for i := 0; i < 80; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var l cxLine
			var p cxPayload
			if json.Unmarshal(line, &l) == nil {
				switch l.Type {
				case "session_meta":
					if json.Unmarshal(l.Payload, &p) == nil {
						if p.ID != "" {
							f.id = p.ID
						}
						f.cwd = p.Cwd
						var m struct {
							Timestamp string `json:"timestamp"`
						}
						if json.Unmarshal(l.Payload, &m) == nil {
							if ts := parseTS(m.Timestamp); ts > 0 {
								f.created = ts
							}
						}
					}
				case "event_msg":
					if json.Unmarshal(l.Payload, &p) == nil && p.Type == "user_message" {
						f.userEvt = true
						if f.title == "" {
							f.title = oneLine(p.Message, 100)
						}
					}
				case "response_item":
					if f.title == "" && json.Unmarshal(l.Payload, &p) == nil && p.Type == "message" && p.Role == "user" {
						f.title = oneLine(userText(p), 100)
					}
				case "turn_context":
					if f.cwd == "" && json.Unmarshal(l.Payload, &p) == nil {
						f.cwd = p.Cwd
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
}

// userText returns the human-typed text of a response_item user message, or ""
// for injected context (AGENTS.md, <environment_context>, ...).
func userText(p cxPayload) string {
	var sb strings.Builder
	for _, c := range p.Content {
		t := strings.TrimSpace(c.Text)
		if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") {
			continue
		}
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (c *Codex) projectRoot(cwd string) string {
	if r, ok := c.roots[cwd]; ok {
		return r
	}
	r := gitRoot(cwd)
	c.roots[cwd] = r
	return r
}

func (c *Codex) Sessions() ([]Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scan()
	out := make([]Session, 0, len(c.files))
	for _, f := range c.files {
		path := f.cwd
		if path != "" {
			path = c.projectRoot(path)
		}
		title := f.title
		if title == "" {
			title = "(no user message)"
		}
		s := Session{
			Kind: "codex", ID: f.id, Title: title, Directory: f.cwd,
			TimeCreated: f.created, MTime: f.mtime.UnixMilli(), Archived: f.archived,
			Running:     time.Since(f.mtime) < runningWindow && codexTurnOpen(f),
			ProjectPath: path, ProjectKey: normPath(path), ProjectName: baseName(path),
		}
		if path == "" {
			s.ProjectKey, s.ProjectName = "(unknown)", "(unknown)"
		}
		out = append(out, s)
	}
	return out, nil
}

// Stats scans only the new tail of the rollout since the previous call.
// Token totals come from the latest cumulative token_count event.
func (c *Codex) Stats(id string) (*Stats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scan()
	f := c.files[c.byID[id]]
	if f == nil {
		return nil, fmt.Errorf("unknown codex session %q", id)
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if st, err := fh.Stat(); err == nil && st.Size() < f.statOff {
		f.statOff, f.stats = 0, Stats{} // replaced/truncated
	}
	if _, err := fh.Seek(f.statOff, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(fh, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			break // EOF or partial line
		}
		f.statOff += int64(len(line))
		switch {
		case bytes.Contains(line, []byte(`"token_count"`)):
			if tc, ok := parseTokenCount(line); ok {
				f.stats.ContextUsed, f.stats.ContextWindow = tc.Last, tc.Window
				f.stats.Usage = tc.Total.usage()
			}
		case bytes.Contains(line, []byte(`"turn_context"`)):
			var l struct {
				Type    string `json:"type"`
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Type == "turn_context" && l.Payload.Model != "" {
				if f.stats.FirstModel == "" {
					f.stats.FirstModel = l.Payload.Model
				}
				f.stats.LastModel = l.Payload.Model
			}
		}
	}
	out := f.stats // Cost stays nil: codex does not record price
	return &out, nil
}

const cxChunk = 8 << 20 // max bytes of rollout parsed per Delta call

func (c *Codex) Delta(id, cursor string) (*Delta, error) {
	c.mu.Lock()
	c.scan()
	path := c.byID[id]
	userEvt := c.files[path] != nil && c.files[path].userEvt
	c.mu.Unlock()
	if path == "" {
		return nil, fmt.Errorf("unknown codex session %q", id)
	}
	off, _ := strconv.ParseInt(cursor, 10, 64)
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	d := &Delta{Messages: []Message{}}
	if st, err := fh.Stat(); err == nil && off > st.Size() {
		off, d.Reset = 0, true // file was replaced/truncated
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(fh, 256<<10)
	calls := map[string][2]int{} // call_id -> (message idx, part idx) within this delta
	read := int64(0)
	for read < cxChunk {
		line, err := r.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			break // EOF or partially written line: retry next time
		}
		lineOff := off + read
		read += int64(len(line))
		convertCodexLine(d, calls, lineOff, line, userEvt)
	}
	d.Cursor = strconv.FormatInt(off+read, 10)
	if read >= cxChunk {
		d.More = true
	}
	return d, nil
}

func convertCodexLine(d *Delta, calls map[string][2]int, off int64, line []byte, userEvt bool) {
	var l cxLine
	var p cxPayload
	if json.Unmarshal(line, &l) != nil || json.Unmarshal(l.Payload, &p) != nil {
		return
	}
	ts := parseTS(l.Timestamp)
	mid := fmt.Sprintf("L%012d", off)
	add := func(role string, part Part) {
		part.MessageID = mid
		d.Messages = append(d.Messages, Message{ID: mid, Role: role, Time: ts, Parts: []Part{part}})
	}
	switch l.Type {
	case "event_msg":
		if p.Type == "user_message" {
			add("user", Part{ID: mid + ".0", Kind: KindText, Text: p.Message})
		}
	case "response_item":
		switch p.Type {
		case "message":
			if p.Role == "user" && !userEvt { // exec-mode sessions have no user_message events
				if t := userText(p); t != "" {
					add("user", Part{ID: mid + ".0", Kind: KindText, Text: t})
				}
				return
			}
			if p.Role != "assistant" {
				return // developer items are injected context
			}
			var sb strings.Builder
			for _, c := range p.Content {
				sb.WriteString(c.Text)
			}
			if sb.Len() > 0 {
				add("assistant", Part{ID: mid + ".0", Kind: KindText, Text: sb.String()})
			}
		case "reasoning":
			var sb strings.Builder
			for _, s := range p.Summary {
				sb.WriteString(s.Text + "\n")
			}
			if t := strings.TrimSpace(sb.String()); t != "" {
				add("assistant", Part{ID: mid + ".0", Kind: KindReasoning, Text: t})
			}
		case "function_call", "custom_tool_call", "local_shell_call":
			in := p.Input
			if p.Type == "function_call" {
				in = p.Arguments
			}
			part := Part{ID: "call:" + p.CallID, Kind: KindTool, Tool: p.Name, Status: "running", Input: rawOrString(in)}
			if p.Type == "custom_tool_call" && p.Status == "completed" {
				part.Status = "running" // output item completes it
			}
			if p.CallID == "" {
				part.ID = mid + ".0"
			}
			add("assistant", part)
			calls[p.CallID] = [2]int{len(d.Messages) - 1, 0}
		case "function_call_output", "custom_tool_call_output":
			out := codexOutput(p.Output)
			part := Part{ID: "call:" + p.CallID, Kind: KindTool, Status: "completed", Output: out}
			if ix, ok := calls[p.CallID]; ok { // call is in this delta: merge
				dst := &d.Messages[ix[0]].Parts[ix[1]]
				dst.Status, dst.Output = part.Status, part.Output
				return
			}
			// call arrived in an earlier delta: parts-only update, merged client-side
			d.Messages = append(d.Messages, Message{ID: "orphan", Parts: []Part{part}})
		case "web_search_call":
			part := Part{ID: mid + ".0", Kind: KindTool, Tool: "web_search", Status: "completed", Input: p.Action}
			add("assistant", part)
		}
	}
}

// codexOutput flattens tool output: plain string, or JSON {"output": "..."} /
// other JSON which is kept verbatim.
func codexOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	var wrapped struct {
		Output *string `json:"output"`
	}
	if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &wrapped) == nil && wrapped.Output != nil {
		return *wrapped.Output
	}
	return s
}

// cxTotals is codex's cumulative token accounting. Its input_tokens INCLUDES
// the cached part, unlike the normalized Usage (where Input is new tokens only).
type cxTotals struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
}

func (t cxTotals) usage() Usage {
	return Usage{Input: max(0, t.Input-t.Cached-t.CacheWrite), CacheRead: t.Cached,
		CacheWrite: t.CacheWrite, Output: t.Output, Reasoning: t.Reasoning}
}

// sub returns t - o, or t itself when the counter went backwards (new baseline).
func (t cxTotals) sub(o cxTotals) cxTotals {
	if t.Input < o.Input || t.Output < o.Output {
		return t
	}
	return cxTotals{t.Input - o.Input, t.Cached - o.Cached, t.CacheWrite - o.CacheWrite, t.Output - o.Output, t.Reasoning - o.Reasoning}
}

type cxTokenCount struct {
	Total  cxTotals
	Last   int64 // prompt size of the latest request (includes cached)
	Window int64
	Time   int64 // unix ms of the event
}

// parseTokenCount decodes an event_msg/token_count line; ok=false when the line
// is not a token_count event or carries no info yet.
func parseTokenCount(line []byte) (cxTokenCount, bool) {
	var l struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type string `json:"type"`
			Info *struct {
				Total cxTotals `json:"total_token_usage"`
				Last  struct {
					Input int64 `json:"input_tokens"`
				} `json:"last_token_usage"`
				Window int64 `json:"model_context_window"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Payload.Type != "token_count" || l.Payload.Info == nil {
		return cxTokenCount{}, false
	}
	i := l.Payload.Info
	return cxTokenCount{Total: i.Total, Last: i.Last.Input, Window: i.Window, Time: parseTS(l.Timestamp)}, true
}

// runningWindow: an unfinished turn only counts as "running" while the history
// was touched this recently (a crashed or killed agent leaves turns open forever).
const runningWindow = 3 * time.Minute

// codexTurnOpen reports whether the newest turn started but has not completed,
// by looking at the tail of the rollout. The result is cached per file state.
func codexTurnOpen(f *cxFile) bool {
	if f.openSize == f.size && f.openMTime.Equal(f.mtime) {
		return f.open
	}
	f.openSize, f.openMTime, f.open = f.size, f.mtime, false
	fh, err := os.Open(f.path)
	if err != nil {
		return false
	}
	defer fh.Close()
	const tail = 256 << 10
	off := max(0, f.size-tail)
	buf := make([]byte, f.size-off)
	n, _ := fh.ReadAt(buf, off)
	buf = buf[:n]
	start := bytes.LastIndex(buf, []byte(`"task_started"`))
	end := max(bytes.LastIndex(buf, []byte(`"task_complete"`)), bytes.LastIndex(buf, []byte(`"turn_aborted"`)))
	f.open = start >= 0 && start > end
	return f.open
}
