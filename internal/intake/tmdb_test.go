package intake

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newMockTMDBClient constructs a TMDBClient backed by a roundTripFunc mock.
// roundTripFunc and jsonResp are defined in tvdb_test.go (same package).
func newMockTMDBClient(apiKey string, fn roundTripFunc) *TMDBClient {
	return NewTMDBClient(apiKey, &http.Client{Transport: fn})
}

// tmdbRouteByPath dispatches mock responses using URL path prefix matching.
// Paths for TMDB all start with /3/.
func tmdbRouteByPath(routes map[string]func(*http.Request) *http.Response) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		for prefix, handler := range routes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				return handler(r), nil
			}
		}
		return jsonResp(http.StatusNotFound, map[string]string{"status_message": "unhandled mock: " + r.URL.Path}), nil
	}
}

var (
	forrestGumpSearchBody = map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 13, "title": "Forrest Gump", "release_date": "1994-07-06", "poster_path": "/arw2vcBveW.jpg"},
		},
	}
	forrestGumpDetailBody = map[string]interface{}{
		"id": 13, "title": "Forrest Gump", "release_date": "1994-07-06", "runtime": 142,
	}
	breakingBadTVSearchBody = map[string]interface{}{
		"results": []map[string]interface{}{
			{"id": 1396, "name": "Breaking Bad", "first_air_date": "2008-01-20", "poster_path": "/ggFHVNu6YYI5L9pCfOacjizRGt.jpg"},
		},
	}
	breakingBadEpisodeBody = map[string]interface{}{
		"name": "Pilot", "air_date": "2008-01-20",
	}
)

func TestTMDBLookupMovie_ExactMatch(t *testing.T) {
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpSearchBody) },
		"/3/movie/":       func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpDetailBody) },
	}))

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}

	result, err := client.LookupMovie(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TMDBID != 13 {
		t.Errorf("TMDBID: want 13, got %d", result.TMDBID)
	}
	if result.Title != "Forrest Gump" {
		t.Errorf("Title: want %q, got %q", "Forrest Gump", result.Title)
	}
	if result.Year != 1994 {
		t.Errorf("Year: want 1994, got %d", result.Year)
	}
	if result.RuntimeMinutes != 142 {
		t.Errorf("RuntimeMinutes: want 142, got %d", result.RuntimeMinutes)
	}
	if result.PosterPath != "/arw2vcBveW.jpg" {
		t.Errorf("PosterPath: want %q, got %q", "/arw2vcBveW.jpg", result.PosterPath)
	}
	if result.MediaType != "movie" {
		t.Errorf("MediaType: want %q, got %q", "movie", result.MediaType)
	}
	// exact + year, no runtime check: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(result.Confidence, 0.90) {
		t.Errorf("Confidence: want ~0.90, got %f", result.Confidence)
	}
}

func TestTMDBLookupMovie_RuntimeCheckPass(t *testing.T) {
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpSearchBody) },
		"/3/movie/":       func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpDetailBody) },
	}))

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}
	// probe says 144 min — within 5 min of TMDB runtime (142)
	probe := 144 * time.Minute

	result, err := client.LookupMovie(context.Background(), parsed, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// exact + year + runtime: 0.50 + 0.30 + 0.10 + 0.10 = 1.00
	if !approxEqual(result.Confidence, 1.00) {
		t.Errorf("Confidence: want ~1.00, got %f", result.Confidence)
	}
}

func TestTMDBLookupMovie_RuntimeCheckFail(t *testing.T) {
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpSearchBody) },
		"/3/movie/":       func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpDetailBody) },
	}))

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}
	// probe says 90 min — 52 min off from TMDB (142), fails the ±5 min check
	probe := 90 * time.Minute

	result, err := client.LookupMovie(context.Background(), parsed, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// exact + year, no runtime bonus: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(result.Confidence, 0.90) {
		t.Errorf("Confidence: want ~0.90 (no runtime bonus), got %f", result.Confidence)
	}
}

