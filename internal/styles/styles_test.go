package styles

import (
	"sort"
	"testing"
)

func TestTableMatchesBot(t *testing.T) {
	want := []Style{
		{"_", "Original"}, {"monet", "Monet"}, {"dali", "Dalí"}, {"picasso", "Picasso"}, {"8bit", "8-bit"},
		{"ega", "EGA"}, {"photo", "Photo"}, {"watercolour", "Watercolour"}, {"handdrawn", "Hand drawn"},
		{"sketch", "Sketch"}, {"lithograph", "Lithograph"}, {"stamp", "Rubber stamp"}, {"tattoo", "Tattoo"},
		{"stencil", "Stencil"}, {"anime", "Anime"}, {"vangogh", "Van Gogh"}, {"oilpainting", "Oil painting"},
		{"cartoon", "Cartoon"}, {"comicbook", "Comic book"}, {"render3d", "3D render"}, {"popart", "Pop art"},
		{"cyberpunk", "Cyberpunk"}, {"ukiyoe", "Ukiyo-e"}, {"bw", "Black & white"},
	}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("table has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
		if Label(want[i].Key) != want[i].Label {
			t.Errorf("Label(%q) = %q, want %q", want[i].Key, Label(want[i].Key), want[i].Label)
		}
		if Rank(want[i].Key) != i || !Known(want[i].Key) {
			t.Errorf("Rank/Known wrong for %q", want[i].Key)
		}
	}
}

func TestAllIsACopy(t *testing.T) {
	a := All()
	a[0].Label = "changed"
	if Label(Base) != "Original" {
		t.Fatal("All() exposed the table")
	}
}

func TestUnknownLabels(t *testing.T) {
	for key, want := range map[string]string{
		"vaporwave":   "Vaporwave",
		"neon_noir":   "Neon Noir",
		"art-deco":    "Art Deco",
		"émail":       "Émail",
		"":            "",
		"__":          "__",
		"3d":          "3d",
		"Monet":       "Monet", // keys are case-sensitive: this is not the table's "monet"
		"low_poly_3d": "Low Poly 3d",
	} {
		if got := Label(key); got != want {
			t.Errorf("Label(%q) = %q, want %q", key, got, want)
		}
	}
	if Known("vaporwave") || Rank("vaporwave") != len(All()) {
		t.Error("unknown key should rank after the table")
	}
}

func TestLess(t *testing.T) {
	keys := []string{"zebra", "bw", "apple", "monet", "_", "8bit"}
	sort.Slice(keys, func(i, j int) bool { return Less(keys[i], keys[j]) })
	want := []string{"_", "monet", "8bit", "bw", "apple", "zebra"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("order = %v, want %v", keys, want)
		}
	}
}
