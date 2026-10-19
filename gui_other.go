//go:build !windows

package main

import (
	"fmt"
	"os"
)

func initLogging() {}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// runGUI is only implemented for the Windows build (embedded WebView2).
func runGUI(srv *Server, addr string, explicit bool) {
	fatal("-gui is only supported in the Windows build; open the printed URL in a browser instead")
}
