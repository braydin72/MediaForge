package intake

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/store"
)

func TestBestTitleSimilarity_OrdinalPrefixAndContainment(t *testing.T) {
	cases := []struct {
		a, b    string
		atLeast float64
		below   float64 // 0 = no upper bound
	}{
		// Real production case: leading "Chapter N" prefix on the filename only.
		{"Great Britain", "Chapter 2 Great Britain", 1.0, 0},
		{"Great Britain", "Chapter Two: Great Britain", 1.0, 0},
		{"Episode 3 - The Heist", "The Heist", 1.0, 0},
		// Whole-word containment: confirms, but stays under the 0.90 relocation bar.
		{"Great Britain", "Great Britain Revisited", tokenContainmentScore, 0.90},
		// Both sides carry an ordinal prefix: the numbers are real information,
		// so the prefix must not be stripped to manufacture an exact match.
		{"Chapter 5 Great Britain", "Chapter 2 Great Britain", 0, 0.999},
		// A title that is nothing but an ordinal is left alone.
		{"Part Two", "Chapter 2 Great Britain", 0, 0.60},
		{"Episode 2", "Chapter 2 Episode 2 Recap", 0, tokenContainmentScore},
		// A single word never matches by containment alone.
		{"Pilot", "Pilot Extended Cut", 0, tokenContainmentScore},
	}
	for _, c := range cases {
		got := bestTitleSimilarity(c.a, c.b)
		if got < c.atLeast {
			t.Errorf("bestTitleSimilarity(%q, %q) = %.2f, want >= %.2f", c.a, c.b, got, c.atLeast)
		}
		if c.below > 0 && got >= c.below {
			t.Errorf("bestTitleSimilarity(%q, %q) = %.2f, want < %.2f", c.a, c.b, got, c.below)
		}
	}
}

func TestSplitSeriesYearSuffix(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantYear int
	}{
		{"Treasure Island (2026)", "Treasure Island", 2026},
		{"Treasure Island", "Treasure Island", 0},
		{"Politics (1)", "Politics (1)", 0},
	}
	for _, c := range cases {
		name, year := splitSeriesYearSuffix(c.in)
		if name != c.wantName || year != c.wantYear {
			t.Errorf("splitSeriesYearSuffix(%q) = (%q, %d), want (%q, %d)", c.in, name, year, c.wantName, c.wantYear)
		}
	}
}

// seriesEpisodeMock returns a TVDB mock where search yields searchData and
// each series ID's episode fetch returns that series' single episode.
func seriesEpisodeMock(searchData []map[string]interface{}, episodes map[string]map[string]interface{}) *TVDBClient {
	return newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{"data": searchData})
		},
		"/v4/series": func(r *http.Request) *http.Response {
			for id, ep := range episodes {
				if strings.Contains(r.URL.Path, "/"+id+"/") {
					return jsonResp(http.StatusOK, map[string]interface{}{
						"data":  map[string]interface{}{"episodes": []map[string]interface{}{ep}},
						"links": map[string]interface{}{"next": ""},
					})
				}
			}
			return jsonResp(http.StatusNotFound, map[string]string{"message": "not found"})
		},
	}))
}

// treasureIslandMock reproduces the production log: TVDB lists older
// same-named series first, every name carries a " (YYYY)" disambiguator, and
// only the 2026 series has "Great Britain" at S01E02.
func treasureIslandMock() *TVDBClient {
	return seriesEpisodeMock(
		[]map[string]interface{}{
			{"tvdb_id": "253556", "name": "Treasure Island (2012)", "year": "2012"},
			{"tvdb_id": "368457", "name": "Treasure Island (2019)", "year": "2019"},
			{"tvdb_id": "474916", "name": "Treasure Island (2026)", "year": "2026"},
			{"tvdb_id": "369335", "name": "Treasure Island with Bear Grylls", "year": "2019"},
		},
		map[string]map[string]interface{}{
			"253556": {"id": 1, "name": "Part Two", "aired": "2012-01-02", "seasonNumber": 1, "number": 2},
			"368457": {"id": 2, "name": "Episode 2", "aired": "2019-05-02", "seasonNumber": 1, "number": 2},
			"474916": {"id": 3, "name": "Great Britain", "aired": "2026-10-20", "seasonNumber": 1, "number": 2},
			"369335": {"id": 4, "name": "Episode 2", "aired": "2019-03-02", "seasonNumber": 1, "number": 2},
		},
	)
}

