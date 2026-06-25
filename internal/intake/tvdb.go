package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	tvdbBaseURL  = "https://api4.thetvdb.com"
	tvdbTokenTTL = 29 * 24 * time.Hour // tokens are valid 30 days; refresh a day early
)

// TVDBResult holds the matched metadata from a successful TVDB lookup.
type TVDBResult struct {
	SeriesID       int
	SeriesName     string
	FirstAiredYear int
	Network        string
	EpisodeTitle   string
	EpisodeAirDate string
	Confidence     float64
}

// TVDBError is a structured TVDB API error. The Reason field is safe to surface
// in the Review Queue as a human-readable failure description.
type TVDBError struct {
	// Code is one of: "no_api_key", "auth_failure", "rate_limit", "not_found", "api_error"
	Code    string
	Reason  string
	wrapped error
}

func (e *TVDBError) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("tvdb %s: %s: %v", e.Code, e.Reason, e.wrapped)
	}
	return fmt.Sprintf("tvdb %s: %s", e.Code, e.Reason)
}

func (e *TVDBError) Unwrap() error { return e.wrapped }

// TVDBClient authenticates with TVDB API v4 and performs series and episode lookups.
// It is not safe for concurrent use.
type TVDBClient struct {
	apiKey     string
	httpClient *http.Client
	token      string
	tokenExpAt time.Time
}

// NewTVDBClient creates a TVDBClient. Pass nil for httpClient to use http.DefaultClient.
func NewTVDBClient(apiKey string, httpClient *http.Client) *TVDBClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TVDBClient{
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

// Lookup searches TVDB for the show, season, and episode encoded in parsed.
// parsed.Year is used for confidence scoring when non-zero.
//
// Confidence scoring:
//   - 0.50 base for any series match
//   - +0.30 for exact (case-insensitive) name match, +0.10 for partial match
//   - +0.10 when parsed.Year matches the series first aired year
//   - +0.10 when the requested episode is found; -0.10 when not found
func (c *TVDBClient) Lookup(ctx context.Context, parsed *ParsedFilename) (*TVDBResult, error) {
	if c.apiKey == "" {
		return nil, &TVDBError{
			Code:   "no_api_key",
			Reason: "TVDB API key is not configured",
		}
	}

	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	candidates, err := c.searchSeries(ctx, parsed.Title)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, &TVDBError{
			Code:   "not_found",
			Reason: fmt.Sprintf("no TVDB series found for %q", parsed.Title),
		}
	}

	best, baseScore := selectBestSeries(candidates, parsed)
	result := &TVDBResult{
		SeriesID:       best.intID(),
		SeriesName:     best.Name,
		FirstAiredYear: best.parsedYear(),
		Network:        best.Network,
		Confidence:     baseScore,
	}

	if parsed.IsTV && parsed.Season > 0 && parsed.Episode > 0 {
		ep, err := c.fetchEpisode(ctx, best.intID(), parsed.Season, parsed.Episode)
		if err != nil {
			if isTVDBNotFound(err) {
				result.Confidence = max(result.Confidence-0.10, 0)
				return result, nil
			}
			return nil, err
		}
		result.EpisodeTitle = ep.Name
		result.EpisodeAirDate = ep.Aired
		result.Confidence = min(result.Confidence+0.10, 1.0)
	}

	return result, nil
}

func isTVDBNotFound(err error) bool {
	tvdbErr, ok := err.(*TVDBError)
	return ok && tvdbErr.Code == "not_found"
}

func (c *TVDBClient) ensureToken(ctx context.Context) error {
	if c.token != "" && time.Now().Before(c.tokenExpAt) {
		return nil
	}
	return c.authenticate(ctx)
}

func (c *TVDBClient) authenticate(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"apikey": c.apiKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tvdbBaseURL+"/v4/login", bytes.NewReader(body))
	if err != nil {
		return &TVDBError{Code: "api_error", Reason: "failed to build login request", wrapped: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &TVDBError{Code: "api_error", Reason: "TVDB login request failed", wrapped: err}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &TVDBError{Code: "auth_failure", Reason: "TVDB authentication failed: check your API key"}
	}
	if resp.StatusCode != http.StatusOK {
		return &TVDBError{Code: "api_error", Reason: fmt.Sprintf("TVDB login returned status %d", resp.StatusCode)}
	}

	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return &TVDBError{Code: "api_error", Reason: "failed to parse TVDB login response", wrapped: err}
	}
	if payload.Data.Token == "" {
		return &TVDBError{Code: "api_error", Reason: "TVDB login response contained no token"}
	}

	c.token = payload.Data.Token
	c.tokenExpAt = time.Now().Add(tvdbTokenTTL)
	return nil
}

