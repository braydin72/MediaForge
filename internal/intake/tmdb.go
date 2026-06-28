package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const tmdbBaseURL = "https://api.themoviedb.org/3"

// TMDBResult holds the matched metadata from a successful TMDB lookup.
// PosterPath is stored as returned by the API (e.g. "/abc123.jpg").
// Prepend "https://image.tmdb.org/t/p/w300" to build a displayable URL.
type TMDBResult struct {
	MediaType      string // "movie" | "tv"
	TMDBID         int
	Title          string
	Year           int
	RuntimeMinutes int    // movie only; 0 for TV
	PosterPath     string // e.g. "/abc123.jpg"
	EpisodeTitle   string // TV only
	EpisodeAirDate string // TV only
	Confidence     float64
}

// TMDBError is a structured TMDB API error with a reason string suitable for
// routing a file to the Review Queue.
type TMDBError struct {
	// Code is one of: "no_api_key", "invalid_key", "rate_limit", "not_found", "api_error"
	Code    string
	Reason  string
	wrapped error
}

func (e *TMDBError) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("tmdb %s: %s: %v", e.Code, e.Reason, e.wrapped)
	}
	return fmt.Sprintf("tmdb %s: %s", e.Code, e.Reason)
}

func (e *TMDBError) Unwrap() error { return e.wrapped }

// TMDBClient performs movie and TV show lookups via the TMDB v3 API.
// It is not safe for concurrent use.
type TMDBClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewTMDBClient creates a TMDBClient. Pass nil for httpClient to use http.DefaultClient.
func NewTMDBClient(apiKey string, httpClient *http.Client) *TMDBClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TMDBClient{apiKey: apiKey, httpClient: httpClient}
}

// LookupMovie searches TMDB for a movie matching parsed.Title and parsed.Year.
// probeDuration is the file duration from ffprobe; pass 0 to skip the runtime
// cross-check. When a year-filtered search returns no results it retries without year.
//
// Confidence scoring:
//   - 0.50 base for any match
//   - +0.30 exact (case-insensitive) title match, +0.10 partial match
//   - +0.10 when parsed.Year matches the release year
//   - +0.10 when probeDuration is within 5 minutes of the TMDB runtime
func (c *TMDBClient) LookupMovie(ctx context.Context, parsed *ParsedFilename, probeDuration time.Duration) (*TMDBResult, error) {
	if c.apiKey == "" {
		return nil, &TMDBError{Code: "no_api_key", Reason: "TMDB API key is not configured"}
	}

	candidates, err := c.searchMovies(ctx, parsed.Title, parsed.Year)
	if err != nil {
		return nil, err
	}

	// When a year-constrained search returns nothing, retry without year.
	if len(candidates) == 0 && parsed.Year > 0 {
		candidates, err = c.searchMovies(ctx, parsed.Title, 0)
		if err != nil {
			return nil, err
		}
	}

	if len(candidates) == 0 {
		return nil, &TMDBError{
			Code:   "not_found",
			Reason: fmt.Sprintf("no TMDB movie found for %q", parsed.Title),
		}
	}

	best, score := selectBestMovie(candidates, parsed)

	detail, err := c.fetchMovieDetail(ctx, best.ID)
	if err != nil {
		return nil, err
	}

	if probeDuration > 0 && detail.Runtime > 0 {
		probeMin := int(probeDuration / time.Minute)
		if absInt(probeMin-detail.Runtime) <= 5 {
			score = min(score+0.10, 1.0)
		}
	}

	return &TMDBResult{
		MediaType:      "movie",
		TMDBID:         best.ID,
		Title:          best.Title,
		Year:           best.releaseYear(),
		RuntimeMinutes: detail.Runtime,
		PosterPath:     best.PosterPath,
		Confidence:     score,
	}, nil
}

// LookupTV searches TMDB for a TV series and optional episode — used as the
// TVDB fallback when TVDB returns no result.
//
// Confidence scoring follows the same scheme as LookupMovie (no runtime check).
// Episode found adds +0.10; episode not found subtracts -0.10.
func (c *TMDBClient) LookupTV(ctx context.Context, parsed *ParsedFilename) (*TMDBResult, error) {
	if c.apiKey == "" {
		return nil, &TMDBError{Code: "no_api_key", Reason: "TMDB API key is not configured"}
	}

	candidates, err := c.searchTV(ctx, parsed.Title)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, &TMDBError{
			Code:   "not_found",
			Reason: fmt.Sprintf("no TMDB TV series found for %q", parsed.Title),
		}
	}

	best, score := selectBestTV(candidates, parsed)

	result := &TMDBResult{
		MediaType:  "tv",
		TMDBID:     best.ID,
		Title:      best.Name,
		Year:       best.firstAirYear(),
		PosterPath: best.PosterPath,
		Confidence: score,
	}

	if parsed.IsTV && parsed.Season > 0 && parsed.Episode > 0 {
		ep, err := c.fetchTVEpisode(ctx, best.ID, parsed.Season, parsed.Episode)
		if err != nil {
			if isTMDBNotFound(err) {
				result.Confidence = max(result.Confidence-0.10, 0)
				return result, nil
			}
			return nil, err
		}
		result.EpisodeTitle = ep.Name
		result.EpisodeAirDate = ep.AirDate
		result.Confidence = min(result.Confidence+0.10, 1.0)
	}

	return result, nil
}

// --- shared HTTP helper ---

// tmdbGet performs a GET against tmdbBaseURL+path with api_key and any extra
// params appended as query string. Callers own resp.Body.
func (c *TMDBClient) tmdbGet(ctx context.Context, path string, params map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tmdbBaseURL+path, nil)
	if err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "failed to build request", wrapped: err}
	}
	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	for k, v := range params {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "TMDB request failed", wrapped: err}
	}
	return resp, nil
}

