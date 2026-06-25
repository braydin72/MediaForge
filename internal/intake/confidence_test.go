package intake

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// --- ScoreMovie ---

func TestScoreMovie_ExactMatchWinsOverFuzzy(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 142 * time.Minute

	exact := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994, RuntimeMin: 142}, probe)
	fuzzy := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gumps", Year: 1994, RuntimeMin: 142}, probe) // 1-char diff

	if exact <= fuzzy {
		t.Errorf("exact %f should beat fuzzy %f", exact, fuzzy)
	}
	// exact + year + runtime≤5: 0.40 + 0.25 + 0.20 = 0.85
	if !approxEqual(exact, 0.85) {
		t.Errorf("exact score: want ~0.85, got %f", exact)
	}
}

func TestScoreMovie_YearExact(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994}, 0)
	// exact + year: 0.40 + 0.25 = 0.65
	if !approxEqual(score, 0.65) {
		t.Errorf("want ~0.65, got %f", score)
	}
}

func TestScoreMovie_YearPlusOrMinus1(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1993}, 0)
	// exact + year±1: 0.40 + 0.10 = 0.50
	if !approxEqual(score, 0.50) {
		t.Errorf("want ~0.50, got %f", score)
	}
}

func TestScoreMovie_RuntimeWithin5(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 144 * time.Minute // 2 min off from 142

	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994, RuntimeMin: 142}, probe)
	// exact + year + runtime≤5: 0.40 + 0.25 + 0.20 = 0.85
	if !approxEqual(score, 0.85) {
		t.Errorf("want ~0.85, got %f", score)
	}
}

func TestScoreMovie_RuntimeWithin10(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 150 * time.Minute // 8 min off from 142

	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994, RuntimeMin: 142}, probe)
	// exact + year + runtime≤10: 0.40 + 0.25 + 0.10 = 0.75
	if !approxEqual(score, 0.75) {
		t.Errorf("want ~0.75, got %f", score)
	}
}

func TestScoreMovie_RuntimeFail(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 90 * time.Minute // 52 min off — far outside ±10

	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994, RuntimeMin: 142}, probe)
	// exact + year, no runtime bonus: 0.40 + 0.25 = 0.65
	if !approxEqual(score, 0.65) {
		t.Errorf("want ~0.65, got %f", score)
	}
}

func TestScoreMovie_Cap(t *testing.T) {
	// Even with all bonuses, score must not exceed 1.0.
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 142 * time.Minute
	score := ScoreMovie(parsed, ScoreInput{Title: "Forrest Gump", Year: 1994, RuntimeMin: 142}, probe)
	if score > 1.0 {
		t.Errorf("score %f exceeds 1.0", score)
	}
}

// --- ScoreTV ---

func TestScoreTV_EpisodeFoundBoostsScore(t *testing.T) {
	parsed := &ParsedFilename{Title: "Breaking Bad", Year: 2008, IsTV: true}

	with := ScoreTV(parsed, ScoreInput{Title: "Breaking Bad", Year: 2008, EpisodeFound: true})
	without := ScoreTV(parsed, ScoreInput{Title: "Breaking Bad", Year: 2008, EpisodeFound: false})

	if with <= without {
		t.Errorf("episode found (%f) should score higher than not found (%f)", with, without)
	}
	// exact + year + episode: 0.40 + 0.20 + 0.25 = 0.85
	if !approxEqual(with, 0.85) {
		t.Errorf("with episode: want ~0.85, got %f", with)
	}
	// exact + year: 0.40 + 0.20 = 0.60
	if !approxEqual(without, 0.60) {
		t.Errorf("without episode: want ~0.60, got %f", without)
	}
}

func TestScoreTV_FuzzySeriesName(t *testing.T) {
	parsed := &ParsedFilename{Title: "Breaking Bad", Year: 2008}
	// "Breaking Bard" — 1 edit in 12 runes, similarity ≈ 0.917 → fuzzy
	score := ScoreTV(parsed, ScoreInput{Title: "Breaking Bard", Year: 2008, EpisodeFound: true})
	// fuzzy + year + episode: 0.25 + 0.20 + 0.25 = 0.70
	if !approxEqual(score, 0.70) {
		t.Errorf("want ~0.70, got %f", score)
	}
}

func TestScoreTV_NoYearInFilename(t *testing.T) {
	parsed := &ParsedFilename{Title: "Breaking Bad", Year: 0} // no year in filename
	score := ScoreTV(parsed, ScoreInput{Title: "Breaking Bad", Year: 2008, EpisodeFound: true})
	// exact + episode (year skipped since parsed.Year==0): 0.40 + 0.25 = 0.65
	if !approxEqual(score, 0.65) {
		t.Errorf("want ~0.65, got %f", score)
	}
}

// --- RuntimeCrossCheck ---

func TestRuntimeCrossCheck_ExactMatch(t *testing.T) {
	ok, delta := RuntimeCrossCheck(142*60, 142)
	if !ok || delta != 0 {
		t.Errorf("exact match: want (true, 0), got (%v, %d)", ok, delta)
	}
}

