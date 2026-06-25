package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const omdbBaseURL = "http://www.omdbapi.com/"

// OMDbResult holds the matched metadata from a successful OMDb lookup.
type OMDbResult struct {
	MediaType      string // "movie" | "tv"
	Title          string // movie title, or show title for TV (what was queried)
	Year           int
	RuntimeMinutes int    // movie only; 0 for TV
	ImdbID         string // e.g. "tt0109830"
	EpisodeTitle   string // TV only (OMDb returns the episode title in the Title field)
	EpisodeAirDate string // TV only (the Released field, e.g. "20 Jan 2008")
	Confidence     float64
}

// OMDbError is a structured OMDb API error with a reason string suitable for
// routing a file to the Review Queue.
type OMDbError struct {
	// Code is one of: "no_api_key", "invalid_key", "rate_limit", "not_found", "api_error"
	Code    string
	Reason  string
	wrapped error
}

func (e *OMDbError) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("omdb %s: %s: %v", e.Code, e.Reason, e.wrapped)
	}
	return fmt.Sprintf("omdb %s: %s", e.Code, e.Reason)
}

func (e *OMDbError) Unwrap() error { return e.wrapped }

// OMDbClient performs movie and TV episode lookups via the OMDb API.
// It is not safe for concurrent use.
type OMDbClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewOMDbClient creates an OMDbClient. Pass nil for httpClient to use http.DefaultClient.
func NewOMDbClient(apiKey string, httpClient *http.Client) *OMDbClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OMDbClient{apiKey: apiKey, httpClient: httpClient}
}

