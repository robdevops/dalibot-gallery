package styles

import (
	"encoding/json"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// A Group is what a gallery can be narrowed to with ?style=<key>: each of the bot's style buttons,
// and a few kinds of picture people ask for in their prompts. An image belongs to a group when it was
// made with one of the group's buttons (its style key is in Keys) or when its prompt or caption names
// one of the group's terms. That needs nothing stored, so it holds for every image the bot has kept.
type Group struct {
	Key   string
	Label string
	Keys  []string   // style keys whose images belong
	Terms [][]string // each a run of words (see Words); any one found in the text makes an image belong
}

// groups: the bot's buttons in its order (lib/styles.py: each button's label and the words of the phrase
// it adds to the prompt), then the kinds that are not buttons. Terms are written as text and turned into
// words by Words, so "8-bit", "8 bit" and "8bit" are told apart only where the text tells them apart.
var groups = func() []Group {
	defs := []struct{ key, label, keys, terms string }{
		{"monet", "Monet", "monet", "Monet"},
		{"dali", "Dalí", "dali", "Dalí"},
		{"picasso", "Picasso", "picasso", "Picasso"},
		{"8bit", "8-bit", "8bit", "8-bit|8bit"},
		{"ega", "EGA", "ega", "EGA"},
		{"photo", "Photo", "photo", "photo|photos|photograph|photographs|photography|photographic"},
		{"watercolour", "Watercolour", "watercolour", "watercolour|watercolours|watercolor|watercolors"},
		{"handdrawn", "Hand drawn", "handdrawn", "hand drawn|handdrawn"},
		{"sketch", "Sketch", "sketch", "sketch|sketches|sketched"},
		{"lithograph", "Lithograph", "lithograph", "lithograph|lithographs|lithography|lithographic"},
		{"stamp", "Rubber stamp", "stamp", "rubber stamp"},
		{"tattoo", "Tattoo", "tattoo", "tattoo|tattoos"},
		{"stencil", "Stencil", "stencil", "stencil|stencilled|stenciled"},
		{"anime", "Anime", "anime", "anime"},
		{"vangogh", "Van Gogh", "vangogh", "Van Gogh"},
		{"oilpainting", "Oil painting", "oilpainting", "oil painting|oil paintings|oil on canvas"},
		{"cartoon", "Cartoon", "cartoon", "cartoon|cartoons"},
		{"comicbook", "Comic book", "comicbook", "comic book|comic books|comic strip|comic strips"},
		{"render3d", "3D render", "render3d", "3D render|3D renders|3D rendering|3D rendered|3D model|CGI"},
		{"popart", "Pop art", "popart", "pop art"},
		{"cyberpunk", "Cyberpunk", "cyberpunk", "cyberpunk"},
		{"ukiyoe", "Ukiyo-e", "ukiyoe", "ukiyo-e|ukiyoe|woodblock|woodcut"},
		{"bw", "Black & white", "bw", "in black & white|black & white photo|black & white photograph|black & white film|monochrome|greyscale|grayscale"},
		// Not buttons: kinds of picture people name. Pixel art takes in the 8-bit and EGA buttons, and
		// Impressionist the Monet button, the impressionist the bot can be asked for by name.
		{"pixelart", "Pixel art", "8bit ega", "pixel art|pixel|pixels|pixelated|pixellated"},
		{"impressionist", "Impressionist", "monet", "impressionist|impressionists|impressionism|impressionistic|Monet"},
	}
	out := make([]Group, 0, len(defs))
	for _, d := range defs {
		g := Group{Key: d.key, Label: d.label, Keys: strings.Fields(d.keys)}
		for _, t := range strings.Split(d.terms, "|") {
			g.Terms = append(g.Terms, Words(t))
		}
		out = append(out, g)
	}
	return out
}()

var groupIndex = func() map[string]int {
	m := make(map[string]int, len(groups))
	for i, g := range groups {
		m[g.Key] = i
	}
	return m
}()

// GroupOf returns the group with this key.
func GroupOf(key string) (Group, bool) {
	if i, ok := groupIndex[key]; ok {
		return groups[i], true
	}
	return Group{}, false
}

// In reports whether an image with this style key and these texts (its prompt and caption) belongs in
// the listing narrowed to key. A key that is no group (the original images' "_", or a style the bot added
// since) takes in the images of that style key only.
func In(key, style string, texts ...string) bool {
	if style == key {
		return true
	}
	g, ok := GroupOf(key)
	if !ok {
		return false
	}
	for _, k := range g.Keys {
		if style == k {
			return true
		}
	}
	for _, t := range texts {
		if g.Names(Words(t)) {
			return true
		}
	}
	return false
}

// Names reports whether the words hold one of the group's terms, word for word.
func (g Group) Names(words []string) bool {
	for _, term := range g.Terms {
		for i := 0; i+len(term) <= len(words); i++ {
			if equal(words[i:i+len(term)], term) {
				return true
			}
		}
	}
	return false
}

func equal(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Words splits text into words as web/app.js does, so that a caption's links and the galleries they
// lead to agree: runs of letters and numbers (a mark or anything else ends one), "&" as the word "and",
// each folded: compatibility-decomposed (NFKD), marks dropped, lower case. So "Dalí" is "dali", "8-bit"
// is "8 bit", "black & white" is "black and white", "ＥＧＡ" is "ega", and "photo" is not found in
// "photorealistic".
func Words(text string) []string {
	var out []string
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out = append(out, fold(text[start:end]))
			start = -1
		}
	}
	for i, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if start < 0 {
				start = i
			}
		case r == '&':
			flush(i)
			out = append(out, "and")
		default:
			flush(i)
		}
	}
	flush(len(text))
	return out
}

func fold(w string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(w) {
		if !unicode.Is(unicode.M, r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// GroupsJSON is the groups as the page uses them, to link the words of a caption:
// [[key, label, [[word, ...], ...]], ...]. One list, here, for the server and the page.
func GroupsJSON() string {
	out := make([][3]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, [3]any{g.Key, g.Label, g.Terms})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
