package styles

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestWords(t *testing.T) {
	for in, want := range map[string][]string{
		"Dalí's melting clocks":     {"dali", "s", "melting", "clocks"},
		"as 8-bit pixel art":        {"as", "8", "bit", "pixel", "art"},
		"in Black & White":          {"in", "black", "and", "white"},
		"UKIYO-E, woodblock":        {"ukiyo", "e", "woodblock"},
		"photorealistic, 3D-render": {"photorealistic", "3d", "render"},
		"crème brûlée à la Señora":  {"creme", "brulee", "a", "la", "senora"},
		"":                          nil,
		"  ,;  ":                    nil,
	} {
		if got := Words(in); !slices.Equal(got, want) {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIn(t *testing.T) {
	for _, c := range []struct {
		key, style, text string
		want             bool
	}{
		// By the button that made it.
		{"ukiyoe", "ukiyoe", "a cat", true},
		{"ukiyoe", "_", "a cat", false},
		// By the words of its caption or prompt, also for images made before buttons, or without one.
		{"ukiyoe", "_", "a cat as a Japanese ukiyo-e woodblock print", true},
		{"ukiyoe", "_", "Ukiyoe cat", true},
		{"watercolour", "_", "a fox in watercolor", true},
		{"dali", "_", "clocks in the style of Salvador Dali", true},
		{"dali", "_", "clocks à la Dalí", true},
		{"popart", "_", "Pop-Art portrait", true},
		{"popart", "_", "a pop song about art", false}, // the words, in order, next to each other
		{"bw", "_", "a street in black and white", true},
		{"bw", "_", "a black & white photo of a street", true},
		{"bw", "_", "a black and white cat", false}, // the colour of the subject, not the picture's
		{"render3d", "_", "a 3D render of a car", true},
		{"render3d", "_", "a vase from a 3D printer", false},
		{"comicbook", "_", "a comic scene", false},
		{"comicbook", "_", "as a comic book page", true},
		{"ega", "_", "a cat, ＥＧＡ palette", true}, // full-width letters fold as the page folds them
		// Whole words only.
		{"photo", "_", "a photograph of a dog", true},
		{"photo", "_", "a photorealistic dog", false},
		{"anime", "_", "animated film", false},
		{"sketch", "_", "sketchy alley", false},
		// The groups that are not buttons take in their buttons and their own words.
		{"pixelart", "8bit", "a castle", true},
		{"pixelart", "ega", "a castle", true},
		{"pixelart", "_", "a pixelated castle", true},
		{"pixelart", "_", "a castle as 8-bit pixel art", true},
		{"pixelart", "monet", "a castle", false},
		{"impressionist", "monet", "a pond", true},
		{"impressionist", "_", "an impressionist pond", true},
		{"impressionist", "_", "a pond in the style of Claude Monet", true},
		{"impressionist", "vangogh", "a starry night", false},
		// A key that is no group: that style key only.
		{"_", "_", "a cat as ukiyo-e", true},
		{"_", "ukiyoe", "a cat", false},
		{"newstyle", "newstyle", "", true},
		{"newstyle", "_", "newstyle", false},
	} {
		if got := In(c.key, c.style, c.text); got != c.want {
			t.Errorf("In(%q, %q, %q) = %v, want %v", c.key, c.style, c.text, got, c.want)
		}
	}
}

func TestGroups(t *testing.T) {
	// Every button of the bot is a group of its own, with its label, and is named by its own label.
	for _, s := range table[1:] {
		g, ok := GroupOf(s.Key)
		if !ok || g.Label != s.Label || Label(s.Key) != s.Label {
			t.Errorf("%s: group %v %+v", s.Key, ok, g)
		}
		if !g.Names(Words(s.Label)) && s.Key != "stamp" && s.Key != "handdrawn" && s.Key != "bw" { // "black & white" alone is often the subject's colours
			t.Errorf("%s: its label %q does not name it", s.Key, s.Label)
		}
	}
	for key, label := range map[string]string{"pixelart": "Pixel art", "impressionist": "Impressionist"} {
		if Label(key) != label {
			t.Errorf("Label(%q) = %q", key, Label(key))
		}
		for _, r := range key { // what ?style= takes: [a-z0-9_-]{1,32}
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') || len(key) > 32 {
				t.Errorf("%q is not a valid style key in a URL", key)
			}
		}
	}
	// The page gets the same list.
	var got [][3]json.RawMessage
	if err := json.Unmarshal([]byte(GroupsJSON()), &got); err != nil || len(got) != len(groups) {
		t.Fatalf("GroupsJSON: %d, %v", len(got), err)
	}
	var terms [][]string
	_ = json.Unmarshal(got[len(got)-2][2], &terms)
	if !slices.ContainsFunc(terms, func(t []string) bool { return slices.Equal(t, []string{"pixel", "art"}) }) {
		t.Errorf("pixel art terms: %q", terms)
	}
}
