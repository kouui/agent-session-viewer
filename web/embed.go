// Package web holds the browser UI (static files) embedded into the binary.
package web

import "embed"

//go:embed index.html css js
var FS embed.FS
