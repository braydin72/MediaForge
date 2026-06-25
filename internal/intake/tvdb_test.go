package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc adapts a function to the http.RoundTripper interface.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newMockTVDBClient(apiKey string, fn roundTripFunc) *TVDBClient {
	return NewTVDBClient(apiKey, &http.Client{Transport: fn})
}

func jsonResp(status int, v interface{}) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     make(http.Header),
	}
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

// routeByPath dispatches mock responses based on URL path prefix.
func routeByPath(routes map[string]func(*http.Request) *http.Response) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		for prefix, handler := range routes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				return handler(r), nil
			}
		}
		return jsonResp(http.StatusNotFound, map[string]string{"message": "unhandled mock path: " + r.URL.Path}), nil
	}
}

var (
	loginOKBody = map[string]interface{}{
		"data": map[string]string{"token": "test-bearer-token"},
	}

	breakingBadSearchBody = map[string]interface{}{
		"data": []map[string]interface{}{
			{"tvdb_id": "81189", "name": "Breaking Bad", "year": "2008", "network": "AMC"},
		},
	}

	pilotEpisodeBody = map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 349232, "name": "Pilot", "aired": "2008-01-20", "seasonNumber": 1, "number": 1},
				{"id": 349233, "name": "Cat's in the Bag", "aired": "2008-01-27", "seasonNumber": 1, "number": 2},
			},
		},
	}
)

func TestTVDBLookup_Success(t *testing.T) {
	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, breakingBadSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, pilotEpisodeBody) },
	}))

	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 1, Episode: 1,
	}

	result, err := client.Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SeriesID != 81189 {
		t.Errorf("SeriesID: want 81189, got %d", result.SeriesID)
	}
	if result.SeriesName != "Breaking Bad" {
		t.Errorf("SeriesName: want %q, got %q", "Breaking Bad", result.SeriesName)
	}
	if result.FirstAiredYear != 2008 {
		t.Errorf("FirstAiredYear: want 2008, got %d", result.FirstAiredYear)
	}
	if result.Network != "AMC" {
		t.Errorf("Network: want %q, got %q", "AMC", result.Network)
	}
	if result.EpisodeTitle != "Pilot" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Pilot", result.EpisodeTitle)
	}
	if result.EpisodeAirDate != "2008-01-20" {
		t.Errorf("EpisodeAirDate: want %q, got %q", "2008-01-20", result.EpisodeAirDate)
	}
	// exact match + year + episode: 0.50 + 0.30 + 0.10 + 0.10 = 1.00
	if !approxEqual(result.Confidence, 1.00) {
		t.Errorf("Confidence: want ~1.00, got %f", result.Confidence)
	}
}

func TestTVDBLookup_SeriesFoundEpisodeNotFound(t *testing.T) {
	emptyEpisodesBody := map[string]interface{}{
		"data": map[string]interface{}{"episodes": []interface{}{}},
	}

	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, breakingBadSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, emptyEpisodesBody) },
	}))

	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 5, Episode: 16,
	}

	result, err := client.Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SeriesName != "Breaking Bad" {
		t.Errorf("SeriesName: want %q, got %q", "Breaking Bad", result.SeriesName)
	}
	if result.EpisodeTitle != "" {
		t.Errorf("EpisodeTitle: want empty, got %q", result.EpisodeTitle)
	}
	// exact match + year, then -0.10 for missing episode: 0.50 + 0.30 + 0.10 - 0.10 = 0.80
	if !approxEqual(result.Confidence, 0.80) {
		t.Errorf("Confidence: want ~0.80, got %f", result.Confidence)
	}
}

func TestTVDBLookup_NoSeriesMatch(t *testing.T) {
	noResultsBody := map[string]interface{}{"data": []interface{}{}}

	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, noResultsBody) },
	}))

	parsed := &ParsedFilename{
		Title: "Xyzzy Show Nobody Has Heard Of", Year: 2024,
		IsTV: true, Season: 1, Episode: 1,
	}

	_, err := client.Lookup(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for no series match, got nil")
	}

	var tvdbErr *TVDBError
	if !errors.As(err, &tvdbErr) {
		t.Fatalf("expected *TVDBError, got %T: %v", err, err)
	}
	if tvdbErr.Code != "not_found" {
		t.Errorf("Code: want %q, got %q", "not_found", tvdbErr.Code)
	}
	if tvdbErr.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestTVDBLookup_NoSeriesMatch_404(t *testing.T) {
	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusNotFound, nil) },
	}))

	parsed := &ParsedFilename{Title: "Unknown Show", IsTV: true, Season: 1, Episode: 1}

	_, err := client.Lookup(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for 404 search, got nil")
	}

	var tvdbErr *TVDBError
	if !errors.As(err, &tvdbErr) {
		t.Fatalf("expected *TVDBError, got %T", err)
	}
	if tvdbErr.Code != "not_found" {
		t.Errorf("Code: want %q, got %q", "not_found", tvdbErr.Code)
	}
}

