package web

import "embed"

//go:embed index.html app.js style.css vim lib manifest.json sw.js icon.svg
var FS embed.FS
