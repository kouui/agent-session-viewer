package main

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// OpenCode reads opencode's SQLite history (opencode.db), strictly read-only.
type OpenCode struct {
	dbPath string

	mu    sync.Mutex
	db    *sql.DB
	stale bool
	err   string

	usage ocUsageCache
}

func NewOpenCode(dbPath string) *OpenCode { return &OpenCode{dbPath: dbPath} }

func (o *OpenCode) Kind() string { return "opencode" }
func (o *OpenCode) Path() string { return o.dbPath }

func (o *OpenCode) Status() (bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stale, o.err
}

func (o *OpenCode) setErr(err error) {
	o.mu.Lock()
	o.err = err.Error()
	o.mu.Unlock()
}

func (o *OpenCode) dsn(immutable bool) string {
	p := filepath.ToSlash(o.dbPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // windows drive path
	}
	u := (&url.URL{Path: p}).EscapedPath()
	q := "?mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"
	if immutable {
		// Fallback for filesystems without shared-memory support (e.g. /mnt/c
		// from WSL): works, but WAL contents are not visible.
		q = "?mode=ro&immutable=1&_pragma=query_only(1)"
	}
	return "file:" + u + q
}

// handle returns a read-only handle. Connections are not pooled so every
// query sees fresh file state (needed for immutable mode).
func (o *OpenCode) handle() (*sql.DB, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.db != nil {
		return o.db, nil
	}
	var lastErr error
	for _, imm := range []bool{false, true} {
		db, err := sql.Open("sqlite", o.dsn(imm))
		if err != nil {
			lastErr = err
			continue
		}
		db.SetMaxIdleConns(0)
		var n int
		if err := db.QueryRow("SELECT count(*) FROM session").Scan(&n); err != nil {
			db.Close()
			lastErr = err
			continue
		}
		o.db, o.stale, o.err = db, imm, ""
		return db, nil
	}
	o.err = lastErr.Error()
	return nil, lastErr
}

func (o *OpenCode) Fingerprint() string {
	var b strings.Builder
	for _, suf := range []string{"", "-wal"} {
		if st, err := os.Stat(o.dbPath + suf); err == nil {
			b.WriteString(st.ModTime().Format("20060102150405.000000000"))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte(';')
		}
	}
	return b.String()
}

