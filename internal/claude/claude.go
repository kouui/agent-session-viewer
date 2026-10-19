// Package claude reads Claude Code history: ~/.claude/projects/<project>/<session>.jsonl, plus
// subagent and workflow-agent transcripts below <session>/subagents/. Read-only.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-viewer/internal/model"
)

// Claude is the Backend for Claude Code transcripts. The delta cursor is a byte
// offset into the transcript. Claude records no price per message, so Stats.Cost
// and UsageRow.Cost stay nil (tokens only).
type Claude struct {
	root string // claude config dir, e.g. ~/.claude

	mu       sync.Mutex
	files    map[string]*clFile // by absolute path
	byID     map[string]*clFile // session id -> file
	agents   map[string]string  // "<top session>|<agent id>" -> session id of that agent's transcript
	lastScan time.Time
	fp       string
	roots    map[string]string // gitRoot cache
	wfNames  map[string]string // workflow run dir -> workflow name

	claimMu sync.Mutex
	claims  map[string]*clFile // assistant message id -> the transcript whose usage rows count it

	idxMu   sync.Mutex
	idxBusy bool
	indexed int // files whose usage has been scanned at least once (progress, for Fingerprint)
}

func New(root string) *Claude {
	return &Claude{root: root, files: map[string]*clFile{}, byID: map[string]*clFile{}, roots: map[string]string{}, wfNames: map[string]string{}}
}

func (c *Claude) Kind() string           { return "claude" }
func (c *Claude) Path() string           { return c.root }
func (c *Claude) Status() (bool, string) { return false, "" }

// clFile is one transcript file: a session, a subagent, or a workflow agent.
type clFile struct {
	path    string
	size    int64
	mtime   time.Time
	id      string
	parent  string // parent session id (subagents and workflow agents)
	group   string // workflow run id (workflow agents)
	project string // project directory name below projects/ (fallback label)

	metaOK     bool
	cwd        string
	title      string // custom/ai title or subagent description
	firstTitle string // fallback: first user prompt
	created    int64

	openSize  int64
	openMTime time.Time
	open      bool // newest turn not finished (cached for openSize/openMTime)

	smu sync.Mutex // guards the incremental scan state below
	sc  clScan
}

// scan refreshes the file index (throttled to once per second). Caller holds mu.
func (c *Claude) scan() {
	if time.Since(c.lastScan) < time.Second {
		return
	}
	c.lastScan = time.Now()
	projects := filepath.Join(c.root, "projects")
	next := map[string]*clFile{}
	var count, size, newest int64
	filepath.WalkDir(projects, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		rel, err := filepath.Rel(projects, p)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		name := strings.TrimSuffix(parts[len(parts)-1], ".jsonl")
		var id, parent, group string
		switch {
		case len(parts) == 2: // <project>/<sid>.jsonl
			id = name
		case len(parts) == 4 && parts[2] == "subagents" && strings.HasPrefix(name, "agent-"): // <project>/<sid>/subagents/agent-x.jsonl
			parent, id = parts[1], parts[1]+"~"+name
		case len(parts) == 6 && parts[2] == "subagents" && parts[3] == "workflows" && strings.HasPrefix(name, "agent-"):
			parent, group, id = parts[1], parts[4], parts[1]+"~"+parts[4]+"~"+name
		default:
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := c.files[p]
		if f == nil {
			f = &clFile{path: p, id: id, parent: parent, group: group, project: parts[0]}
		}
		if f.size != info.Size() || !f.mtime.Equal(info.ModTime()) {
			f.size, f.mtime = info.Size(), info.ModTime()
			if f.cwd == "" || f.title == "" && f.firstTitle == "" {
				f.metaOK = false // head may have been incomplete; re-read
			}
		}
		next[p] = f
		count++
		size += info.Size()
		newest = max(newest, info.ModTime().UnixNano())
		return nil
	})
	c.files = next
	c.byID = make(map[string]*clFile, len(next))
	c.agents = map[string]string{}
	var todo []*clFile
	for _, f := range next {
		c.byID[f.id] = f
		if f.parent != "" {
			c.agents[f.parent+"|"+strings.TrimPrefix(f.id[strings.LastIndex(f.id, "~")+1:], "agent-")] = f.id
		}
		if !f.metaOK {
			todo = append(todo, f)
		}
	}
	parseMetas(todo)
	c.idxMu.Lock()
	idx := c.indexed / 25 // coarse: indexing progress must not flood change events
	c.idxMu.Unlock()
	c.fp = fmt.Sprintf("%d:%d:%d:%d", count, size, newest, idx)
}