func checkTMDBStatus(resp *http.Response, label string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return &TMDBError{Code: "invalid_key", Reason: "TMDB API key is invalid"}
	case http.StatusNotFound:
		return &TMDBError{Code: "not_found", Reason: fmt.Sprintf("TMDB %s not found", label)}
	case http.StatusTooManyRequests:
		return &TMDBError{Code: "rate_limit", Reason: "TMDB rate limit exceeded: retry later"}
	default:
		return &TMDBError{Code: "api_error", Reason: fmt.Sprintf("TMDB returned status %d for %s", resp.StatusCode, label)}
	}
}

func isTMDBNotFound(err error) bool {
	tmdbErr, ok := err.(*TMDBError)
	return ok && tmdbErr.Code == "not_found"
}

// --- movie search ---

type tmdbMovieResult struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"` // "YYYY-MM-DD"
	PosterPath  string `json:"poster_path"`
}

func (r *tmdbMovieResult) releaseYear() int {
	if len(r.ReleaseDate) >= 4 {
		y, _ := strconv.Atoi(r.ReleaseDate[:4])
		return y
	}
	return 0
}

func (c *TMDBClient) searchMovies(ctx context.Context, title string, year int) ([]tmdbMovieResult, error) {
	params := map[string]string{"query": title}
	if year > 0 {
		params["year"] = strconv.Itoa(year)
	}

	resp, err := c.tmdbGet(ctx, "/search/movie", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkTMDBStatus(resp, "movie search"); err != nil {
		return nil, err
	}

	var payload struct {
		Results []tmdbMovieResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "failed to parse TMDB movie search response", wrapped: err}
	}
	return payload.Results, nil
}

// --- movie detail ---

type tmdbMovieDetail struct {
	Runtime int `json:"runtime"` // minutes
}

func (c *TMDBClient) fetchMovieDetail(ctx context.Context, id int) (*tmdbMovieDetail, error) {
	resp, err := c.tmdbGet(ctx, fmt.Sprintf("/movie/%d", id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkTMDBStatus(resp, fmt.Sprintf("movie %d", id)); err != nil {
		return nil, err
	}

	var detail tmdbMovieDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "failed to parse TMDB movie detail response", wrapped: err}
	}
	return &detail, nil
}

// --- TV search ---

type tmdbTVResult struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	FirstAirDate string `json:"first_air_date"` // "YYYY-MM-DD"
	PosterPath   string `json:"poster_path"`
}

func (r *tmdbTVResult) firstAirYear() int {
	if len(r.FirstAirDate) >= 4 {
		y, _ := strconv.Atoi(r.FirstAirDate[:4])
		return y
	}
	return 0
}

func (c *TMDBClient) searchTV(ctx context.Context, name string) ([]tmdbTVResult, error) {
	resp, err := c.tmdbGet(ctx, "/search/tv", map[string]string{"query": name})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkTMDBStatus(resp, "TV search"); err != nil {
		return nil, err
	}

	var payload struct {
		Results []tmdbTVResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "failed to parse TMDB TV search response", wrapped: err}
	}
	return payload.Results, nil
}

// --- TV episode detail ---

type tmdbTVEpisode struct {
	Name    string `json:"name"`
	AirDate string `json:"air_date"`
}

func (c *TMDBClient) fetchTVEpisode(ctx context.Context, seriesID, season, episode int) (*tmdbTVEpisode, error) {
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d", seriesID, season, episode)
	resp, err := c.tmdbGet(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkTMDBStatus(resp, fmt.Sprintf("S%02dE%02d of TV series %d", season, episode, seriesID)); err != nil {
		return nil, err
	}

	var ep tmdbTVEpisode
	if err := json.NewDecoder(resp.Body).Decode(&ep); err != nil {
		return nil, &TMDBError{Code: "api_error", Reason: "failed to parse TMDB episode response", wrapped: err}
	}
	return &ep, nil
}

// --- candidate scoring ---

func selectBestMovie(candidates []tmdbMovieResult, parsed *ParsedFilename) (tmdbMovieResult, float64) {
	queryNorm := normTitle(parsed.Title)
	bestIdx, bestScore := 0, -1.0

	for i, m := range candidates {
		score := 0.50
		titleLower := normTitle(m.Title)

		if titleLower == queryNorm {
			score += 0.30
		} else if strings.Contains(titleLower, queryNorm) || strings.Contains(queryNorm, titleLower) {
			score += 0.10
		}

		if parsed.Year > 0 {
			if ry := m.releaseYear(); ry == parsed.Year {
				score += 0.10
			} else if absInt(ry-parsed.Year) == 1 {
				score += 0.05
			}
		}

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	return candidates[bestIdx], bestScore
}

func selectBestTV(candidates []tmdbTVResult, parsed *ParsedFilename) (tmdbTVResult, float64) {
	queryNorm := normTitle(parsed.Title)
	bestIdx, bestScore := 0, -1.0

	for i, s := range candidates {
		score := 0.50
		nameLower := normTitle(s.Name)

		if nameLower == queryNorm {
			score += 0.30
		} else if strings.Contains(nameLower, queryNorm) || strings.Contains(queryNorm, nameLower) {
			score += 0.10
		}

		if parsed.Year > 0 {
			if ay := s.firstAirYear(); ay == parsed.Year {
				score += 0.10
			} else if absInt(ay-parsed.Year) == 1 {
				score += 0.05
			}
		}

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	return candidates[bestIdx], bestScore
}

// normTitle lowercases s, strips punctuation, and collapses whitespace so that
// "Avatar: Fire and Ash" and "avatar fire and ash" compare as equal.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// absInt returns the absolute value of n.
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
