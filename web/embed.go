package web

import "embed"

//go:embed index.html app.js style.css vim
var FS embed.FS