func TestTMDBLookupMovie_FuzzyMatch(t *testing.T) {
	// Query "Forrest" is a subset of the returned title "Forrest Gump" — fuzzy match.
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpSearchBody) },
		"/3/movie/":       func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, forrestGumpDetailBody) },
	}))

	parsed := &ParsedFilename{Title: "Forrest", Year: 1994, MediaType: "movie"}

	result, err := client.LookupMovie(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "Forrest Gump" {
		t.Errorf("Title: want %q, got %q", "Forrest Gump", result.Title)
	}
	// fuzzy + year: 0.50 + 0.10 + 0.10 = 0.70
	if !approxEqual(result.Confidence, 0.70) {
		t.Errorf("Confidence: want ~0.70, got %f", result.Confidence)
	}
}

func TestTMDBLookupMovie_NotFound(t *testing.T) {
	emptyBody := map[string]interface{}{"results": []interface{}{}}
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/movie": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, emptyBody) },
	}))

	parsed := &ParsedFilename{Title: "Nonexistent Film XYZZY 9999", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for no results, got nil")
	}

	var tmdbErr *TMDBError
	if !errors.As(err, &tmdbErr) {
		t.Fatalf("expected *TMDBError, got %T: %v", err, err)
	}
	if tmdbErr.Code != "not_found" {
		t.Errorf("Code: want %q, got %q", "not_found", tmdbErr.Code)
	}
}

func TestTMDBLookupMovie_YearRetry(t *testing.T) {
	searchCount := 0
	client := newMockTMDBClient("validkey", func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/3/search/movie":
			searchCount++
			if r.URL.Query().Get("year") != "" {
				// With year: no results
				return jsonResp(http.StatusOK, map[string]interface{}{"results": []interface{}{}}), nil
			}
			// Without year: result found
			return jsonResp(http.StatusOK, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 680, "title": "Pulp Fiction", "release_date": "1994-10-14", "poster_path": "/d5iIlFn5s0XqsBEmoDe.jpg"},
				},
			}), nil
		default:
			return jsonResp(http.StatusOK, map[string]interface{}{
				"id": 680, "runtime": 154,
			}), nil
		}
	})

	parsed := &ParsedFilename{Title: "Pulp Fiction", Year: 1994, MediaType: "movie"}

	result, err := client.LookupMovie(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchCount != 2 {
		t.Errorf("expected 2 search requests (with year then without), got %d", searchCount)
	}
	if result.Title != "Pulp Fiction" {
		t.Errorf("Title: want %q, got %q", "Pulp Fiction", result.Title)
	}
}

func TestTMDBLookupTV_Fallback(t *testing.T) {
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/tv": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, breakingBadTVSearchBody)
		},
		"/3/tv/": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, breakingBadEpisodeBody)
		},
	}))

	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 1, Episode: 1, MediaType: "tv",
	}

	result, err := client.LookupTV(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MediaType != "tv" {
		t.Errorf("MediaType: want %q, got %q", "tv", result.MediaType)
	}
	if result.TMDBID != 1396 {
		t.Errorf("TMDBID: want 1396, got %d", result.TMDBID)
	}
	if result.Title != "Breaking Bad" {
		t.Errorf("Title: want %q, got %q", "Breaking Bad", result.Title)
	}
	if result.EpisodeTitle != "Pilot" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Pilot", result.EpisodeTitle)
	}
	if result.EpisodeAirDate != "2008-01-20" {
		t.Errorf("EpisodeAirDate: want %q, got %q", "2008-01-20", result.EpisodeAirDate)
	}
	// exact + year + episode: 0.50 + 0.30 + 0.10 + 0.10 = 1.00
	if !approxEqual(result.Confidence, 1.00) {
		t.Errorf("Confidence: want ~1.00, got %f", result.Confidence)
	}
}

func TestTMDBLookupTV_EpisodeNotFound(t *testing.T) {
	client := newMockTMDBClient("validkey", tmdbRouteByPath(map[string]func(*http.Request) *http.Response{
		"/3/search/tv": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, breakingBadTVSearchBody)
		},
		"/3/tv/": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusNotFound, map[string]string{"status_message": "not found"})
		},
	}))

	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 99, Episode: 99, MediaType: "tv",
	}

	result, err := client.LookupTV(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.EpisodeTitle != "" {
		t.Errorf("EpisodeTitle: want empty for not-found episode, got %q", result.EpisodeTitle)
	}
	// exact + year, then -0.10 for missing episode: 0.50 + 0.30 + 0.10 - 0.10 = 0.80
	if !approxEqual(result.Confidence, 0.80) {
		t.Errorf("Confidence: want ~0.80, got %f", result.Confidence)
	}
}