// parseMetas reads the head of many transcripts concurrently.
func parseMetas(fs []*clFile) {
	var wg sync.WaitGroup
	ch := make(chan *clFile)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				parseMeta(f)
			}
		}()
	}
	for _, f := range fs {
		ch <- f
	}
	close(ch)
	wg.Wait()
}

func (c *Claude) Fingerprint() string { c.mu.Lock(); defer c.mu.Unlock(); c.scan(); return c.fp }

// ---- line shapes ----------------------------------------------------------

type clLine struct {
	Type      string     `json:"type"`
	Subtype   string     `json:"subtype"`
	Timestamp string     `json:"timestamp"`
	Cwd       string     `json:"cwd"`
	IsMeta    bool       `json:"isMeta"`
	AITitle   string     `json:"aiTitle"`
	Message   *clMessage `json:"message"`
	// toolUseResult of a launch carries agentId (Agent/SendMessage) or runId (Workflow)
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type clMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"` // string or []clBlock
	Usage      *struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type clBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result: string or []{type,text}
	IsError   bool            `json:"is_error"`
}

// skippable reports lines that never matter and can be huge (request blobs).
func skippable(line []byte) bool {
	head := line
	if len(head) > 96 {
		head = head[:96]
	}
	return bytes.Contains(head, []byte(`"type":"api-request`)) || bytes.Contains(head, []byte(`"type":"file-history`))
}

func blocks(raw json.RawMessage) (text string, bs []clBlock) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		json.Unmarshal(raw, &text)
		return text, nil
	}
	json.Unmarshal(raw, &bs)
	return "", bs
}

func parseTS(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---- metadata (head of the file + sidecar json) -----------------------------

const headLimit = 1 << 20

func parseMeta(f *clFile) {
	f.metaOK = true
	f.created = f.mtime.UnixMilli()
	switch {
	case f.parent == "": // top-level session: <proj>/<sid>/custom-title.json
		var t struct {
			CustomTitle string `json:"customTitle"`
		}
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(f.path), f.id, "custom-title.json")); err == nil && json.Unmarshal(b, &t) == nil {
			f.title = t.CustomTitle
		}
	default: // subagent / workflow agent: agent-x.meta.json holds the description
		var m struct {
			Description string `json:"description"`
			AgentType   string `json:"agentType"`
		}
		meta := strings.TrimSuffix(f.path, ".jsonl") + ".meta.json"
		if b, err := os.ReadFile(meta); err == nil && json.Unmarshal(b, &m) == nil {
			f.title = m.Description
			if f.title == "" {
				f.title = m.AgentType
			}
		}
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer fh.Close()
	r := bufio.NewReaderSize(io.LimitReader(fh, headLimit), 64<<10)
	aiTitle := ""
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && !skippable(line) {
			var l clLine
			if json.Unmarshal(line, &l) == nil {
				if f.cwd == "" && l.Cwd != "" {
					f.cwd = l.Cwd
				}
				if ts := parseTS(l.Timestamp); ts > 0 && f.created == f.mtime.UnixMilli() {
					f.created = ts
				}
				if l.Type == "ai-title" || l.AITitle != "" {
					aiTitle = l.AITitle
				}
				if f.firstTitle == "" && l.Type == "user" && !l.IsMeta && l.Message != nil {
					t, bs := blocks(l.Message.Content)
					for _, b := range bs {
						if b.Type == "text" {
							t += b.Text
						}
					}
					if t = strings.TrimSpace(t); t != "" && !strings.HasPrefix(t, "<") {
						f.firstTitle = oneLine(t, 100)
					}
				}
			}
		}
		if err != nil || (f.cwd != "" && aiTitle != "" && f.firstTitle != "") {
			break
		}
	}
	if f.title == "" && f.parent == "" {
		f.title = oneLine(aiTitle, 100)
	}
}

