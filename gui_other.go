//go:build !windows

package main

import "log"

// runGUI is only implemented for the Windows build (embedded WebView2).
func runGUI(srv *Server, addr string, explicit bool) {
	log.Fatal("-gui is only supported in the Windows build; open the printed URL in a browser instead")
}
