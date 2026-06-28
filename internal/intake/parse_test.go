package intake

import (
	"testing"
)

func TestParseFilename(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTitle string
		wantYear  int
		wantIsTV  bool
		wantS     int
		wantE     int
		wantE2    int
		wantType  string
	}{
		// --- Movies ---
		{
			name:      "standard movie no year",
			input:     "Inception.mkv",
			wantTitle: "Inception",
			wantType:  "movie",
		},
		{
			name:      "movie with standalone year",
			input:     "The.Dark.Knight.2008.mkv",
			wantTitle: "The Dark Knight",
			wantYear:  2008,
			wantType:  "movie",
		},
		{
			name:      "movie with year in parentheses",
			input:     "Inception (2010).mkv",
			wantTitle: "Inception",
			wantYear:  2010,
			wantType:  "movie",
		},
		{
			name:      "movie with year in parentheses and scene tags",
			input:     "The.Matrix (1999) 1080p BluRay x264.mkv",
			wantTitle: "The Matrix",
			wantYear:  1999,
			wantType:  "movie",
		},
		{
			name:      "dots as separators",
			input:     "The.Matrix.Reloaded.2003.mkv",
			wantTitle: "The Matrix Reloaded",
			wantYear:  2003,
			wantType:  "movie",
		},
		{
			name:      "full scene-tagged release",
			input:     "The.Dark.Knight.2008.1080p.BluRay.x264-YIFY.mkv",
			wantTitle: "The Dark Knight",
			wantYear:  2008,
			wantType:  "movie",
		},
		{
			name:      "resolution tag without year",
			input:     "Inception.1080p.BluRay.x264.mkv",
			wantTitle: "Inception",
			wantType:  "movie",
		},
		{
			name:      "webrip source tag",
			input:     "Dune.2021.WEBRip.x265.mkv",
			wantTitle: "Dune",
			wantYear:  2021,
			wantType:  "movie",
		},
		{
			name:      "streaming source tag",
			input:     "The.Irishman.2019.AMZN.WEBRip.1080p.mkv",
			wantTitle: "The Irishman",
			wantYear:  2019,
			wantType:  "movie",
		},
		{
			name:      "no year no tags uses whole stem",
			input:     "My.Home.Movie.mkv",
			wantTitle: "My Home Movie",
			wantType:  "movie",
		},
		{
			name:      "non-English title with year",
			input:     "Parasite.2019.KOREAN.1080p.BluRay.x264.mkv",
			wantTitle: "Parasite",
			wantYear:  2019,
			wantType:  "movie",
		},
		{
			name:      "title starting with a year (2001 A Space Odyssey)",
			input:     "2001.A.Space.Odyssey.1968.1080p.BluRay.mkv",
			wantTitle: "2001 A Space Odyssey",
			wantYear:  1968,
			wantType:  "movie",
		},
		{
			name:      "hyphenated title preserved",
			input:     "Spider-Man.2002.1080p.BluRay.mkv",
			wantTitle: "Spider-Man",
			wantYear:  2002,
			wantType:  "movie",
		},
		{
			name:      "underscores as separators",
			input:     "The_Godfather_1972_1080p.mkv",
			wantTitle: "The Godfather",
			wantYear:  1972,
			wantType:  "movie",
		},
		{
			name:      "4K UHD release",
			input:     "Blade.Runner.2049.2017.2160p.4K.BluRay.mkv",
			wantTitle: "Blade Runner 2049",
			wantYear:  2017,
			wantType:  "movie",
		},
		{
			name:      "year in parens directly attached to title (no space)",
			input:     "avatar fire and ash(2025).mp4",
			wantTitle: "avatar fire and ash",
			wantYear:  2025,
			wantType:  "movie",
		},
		// --- TV shows ---
		{
			name:      "TV single episode dot-separated",
			input:     "Breaking.Bad.S01E01.mkv",
			wantTitle: "Breaking Bad",
			wantIsTV:  true,
			wantS:     1,
			wantE:     1,
			wantType:  "tv",
		},
		{
			name:      "TV single episode lowercase",
			input:     "game.of.thrones.s03e09.mkv",
			wantTitle: "game of thrones",
			wantIsTV:  true,
			wantS:     3,
			wantE:     9,
			wantType:  "tv",
		},
		{
			name:      "TV single episode with scene tags",
			input:     "Breaking.Bad.S05E14.1080p.BluRay.x264.mkv",
			wantTitle: "Breaking Bad",
			wantIsTV:  true,
			wantS:     5,
			wantE:     14,
			wantType:  "tv",
		},
		{
			name:      "TV multi-episode hyphen form SxxExx-Exx",
			input:     "Breaking.Bad.S01E01-E02.720p.HDTV.mkv",
			wantTitle: "Breaking Bad",
			wantIsTV:  true,
			wantS:     1,
			wantE:     1,
			wantE2:    2,
			wantType:  "tv",
		},
		{
			name:      "TV multi-episode concat form SxxExxExx",
			input:     "The.Office.S03E01E02.WEBRip.mkv",
			wantTitle: "The Office",
			wantIsTV:  true,
			wantS:     3,
			wantE:     1,
			wantE2:    2,
			wantType:  "tv",
		},
		{
			name:      "TV with fansub bracket group stripped",
			input:     "[SubGroup] Anime Show - S01E05 - Episode Title [1080p].mkv",
			wantTitle: "Anime Show",
			wantIsTV:  true,
			wantS:     1,
			wantE:     5,
			wantType:  "tv",
		},
		{
			name:      "TV single-digit season",
			input:     "Seinfeld.s2e12.mkv",
			wantTitle: "Seinfeld",
			wantIsTV:  true,
			wantS:     2,
			wantE:     12,
			wantType:  "tv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseFilename(tt.input)

			if got.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Year != tt.wantYear {
				t.Errorf("Year = %d, want %d", got.Year, tt.wantYear)
			}
			if got.IsTV != tt.wantIsTV {
				t.Errorf("IsTV = %v, want %v", got.IsTV, tt.wantIsTV)
			}
			if got.Season != tt.wantS {
				t.Errorf("Season = %d, want %d", got.Season, tt.wantS)
			}
			if got.Episode != tt.wantE {
				t.Errorf("Episode = %d, want %d", got.Episode, tt.wantE)
			}
			if got.Episode2 != tt.wantE2 {
				t.Errorf("Episode2 = %d, want %d", got.Episode2, tt.wantE2)
			}
			if got.MediaType != tt.wantType {
				t.Errorf("MediaType = %q, want %q", got.MediaType, tt.wantType)
			}
			// Raw is always the stem without extension.
			if got.Raw == "" {
				t.Error("Raw is empty")
			}
		})
	}
}