// ---- sessions ---------------------------------------------------------------

func (c *Claude) projectRoot(cwd string) string {
	if r, ok := c.roots[cwd]; ok {
		return r
	}
	r := model.GitRoot(cwd)
	c.roots[cwd] = r
	return r
}

// workflowName reads <sid>/workflows/wf_<run>.json once per run.
func (c *Claude) workflowName(f *clFile) string {
	// from <sid>/subagents/workflows/wf_x/agent.jsonl to <sid>/workflows/wf_x.json
	dir := filepath.Join(filepath.Dir(f.path), "..", "..", "..", "workflows")
	key := filepath.Join(dir, f.group+".json")
	if n, ok := c.wfNames[key]; ok {
		return n
	}
	name := ""
	if b, err := os.ReadFile(key); err == nil {
		var w struct {
			Name string `json:"workflowName"`
		}
		if json.Unmarshal(b, &w) == nil {
			name = w.Name
		}
	}
	c.wfNames[key] = name
	return name
}

func (c *Claude) Sessions() ([]model.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scan()
	out := make([]model.Session, 0, len(c.files))
	for _, f := range c.files {
		path := f.cwd
		if path != "" {
			path = c.projectRoot(path)
		}
		title := f.title
		if title == "" {
			title = f.firstTitle
		}
		if title == "" {
			title = "(no prompt)"
		}
		s := model.Session{
			Kind: "claude", ID: f.id, Title: title, Directory: f.cwd, ParentID: f.parent,
			TimeCreated: f.created, MTime: f.mtime.UnixMilli(),
			ProjectPath: path, ProjectKey: model.NormPath(path), ProjectName: model.BaseName(path),
		}
		if path == "" {
			s.ProjectKey, s.ProjectName = "(unknown)", "(unknown)"
		}
		if f.group != "" {
			s.Group = f.group
			s.GroupName = c.workflowName(f)
			if s.GroupName == "" {
				s.GroupName = "workflow " + strings.TrimPrefix(f.group, "wf_")
			}
		}
		if time.Since(f.mtime) < model.RunningWindow {
			s.Running = turnOpen(f)
		}
		out = append(out, s)
	}
	return out, nil
}

// turnOpen reports whether the newest turn is unfinished: the last meaningful record is
// a user/tool-result line or an assistant line that did not end its turn.
func turnOpen(f *clFile) bool {
	if f.openSize == f.size && f.openMTime.Equal(f.mtime) {
		return f.open
	}
	f.openSize, f.openMTime = f.size, f.mtime
	f.open = tailOpen(f.path, f.size)
	return f.open
}

func tailOpen(path string, size int64) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	for n := int64(256 << 10); ; n *= 4 {
		off := max(0, size-n)
		buf := make([]byte, size-off)
		if _, err := fh.ReadAt(buf, off); err != nil && err != io.EOF {
			return false
		}
		lines := bytes.Split(buf, []byte("\n"))
		if off > 0 {
			lines = lines[1:] // first line is partial
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if len(lines[i]) == 0 || skippable(lines[i]) {
				continue
			}
			var l clLine
			if json.Unmarshal(lines[i], &l) != nil {
				continue
			}
			switch {
			case l.Type == "system" && l.Subtype == "turn_duration":
				return false
			case l.Type == "assistant" && l.Message != nil:
				switch l.Message.StopReason {
				case "end_turn", "stop_sequence", "max_tokens", "refusal":
					return false
				}
				return true
			case l.Type == "user" && !l.IsMeta:
				return true
			}
		}
		if off == 0 {
			return false
		}
	}
}

