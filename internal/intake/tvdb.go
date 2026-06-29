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

	"github.com/braydin72/mediaforge/internal/logger"
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
	EpisodeFound   bool
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
// The search query is the show name only — year is never sent to the API.
//
// Confidence is computed from four weighted components:
//   - Show name match (fuzzy):             40%
//   - Season + episode exact match:        40%
//   - Episode title match (fuzzy):         15%  (0 when no title in filename)
//   - Year match ±1, premiere or air year:  5%
//
// Override shortcuts (applied after the weighted sum):
//   - Exact name + episode found + title match in filename → 1.0
//   - Exact name + episode found + no title in filename   → 0.90
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

	logger.Debug("TVDB: series search", "query", parsed.Title)
	candidates, err := c.searchSeries(ctx, parsed.Title)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		logger.Debug("TVDB: no series candidates returned", "query", parsed.Title)
		return nil, &TVDBError{
			Code:   "not_found",
			Reason: fmt.Sprintf("no TVDB series found for %q", parsed.Title),
		}
	}

	best, selScore := selectBestSeries(candidates, parsed)
	logger.Debug("TVDB: best series candidate",
		"name", best.Name, "year", best.Year,
		"tvdb_id", best.TVDBIDStr, "selection_score", fmt.Sprintf("%.2f", selScore))

	result := &TVDBResult{
		SeriesID:       best.intID(),
		SeriesName:     best.Name,
		FirstAiredYear: best.parsedYear(),
		Network:        best.Network,
	}

	if parsed.IsTV && parsed.Season > 0 && parsed.Episode > 0 {
		logger.Debug("TVDB: fetching episode",
			"tvdb_id", best.TVDBIDStr, "season", parsed.Season, "episode", parsed.Episode)
		ep, err := c.fetchEpisode(ctx, best.intID(), parsed.Season, parsed.Episode)
		if err != nil {
			if isTVDBNotFound(err) {
				logger.Debug("TVDB: episode not found", "error", err)
			} else {
				return nil, err
			}
		} else {
			result.EpisodeFound = true
			result.EpisodeTitle = ep.Name
			result.EpisodeAirDate = ep.Aired
			logger.Debug("TVDB: episode found", "title", ep.Name, "aired", ep.Aired)
		}
	}

	result.Confidence = computeTVDBConfidence(parsed, result)
	logger.Debug("TVDB: confidence", "score", fmt.Sprintf("%.2f", result.Confidence))
	return result, nil
}

// computeTVDBConfidence scores a TVDB result against the parsed filename using a
// 4-component weighted formula.  See Lookup for the component weights and override rules.
func computeTVDBConfidence(parsed *ParsedFilename, r *TVDBResult) float64 {
	// Component 1: show name match (40%)
	nameSim := stringSimilarity(r.SeriesName, parsed.Title)
	var nameScore float64
	switch {
	case nameSim == 1.0:
		nameScore = 0.40
	case nameSim >= 0.80:
		nameScore = 0.30
	default:
		// Substring containment is a strong signal even when edit-distance similarity
		// is below 0.80 (e.g. "Walking Dead" ⊂ "The Walking Dead", sim ≈ 0.75).
		a := strings.ToLower(r.SeriesName)
		b := strings.ToLower(parsed.Title)
		if strings.Contains(a, b) || strings.Contains(b, a) {
			nameScore = 0.40
		}
	}

	// Component 2: season + episode exact match (40%)
	var seScore float64
	if r.EpisodeFound {
		seScore = 0.40
	}

	// Component 3: episode title match (15%) — only when filename carries a title
	var titleScore float64
	if parsed.ParsedEpisodeTitle != "" && r.EpisodeTitle != "" {
		titleScore = stringSimilarity(r.EpisodeTitle, parsed.ParsedEpisodeTitle) * 0.15
	}

	// Component 4: year match — premiere year OR episode air year, ±1 tolerance (5%)
	var yearScore float64
	if parsed.Year > 0 {
		var airYear int
		if len(r.EpisodeAirDate) >= 4 {
			airYear, _ = strconv.Atoi(r.EpisodeAirDate[:4])
		}
		if (r.FirstAiredYear > 0 && absInt(r.FirstAiredYear-parsed.Year) <= 1) ||
			(airYear > 0 && absInt(airYear-parsed.Year) <= 1) {
			yearScore = 0.05
		}
	}

	confidence := nameScore + seScore + titleScore + yearScore

	// Override shortcuts: exact name + episode found → shortcut based on title presence.
	if nameSim == 1.0 && r.EpisodeFound {
		if parsed.ParsedEpisodeTitle != "" {
			if stringSimilarity(r.EpisodeTitle, parsed.ParsedEpisodeTitle) >= 0.90 {
				return 1.0
			}
		} else {
			if confidence < 0.90 {
				confidence = 0.90
			}
		}
	}

	return min(confidence, 1.0)
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

		// Award the year bonus when the series premiere year <= the filename year.
		// The year in a TV filename is the episode/season year, not the premiere year,
		// so an exact match is not expected — we only need the series to have existed.
		if seriesYear := s.parsedYear(); parsed.Year > 0 && seriesYear > 0 && seriesYear <= parsed.Year {
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

// fetchEpisode retrieves a specific episode from TVDB using the default episode order.
// It filters by season so only that season's episodes are returned (typically 10–24),
// avoiding the page-0-only limit that would miss episodes in later seasons.
func (c *TVDBClient) fetchEpisode(ctx context.Context, seriesID, season, episode int) (*tvdbEpisode, error) {
	url := fmt.Sprintf("%s/v4/series/%d/episodes/default", tvdbBaseURL, seriesID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &TVDBError{Code: "api_error", Reason: "failed to build episode request", wrapped: err}
	}
	q := req.URL.Query()
	q.Set("season", strconv.Itoa(season))
	req.URL.RawQuery = q.Encode()
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
