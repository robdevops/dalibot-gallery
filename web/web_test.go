package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
)

type indexTile struct{ Key, Alt, Src240, Src480, Full string }

func TestFilesPresent(t *testing.T) {
	for _, name := range []string{"index.html", "login.html", "hidden.html", "app.js", "app.css", "admin.js", "admin.css", "favicon.png"} {
		if b, err := fs.ReadFile(Files, name); err != nil || len(b) == 0 {
			t.Errorf("%s: missing or empty (%v)", name, err)
		}
	}
}

// The script and the stylesheet together must stay under 20 KB as shipped.
// (The plan's limit was 10 KB, for a grid and a plain viewer. The viewer has
// since gained swipe, touchpad, zoom and pan handling, which did not fit.)
func TestSizeBudget(t *testing.T) {
	js, _ := fs.ReadFile(Files, "app.js")
	css, _ := fs.ReadFile(Files, "app.css")
	if n := len(js) + len(css); n >= 20_000 {
		t.Errorf("app.js + app.css = %d bytes, want < 20000", n)
	}
}

// The pages must not carry anything the Content-Security-Policy forbids.
func TestNoInlineCode(t *testing.T) {
	for _, name := range []string{"index.html", "login.html", "hidden.html"} {
		b, _ := fs.ReadFile(Files, name)
		s := strings.ToLower(string(b))
		// The one address outside the gallery is the link-preview picture. Only preview
		// fetchers read that tag; the browser never loads it, so the policy is not involved.
		// The admin's page of hidden images has no preview.
		const preview = `<meta property="og:image" content="https://raw.githubusercontent.com/robdevops/dalibot-gallery/main/docs/og-image.png">`
		want := 1
		if name == "hidden.html" {
			want = 0
		}
		if strings.Count(s, preview) != want {
			t.Errorf("%s: want the link-preview picture %d times", name, want)
		}
		s = strings.Replace(s, preview, "", 1)
		for _, bad := range []string{"<style", " style=", " onclick=", " onload=", " onerror=", "javascript:", "data:", "http://", "https://"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		// The scripts: /app.js for everyone on the gallery page, /admin.js
		// for admins (TestAdminOnlyParts), and nothing on the login page.
		scripts := map[string][]string{
			"index.html":  {`<script src="/app.js" defer></script>`, `<script src="/admin.js" defer></script>`},
			"login.html":  nil,
			"hidden.html": {`<script src="/admin.js" defer></script>`},
		}[name]
		for _, tag := range scripts {
			if strings.Count(s, tag) != 1 {
				t.Errorf("%s: want %s exactly once", name, tag)
			}
		}
		if strings.Count(s, "<script") != len(scripts) {
			t.Errorf("%s has %d scripts, want %d", name, strings.Count(s, "<script"), len(scripts))
		}
	}
}

// Everything for hiding images is outside what an ordinary visitor is sent:
// the script and stylesheet of every visitor do not mention it, and the page
// refers to the admin's files only for an admin who can hide.
func TestAdminOnlyParts(t *testing.T) {
	for _, name := range []string{"app.js", "app.css", "login.html"} {
		b, _ := fs.ReadFile(Files, name)
		for _, not := range []string{"admin", "restore", "undo", "Hide", "Restore", "Undo", "#adm"} {
			// "admin" appears on the login page only as the label of the admin's sign-in.
			if name == "login.html" && not == "admin" {
				continue
			}
			if strings.Contains(string(b), not) {
				t.Errorf("%s contains %q", name, not)
			}
		}
	}
	tpl := template.Must(template.ParseFS(Files, "index.html"))
	type data struct {
		V                                string
		Tiles                            []indexTile
		Next                             string
		Admin, AdminLogin, SignOut, Hide bool
	}
	parts := []string{`<link rel="stylesheet" href="/admin.css">`, `<script src="/admin.js" defer></script>`, `<a href="/admin/hidden">Hidden images</a>`}
	for _, d := range []data{
		{}, {SignOut: true}, {AdminLogin: true, SignOut: true},
		{Admin: true, SignOut: true}, // an admin of a gallery where nothing can be hidden
		{Admin: true, SignOut: true, Hide: true}, {Admin: true, Hide: true},
	} {
		var out bytes.Buffer
		if err := tpl.Execute(&out, d); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		for _, part := range parts {
			if strings.Contains(s, part) != d.Hide {
				t.Errorf("index.html with %+v: %q present = %v", d, part, !d.Hide)
			}
		}
		if !d.Hide {
			for _, not := range []string{"admin.", "/admin", "Hidden", "Hide", "Restore", "Undo"} {
				if strings.Contains(s, not) {
					t.Errorf("index.html with %+v contains %q", d, not)
				}
			}
		}
		// The admin's script comes after app.js, whose page it works on.
		if d.Hide && strings.Index(s, `src="/admin.js"`) < strings.Index(s, `src="/app.js"`) {
			t.Error("admin.js is loaded before app.js")
		}
	}
}

func TestHiddenTemplate(t *testing.T) {
	tpl := template.Must(template.ParseFS(Files, "hidden.html"))
	type tile struct{ Key, Alt, Text, Src240, Src480, Full string }
	type data struct {
		Tiles   []tile
		Gone    int
		SignOut bool
	}
	var out bytes.Buffer
	d := data{Tiles: []tile{{
		Key: "RQ2AvWdtw2pN5Bhv0Y3CdX", Alt: `a "fox" <b>`, Text: "/dream · Monet",
		Src240: "/img/RQ2AvWdtw2pN5Bhv0Y3CdX-240.webp?v=1", Src480: "/img/RQ2AvWdtw2pN5Bhv0Y3CdX-480.webp?v=1",
		Full: "/img/RQ2AvWdtw2pN5Bhv0Y3CdX.jpg",
	}}, Gone: 3, SignOut: true}
	if err := tpl.Execute(&out, d); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		`<html lang="en" data-admin="1">`,
		`<p id="none" hidden>No hidden images to show.</p>`,
		`<a href="/img/RQ2AvWdtw2pN5Bhv0Y3CdX.jpg"><img src="/img/RQ2AvWdtw2pN5Bhv0Y3CdX-240.webp?v=1"`,
		`alt="a &#34;fox&#34; &lt;b&gt;"`,
		`<p>a &#34;fox&#34; &lt;b&gt;</p><p class="cmd">/dream · Monet</p>`,
		`<button type="button" data-restore="RQ2AvWdtw2pN5Bhv0Y3CdX">Restore</button>`,
		`no longer in the bot's records: 3.`,
		`<form method="post" action="/logout">`,
		`<a href="/">Back to the gallery</a>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hidden.html output lacks %q", want)
		}
	}
	out.Reset()
	if err := tpl.Execute(&out, data{}); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	if !strings.Contains(s, `<p id="none">No hidden images to show.</p>`) {
		t.Error("an empty list does not say so")
	}
	for _, not := range []string{"data-restore", "no longer in the bot's records", "/logout"} {
		if strings.Contains(s, not) {
			t.Errorf("hidden.html for an empty list contains %q", not)
		}
	}
}

func TestIndexTemplate(t *testing.T) {
	tpl := template.Must(template.ParseFS(Files, "index.html"))
	data := struct {
		V                          string
		Tiles                      []indexTile
		Next                       string
		Admin, AdminLogin, SignOut bool
		Hide                       bool
	}{
		V: "1",
		Tiles: []indexTile{{
			Key: "RQ2AvWdtw2pN5Bhv0Y3CdX", Alt: `a "fox" <b>`,
			Src240: "/img/RQ2AvWdtw2pN5Bhv0Y3CdX-240.webp?v=1",
			Src480: "/img/RQ2AvWdtw2pN5Bhv0Y3CdX-480.webp?v=1",
			Full:   "/img/RQ2AvWdtw2pN5Bhv0Y3CdX.jpg",
		}},
		Next: "abc_DEF-1",
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		`<ul class="grid" id="grid" aria-label="Gallery"></ul>`,
		`<li class="tile"><a href="/img/RQ2AvWdtw2pN5Bhv0Y3CdX.jpg"><img src="/img/RQ2AvWdtw2pN5Bhv0Y3CdX-240.webp?v=1" srcset="/img/RQ2AvWdtw2pN5Bhv0Y3CdX-240.webp?v=1 240w, /img/RQ2AvWdtw2pN5Bhv0Y3CdX-480.webp?v=1 480w"`,
		`alt="a &#34;fox&#34; &lt;b&gt;"`,
		`<a href="/?cursor=abc_DEF-1">Load more</a>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("index.html output lacks %q", want)
		}
	}
	// An ordinary visitor's page says nothing about admins or sessions.
	for _, not := range []string{"data-admin", "/login", "/logout", "<footer", "Admin", "Sign out"} {
		if strings.Contains(s, not) {
			t.Errorf("index.html for an ordinary visitor contains %q", not)
		}
	}
	data.Next, data.Tiles = "", nil
	out.Reset()
	if err := tpl.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Load more") {
		t.Error("Load more shown on the last page")
	}

	// The admin flag, the link to the admin sign-in and the sign-out form.
	const (
		flag    = `<html lang="en" data-admin="1">`
		link    = `<a href="/login">Admin</a>`
		signOut = `<form method="post" action="/logout"><button type="submit">Sign out</button></form>`
	)
	for _, c := range []struct {
		admin, adminLogin, signOut bool
	}{{true, false, true}, {false, true, false}, {false, true, true}, {false, false, true}, {true, false, false}} {
		data.Admin, data.AdminLogin, data.SignOut = c.admin, c.adminLogin, c.signOut
		out.Reset()
		if err := tpl.Execute(&out, data); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		for want, on := range map[string]bool{flag: c.admin, link: c.adminLogin, signOut: c.signOut, `<footer class="foot">`: c.adminLogin || c.signOut} {
			if strings.Contains(s, want) != on {
				t.Errorf("index.html with %+v: %q present = %v", c, want, !on)
			}
		}
	}
}

func TestLoginTemplate(t *testing.T) {
	tpl := template.Must(template.ParseFS(Files, "login.html"))
	for _, c := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		failed, adminOnly := c[0], c[1]
		var out bytes.Buffer
		if err := tpl.Execute(&out, struct{ Error, AdminOnly bool }{failed, adminOnly}); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		// In a public gallery the form is the admin's sign-in and says so.
		label, back := `<label for="token">Access token</label>`, `<a href="/">Back to the gallery</a>`
		if adminOnly {
			label = `<label for="token">Admin token</label>`
		}
		if !strings.Contains(s, label) || strings.Contains(s, back) != adminOnly {
			t.Errorf("login.html (AdminOnly=%v): wrong label or link back", adminOnly)
		}
		for _, want := range []string{`<form method="post" action="/login">`, `name="token" autocomplete="current-password" required autofocus`, `<button type="submit">`} {
			if !strings.Contains(s, want) {
				t.Errorf("login.html (Error=%v) lacks %q", failed, want)
			}
		}
		if got := strings.Contains(s, `role="alert"`); got != failed {
			t.Errorf("login.html (Error=%v): error message shown = %v", failed, got)
		}
	}
}
