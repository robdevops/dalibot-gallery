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
	for _, name := range []string{"index.html", "login.html", "app.js", "app.css", "favicon.png"} {
		if b, err := fs.ReadFile(Files, name); err != nil || len(b) == 0 {
			t.Errorf("%s: missing or empty (%v)", name, err)
		}
	}
}

// The script and the stylesheet together must stay under 16 KB as shipped.
// (The plan's limit was 10 KB, for a grid and a plain viewer. The viewer has
// since gained touch and touchpad swipes, which did not fit.)
func TestSizeBudget(t *testing.T) {
	js, _ := fs.ReadFile(Files, "app.js")
	css, _ := fs.ReadFile(Files, "app.css")
	if n := len(js) + len(css); n >= 16_000 {
		t.Errorf("app.js + app.css = %d bytes, want < 16000", n)
	}
}

// The pages must not carry anything the Content-Security-Policy forbids.
func TestNoInlineCode(t *testing.T) {
	for _, name := range []string{"index.html", "login.html"} {
		b, _ := fs.ReadFile(Files, name)
		s := strings.ToLower(string(b))
		// The one address outside the gallery is the link-preview picture. Only preview
		// fetchers read that tag; the browser never loads it, so the policy is not involved.
		const preview = `<meta property="og:image" content="https://raw.githubusercontent.com/robdevops/dalibot-gallery/main/docs/og-image.png">`
		if strings.Count(s, preview) != 1 {
			t.Errorf("%s: want the link-preview picture exactly once", name)
		}
		s = strings.Replace(s, preview, "", 1)
		for _, bad := range []string{"<style", " style=", " onclick=", " onload=", " onerror=", "javascript:", "data:", "http://", "https://"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		if strings.Count(s, "<script") != strings.Count(s, `<script src="/app.js" defer></script>`) {
			t.Errorf("%s has a script other than /app.js", name)
		}
	}
}

func TestIndexTemplate(t *testing.T) {
	tpl := template.Must(template.ParseFS(Files, "index.html"))
	data := struct {
		V     string
		Tiles []indexTile
		Next  string
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
	data.Next, data.Tiles = "", nil
	out.Reset()
	if err := tpl.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Load more") {
		t.Error("Load more shown on the last page")
	}
}

func TestLoginTemplate(t *testing.T) {
	tpl := template.Must(template.ParseFS(Files, "login.html"))
	for _, failed := range []bool{false, true} {
		var out bytes.Buffer
		if err := tpl.Execute(&out, struct{ Error bool }{failed}); err != nil {
			t.Fatal(err)
		}
		s := out.String()
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
