// Package web holds the gallery's static front end: two html/template pages
// (index.html, login.html) and the script and stylesheet they reference.
// There is no build step; the files are served as they are.
package web

import "embed"

// Files contains index.html, login.html, app.js and app.css at its root.
//
//go:embed index.html login.html app.js app.css
var Files embed.FS