func TestTVDBLookup_RemakeWithChapterPrefixResolves(t *testing.T) {
	parsed := &ParsedFilename{
		Title: "Treasure Island", Year: 2026, IsTV: true, Season: 1, Episode: 2,
		ParsedEpisodeTitle: "Chapter 2 Great Britain",
	}
	result, err := treasureIslandMock().Lookup(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SeriesID != 474916 {
		t.Errorf("SeriesID: want 474916 (Treasure Island (2026)), got %d (%s)", result.SeriesID, result.SeriesName)
	}
	if result.TitleMismatchUnresolved || result.SeriesAmbiguous {
		t.Errorf("want a clean match, got TitleMismatchUnresolved=%v SeriesAmbiguous=%v",
			result.TitleMismatchUnresolved, result.SeriesAmbiguous)
	}
	if result.EpisodeTitle != "Great Britain" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Great Britain", result.EpisodeTitle)
	}
	if result.Confidence < 0.85 {
		t.Errorf("Confidence: want >= 0.85 (auto-accept), got %.2f", result.Confidence)
	}
}

// TestBaseSeriesScore_YearSuffixMatchingFilenameYearIsExact: a TVDB name of
// "Treasure Island (2026)" for a filename "Treasure Island (2026)" is a
// perfect name+year match and must score the same as an un-suffixed exact
// name with an exact year (1.00), not a near-match.
func TestBaseSeriesScore_YearSuffixMatchingFilenameYearIsExact(t *testing.T) {
	parsed := &ParsedFilename{Title: "Treasure Island", Year: 2026}
	q := normTitle(parsed.Title)
	if got := baseSeriesScore(tvdbSearchResult{Name: "Treasure Island (2026)", Year: "2026"}, parsed, q); !approxEqual(got, 1.00) {
		t.Errorf("suffix year == filename year: want 1.00, got %.2f", got)
	}
	if got := baseSeriesScore(tvdbSearchResult{Name: "Treasure Island (2012)", Year: "2012"}, parsed, q); !approxEqual(got, 0.85) {
		t.Errorf("suffix year != filename year: want 0.85, got %.2f", got)
	}
}

// tiedSeriesMock has two remakes with the same episode title at S01E01 and no
// filename year to tell them apart — a genuine close call.
func tiedSeriesMock() *TVDBClient {
	return seriesEpisodeMock(
		[]map[string]interface{}{
			{"tvdb_id": "100", "name": "Foo (2010)", "year": "2010"},
			{"tvdb_id": "200", "name": "Foo (2011)", "year": "2011"},
		},
		map[string]map[string]interface{}{
			"100": {"id": 1, "name": "The Heist", "aired": "2010-01-01", "seasonNumber": 1, "number": 1},
			"200": {"id": 2, "name": "The Heist", "aired": "2011-01-01", "seasonNumber": 1, "number": 1},
		},
	)
}

func tiedSeriesParsed() ParsedFilename {
	return ParsedFilename{
		Raw: "Foo - S01E01 - The Heist.mkv", Title: "Foo", IsTV: true, MediaType: "tv",
		Season: 1, Episode: 1, ParsedEpisodeTitle: "The Heist",
	}
}

