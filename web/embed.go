package web

import "embed"

//go:embed index.html app.js style.css vim lib
var FS embed.FS