// tvdbSearchResult is the per-item shape returned by GET /v4/search.
// tvdb_id is a string in the search response (e.g., "81189").
type tvdbSearchResult struct {
	TVDBIDStr string `json:"tvdb_id"`
	Name      string `json:"name"`
	Year      string `json:"year"`
	Network   string `json:"network"`
}

func (s *tvdbSearchResult) intID() int {
	id, _ := strconv.Atoi(s.TVDBIDStr)
	return id
}

func (s *tvdbSearchResult) parsedYear() int {
	y, _ := strconv.Atoi(s.Year)
	return y
}

func (c *TVDBClient) searchSeries(ctx context.Context, name string) ([]tvdbSearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tvdbBaseURL+"/v4/search", nil)
	if err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "failed to build search request", wrapped: err}
	}
	q := req.URL.Query()
	q.Set("query", name)
	q.Set("type", "series")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "TVDB series search failed", wrapped: err}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, &TVDBError{Code: "auth_failure", Reason: "TVDB authentication expired during search"}
	case http.StatusTooManyRequests:
		return nil, &TVDBError{Code: "rate_limit", Reason: "TVDB rate limit exceeded: retry later"}
	case http.StatusNotFound:
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &TVDBError{Code: "api_error", Reason: fmt.Sprintf("TVDB search returned status %d", resp.StatusCode)}
	}

	var payload struct {
		Data []tvdbSearchResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "failed to parse TVDB search response", wrapped: err}
	}
	return payload.Data, nil
}

// selectBestSeries picks the candidate with the highest base confidence score.
// Episode lookup adjusts the returned score further.
func selectBestSeries(candidates []tvdbSearchResult, parsed *ParsedFilename) (tvdbSearchResult, float64) {
	queryLower := strings.ToLower(parsed.Title)

	bestIdx := 0
	bestScore := -1.0

	for i, s := range candidates {
		score := 0.50
		nameLower := strings.ToLower(s.Name)

		if nameLower == queryLower {
			score += 0.30
		} else if strings.Contains(nameLower, queryLower) || strings.Contains(queryLower, nameLower) {
			score += 0.10
		}

		if parsed.Year > 0 && s.parsedYear() == parsed.Year {
			score += 0.10
		}

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	return candidates[bestIdx], bestScore
}

// tvdbEpisode is the per-episode shape from GET /v4/series/{id}/episodes/default/page/{n}.
type tvdbEpisode struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Aired        string `json:"aired"`
	SeasonNumber int    `json:"seasonNumber"`
	Number       int    `json:"number"`
}

// fetchEpisode retrieves episode metadata from page 0 of the default episode order.
// For series with more than 100 episodes, episodes beyond page 0 are not checked.
func (c *TVDBClient) fetchEpisode(ctx context.Context, seriesID, season, episode int) (*tvdbEpisode, error) {
	url := fmt.Sprintf("%s/v4/series/%d/episodes/default/page/0", tvdbBaseURL, seriesID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "failed to build episode request", wrapped: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "TVDB episode fetch failed", wrapped: err}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, &TVDBError{Code: "auth_failure", Reason: "TVDB authentication expired during episode fetch"}
	case http.StatusTooManyRequests:
		return nil, &TVDBError{Code: "rate_limit", Reason: "TVDB rate limit exceeded: retry later"}
	case http.StatusNotFound:
		return nil, &TVDBError{Code: "not_found", Reason: fmt.Sprintf("TVDB series %d not found", seriesID)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &TVDBError{Code: "api_error", Reason: fmt.Sprintf("TVDB episode fetch returned status %d", resp.StatusCode)}
	}

	var payload struct {
		Data struct {
			Episodes []tvdbEpisode `json:"episodes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "failed to parse TVDB episode response", wrapped: err}
	}

	for i := range payload.Data.Episodes {
		ep := &payload.Data.Episodes[i]
		if ep.SeasonNumber == season && ep.Number == episode {
			return ep, nil
		}
	}

	return nil, &TVDBError{
		Code:   "not_found",
		Reason: fmt.Sprintf("S%02dE%02d not found in TVDB series %d", season, episode, seriesID),
	}
}