// ---- stats and usage (one incremental scan state per file) -----------------

type clUse struct {
	ts    int64
	model string
	u     model.Usage
	dup   bool // already counted by another transcript (resumed/forked sessions copy history): skip in usage rows
}

type clScan struct {
	off     int64
	msgs    map[string]*clUse // assistant message id -> usage (streamed lines repeat it)
	ctx     int64
	first   string
	last    string
	rows    []model.UsageRow // cache of hourly rows, rebuilt when dirty
	dirty   bool
	scanned bool
}

// scanFile parses the appended tail of f. Caller holds f.smu.
func (f *clFile) scanFile(claim func(id string, f *clFile) bool) {
	fh, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer fh.Close()
	st := &f.sc
	if fi, err := fh.Stat(); err == nil {
		if fi.Size() < st.off {
			*st = clScan{}
		} else if fi.Size() == st.off && st.scanned {
			return
		}
	}
	if st.msgs == nil {
		st.msgs = map[string]*clUse{}
	}
	if _, err := fh.Seek(st.off, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(fh, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			break // EOF or partial line
		}
		st.off += int64(len(line))
		if skippable(line) || !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var l clLine
		if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil || l.Message.ID == "" {
			continue
		}
		m := l.Message
		if m.Model == "<synthetic>" {
			continue
		}
		u := model.Usage{Input: m.Usage.Input, Output: m.Usage.Output, CacheRead: m.Usage.CacheRead, CacheWrite: m.Usage.CacheWrite}
		if old, ok := st.msgs[m.ID]; ok { // streamed lines repeat the message: keep the latest usage
			old.ts, old.model, old.u = parseTS(l.Timestamp), m.Model, u
		} else {
			st.msgs[m.ID] = &clUse{ts: parseTS(l.Timestamp), model: m.Model, u: u, dup: !claim(m.ID, f)}
		}
		st.ctx = u.Input + u.CacheRead + u.CacheWrite
		if st.first == "" {
			st.first = m.Model
		}
		st.last = m.Model
		st.dirty = true
	}
	st.scanned = true
}

func (c *Claude) file(id string) *clFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scan()
	return c.byID[id]
}

func (c *Claude) Stats(id string) (*model.Stats, error) {
	f := c.file(id)
	if f == nil {
		return nil, fmt.Errorf("unknown claude session %q", id)
	}
	f.smu.Lock()
	defer f.smu.Unlock()
	f.scanFile(c.claim)
	out := &model.Stats{ContextUsed: f.sc.ctx, FirstModel: f.sc.first, LastModel: f.sc.last}
	for _, m := range f.sc.msgs {
		out.Usage.Input += m.u.Input
		out.Usage.Output += m.u.Output
		out.Usage.CacheRead += m.u.CacheRead
		out.Usage.CacheWrite += m.u.CacheWrite
	}
	return out, nil // Cost stays nil: Claude Code does not record price per message
}

