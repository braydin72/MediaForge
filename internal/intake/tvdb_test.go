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

	twdSearchBody = map[string]interface{}{
		"data": []map[string]interface{}{
			{"tvdb_id": "153021", "name": "The Walking Dead", "year": "2010", "network": "AMC"},
		},
	}

	twdS6E4Body = map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 5652985, "name": "Here's Not Here", "aired": "2015-10-25", "seasonNumber": 6, "number": 4},
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
	// exact name + episode found, no episode title in parsed filename → override 0.90
	if !approxEqual(result.Confidence, 0.90) {
		t.Errorf("Confidence: want ~0.90, got %f", result.Confidence)
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
	// exact name (0.40) + year match 2008==2008 (0.05), no episode found (0): 0.45
	if !approxEqual(result.Confidence, 0.45) {
		t.Errorf("Confidence: want ~0.45, got %f", result.Confidence)
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

	// No episode title in parsed → base name+year scoring only (no HTTP).
	best, score := (&TVDBClient{}).selectBestSeries(context.Background(), candidates, parsed)

	if best.TVDBIDStr != "2" {
		t.Errorf("expected exact match to win, got %q", best.Name)
	}
	// selectBestSeries uses the old additive formula for candidate selection only:
	// exact (0.50+0.30) + year (0.10) = 0.90
	if !approxEqual(score, 0.90) {
		t.Errorf("score: want ~0.90, got %f", score)
	}
}

// TestSelectBestSeries_EpisodeNameDisambiguates verifies that when two shows
// share a name, the candidate whose S01E01 episode title matches the filename
// wins — "The Office (2005) S01E01 Pilot" must resolve to the US series
// (S01E01="Pilot"), not the 2001 UK series (S01E01="Downsize").
func TestSelectBestSeries_EpisodeNameDisambiguates(t *testing.T) {
	officeSearchBody := map[string]interface{}{
		"data": []map[string]interface{}{
			{"tvdb_id": "78107", "name": "The Office", "year": "2001", "network": "BBC"},
			{"tvdb_id": "73244", "name": "The Office", "year": "2005", "network": "NBC"},
		},
	}
	ukEp := map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 1, "name": "Downsize", "aired": "2001-07-09", "seasonNumber": 1, "number": 1},
			},
		},
	}
	usEp := map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 2, "name": "Pilot", "aired": "2005-03-24", "seasonNumber": 1, "number": 1},
			},
		},
	}

	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, officeSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/78107/") {
				return jsonResp(http.StatusOK, ukEp)
			}
			return jsonResp(http.StatusOK, usEp)
		},
	}))

	parsed := &ParsedFilename{
		Title:              "The Office",
		Year:               2005,
		IsTV:               true,
		Season:             1,
		Episode:            1,
		ParsedEpisodeTitle: "Pilot",
	}

	result, err := client.Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SeriesID != 73244 {
		t.Errorf("expected US series (73244) to win via episode-name match, got %d", result.SeriesID)
	}
	if result.EpisodeTitle != "Pilot" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Pilot", result.EpisodeTitle)
	}
}

// TestTVDBLookup_EpisodeSeasonQueryParam verifies that fetchEpisode sends the
// season number as a query parameter (not as a path segment) so episodes in
// later seasons are reachable beyond page 0.
func TestTVDBLookup_EpisodeSeasonQueryParam(t *testing.T) {
	var capturedSeason string
	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, breakingBadSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response {
			capturedSeason = r.URL.Query().Get("season")
			ep9Body := map[string]interface{}{
				"data": map[string]interface{}{
					"episodes": []map[string]interface{}{
						{"id": 99901, "name": "Blood Money", "aired": "2013-08-11", "seasonNumber": 5, "number": 9},
					},
				},
			}
			return jsonResp(http.StatusOK, ep9Body)
		},
	}))

	parsed := &ParsedFilename{
		Title: "Breaking Bad", Year: 2008,
		IsTV: true, Season: 5, Episode: 9,
	}

	result, err := client.Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedSeason != "5" {
		t.Errorf("season query param: want %q, got %q", "5", capturedSeason)
	}
	if result.EpisodeTitle != "Blood Money" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Blood Money", result.EpisodeTitle)
	}
}

func twdMockClient() *TVDBClient {
	return newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, twdSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, twdS6E4Body) },
	}))
}

// TestTVDBLookup_TWD_EpisodeYearMatch: year in filename matches episode air year (2015).
// All four components match → confidence 1.0.
func TestTVDBLookup_TWD_EpisodeYearMatch(t *testing.T) {
	parsed := &ParsedFilename{
		Title:              "The Walking Dead",
		Year:               2015,
		IsTV:               true,
		Season:             6,
		Episode:            4,
		ParsedEpisodeTitle: "Here's Not Here",
	}

	result, err := twdMockClient().Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEqual(result.Confidence, 1.0) {
		t.Errorf("Confidence: want 1.0, got %f", result.Confidence)
	}
}

// TestTVDBLookup_TWD_PremiereYearMatch: year in filename matches series premiere year (2010),
// not the episode air year (2015). Year scoring checks EITHER, so this still scores 1.0.
func TestTVDBLookup_TWD_PremiereYearMatch(t *testing.T) {
	parsed := &ParsedFilename{
		Title:              "The Walking Dead",
		Year:               2010,
		IsTV:               true,
		Season:             6,
		Episode:            4,
		ParsedEpisodeTitle: "Here's Not Here",
	}

	result, err := twdMockClient().Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEqual(result.Confidence, 1.0) {
		t.Errorf("Confidence: want 1.0, got %f (year mismatch must not drop a full name+episode+title match)", result.Confidence)
	}
}