func TestRuntimeCrossCheck_WithinFive(t *testing.T) {
	ok, delta := RuntimeCrossCheck(145*60, 142) // 3 min off
	if !ok || delta != 3 {
		t.Errorf("within 5: want (true, 3), got (%v, %d)", ok, delta)
	}
}

func TestRuntimeCrossCheck_ExactlyFive(t *testing.T) {
	ok, delta := RuntimeCrossCheck(147*60, 142) // exactly 5 min off
	if !ok || delta != 5 {
		t.Errorf("exactly 5: want (true, 5), got (%v, %d)", ok, delta)
	}
}

func TestRuntimeCrossCheck_SixMinutesFail(t *testing.T) {
	ok, delta := RuntimeCrossCheck(148*60, 142) // 6 min off — just outside
	if ok || delta != 6 {
		t.Errorf("6 min: want (false, 6), got (%v, %d)", ok, delta)
	}
}

func TestRuntimeCrossCheck_LargeGapFail(t *testing.T) {
	ok, delta := RuntimeCrossCheck(90*60, 142) // 52 min off
	if ok || delta != 52 {
		t.Errorf("large gap: want (false, 52), got (%v, %d)", ok, delta)
	}
}

// --- stringSimilarity ---

func TestStringSimilarity_Exact(t *testing.T) {
	if got := stringSimilarity("Breaking Bad", "Breaking Bad"); got != 1.0 {
		t.Errorf("identical strings: want 1.0, got %f", got)
	}
}

func TestStringSimilarity_CaseInsensitive(t *testing.T) {
	if got := stringSimilarity("BREAKING BAD", "breaking bad"); got != 1.0 {
		t.Errorf("case insensitive: want 1.0, got %f", got)
	}
}

func TestStringSimilarity_OneEditFuzzy(t *testing.T) {
	got := stringSimilarity("Breaking Bard", "Breaking Bad") // 1 edit in 12 → ≈0.917
	if got < 0.80 || got >= 1.0 {
		t.Errorf("1-edit fuzzy: want [0.80, 1.0), got %f", got)
	}
}

func TestStringSimilarity_CompletelyDifferent(t *testing.T) {
	got := stringSimilarity("XYZZY", "Breaking Bad")
	if got >= 0.50 {
		t.Errorf("completely different: want < 0.50, got %f", got)
	}
}

func TestStringSimilarity_EmptyString(t *testing.T) {
	if got := stringSimilarity("", "Breaking Bad"); got != 0.0 {
		t.Errorf("empty a: want 0.0, got %f", got)
	}
	if got := stringSimilarity("Breaking Bad", ""); got != 0.0 {
		t.Errorf("empty b: want 0.0, got %f", got)
	}
}

// --- Orchestrator mock helpers ---

func makeTVDBSuccessMock() roundTripFunc {
	return routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{
				"data": map[string]string{"token": "tok"},
			})
		},
		"/v4/search": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{
				"data": []map[string]interface{}{
					{"tvdb_id": "81189", "name": "Breaking Bad", "year": "2008", "network": "AMC"},
				},
			})
		},
		"/v4/series": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"episodes": []map[string]interface{}{
						{"id": 1, "name": "Pilot", "aired": "2008-01-20", "seasonNumber": 1, "number": 1},
					},
				},
			})
		},
	})
}

func makeTVDBFailMock() roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, nil), nil
	}
}

func makeTMDBTVFailMock() roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusNotFound, map[string]string{"status_message": "Not Found"}), nil
	}
}

func makeOMDbTVSuccessMock() roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, map[string]interface{}{
			"Response": "True",
			"Title":    "Pilot",
			"Year":     "2008",
			"Runtime":  "58 min",
			"imdbID":   "tt0959621",
			"Released": "20 Jan 2008",
		}), nil
	}
}

func makeOMDbFailMock() roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, map[string]interface{}{
			"Response": "False",
			"Error":    "Series or episode not found!",
		}), nil
	}
}

// --- Orchestrator: TV chain ---

func TestOrchestrator_TVDBSuccessStopsChain(t *testing.T) {
	tmdbCalls, omdbCalls := 0, 0

	tvdb := newMockTVDBClient("key", makeTVDBSuccessMock())
	tmdb := newMockTMDBClient("key", func(r *http.Request) (*http.Response, error) {
		tmdbCalls++
		return jsonResp(http.StatusOK, map[string]interface{}{"results": []interface{}{}}), nil
	})
	omdb := newMockOMDbClient("key", func(r *http.Request) (*http.Response, error) {
		omdbCalls++
		return jsonResp(http.StatusOK, map[string]interface{}{
			"Response": "False", "Error": "Series or episode not found!",
		}), nil
	})

	orch := NewOrchestrator(tvdb, tmdb, omdb)
	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 1, Episode: 1,
	}

	result, err := orch.LookupTV(context.Background(), parsed, 0.60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != "tvdb" {
		t.Errorf("Source: want %q, got %q", "tvdb", result.Source)
	}
	if tmdbCalls != 0 {
		t.Errorf("TMDB should not be called when TVDB succeeds, called %d times", tmdbCalls)
	}
	if omdbCalls != 0 {
		t.Errorf("OMDb should not be called when TVDB succeeds, called %d times", omdbCalls)
	}
	// exact + year + episode: 0.40 + 0.20 + 0.25 = 0.85
	if result.Confidence < 0.60 {
		t.Errorf("confidence %f should be >= reviewThreshold 0.60", result.Confidence)
	}
}