func TestTVDBLookup_AuthFailure(t *testing.T) {
	authFailBody := map[string]string{"message": "Invalid API Key"}

	client := newMockTVDBClient("badkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusUnauthorized, authFailBody)
		},
	}))

	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := client.Lookup(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for auth failure, got nil")
	}

	var tvdbErr *TVDBError
	if !errors.As(err, &tvdbErr) {
		t.Fatalf("expected *TVDBError, got %T: %v", err, err)
	}
	if tvdbErr.Code != "auth_failure" {
		t.Errorf("Code: want %q, got %q", "auth_failure", tvdbErr.Code)
	}
	if tvdbErr.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestTVDBLookup_EmptyAPIKey(t *testing.T) {
	requestMade := false
	client := newMockTVDBClient("", func(r *http.Request) (*http.Response, error) {
		requestMade = true
		return jsonResp(http.StatusOK, loginOKBody), nil
	})

	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := client.Lookup(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected error for empty API key, got nil")
	}

	var tvdbErr *TVDBError
	if !errors.As(err, &tvdbErr) {
		t.Fatalf("expected *TVDBError, got %T: %v", err, err)
	}
	if tvdbErr.Code != "no_api_key" {
		t.Errorf("Code: want %q, got %q", "no_api_key", tvdbErr.Code)
	}
	if requestMade {
		t.Error("HTTP request should not be made when API key is empty")
	}
}

func TestTVDBLookup_RateLimit(t *testing.T) {
	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusTooManyRequests, nil) },
	}))

	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	_, err := client.Lookup(context.Background(), parsed)
	if err == nil {
		t.Fatal("expected rate limit error, got nil")
	}

	var tvdbErr *TVDBError
	if !errors.As(err, &tvdbErr) {
		t.Fatalf("expected *TVDBError, got %T: %v", err, err)
	}
	if tvdbErr.Code != "rate_limit" {
		t.Errorf("Code: want %q, got %q", "rate_limit", tvdbErr.Code)
	}
}

func TestTVDBLookup_TokenCached(t *testing.T) {
	loginCount := 0
	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login": func(r *http.Request) *http.Response {
			loginCount++
			return jsonResp(http.StatusOK, loginOKBody)
		},
		"/v4/search": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, breakingBadSearchBody)
		},
		"/v4/series": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, pilotEpisodeBody)
		},
	}))

	parsed := &ParsedFilename{Title: "Breaking Bad", IsTV: true, Season: 1, Episode: 1}

	for i := 0; i < 3; i++ {
		if _, err := client.Lookup(context.Background(), parsed); err != nil {
			t.Fatalf("lookup %d failed: %v", i, err)
		}
	}

	if loginCount != 1 {
		t.Errorf("expected login called once, got %d", loginCount)
	}
}

func TestSelectBestSeries_ExactMatchWins(t *testing.T) {
	candidates := []tvdbSearchResult{
		{TVDBIDStr: "1", Name: "Breaking Bad Spinoff", Year: "2015"},
		{TVDBIDStr: "2", Name: "Breaking Bad", Year: "2008"},
	}
	parsed := &ParsedFilename{Title: "Breaking Bad", Year: 2008}

	best, score := selectBestSeries(candidates, parsed)

	if best.TVDBIDStr != "2" {
		t.Errorf("expected exact match to win, got %q", best.Name)
	}
	// exact + year: 0.50 + 0.30 + 0.10 = 0.90
	if !approxEqual(score, 0.90) {
		t.Errorf("score: want ~0.90, got %f", score)
	}
}

func TestTVDBError_ReviewQueueReason(t *testing.T) {
	codes := []string{"no_api_key", "auth_failure", "rate_limit", "not_found", "api_error"}
	for _, code := range codes {
		e := &TVDBError{Code: code, Reason: "some reason"}
		if e.Reason == "" {
			t.Errorf("code %q: Reason should not be empty", code)
		}
		if e.Error() == "" {
			t.Errorf("code %q: Error() should not be empty", code)
		}
	}
}
