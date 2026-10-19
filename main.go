// agent-viewer: read-only realtime web viewer for coding-agent session history (opencode).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:9191", "listen address")
	poll := flag.Duration("poll", time.Second, "file change poll interval")
	var dirs multiFlag
	flag.Var(&dirs, "dir", "extra history path: opencode data dir / opencode.db / codex home (repeatable)")
	gui := flag.Bool("gui", false, "show the UI in an embedded native window (Windows build)")
	flag.Parse()
	addrSet := false
	flag.Visit(func(f *flag.Flag) { addrSet = addrSet || f.Name == "addr" })

	sources := detectSources(dirs)
	if len(sources) == 0 {
		log.Fatal("no opencode or codex history found (use -dir to point at one)")
	}
	for _, s := range sources {
		fmt.Printf("source %-20s %s\n", s.Label, s.B.Path())
	}

	srv := NewServer(sources)
	go srv.watch(*poll)
	if *gui {
		runGUI(srv, *addr, addrSet)
		return
	}
	fmt.Printf("listening on http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
