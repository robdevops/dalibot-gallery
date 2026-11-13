// Package web holds the gallery's static front end: three html/template pages
// (index.html, login.html and, for admins, hidden.html) and the scripts and
// stylesheets they reference. There is no build step; the files are served
// as they are.
//
// app.js and app.css are what every visitor gets. admin.js and admin.css
// hold everything for hiding and restoring images; the server sends them to
// admins only, and only an admin's page refers to them.
package web

import "embed"

// Files contains index.html, login.html, hidden.html, app.js, app.css,
// admin.js, admin.css and favicon.png at its root.
//
//go:embed index.html login.html hidden.html app.js app.css admin.js admin.css favicon.png
var Files embed.FS
