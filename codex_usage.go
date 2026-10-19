package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"
)

// Hourly usage for codex: token_count events carry cumulative totals, so each
// event contributes the delta from the previous one, attributed to the event's
// hour and the model of the latest turn_context. Files are parsed once and then
// only their appended tail.

type cxUsageKey struct {
	hour  int64
	model string
}

type cxUsageState struct {
	off     int64
	prev    cxTotals
	model   string
	buckets map[cxUsageKey]*Usage
}

type cxUsageCache struct {
	mu    sync.Mutex
	state map[string]*cxUsageState // by file path
}

func (c *Codex) Usage() ([]UsageRow, error) {
	// snapshot the file index, then parse under a separate lock so the (slow)
	// first scan does not block session listing
	c.mu.Lock()
	c.scan()
	type fileRef struct{ path, id string }
	refs := make([]fileRef, 0, len(c.files))
	for p, f := range c.files {
		refs = append(refs, fileRef{p, f.id})
	}
	c.mu.Unlock()

	uc := &c.usage
	uc.mu.Lock()
	defer uc.mu.Unlock()
	if uc.state == nil {
		uc.state = map[string]*cxUsageState{}
	}
	var out []UsageRow
	for _, ref := range refs {
		st := uc.state[ref.path]
		if st == nil {
			st = &cxUsageState{buckets: map[cxUsageKey]*Usage{}}
			uc.state[ref.path] = st
		}
		cxUsageScan(ref.path, st)
		for k, u := range st.buckets {
			out = append(out, UsageRow{Session: ref.id, Model: k.model, Hour: k.hour, Usage: *u})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour < out[j].Hour })
	return out, nil
}

func cxUsageScan(path string, st *cxUsageState) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	if fi, err := fh.Stat(); err == nil {
		if fi.Size() < st.off { // replaced/truncated: start over
			*st = cxUsageState{buckets: map[cxUsageKey]*Usage{}}
		} else if fi.Size() == st.off {
			return
		}
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
		switch {
		case bytes.Contains(line, []byte(`"token_count"`)):
			tc, ok := parseTokenCount(line)
			if !ok || tc.Time == 0 {
				continue
			}
			d := tc.Total.sub(st.prev)
			st.prev = tc.Total
			u := d.usage()
			if u == (Usage{}) {
				continue
			}
			k := cxUsageKey{tc.Time / 3600000 * 3600000, st.model}
			b := st.buckets[k]
			if b == nil {
				b = &Usage{}
				st.buckets[k] = b
			}
			b.Input += u.Input
			b.Output += u.Output
			b.Reasoning += u.Reasoning
			b.CacheRead += u.CacheRead
			b.CacheWrite += u.CacheWrite
		case bytes.Contains(line, []byte(`"turn_context"`)):
			var l struct {
				Type    string `json:"type"`
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Type == "turn_context" && l.Payload.Model != "" {
				st.model = l.Payload.Model
			}
		}
	}
}
