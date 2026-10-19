//go:build !windows

// Package gui hosts the embedded native window (Windows only, WebView2).
package gui

import (
	"fmt"
	"net/http"
	"os"
)

// Init is called once at startup; embedded: the UI runs in the native window; logToFile: no console, log to a file.
func Init(embedded, logToFile bool) {}

// Fatal prints msg and exits.
func Fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// Run is only implemented for the Windows build (embedded WebView2).
func Run(h interface{ Handler() http.Handler }, addr string, explicit bool) {
	Fatal("-gui is only supported in the Windows build; open the printed URL in a browser instead")
}
