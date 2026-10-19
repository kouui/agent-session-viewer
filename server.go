package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	sources []*Source
	byLabel map[string]*Source

	mu   sync.Mutex
	subs map[chan string]struct{}
}

func NewServer(sources []*Source) *Server {
	s := &Server{sources: sources, byLabel: map[string]*Source{}, subs: map[chan string]struct{}{}}
	for _, src := range sources {
		s.byLabel[src.Label] = src
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServerFS(sub))
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/events", s.handleEvents)
	return localOnly(mux)
}

// localOnly rejects requests whose Host is not a loopback name (DNS rebinding guard).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

type ProjectOut struct {
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	MTime    int64     `json:"mtime"`
	Sessions []Session `json:"sessions"`
}

type SourceOut struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Stale bool   `json:"stale"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	var (
		projects = []*ProjectOut{}
		srcs     = []SourceOut{}
		idx      = map[string]*ProjectOut{}
	)
	for _, src := range s.sources {
		list, err := src.B.Sessions()
		stale, msg := src.B.Status()
		if err != nil && msg == "" {
			msg = err.Error()
		}
		srcs = append(srcs, SourceOut{Label: src.Label, Kind: src.B.Kind(), Path: src.B.Path(), Stale: stale, Error: msg})
		for _, ss := range list {
			ss.Source = src.Label
			p := idx[ss.ProjectKey]
			if p == nil {
				name := ss.ProjectName
				if name == "" {
					name = "(global)"
				}
				p = &ProjectOut{Key: ss.ProjectKey, Name: name, Path: ss.ProjectPath, Sessions: []Session{}}
				idx[ss.ProjectKey] = p
				projects = append(projects, p)
			}
			p.Sessions = append(p.Sessions, ss)
			p.MTime = max(p.MTime, ss.MTime)
		}
	}
	for _, p := range projects {
		sort.SliceStable(p.Sessions, func(i, j int) bool { return p.Sessions[i].MTime > p.Sessions[j].MTime })
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].MTime > projects[j].MTime })
	writeJSON(w, map[string]any{"sources": srcs, "projects": projects})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := s.byLabel[q.Get("src")]
	id := q.Get("id")
	if src == nil || id == "" {
		http.Error(w, "bad src/id", http.StatusBadRequest)
		return
	}
	d, err := src.B.Delta(id, q.Get("cursor"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (s *Server) broadcast(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- msg:
		default: // slow client: it will catch up on the next event
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan string, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			fmt.Fprintf(w, "event: change\ndata: %s\n\n", m)
			fl.Flush()
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

// watch polls file stats (no DB access) and broadcasts when a source changes.
func (s *Server) watch(every time.Duration) {
	last := map[string]string{}
	for _, src := range s.sources {
		last[src.Label] = src.B.Fingerprint()
	}
	for range time.Tick(every) {
		for _, src := range s.sources {
			if fp := src.B.Fingerprint(); fp != last[src.Label] {
				last[src.Label] = fp
				b, _ := json.Marshal(map[string]string{"src": src.Label})
				s.broadcast(string(b))
			}
		}
	}
}
