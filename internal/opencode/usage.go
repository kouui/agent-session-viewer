package opencode

import (
	"agent-viewer/internal/model"
	"encoding/json"
	"sort"
	"sync"
)

// Hourly usage for opencode, built incrementally from assistant messages:
// each refresh re-reads only messages at/after a per-session cursor, so a
// large database is scanned once and then only touched where it changed.

type ocRec struct {
	model   string
	created int64
	u       model.Usage
	cost    float64
	done    bool // message finished: tokens/cost are final
}

type ocSessUsage struct {
	cursor int64
	msgs   map[string]*ocRec
}

type ocUsageCache struct {
	mu       sync.Mutex
	sessions map[string]*ocSessUsage
	fp       string
	rows     []model.UsageRow
}

func (o *OpenCode) Usage() ([]model.UsageRow, error) {
	c := &o.usage
	c.mu.Lock()
	defer c.mu.Unlock()
	fp := o.Fingerprint()
	if c.rows != nil && fp == c.fp {
		return c.rows, nil
	}
	db, err := o.handle()
	if err != nil {
		return nil, err
	}
	if c.sessions == nil {
		c.sessions = map[string]*ocSessUsage{}
	}
	idRows, err := db.Query(`SELECT id FROM session`)
	if err != nil {
		o.setErr(err)
		return nil, err
	}
	var ids []string
	for idRows.Next() {
		var id string
		if idRows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	idRows.Close()

	for _, id := range ids {
		su := c.sessions[id]
		if su == nil {
			su = &ocSessUsage{msgs: map[string]*ocRec{}}
			c.sessions[id] = su
		}
		rows, err := db.Query(`SELECT id, time_created, data FROM message
WHERE session_id = ? AND time_created >= ? AND json_extract(data,'$.role') = 'assistant'`, id, su.cursor)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var mid, data string
			var created int64
			if rows.Scan(&mid, &created, &data) != nil {
				continue
			}
			var x struct {
				ModelID    string  `json:"modelID"`
				ProviderID string  `json:"providerID"`
				Cost       float64 `json:"cost"`
				Tokens     struct {
					Input     int64 `json:"input"`
					Output    int64 `json:"output"`
					Reasoning int64 `json:"reasoning"`
					Cache     struct {
						Read  int64 `json:"read"`
						Write int64 `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
				Time struct {
					Completed int64 `json:"completed"`
				} `json:"time"`
			}
			if json.Unmarshal([]byte(data), &x) != nil {
				continue
			}
			su.msgs[mid] = &ocRec{model: ocModelName(x.ProviderID, x.ModelID), created: created, cost: x.Cost, done: x.Time.Completed > 0,
				u: model.Usage{Input: x.Tokens.Input, Output: x.Tokens.Output, Reasoning: x.Tokens.Reasoning,
					CacheRead: x.Tokens.Cache.Read, CacheWrite: x.Tokens.Cache.Write}}
		}
		rows.Close()
		// next time restart from the oldest unfinished message, else the newest one
		var newest, oldestOpen int64
		for _, r := range su.msgs {
			newest = max(newest, r.created)
			if !r.done && (oldestOpen == 0 || r.created < oldestOpen) {
				oldestOpen = r.created
			}
		}
		su.cursor = newest
		if oldestOpen != 0 {
			su.cursor = oldestOpen
		}
	}

	type key struct {
		sid, model string
		hour       int64
	}
	agg := map[key]*model.UsageRow{}
	for sid, su := range c.sessions {
		for _, r := range su.msgs {
			if r.cost == 0 && r.u == (model.Usage{}) {
				continue
			}
			k := key{sid, r.model, r.created / 3600000 * 3600000}
			row := agg[k]
			if row == nil {
				zero := 0.0
				row = &model.UsageRow{Session: sid, Model: r.model, Hour: k.hour, Cost: &zero}
				agg[k] = row
			}
			row.Usage.Input += r.u.Input
			row.Usage.Output += r.u.Output
			row.Usage.Reasoning += r.u.Reasoning
			row.Usage.CacheRead += r.u.CacheRead
			row.Usage.CacheWrite += r.u.CacheWrite
			*row.Cost += r.cost
		}
	}
	out := make([]model.UsageRow, 0, len(agg))
	for _, r := range agg {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	c.rows, c.fp = out, fp
	return out, nil
}