func TestTMDBLookupMovie_InvalidKey(t *testing.T) {
	client := newMockTMDBClient("badkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, map[string]string{"status_message": "Invalid API key"}), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for invalid key, got nil")
	}

	var tmdbErr *TMDBError
	if !errors.As(err, &tmdbErr) {
		t.Fatalf("expected *TMDBError, got %T: %v", err, err)
	}
	if tmdbErr.Code != "invalid_key" {
		t.Errorf("Code: want %q, got %q", "invalid_key", tmdbErr.Code)
	}
	if tmdbErr.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestTMDBLookupMovie_RateLimit(t *testing.T) {
	client := newMockTMDBClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusTooManyRequests, nil), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected rate limit error, got nil")
	}

	var tmdbErr *TMDBError
	if !errors.As(err, &tmdbErr) {
		t.Fatalf("expected *TMDBError, got %T: %v", err, err)
	}
	if tmdbErr.Code != "rate_limit" {
		t.Errorf("Code: want %q, got %q", "rate_limit", tmdbErr.Code)
	}
}

func TestTMDBLookupMovie_EmptyAPIKey(t *testing.T) {
	requestMade := false
	client := newMockTMDBClient("", func(r *http.Request) (*http.Response, error) {
		requestMade = true
		return jsonResp(http.StatusOK, nil), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}

	var tmdbErr *TMDBError
	if !errors.As(err, &tmdbErr) {
		t.Fatalf("expected *TMDBError, got %T: %v", err, err)
	}
	if tmdbErr.Code != "no_api_key" {
		t.Errorf("Code: want %q, got %q", "no_api_key", tmdbErr.Code)
	}
	if requestMade {
		t.Error("HTTP request should not be made when API key is empty")
	}
}

func TestTMDBLookupTV_EmptyAPIKey(t *testing.T) {
	client := NewTMDBClient("", nil)
	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := client.LookupTV(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}

	var tmdbErr *TMDBError
	if !errors.As(err, &tmdbErr) {
		t.Fatalf("expected *TMDBError, got %T: %v", err, err)
	}
	if tmdbErr.Code != "no_api_key" {
		t.Errorf("Code: want %q, got %q", "no_api_key", tmdbErr.Code)
	}
}

func TestSelectBestMovie_ExactMatchWins(t *testing.T) {
	candidates := []tmdbMovieResult{
		{ID: 1, Title: "Forrest Gump Returns", ReleaseDate: "2010-01-01"},
		{ID: 2, Title: "Forrest Gump", ReleaseDate: "1994-07-06"},
	}
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}

	best, score := selectBestMovie(candidates, parsed)

	if best.ID != 2 {
		t.Errorf("expected exact match (id=2) to win, got id=%d (%s)", best.ID, best.Title)
	}
	// exact + year: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(score, 0.90) {
		t.Errorf("score: want ~0.90, got %f", score)
	}
}