func TestParseFilename_PathHandling(t *testing.T) {
	// filepath.Base must strip the directory correctly on both / and \ paths.
	cases := []struct {
		input     string
		wantTitle string
	}{
		{`/incoming/The.Dark.Knight.2008.mkv`, "The Dark Knight"},
		{`C:\incoming\The.Dark.Knight.2008.mkv`, "The Dark Knight"},
	}
	for _, c := range cases {
		got := ParseFilename(c.input)
		if got.Title != c.wantTitle {
			t.Errorf("ParseFilename(%q).Title = %q, want %q", c.input, got.Title, c.wantTitle)
		}
		if got.Year != 2008 {
			t.Errorf("ParseFilename(%q).Year = %d, want 2008", c.input, got.Year)
		}
	}
}

func TestParseFilename_RawIsAlwaysStem(t *testing.T) {
	cases := []struct {
		input   string
		wantRaw string
	}{
		{"Movie.2008.mkv", "Movie.2008"},
		{"Show.S01E01.mp4", "Show.S01E01"},
		{"no_ext", "no_ext"},
	}
	for _, c := range cases {
		got := ParseFilename(c.input)
		if got.Raw != c.wantRaw {
			t.Errorf("ParseFilename(%q).Raw = %q, want %q", c.input, got.Raw, c.wantRaw)
		}
	}
}
