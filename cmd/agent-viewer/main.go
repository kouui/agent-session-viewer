// agent-viewer: read-only realtime web viewer for coding-agent session history (opencode, codex).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-viewer/internal/detect"
	"agent-viewer/internal/gui"
	"agent-viewer/internal/server"
)

// defaultGUI is set to "1" at link time for the no-console Windows build
// (-ldflags "-H=windowsgui -X main.defaultGUI=1").
var defaultGUI string

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:9191", "listen address")
	poll := flag.Duration("poll", time.Second, "file change poll interval")
	var dirs multiFlag
	flag.Var(&dirs, "dir", "extra history path: opencode data dir / opencode.db / codex home (repeatable)")
	embedded := flag.Bool("gui", defaultGUI == "1", "show the UI in an embedded native window (Windows build)")
	flag.Parse()
	gui.Init(*embedded, defaultGUI == "1")
	addrSet := false
	flag.Visit(func(f *flag.Flag) { addrSet = addrSet || f.Name == "addr" })

	sources := detect.Sources(dirs)
	if len(sources) == 0 {
		gui.Fatal("no opencode or codex history found (use -dir to point at one)")
	}
	for _, s := range sources {
		fmt.Printf("source %-20s %s\n", s.Label, s.B.Path())
	}

	srv := server.New(sources)
	go srv.Watch(*poll)
	if *embedded {
		gui.Run(srv, *addr, addrSet)
		return
	}
	fmt.Printf("listening on http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