// TestTVDBLookup_TWD_NoYearNoTitle: "Walking Dead - S06E04.mp4" — partial name (substring
// of "The Walking Dead"), no year, no episode title in filename → ~0.80.
func TestTVDBLookup_TWD_NoYearNoTitle(t *testing.T) {
	parsed := &ParsedFilename{
		Title:   "Walking Dead",
		Year:    0,
		IsTV:    true,
		Season:  6,
		Episode: 4,
	}

	result, err := twdMockClient().Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEqual(result.Confidence, 0.80) {
		t.Errorf("Confidence: want ~0.80, got %f", result.Confidence)
	}
}

// TestTVDBLookup_ReconcileWrongSeasonEpisode: the filename numbers the episode as
// S48E30 (a streaming service numbered the season wrong), but the episode at S48E30
// is a different title. "Her Last Call" actually lives at S45E12. Reconciliation must
// search the series by episode name, correct the season/episode, and file it there.
func TestTVDBLookup_ReconcileWrongSeasonEpisode(t *testing.T) {
	searchBody := map[string]interface{}{
		"data": []map[string]interface{}{
			{"tvdb_id": "72231", "name": "20/20", "year": "1978", "network": "ABC"},
		},
	}
	// Season-48 fetch returns the wrong-named episode at E30.
	s48Body := map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 1, "name": "I Have Killed For You", "aired": "2026-06-27", "seasonNumber": 48, "number": 30},
			},
		},
	}
	// Full paginated episode list contains "Her Last Call" at S45E12.
	pagedBody := map[string]interface{}{
		"data": map[string]interface{}{
			"episodes": []map[string]interface{}{
				{"id": 1, "name": "I Have Killed For You", "aired": "2026-06-27", "seasonNumber": 48, "number": 30},
				{"id": 2, "name": "Her Last Call", "aired": "2023-03-10", "seasonNumber": 45, "number": 12},
				{"id": 3, "name": "The Reckoning", "aired": "2023-03-17", "seasonNumber": 45, "number": 13},
			},
		},
		"links": map[string]interface{}{"next": ""},
	}

	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, searchBody) },
		"/v4/series": func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/page/") {
				return jsonResp(http.StatusOK, pagedBody)
			}
			return jsonResp(http.StatusOK, s48Body)
		},
	}))

	parsed := &ParsedFilename{
		Title:              "20/20",
		IsTV:               true,
		Season:             48,
		Episode:            30,
		ParsedEpisodeTitle: "Her Last Call",
	}

	result, err := client.Lookup(context.Background(), parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Season != 45 || result.Episode != 12 {
		t.Errorf("season/episode: want S45E12, got S%02dE%02d", result.Season, result.Episode)
	}
	if result.EpisodeTitle != "Her Last Call" {
		t.Errorf("EpisodeTitle: want %q, got %q", "Her Last Call", result.EpisodeTitle)
	}
	if !result.EpisodeFound {
		t.Error("EpisodeFound: want true after reconciliation")
	}
}

// TestSelectBestSeries_PunctuationNormalizedForNameMatch reproduces a real
// production bug: the filename parser turns "20_20" into the title "20 20"
// (space), but TVDB lists the real show as "20/20" (slash). Comparing raw
// lowercased strings (no punctuation stripping) missed the exact-name match,
// so the real show scored the same low base score as unrelated decoys — and a
// decoy whose season 48 doesn't exist at all (a fetchEpisode 404, penalized
// -0.20) outscored the real show, whose season 48 exists but disagrees on
// episode name at E30 (penalized -0.30 for the mismatch). The real show must
// win once name comparison is punctuation-normalized, regardless of episode
// mismatch penalties, so the later episode-name reconciliation step (which
// operates on whichever series selectBestSeries picks) has a chance to run
// against the correct series.
func TestSelectBestSeries_PunctuationNormalizedForNameMatch(t *testing.T) {
	candidates := []tvdbSearchResult{
		// The real show: name has different punctuation than the parsed title,
		// and its season 48 exists but the wrong episode is at E30.
		{TVDBIDStr: "72289", Name: "20/20", Year: "1978"},
		// A decoy with a garbage year that trivially satisfies "seriesYear <=
		// parsed.Year" and has no season 48 at all (fetchEpisode 404s).
		{TVDBIDStr: "353196", Name: "20 Tage im 20. Jahrhundert", Year: "0099"},
	}

	client := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/series": func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/72289/") {
				return jsonResp(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"episodes": []map[string]interface{}{
							{"id": 1, "name": "I Have Killed For You", "aired": "2026-06-27", "seasonNumber": 48, "number": 30},
						},
					},
				})
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}
		},
	}))

	parsed := &ParsedFilename{
		Title:              "20 20",
		Year:               1978,
		IsTV:               true,
		Season:             48,
		Episode:            30,
		ParsedEpisodeTitle: "Her Last Call",
	}

	best, _ := client.selectBestSeries(context.Background(), candidates, parsed)
	if best.TVDBIDStr != "72289" {
		t.Errorf("expected real show (72289) to win despite episode mismatch, got %q (%s)", best.Name, best.TVDBIDStr)
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