// Usage returns hourly usage of everything indexed so far. Indexing runs in the
// background (the transcripts can add up to many GB); Fingerprint changes as it progresses.
func (c *Claude) Usage() ([]model.UsageRow, error) {
	c.mu.Lock()
	c.scan()
	files := make([]*clFile, 0, len(c.files))
	for _, f := range c.files {
		files = append(files, f)
	}
	c.mu.Unlock()
	c.startIndex(files)
	var out []model.UsageRow
	for _, f := range files {
		if !f.smu.TryLock() { // being scanned right now: skip, the next call sees it
			continue
		}
		if f.sc.dirty || (f.sc.scanned && f.sc.rows == nil && len(f.sc.msgs) > 0) {
			f.sc.rows = f.sc.buildRows(f.id)
			f.sc.dirty = false
		}
		out = append(out, f.sc.rows...)
		f.smu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}

func (st *clScan) buildRows(sid string) []model.UsageRow {
	type key struct {
		hour  int64
		model string
	}
	b := map[key]*model.Usage{}
	for _, m := range st.msgs {
		if m.ts == 0 || m.dup {
			continue
		}
		k := key{m.ts / 3600000 * 3600000, m.model}
		u := b[k]
		if u == nil {
			u = &model.Usage{}
			b[k] = u
		}
		u.Input += m.u.Input
		u.Output += m.u.Output
		u.CacheRead += m.u.CacheRead
		u.CacheWrite += m.u.CacheWrite
	}
	rows := make([]model.UsageRow, 0, len(b))
	for k, u := range b {
		rows = append(rows, model.UsageRow{Session: sid, Model: k.model, Hour: k.hour, Usage: *u})
	}
	return rows
}

// startIndex scans every transcript (newest first) in one background goroutine.
func (c *Claude) startIndex(files []*clFile) {
	c.idxMu.Lock()
	if c.idxBusy {
		c.idxMu.Unlock()
		return
	}
	c.idxBusy = true
	c.idxMu.Unlock()
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	go func() {
		done := 0
		for _, f := range files {
			f.smu.Lock()
			first := !f.sc.scanned
			f.scanFile(c.claim)
			f.smu.Unlock()
			if first {
				done++
				c.idxMu.Lock()
				c.indexed++
				c.idxMu.Unlock()
			}
		}
		c.idxMu.Lock()
		c.idxBusy = false
		c.idxMu.Unlock()
	}()
}

// ---- messages ---------------------------------------------------------------

const clChunk = 8 << 20 // max bytes parsed per Delta call

func (c *Claude) Delta(id, cursor string) (*model.Delta, error) {
	f := c.file(id)
	if f == nil {
		return nil, fmt.Errorf("unknown claude session %q", id)
	}
	off, _ := strconv.ParseInt(cursor, 10, 64)
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	d := &model.Delta{Messages: []model.Message{}}
	if st, err := fh.Stat(); err == nil && off > st.Size() {
		off, d.Reset = 0, true // file was replaced/truncated
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(fh, 256<<10)
	read := int64(0)
	for read < clChunk {
		line, err := r.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			break
		}
		lineOff := off + read
		read += int64(len(line))
		if !skippable(line) {
			convertLine(d, lineOff, line, f.top(), c.agentSession)
		}
	}
	d.Cursor = strconv.FormatInt(off+read, 10)
	d.More = read >= clChunk
	return d, nil
}

// Recent parses the tail of the transcript; each JSONL line becomes its own message.
func (c *Claude) Recent(id string, limit int) ([]model.Message, error) {
	f := c.file(id)
	if f == nil {
		return nil, fmt.Errorf("unknown claude session %q", id)
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	var msgs []model.Message
	for n := int64(512 << 10); ; n *= 4 { // widen until enough items or the whole file is read
		off := max(0, st.Size()-n)
		buf := make([]byte, st.Size()-off)
		if _, err := fh.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil, err
		}
		d := &model.Delta{}
		pos := int64(0)
		if off > 0 {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				return []model.Message{}, nil
			}
			pos = int64(i + 1)
		}
		for pos < int64(len(buf)) {
			i := bytes.IndexByte(buf[pos:], '\n')
			if i < 0 {
				break
			}
			line := buf[pos : pos+int64(i)+1]
			if !skippable(line) {
				convertLine(d, off+pos, line, f.top(), c.agentSession)
			}
			pos += int64(i) + 1
		}
		msgs = msgs[:0]
		for _, m := range d.Messages {
			if m.Role != "" {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) >= limit || off == 0 {
			break
		}
	}
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			} else {
				sb.WriteString("[" + p.Type + "]")
			}
		}
		return sb.String()
	}
	return string(raw)
}

