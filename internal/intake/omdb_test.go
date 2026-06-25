package intake

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// newMockOMDbClient constructs an OMDbClient backed by a roundTripFunc mock.
// roundTripFunc and jsonResp are defined in tvdb_test.go (same package).
func newMockOMDbClient(apiKey string, fn roundTripFunc) *OMDbClient {
	return NewOMDbClient(apiKey, &http.Client{Transport: fn})
}

var (
	forrestGumpOMDb = map[string]interface{}{
		"Response": "True",
		"Title":    "Forrest Gump",
		"Year":     "1994",
		"Runtime":  "142 min",
		"imdbID":   "tt0109830",
		"Released": "06 Jul 1994",
	}
	breakingBadPilotOMDb = map[string]interface{}{
		"Response": "True",
		"Title":    "Pilot",       // episode title in the Title field
		"Year":     "2008",
		"Runtime":  "58 min",
		"imdbID":   "tt0959621",
		"Released": "20 Jan 2008",
	}
	notFoundOMDb = map[string]interface{}{
		"Response": "False",
		"Error":    "Movie not found!",
	}
	invalidKeyOMDb = map[string]interface{}{
		"Response": "False",
		"Error":    "Invalid API key!.",
	}
	rateLimitOMDb = map[string]interface{}{
		"Response": "False",
		"Error":    "Request limit reached! The daily limit is 1,000 requests per day for this API key.",
	}
)

func TestOMDbLookupMovie_Found(t *testing.T) {
	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}

	result, err := client.LookupMovie(context.Background(), parsed, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	if result.ImdbID != "tt0109830" {
		t.Errorf("ImdbID: want %q, got %q", "tt0109830", result.ImdbID)
	}
	if result.MediaType != "movie" {
		t.Errorf("MediaType: want %q, got %q", "movie", result.MediaType)
	}
	// exact + year, no runtime check: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(result.Confidence, 0.90) {
		t.Errorf("Confidence: want ~0.90, got %f", result.Confidence)
	}
}

func TestOMDbLookupMovie_RuntimeCheckPass(t *testing.T) {
	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}
	// probe 144 min — within 5 min of OMDb runtime (142)
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

func TestOMDbLookupMovie_NotFound(t *testing.T) {
	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, notFoundOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Nonexistent XYZZY 9999", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for not found, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "not_found" {
		t.Errorf("Code: want %q, got %q", "not_found", omdbErr.Code)
	}
	if omdbErr.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestOMDbLookupTV_EpisodeFound(t *testing.T) {
	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, breakingBadPilotOMDb), nil
	})

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
	if result.Title != "Breaking Bad" {
		t.Errorf("Title (show): want %q, got %q", "Breaking Bad", result.Title)
	}
	if result.EpisodeTitle != "Pilot" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Pilot", result.EpisodeTitle)
	}
	if result.EpisodeAirDate != "20 Jan 2008" {
		t.Errorf("EpisodeAirDate: want %q, got %q", "20 Jan 2008", result.EpisodeAirDate)
	}
	if result.ImdbID != "tt0959621" {
		t.Errorf("ImdbID: want %q, got %q", "tt0959621", result.ImdbID)
	}
	// base 0.60 + year match 0.10 = 0.70
	if !approxEqual(result.Confidence, 0.70) {
		t.Errorf("Confidence: want ~0.70, got %f", result.Confidence)
	}
}

func TestOMDbLookupTV_NotFound(t *testing.T) {
	tvNotFound := map[string]interface{}{
		"Response": "False",
		"Error":    "Series or episode not found!",
	}

	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, tvNotFound), nil
	})

	parsed := &ParsedFilename{Title: "Nonexistent Show XYZZY", IsTV: true, Season: 1, Episode: 1}

	_, err := client.LookupTV(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for not found, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "not_found" {
		t.Errorf("Code: want %q, got %q", "not_found", omdbErr.Code)
	}
}

func TestOMDbLookupMovie_InvalidKey(t *testing.T) {
	client := newMockOMDbClient("badkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, invalidKeyOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for invalid key, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "invalid_key" {
		t.Errorf("Code: want %q, got %q", "invalid_key", omdbErr.Code)
	}
}

func TestOMDbLookupMovie_InvalidKey_HTTP401(t *testing.T) {
	client := newMockOMDbClient("badkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, nil), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "invalid_key" {
		t.Errorf("Code: want %q, got %q", "invalid_key", omdbErr.Code)
	}
}

