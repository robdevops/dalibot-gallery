// Package web holds the gallery's static front end: two html/template pages
// (index.html, login.html) and the script and stylesheet they reference.
// There is no build step; the files are served as they are.
package web

import "embed"

// Files contains index.html, login.html, app.js, app.css and favicon.png at its root.
//
//go:embed index.html login.html app.js app.css favicon.png
var Files embed.FS