// convertLine turns one transcript line into messages. sid is the top-level session id; agentSession
// maps an agent id to the session id of its transcript ("" if unknown).
func convertLine(d *model.Delta, off int64, line []byte, sid string, agentSession func(sid, agent string) string) {
	if !bytes.Contains(line, []byte(`"message"`)) {
		return
	}
	var l clLine
	if json.Unmarshal(line, &l) != nil || l.Message == nil || (l.Type != "user" && l.Type != "assistant") {
		return
	}
	ts := parseTS(l.Timestamp)
	mid := fmt.Sprintf("L%012d", off)
	msg := model.Message{ID: mid, Time: ts}
	text, bs := blocks(l.Message.Content)
	n := 0
	add := func(p model.Part) {
		p.MessageID = mid
		msg.Parts = append(msg.Parts, p)
		n++
	}
	if l.Type == "user" {
		if l.IsMeta {
			return
		}
		msg.Role = "user"
		if t := strings.TrimSpace(text); t != "" {
			add(model.Part{ID: fmt.Sprintf("%s.%d", mid, n), Kind: model.KindText, Text: text})
		}
		var results []model.Part
		for _, b := range bs {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					add(model.Part{ID: fmt.Sprintf("%s.%d", mid, n), Kind: model.KindText, Text: b.Text})
				}
			case "tool_result":
				st := "completed"
				if b.IsError {
					st = "error"
				}
				rp := model.Part{ID: "tool:" + b.ToolUseID, Kind: model.KindTool, Status: st, Output: toolOutput(b.Content)}
				var tr struct {
					AgentID string `json:"agentId"`
					RunID   string `json:"runId"`
				}
				if len(l.ToolUseResult) > 0 && l.ToolUseResult[0] == '{' && json.Unmarshal(l.ToolUseResult, &tr) == nil {
					if tr.RunID != "" {
						rp.Link = &model.Link{Group: tr.RunID}
					} else if id := agentSession(sid, tr.AgentID); tr.AgentID != "" && id != "" {
						rp.Link = &model.Link{SessionID: id}
					}
				}
				results = append(results, rp)
			case "image":
				add(model.Part{ID: fmt.Sprintf("%s.%d", mid, n), Kind: model.KindText, Text: "[image]"})
			}
		}
		if len(results) > 0 { // parts-only update for tool calls sent earlier
			d.Messages = append(d.Messages, model.Message{ID: "result", Parts: results})
		}
		if len(msg.Parts) == 0 {
			return
		}
	} else {
		msg.Role, msg.Model, msg.Finish = "assistant", l.Message.Model, l.Message.StopReason
		for _, b := range bs {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					add(model.Part{ID: fmt.Sprintf("%s.%d", mid, n), Kind: model.KindText, Text: b.Text})
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					add(model.Part{ID: fmt.Sprintf("%s.%d", mid, n), Kind: model.KindReasoning, Text: b.Thinking})
				}
			case "tool_use":
				add(model.Part{ID: "tool:" + b.ID, Kind: model.KindTool, Tool: b.Name, Status: "running", Input: b.Input})
			}
		}
		if len(msg.Parts) == 0 {
			return
		}
	}
	d.Messages = append(d.Messages, msg)
}

// top is the id of the top-level session this transcript belongs to.
func (f *clFile) top() string {
	if f.parent != "" {
		return f.parent
	}
	return f.id
}

func (c *Claude) agentSession(sid, agent string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agents[sid+"|"+agent]
}

// claim reports whether f owns usage of message id (the first transcript to see it does).
func (c *Claude) claim(id string, f *clFile) bool {
	c.claimMu.Lock()
	defer c.claimMu.Unlock()
	if c.claims == nil {
		c.claims = map[string]*clFile{}
	}
	if o, ok := c.claims[id]; ok {
		return o == f
	}
	c.claims[id] = f
	return true
}
