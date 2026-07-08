package intake

import "testing"

func TestSanitizePathComponent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"colon with space", "9-1-1: Nashville", "9-1-1 - Nashville"},
		{"colon no space", "Show:Title", "Show - Title"},
		{"slash", "Good/Bad", "Good_Bad"},
		{"backslash", `Good\Bad`, "Good_Bad"},
		{"question mark", "What Are You?", "What Are You_"},
		{"asterisk", "Star*Wars", "Star_Wars"},
		{"quote", `Say "Hi"`, "Say _Hi_"},
		{"angle brackets", "<Title>", "_Title_"},
		{"pipe", "A|B", "A_B"},
		{"no special chars", "Normal Title", "Normal Title"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizePathComponent(tc.in)
			if got != tc.want {
				t.Errorf("sanitizePathComponent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyNamingTemplateColonInShowTitle(t *testing.T) {
	parsed := &ParsedFilename{
		Title:     "9-1-1: Nashville",
		Year:      2025,
		MediaType: "tv",
		Season:    1,
		Episode:   1,
	}

	folder := applyNamingTemplate("{show} ({year})", parsed)
	if want := "9-1-1 - Nashville (2025)"; folder != want {
		t.Errorf("show folder = %q, want %q", folder, want)
	}
}
