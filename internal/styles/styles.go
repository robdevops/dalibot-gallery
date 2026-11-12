// Package styles maps the bot's style keys to the labels shown in the gallery.
package styles

import (
	"strings"
	"unicode"
)

// Base is the key of the image with no style.
const Base = "_"

// Style is one entry of the ordered label table.
type Style struct {
	Key   string
	Label string
}

// table is in the bot's own order (lib/styles.py), with the unstyled image first.
var table = []Style{
	{Base, "Original"},
	{"monet", "Monet"},
	{"dali", "Dalí"},
	{"picasso", "Picasso"},
	{"8bit", "8-bit"},
	{"ega", "EGA"},
	{"photo", "Photo"},
	{"watercolour", "Watercolour"},
	{"handdrawn", "Hand drawn"},
	{"sketch", "Sketch"},
	{"lithograph", "Lithograph"},
	{"stamp", "Rubber stamp"},
	{"tattoo", "Tattoo"},
	{"stencil", "Stencil"},
	{"anime", "Anime"},
	{"vangogh", "Van Gogh"},
	{"oilpainting", "Oil painting"},
	{"cartoon", "Cartoon"},
	{"comicbook", "Comic book"},
	{"render3d", "3D render"},
	{"popart", "Pop art"},
	{"cyberpunk", "Cyberpunk"},
	{"ukiyoe", "Ukiyo-e"},
	{"bw", "Black & white"},
}

var index = func() map[string]int {
	m := make(map[string]int, len(table))
	for i, s := range table {
		m[s.Key] = i
	}
	return m
}()

// All returns a copy of the label table in display order.
func All() []Style {
	return append([]Style(nil), table...)
}

// Known reports whether key is in the table.
func Known(key string) bool {
	_, ok := index[key]
	return ok
}

// Rank is the key's position in the table. Unknown keys all get len(table),
// so they sort after every known key; order those by key.
func Rank(key string) int {
	if i, ok := index[key]; ok {
		return i
	}
	return len(table)
}

// Label returns the label for key. Unknown or removed keys get the key itself,
// title-cased, with '_' and '-' shown as spaces.
func Label(key string) string {
	if i, ok := index[key]; ok {
		return table[i].Label
	}
	return titleCase(key)
}

// Less orders style keys: table order first, then unknown keys alphabetically.
func Less(a, b string) bool {
	ra, rb := Rank(a), Rank(b)
	if ra != rb {
		return ra < rb
	}
	return a < b
}

func titleCase(key string) string {
	words := strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) })
	if len(words) == 0 {
		return key
	}
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