// LookupMovie fetches a single movie result from OMDb by title and optional year.
// probeDuration is the file duration from ffprobe; pass 0 to skip the runtime cross-check.
//
// Confidence scoring:
//   - 0.50 base for any match
//   - +0.30 exact (case-insensitive) title match, +0.10 partial match
//   - +0.10 when parsed.Year matches the returned release year
//   - +0.10 when probeDuration is within 5 minutes of the OMDb runtime
func (c *OMDbClient) LookupMovie(ctx context.Context, parsed *ParsedFilename, probeDuration time.Duration) (*OMDbResult, error) {
	if c.apiKey == "" {
		return nil, &OMDbError{Code: "no_api_key", Reason: "OMDb API key is not configured"}
	}

	params := map[string]string{
		"t":    parsed.Title,
		"type": "movie",
	}
	if parsed.Year > 0 {
		params["y"] = strconv.Itoa(parsed.Year)
	}

	raw, err := c.omdbGet(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := checkOMDbResponse(raw); err != nil {
		return nil, err
	}

	runtimeMin := parseOMDbRuntime(raw.Runtime)
	respYear := parseOMDbYear(raw.Year)
	score := omdbMovieScore(raw.Title, parsed, respYear, runtimeMin, probeDuration)

	return &OMDbResult{
		MediaType:      "movie",
		Title:          raw.Title,
		Year:           respYear,
		RuntimeMinutes: runtimeMin,
		ImdbID:         raw.ImdbID,
		Confidence:     score,
	}, nil
}

// LookupTV fetches a specific episode from OMDb by show title, season, and episode number.
// Used as last-resort fallback after TVDB and TMDB have both failed.
//
// Confidence scoring:
//   - 0.60 base (implicit show+episode match since OMDb would have returned False otherwise)
//   - +0.10 when parsed.Year matches the episode air year
func (c *OMDbClient) LookupTV(ctx context.Context, parsed *ParsedFilename) (*OMDbResult, error) {
	if c.apiKey == "" {
		return nil, &OMDbError{Code: "no_api_key", Reason: "OMDb API key is not configured"}
	}

	params := map[string]string{
		"t":       parsed.Title,
		"Season":  strconv.Itoa(parsed.Season),
		"Episode": strconv.Itoa(parsed.Episode),
		"type":    "series",
	}

	raw, err := c.omdbGet(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := checkOMDbResponse(raw); err != nil {
		return nil, err
	}

	// TV: show+episode confirmed by a successful response.
	// Year comparison is against the episode air year, which only matches parsed.Year
	// (show first-air year) for S01 episodes.
	score := 0.60
	epYear := parseOMDbYear(raw.Year)
	if parsed.Year > 0 && epYear == parsed.Year {
		score += 0.10
	}

	return &OMDbResult{
		MediaType:      "tv",
		Title:          parsed.Title, // show title (what we queried)
		Year:           epYear,
		ImdbID:         raw.ImdbID,
		EpisodeTitle:   raw.Title,    // OMDb puts the episode title in the Title field
		EpisodeAirDate: raw.Released,
		Confidence:     score,
	}, nil
}

// --- internal HTTP helper ---

// omdbResponse mirrors the OMDb API JSON envelope for both success and error cases.
type omdbResponse struct {
	Response string `json:"Response"` // "True" or "False"
	Error    string `json:"Error"`
	Title    string `json:"Title"`
	Year     string `json:"Year"`
	Runtime  string `json:"Runtime"`  // e.g. "142 min" or "N/A"
	ImdbID   string `json:"imdbID"`   // lowercase 'i' in the OMDb JSON key
	Released string `json:"Released"` // e.g. "20 Jan 2008"
}

func (c *OMDbClient) omdbGet(ctx context.Context, params map[string]string) (*omdbResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, omdbBaseURL, nil)
	if err != nil {
		return nil, &OMDbError{Code: "api_error", Reason: "failed to build request", wrapped: err}
	}
	q := req.URL.Query()
	q.Set("apikey", c.apiKey)
	for k, v := range params {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &OMDbError{Code: "api_error", Reason: "OMDb request failed", wrapped: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &OMDbError{Code: "invalid_key", Reason: "OMDb API key is invalid"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &OMDbError{Code: "api_error", Reason: fmt.Sprintf("OMDb returned status %d", resp.StatusCode)}
	}

	var result omdbResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &OMDbError{Code: "api_error", Reason: "failed to parse OMDb response", wrapped: err}
	}
	return &result, nil
}

// checkOMDbResponse converts a False response into a structured error.
func checkOMDbResponse(r *omdbResponse) error {
	if r.Response == "True" {
		return nil
	}
	lower := strings.ToLower(r.Error)
	switch {
	case strings.Contains(lower, "not found"):
		return &OMDbError{Code: "not_found", Reason: fmt.Sprintf("OMDb: %s", r.Error)}
	case strings.Contains(lower, "invalid api key") || strings.Contains(lower, "no api key"):
		return &OMDbError{Code: "invalid_key", Reason: "OMDb API key is invalid"}
	case strings.Contains(lower, "limit"):
		return &OMDbError{Code: "rate_limit", Reason: "OMDb rate limit exceeded: retry later"}
	default:
		return &OMDbError{Code: "api_error", Reason: fmt.Sprintf("OMDb error: %s", r.Error)}
	}
}

// --- scoring and parsing helpers ---

// omdbMovieScore computes confidence for a movie result.
func omdbMovieScore(returnedTitle string, parsed *ParsedFilename, respYear, runtimeMin int, probeDuration time.Duration) float64 {
	score := 0.50
	titleLower := strings.ToLower(returnedTitle)
	queryLower := strings.ToLower(parsed.Title)

	if titleLower == queryLower {
		score += 0.30
	} else if strings.Contains(titleLower, queryLower) || strings.Contains(queryLower, titleLower) {
		score += 0.10
	}

	if parsed.Year > 0 && respYear == parsed.Year {
		score += 0.10
	}

	if probeDuration > 0 && runtimeMin > 0 {
		probeMin := int(probeDuration / time.Minute)
		if absInt(probeMin-runtimeMin) <= 5 {
			score = min(score+0.10, 1.0)
		}
	}

	return score
}

// parseOMDbRuntime converts "142 min" (or "N/A") to an integer minute count.
func parseOMDbRuntime(s string) int {
	fields := strings.Fields(s) // ["142", "min"] or ["N/A"]
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(fields[0])
	return n
}

// parseOMDbYear converts "1994", "1994-2002", or "2008–" to an integer year.
func parseOMDbYear(s string) int {
	s = strings.TrimSpace(s)
	// Stop at the first hyphen or en-dash (range notation).
	for i, ch := range s {
		if ch == '-' || ch == '–' { // '-' or en-dash
			s = s[:i]
			break
		}
	}
	y, _ := strconv.Atoi(strings.TrimSpace(s))
	return y
}
