package detect

import (
	"agent-viewer/internal/codex"
	"agent-viewer/internal/model"
	"agent-viewer/internal/opencode"
	"os"
	"path/filepath"
	"runtime"
)

// hostName labels the machine this binary runs on.
func hostName() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	return "linux"
}

// Sources looks only at the current user's own home (the WSL build sees
// its ~, the Windows build sees %USERPROFILE%); run the matching build on each
// side. extra are -dir paths: an opencode.db file, an opencode data dir, or a
// codex home dir.
func Sources(extra []string) []*model.Source {
	home, _ := os.UserHomeDir()
	var cands []string
	cands = append(cands, extra...)
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		cands = append(cands, filepath.Join(xdg, "opencode"))
	}
	if home != "" {
		cands = append(cands, filepath.Join(home, ".local", "share", "opencode"))
	}
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		cands = append(cands, ch)
	}
	if home != "" {
		cands = append(cands, filepath.Join(home, ".codex"))
	}

	seen := map[string]bool{}
	var out []*model.Source
	for _, c := range cands {
		b := sniff(c)
		if b == nil {
			continue
		}
		key := b.Kind() + "|" + filepath.Clean(b.Path())
		if seen[key] {
			continue
		}
		seen[key] = true
		label := b.Kind() + "@" + hostName()
		for _, o := range out {
			if o.Label == label {
				label += "#" + filepath.Base(filepath.Dir(b.Path()))
			}
		}
		out = append(out, &model.Source{Label: label, B: b})
	}
	return out
}

// sniff identifies which backend a path belongs to, or returns nil.
func sniff(p string) model.Backend {
	st, err := os.Stat(p)
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		if filepath.Base(p) == "opencode.db" {
			return opencode.New(p)
		}
		return nil
	}
	if s, err := os.Stat(filepath.Join(p, "opencode.db")); err == nil && !s.IsDir() {
		return opencode.New(filepath.Join(p, "opencode.db"))
	}
	for _, sub := range []string{"sessions", "archived_sessions"} {
		if s, err := os.Stat(filepath.Join(p, sub)); err == nil && s.IsDir() {
			return codex.New(p)
		}
	}
	return nil
}