// TestTMDBLookupMovie_YearRetry_AvatarFireAndAsh mirrors the real-world case:
// "avatar fire and ash(2025)" parses to year=2025; TMDB returns no results when
// year=2025 is sent, but finds the film when year is omitted.
func TestTMDBLookupMovie_YearRetry_AvatarFireAndAsh(t *testing.T) {
	searchCount := 0
	client := newMockTMDBClient("validkey", func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/3/search/movie":
			searchCount++
			if r.URL.Query().Get("year") != "" {
				// Year-filtered search returns nothing (TMDB behaviour confirmed manually).
				return jsonResp(http.StatusOK, map[string]interface{}{"results": []interface{}{}}), nil
			}
			// No-year search finds the canonical entry.
			return jsonResp(http.StatusOK, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 83533, "title": "Avatar: Fire and Ash", "release_date": "2025-12-19", "poster_path": "/avatar3.jpg"},
				},
			}), nil
		default:
			// Detail fetch for movie 83533.
			return jsonResp(http.StatusOK, map[string]interface{}{"id": 83533, "runtime": 145}), nil
		}
	})

	parsed := &ParsedFilename{Title: "avatar fire and ash", Year: 2025, MediaType: "movie"}

	result, err := client.LookupMovie(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchCount != 2 {
		t.Errorf("expected 2 search requests (with year then without), got %d", searchCount)
	}
	if result.TMDBID != 83533 {
		t.Errorf("TMDBID: want 83533, got %d", result.TMDBID)
	}
	if result.Title != "Avatar: Fire and Ash" {
		t.Errorf("Title: want %q, got %q", "Avatar: Fire and Ash", result.Title)
	}
	if result.Year != 2025 {
		t.Errorf("Year: want 2025, got %d", result.Year)
	}
	if result.RuntimeMinutes != 145 {
		t.Errorf("RuntimeMinutes: want 145, got %d", result.RuntimeMinutes)
	}
	// exact title after punctuation stripping + exact year: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(result.Confidence, 0.90) {
		t.Errorf("Confidence: want ~0.90, got %f", result.Confidence)
	}
}

func TestSelectBestMovie_ColonInTitle(t *testing.T) {
	// TMDB canonical title has a colon; filename never does. After punctuation
	// stripping both normalize to the same string and score as an exact match.
	candidates := []tmdbMovieResult{
		{ID: 83533, Title: "Avatar: Fire and Ash", ReleaseDate: "2025-12-19"},
	}
	parsed := &ParsedFilename{Title: "avatar fire and ash", Year: 2025}

	_, score := selectBestMovie(candidates, parsed)
	// exact (after norm) + exact year: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(score, 0.90) {
		t.Errorf("score: want ~0.90 (colon stripped), got %f", score)
	}
}

func TestSelectBestMovie_YearOffByOne(t *testing.T) {
	// Filename says 1986, film released 1985 — common regional/marketing mismatch.
	candidates := []tmdbMovieResult{
		{ID: 105, Title: "Back to the Future", ReleaseDate: "1985-07-03"},
	}
	parsed := &ParsedFilename{Title: "Back to the Future", Year: 1986}

	_, score := selectBestMovie(candidates, parsed)
	// exact title + ±1 year: 0.50 + 0.30 + 0.05 = 0.85
	if !approxEqual(score, 0.85) {
		t.Errorf("score: want ~0.85 for ±1 year, got %f", score)
	}
}

func TestSelectBestMovie_YearOffByTwo(t *testing.T) {
	candidates := []tmdbMovieResult{
		{ID: 105, Title: "Back to the Future", ReleaseDate: "1985-07-03"},
	}
	parsed := &ParsedFilename{Title: "Back to the Future", Year: 1987}

	_, score := selectBestMovie(candidates, parsed)
	// exact title, year off by 2 — no year bonus: 0.50 + 0.30 = 0.80
	if !approxEqual(score, 0.80) {
		t.Errorf("score: want ~0.80 for year off by 2, got %f", score)
	}
}

func TestSelectBestTV_YearOffByOne(t *testing.T) {
	candidates := []tmdbTVResult{
		{ID: 1396, Name: "Breaking Bad", FirstAirDate: "2008-01-20"},
	}
	parsed := &ParsedFilename{Title: "Breaking Bad", Year: 2009}

	_, score := selectBestTV(candidates, parsed)
	// exact title + ±1 year: 0.50 + 0.30 + 0.05 = 0.85
	if !approxEqual(score, 0.85) {
		t.Errorf("score: want ~0.85 for ±1 year, got %f", score)
	}
}

func TestAbsInt(t *testing.T) {
	cases := [][2]int{{5, 5}, {-5, 5}, {0, 0}, {-142, 142}}
	for _, c := range cases {
		if got := absInt(c[0]); got != c[1] {
			t.Errorf("absInt(%d): want %d, got %d", c[0], c[1], got)
		}
	}
}