func (o *OpenCode) Sessions() ([]Session, error) {
	db, err := o.handle()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
SELECT s.id, s.project_id, COALESCE(p.worktree,''), COALESCE(p.name,''),
       COALESCE(s.parent_id,''), s.title, s.directory, s.time_created, s.time_updated,
       COALESCE((SELECT MAX(m.time_created) FROM message m WHERE m.session_id = s.id), 0),
       COALESCE(s.time_archived, 0),
       -- newest message is an assistant message that has not completed yet
       COALESCE((SELECT 1 FROM message m WHERE m.session_id = s.id
                 AND m.time_created = (SELECT MAX(time_created) FROM message WHERE session_id = s.id)
                 AND json_extract(m.data,'$.role') = 'assistant' AND json_extract(m.data,'$.time.completed') IS NULL LIMIT 1), 0)
FROM session s LEFT JOIN project p ON p.id = s.project_id`)
	if err != nil {
		o.setErr(err)
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		var pid, worktree, pname string
		var upd, lastMsg, arch, open int64
		if err := rows.Scan(&x.ID, &pid, &worktree, &pname, &x.ParentID, &x.Title,
			&x.Directory, &x.TimeCreated, &upd, &lastMsg, &arch, &open); err != nil {
			return nil, err
		}
		x.Kind = "opencode"
		x.MTime = max(upd, lastMsg)
		x.Archived = arch != 0
		x.Running = open != 0 && time.Since(time.UnixMilli(x.MTime)) < runningWindow
		path := worktree
		if pid == "global" || path == "" || path == "/" {
			path = x.Directory
			pname = ""
		}
		x.ProjectPath = path
		x.ProjectKey = normPath(path)
		x.ProjectName = pname
		if x.ProjectName == "" {
			x.ProjectName = baseName(path)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Stats reads session-level aggregates (opencode keeps them on the session row)
// and the first/latest model seen in the messages.
func (o *OpenCode) Stats(id string) (*Stats, error) {
	db, err := o.handle()
	if err != nil {
		return nil, err
	}
	var cost float64
	st := &Stats{}
	var reasoning int64
	err = db.QueryRow(`SELECT cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write
FROM session WHERE id = ?`, id).Scan(&cost, &st.Usage.Input, &st.Usage.Output, &reasoning, &st.Usage.CacheRead, &st.Usage.CacheWrite)
	if err != nil {
		o.setErr(err)
		return nil, err
	}
	st.Usage.Reasoning = reasoning
	st.Cost = &cost
	const model = `COALESCE(json_extract(data,'$.modelID'), json_extract(data,'$.model.modelID'))`
	const provider = `COALESCE(json_extract(data,'$.providerID'), json_extract(data,'$.model.providerID'), '')`
	for _, q := range []struct {
		dst   *string
		order string
	}{{&st.FirstModel, "ASC"}, {&st.LastModel, "DESC"}} {
		var prov, mdl string
		if db.QueryRow(`SELECT `+provider+`, `+model+` FROM message WHERE session_id = ? AND `+model+` IS NOT NULL
ORDER BY time_created `+q.order+` LIMIT 1`, id).Scan(&prov, &mdl) == nil {
			*q.dst = ocModelName(prov, mdl)
		}
	}
	// context length = prompt size (new + cached input) of the latest model call
	var last string
	if db.QueryRow(`SELECT data FROM message WHERE session_id = ?
AND json_extract(data,'$.role') = 'assistant' AND COALESCE(json_extract(data,'$.tokens.input'),0) + COALESCE(json_extract(data,'$.tokens.cache.read'),0) > 0
ORDER BY time_created DESC LIMIT 1`, id).Scan(&last) == nil {
		var m struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
			Tokens     struct {
				Input int64 `json:"input"`
				Cache struct {
					Read  int64 `json:"read"`
					Write int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		}
		if json.Unmarshal([]byte(last), &m) == nil {
			st.ContextUsed = m.Tokens.Input + m.Tokens.Cache.Read + m.Tokens.Cache.Write
			st.ContextWindow = contextWindow(m.ProviderID, m.ModelID)
		}
	}
	return st, nil
}

// ---- conversion: opencode rows -> normalized contract -----------------------

// ocModelName renders "provider/model" (just the model when the provider is unknown).
func ocModelName(provider, model string) string {
	if model == "" || provider == "" {
		return model
	}
	return provider + "/" + model
}

type ocMessage struct {
	Role       string `json:"role"`
	Agent      string `json:"agent"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Model      struct {
		ModelID    string `json:"modelID"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
	Finish string `json:"finish"`
	Error  *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

type ocPart struct {
	Type   string   `json:"type"`
	Text   string   `json:"text"`
	Tool   string   `json:"tool"`
	Files  []string `json:"files"`
	Reason string   `json:"reason"`
	Cost   float64  `json:"cost"`
	Tokens *struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	State *struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
		Error  string          `json:"error"`
		Title  string          `json:"title"`
		Time   struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
}

func convertOCMessage(id string, created int64, data string) Message {
	m := Message{ID: id, Time: created}
	var x ocMessage
	if json.Unmarshal([]byte(data), &x) != nil {
		m.Role = "assistant"
		return m
	}
	m.Role, m.Agent, m.Finish = x.Role, x.Agent, x.Finish
	m.Model = ocModelName(x.ProviderID, x.ModelID)
	if m.Model == "" {
		m.Model = ocModelName(x.Model.ProviderID, x.Model.ModelID)
	}
	if x.Error != nil {
		m.Error = strings.TrimSpace(x.Error.Name + " " + x.Error.Data.Message)
	}
	return m
}

// convertOCPart returns ok=false for parts that carry no display value.
func convertOCPart(id, messageID, data string) (Part, bool) {
	p := Part{ID: id, MessageID: messageID}
	var x ocPart
	if json.Unmarshal([]byte(data), &x) != nil {
		p.Kind, p.Raw = KindOther, rawString(data)
		return p, true
	}
	switch x.Type {
	case "text":
		p.Kind, p.Text = KindText, x.Text
	case "reasoning":
		p.Kind, p.Text = KindReasoning, x.Text
	case "tool":
		p.Kind, p.Tool = KindTool, x.Tool
		if s := x.State; s != nil {
			p.Status, p.Title, p.Input, p.Error = s.Status, s.Title, s.Input, s.Error
			if len(s.Output) > 0 {
				var str string
				if json.Unmarshal(s.Output, &str) == nil {
					p.Output = str
				} else {
					p.Output = string(s.Output)
				}
			}
			if s.Time.End > 0 && s.Time.Start > 0 {
				p.DurationMs = s.Time.End - s.Time.Start
			}
		}
	case "patch":
		p.Kind, p.Files = KindPatch, x.Files
	case "step-start":
		return p, false
	case "step-finish":
		p.Kind, p.Text, p.Cost = KindStep, x.Reason, x.Cost
		if t := x.Tokens; t != nil {
			p.Usage = &Usage{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write}
		}
	default:
		p.Kind, p.Raw = KindOther, rawOrString(data)
		p.Tool = x.Type
	}
	return p, true
}

// Delta returns messages/parts updated at or after the cursor (a unix ms
// timestamp). Re-sent rows are harmless: the client upserts by id.
func (o *OpenCode) Delta(id, cursor string) (*Delta, error) {
	db, err := o.handle()
	if err != nil {
		return nil, err
	}
	since, _ := strconv.ParseInt(cursor, 10, 64)
	cur := since
	d := &Delta{Messages: []Message{}}
	idx := map[string]int{}

	rows, err := db.Query(`SELECT id, time_created, time_updated, data FROM message
WHERE session_id = ? AND time_updated >= ? ORDER BY time_created, id`, id, since)
	if err != nil {
		o.setErr(err)
		return nil, err
	}
	for rows.Next() {
		var mid, data string
		var created, updated int64
		if err := rows.Scan(&mid, &created, &updated, &data); err != nil {
			rows.Close()
			return nil, err
		}
		cur = max(cur, updated)
		idx[mid] = len(d.Messages)
		d.Messages = append(d.Messages, convertOCMessage(mid, created, data))
	}
	rows.Close()

	rows, err = db.Query(`SELECT id, message_id, time_updated, data FROM part
WHERE session_id = ? AND time_updated >= ? ORDER BY id`, id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, mid, data string
		var updated int64
		if err := rows.Scan(&pid, &mid, &updated, &data); err != nil {
			return nil, err
		}
		cur = max(cur, updated)
		part, ok := convertOCPart(pid, mid, data)
		if !ok {
			continue
		}
		i, seen := idx[mid]
		if !seen { // parts-only update for an already-sent message
			i = len(d.Messages)
			idx[mid] = i
			d.Messages = append(d.Messages, Message{ID: mid})
		}
		d.Messages[i].Parts = append(d.Messages[i].Parts, part)
	}
	d.Cursor = strconv.FormatInt(cur, 10)
	return d, rows.Err()
}

// ---- context window lookup ---------------------------------------------------
// opencode does not store model limits in the DB, but caches the models.dev
// catalog at <cache>/opencode/models.json.

var (
	mcMu    sync.Mutex
	mcPath  string
	mcMTime time.Time
	mcData  map[string]map[string]int64 // provider -> model -> context window
)

func modelsCachePath() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "opencode", "models.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "opencode", "models.json")
}

// contextWindow returns 0 when the model is unknown.
func contextWindow(provider, model string) int64 {
	mcMu.Lock()
	defer mcMu.Unlock()
	p := modelsCachePath()
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	if mcData == nil || mcPath != p || !st.ModTime().Equal(mcMTime) {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0
		}
		var raw map[string]struct {
			Models map[string]struct {
				Limit struct {
					Context int64 `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		}
		if json.Unmarshal(b, &raw) != nil {
			return 0
		}
		mcData = map[string]map[string]int64{}
		for pid, pv := range raw {
			mcData[pid] = map[string]int64{}
			for mid, mv := range pv.Models {
				mcData[pid][mid] = mv.Limit.Context
			}
		}
		mcPath, mcMTime = p, st.ModTime()
	}
	return mcData[provider][model]
}