func TestOrchestrator_FallsThroughToOMDb(t *testing.T) {
	tvdb := newMockTVDBClient("key", makeTVDBFailMock())
	tmdb := newMockTMDBClient("key", makeTMDBTVFailMock())
	omdb := newMockOMDbClient("key", makeOMDbTVSuccessMock())

	orch := NewOrchestrator(tvdb, tmdb, omdb)
	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 1, Episode: 1,
	}

	result, err := orch.LookupTV(context.Background(), parsed, 0.60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != "omdb" {
		t.Errorf("Source: want %q, got %q", "omdb", result.Source)
	}
	if result.EpisodeTitle != "Pilot" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Pilot", result.EpisodeTitle)
	}
}

func TestOrchestrator_AllSourcesFail_NoMatch(t *testing.T) {
	tvdb := newMockTVDBClient("key", makeTVDBFailMock())
	tmdb := newMockTMDBClient("key", makeTMDBTVFailMock())
	omdb := newMockOMDbClient("key", makeOMDbFailMock())

	orch := NewOrchestrator(tvdb, tmdb, omdb)
	parsed := &ParsedFilename{Title: "Nonexistent Show XYZZY", IsTV: true, Season: 1, Episode: 1}

	_, err := orch.LookupTV(context.Background(), parsed, 0.60)
	if err == nil {
		t.Fatal("expected NoMatchError, got nil")
	}

	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) {
		t.Fatalf("expected *NoMatchError, got %T: %v", err, err)
	}
	if len(noMatch.Reasons) == 0 {
		t.Error("NoMatchError.Reasons should not be empty")
	}
}

func TestOrchestrator_NilClientsSkipped(t *testing.T) {
	// All nil clients → no candidates → NoMatchError.
	orch := NewOrchestrator(nil, nil, nil)
	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := orch.LookupTV(context.Background(), parsed, 0.60)
	if err == nil {
		t.Fatal("expected NoMatchError for nil clients, got nil")
	}
	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) {
		t.Fatalf("expected *NoMatchError, got %T", err)
	}
	if len(noMatch.Reasons) != 0 {
		t.Errorf("no reasons expected for nil clients, got %v", noMatch.Reasons)
	}
}

// --- Orchestrator: movie chain ---

func TestOrchestrator_Movie_TMDBSuccessStopsChain(t *testing.T) {
	omdbCalls := 0

	tmdb := newMockTMDBClient("key", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 13, "title": "Forrest Gump", "release_date": "1994-07-06", "poster_path": ""},
				},
			})
		},
		"/3/movie/": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{"id": 13, "runtime": 142})
		},
	}))
	omdb := newMockOMDbClient("key", func(r *http.Request) (*http.Response, error) {
		omdbCalls++
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	orch := NewOrchestrator(nil, tmdb, omdb)
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}

	result, err := orch.LookupMovie(context.Background(), parsed, 142*time.Minute, 0.60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != "tmdb" {
		t.Errorf("Source: want %q, got %q", "tmdb", result.Source)
	}
	if omdbCalls != 0 {
		t.Errorf("OMDb should not be called when TMDB succeeds, called %d times", omdbCalls)
	}
}

func TestOrchestrator_Movie_FallsBackToOMDb(t *testing.T) {
	tmdb := newMockTMDBClient("key", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, nil), nil
	})
	omdb := newMockOMDbClient("key", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	orch := NewOrchestrator(nil, tmdb, omdb)
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}

	result, err := orch.LookupMovie(context.Background(), parsed, 0, 0.60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != "omdb" {
		t.Errorf("Source: want %q, got %q", "omdb", result.Source)
	}
}

func TestOrchestrator_Movie_AllFail(t *testing.T) {
	tmdb := newMockTMDBClient("key", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, nil), nil
	})
	omdb := newMockOMDbClient("key", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, map[string]interface{}{
			"Response": "False", "Error": "Movie not found!",
		}), nil
	})

	orch := NewOrchestrator(nil, tmdb, omdb)
	parsed := &ParsedFilename{Title: "Nonexistent Film XYZZY", MediaType: "movie"}

	_, err := orch.LookupMovie(context.Background(), parsed, 0, 0.60)
	if err == nil {
		t.Fatal("expected NoMatchError, got nil")
	}
	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) {
		t.Fatalf("expected *NoMatchError, got %T: %v", err, err)
	}
	if len(noMatch.Reasons) == 0 {
		t.Error("NoMatchError.Reasons should not be empty")
	}
}

// --- NoMatchError ---

func TestNoMatchError_Message(t *testing.T) {
	err := &NoMatchError{Reasons: []string{"TVDB: not found", "TMDB: rate limit"}}
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should not be empty")
	}
	for _, r := range err.Reasons {
		if len(msg) == 0 {
			t.Errorf("reason %q missing from error message", r)
		}
	}
}