func TestOMDbLookupMovie_RateLimit(t *testing.T) {
	client := newMockOMDbClient("validkey", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, rateLimitOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected rate limit error, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "rate_limit" {
		t.Errorf("Code: want %q, got %q", "rate_limit", omdbErr.Code)
	}
}

func TestOMDbLookupMovie_EmptyAPIKey(t *testing.T) {
	requestMade := false
	client := newMockOMDbClient("", func(r *http.Request) (*http.Response, error) {
		requestMade = true
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", MediaType: "movie"}

	_, err := client.LookupMovie(context.Background(), parsed, 0)
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "no_api_key" {
		t.Errorf("Code: want %q, got %q", "no_api_key", omdbErr.Code)
	}
	if requestMade {
		t.Error("HTTP request should not be made when API key is empty")
	}
}

func TestOMDbLookupTV_EmptyAPIKey(t *testing.T) {
	client := NewOMDbClient("", nil)
	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := client.LookupTV(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}

	var omdbErr *OMDbError
	if !errors.As(err, &omdbErr) {
		t.Fatalf("expected *OMDbError, got %T: %v", err, err)
	}
	if omdbErr.Code != "no_api_key" {
		t.Errorf("Code: want %q, got %q", "no_api_key", omdbErr.Code)
	}
}

func TestParseOMDbRuntime(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"136 min", 136},
		{"142 min", 142},
		{"58 min", 58},
		{"N/A", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseOMDbRuntime(c.input); got != c.want {
			t.Errorf("parseOMDbRuntime(%q): want %d, got %d", c.input, c.want, got)
		}
	}
}

func TestParseOMDbYear(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"1994", 1994},
		{"2008", 2008},
		{"1994-2002", 1994},
		{"2008–", 2008}, // en-dash for ongoing series
		{"N/A", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseOMDbYear(c.input); got != c.want {
			t.Errorf("parseOMDbYear(%q): want %d, got %d", c.input, c.want, got)
		}
	}
}

func TestOMDbMovieScore_ExactMatchYearRuntime(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994}
	probe := 144 * time.Minute // within 5 min of 142
	score := omdbMovieScore("Forrest Gump", parsed, 1994, 142, probe)
	// exact + year + runtime: 0.50 + 0.30 + 0.10 + 0.10 = 1.00
	if !approxEqual(score, 1.00) {
		t.Errorf("score: want ~1.00, got %f", score)
	}
}

func TestOMDbMovieScore_FuzzyNoRuntime(t *testing.T) {
	parsed := &ParsedFilename{Title: "Forrest", Year: 1994}
	score := omdbMovieScore("Forrest Gump", parsed, 1994, 142, 0)
	// fuzzy + year, no runtime: 0.50 + 0.10 + 0.10 = 0.70
	if !approxEqual(score, 0.70) {
		t.Errorf("score: want ~0.70, got %f", score)
	}
}

func TestOMDbLookupMovie_QueryParamsSet(t *testing.T) {
	var capturedQuery string
	client := newMockOMDbClient("mykey", func(r *http.Request) (*http.Response, error) {
		capturedQuery = r.URL.RawQuery
		return jsonResp(http.StatusOK, forrestGumpOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Forrest Gump", Year: 1994, MediaType: "movie"}
	client.LookupMovie(context.Background(), parsed, 0) //nolint:errcheck

	for _, want := range []string{"apikey=mykey", "t=Forrest+Gump", "y=1994", "type=movie"} {
		if !containsParam(capturedQuery, want) {
			t.Errorf("query %q missing expected param %q", capturedQuery, want)
		}
	}
}

func TestOMDbLookupTV_QueryParamsSet(t *testing.T) {
	var capturedQuery string
	client := newMockOMDbClient("mykey", func(r *http.Request) (*http.Response, error) {
		capturedQuery = r.URL.RawQuery
		return jsonResp(http.StatusOK, breakingBadPilotOMDb), nil
	})

	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}
	client.LookupTV(context.Background(), parsed) //nolint:errcheck

	for _, want := range []string{"apikey=mykey", "Season=1", "Episode=1", "type=series"} {
		if !containsParam(capturedQuery, want) {
			t.Errorf("query %q missing expected param %q", capturedQuery, want)
		}
	}
}

// containsParam checks that a raw query string contains a key=value pair.
func containsParam(rawQuery, pair string) bool {
	for _, part := range splitQuery(rawQuery) {
		if part == pair {
			return true
		}
	}
	return false
}

func splitQuery(q string) []string {
	if q == "" {
		return nil
	}
	var out []string
	for _, p := range splitAmpersand(q) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitAmpersand(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