func TestTVDBLookup_TiedSeriesFlaggedAmbiguous(t *testing.T) {
	parsed := tiedSeriesParsed()
	result, err := tiedSeriesMock().Lookup(context.Background(), &parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.SeriesAmbiguous {
		t.Fatal("SeriesAmbiguous: want true when two series tie on name and episode title")
	}
	if len(result.Alternates) != 1 || result.Alternates[0].SeriesID != 200 {
		t.Fatalf("Alternates: want [Foo (2011)], got %+v", result.Alternates)
	}
}

func TestTVDBLookup_UnresolvedMismatchCarriesAlternates(t *testing.T) {
	client := seriesEpisodeMock(
		[]map[string]interface{}{
			{"tvdb_id": "100", "name": "Bar", "year": "2001"},
			{"tvdb_id": "200", "name": "Bar Stories", "year": "2005"},
		},
		map[string]map[string]interface{}{
			"100": {"id": 1, "name": "Something Else", "aired": "2001-01-01", "seasonNumber": 1, "number": 1},
			"200": {"id": 2, "name": "Another Thing", "aired": "2005-01-01", "seasonNumber": 1, "number": 1},
		},
	)
	parsed := &ParsedFilename{Title: "Bar", IsTV: true, Season: 1, Episode: 1, ParsedEpisodeTitle: "Xyzzy Quux"}
	result, err := client.Lookup(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TitleMismatchUnresolved {
		t.Fatal("TitleMismatchUnresolved: want true")
	}
	if len(result.Alternates) != 1 || result.Alternates[0].SeriesID != 200 {
		t.Fatalf("Alternates: want [Bar Stories], got %+v", result.Alternates)
	}
}

func newGateTestWatcher(t *testing.T, llm *LLMClient) (*Watcher, *store.SQLiteStore) {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.IntakeConfig{EnableNamingLookup: true}
	w := NewWatcher(&cfg, "ffprobe", st)
	w.Orchestrator = &Orchestrator{TVDB: tiedSeriesMock()}
	w.LLMClient = llm
	return w, st
}

func mockLLMAnswer(answer string) *LLMClient {
	return newMockLLMClient("anthropic", "sk-test", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, anthropicResp(answer)), nil
	})
}

func TestResolveAndGate_AmbiguousSeriesNoLLMRoutesToReview(t *testing.T) {
	w, st := newGateTestWatcher(t, nil)
	parsed := tiedSeriesParsed()
	if _, ok := w.resolveAndGate(context.Background(), "/incoming/"+parsed.Raw, &parsed, nil); ok {
		t.Fatal("resolveAndGate: want ok=false for an ambiguous series match without an LLM")
	}
	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("review queue count: want 1, got %d", len(entries))
	}
	for _, want := range []string{"ambiguous", "Foo (2010)", "Foo (2011)", "LLM not configured"} {
		if !strings.Contains(entries[0].Reason, want) {
			t.Errorf("review reason %q: want it to mention %q", entries[0].Reason, want)
		}
	}
}

func TestResolveAndGate_AmbiguousSeriesResolvedByLLM(t *testing.T) {
	w, st := newGateTestWatcher(t, mockLLMAnswer(`{"candidate_id":"2","confidence":0.95,"reasoning":"2011 series"}`))
	parsed := tiedSeriesParsed()
	result, ok := w.resolveAndGate(context.Background(), "/incoming/"+parsed.Raw, &parsed, nil)
	if !ok {
		entries, _ := st.GetReviewQueue()
		t.Fatalf("resolveAndGate: want ok=true after LLM picks a candidate, got false (queue=%+v)", entries)
	}
	if result.TVDBSeriesID != 200 || parsed.Title != "Foo (2011)" {
		t.Errorf("want the LLM's pick (Foo (2011), 200), got %q (%d)", parsed.Title, result.TVDBSeriesID)
	}
	if result.SeriesAmbiguous || len(result.Alternates) != 0 {
		t.Error("accepted result must have its ambiguity flag and alternates cleared")
	}
}

func TestResolveAndGate_AmbiguousSeriesLowLLMConfidenceRoutesToReview(t *testing.T) {
	w, st := newGateTestWatcher(t, mockLLMAnswer(`{"candidate_id":"1","confidence":0.70,"reasoning":"unsure"}`))
	parsed := tiedSeriesParsed()
	if _, ok := w.resolveAndGate(context.Background(), "/incoming/"+parsed.Raw, &parsed, nil); ok {
		t.Fatal("resolveAndGate: want ok=false when the LLM is below confidence_threshold on a close call")
	}
	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Reason, "70% confidence") {
		t.Fatalf("want one review entry citing the LLM's 70%% confidence, got %+v", entries)
	}
}
