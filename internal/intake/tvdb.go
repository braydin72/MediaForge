package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/braydin72/mediaforge/internal/logger"
)

const (
	tvdbBaseURL  = "https://api4.thetvdb.com"
	tvdbTokenTTL = 29 * 24 * time.Hour // tokens are valid 30 days; refresh a day early

	// multiPartRuntimeTolerancePct is how far a file's probed duration may
	// differ from the sum of two TVDB episodes' listed runtimes and still be
	// accepted as confirmation that the file actually contains both episodes.
	multiPartRuntimeTolerancePct = 15
)

// reMultiPartTitle matches filename episode titles that describe a combined
// multi-part episode (e.g. "Children of the Gods Parts 1 & 2", "Part 1 and 2",
// "Pilot 1-2"). Used as the first-pass signal for detecting a source file that
// numbers a double-length episode as a single episode; see the multi-part
// detection block in Lookup for the full (title + duration) confirmation.
var reMultiPartTitle = regexp.MustCompile(`(?i)\bparts?\s*1\s*(?:&|\+|and|-|to)\s*2\b|\b1\s*&\s*2\b`)

// isMultiPartTitle reports whether title looks like it describes two combined
// episodes (e.g. "Parts 1 & 2") rather than a single episode.
func isMultiPartTitle(title string) bool {
	return reMultiPartTitle.MatchString(title)
}

// reTrailingPartSuffix matches a trailing " (N)" TVDB commonly appends to an
// episode name to disambiguate a title reused across a multi-parter (e.g.
// "Politics (1)" / "Politics (2)"). Filenames from the source almost never
// include this — see stripTrailingPartSuffix.
var reTrailingPartSuffix = regexp.MustCompile(`\s*\(\d+\)\s*$`)

func stripTrailingPartSuffix(s string) string {
	return reTrailingPartSuffix.ReplaceAllString(s, "")
}

// bestTitleSimilarity compares a and b, and also compares each with a
// trailing " (N)" TVDB disambiguator suffix stripped (see
// reTrailingPartSuffix), returning the highest resulting score. Without this,
// a filename's bare "Politics" scores well below TVDB's confidence bar
// against "Politics (1)" even though it's the correct episode — a real
// production case (TVDB episode 85770) that caused a genuinely-matching
// episode to be rejected as unconfident and routed to Review Queue.
//
// It also strips a leading ordinal prefix ("Chapter 2 -", "Episode Two:",
// "Part 3") from one side at a time, and scores a whole-word containment of
// one title inside the other (see tokenContainmentSimilarity). Filename
// "Chapter 2 Great Britain" vs TVDB "Great Britain" previously scored 0.57 on
// raw edit distance, below every match threshold, which let the correct series
// tie with unrelated ones during selection and lose on TVDB's result order.
func bestTitleSimilarity(a, b string) float64 {
	sim := stringSimilarity(a, b)
	try := func(x, y string) {
		if s := stringSimilarity(x, y); s > sim {
			sim = s
		}
	}
	if strippedA := stripTrailingPartSuffix(a); strippedA != a {
		try(strippedA, b)
	}
	if strippedB := stripTrailingPartSuffix(b); strippedB != b {
		try(a, strippedB)
	}
	// Only strip the ordinal prefix from one side: when both carry one
	// ("Chapter 2 X" vs "Chapter 5 X") the numbers are real information and
	// the raw comparison above already accounts for them.
	if strippedA := stripLeadingOrdinalPrefix(a); strippedA != a {
		try(strippedA, b)
	}
	if strippedB := stripLeadingOrdinalPrefix(b); strippedB != b {
		try(a, strippedB)
	}
	if s := tokenContainmentSimilarity(a, b); s > sim {
		sim = s
	}
	return sim
}

// reLeadingOrdinalPrefix matches a leading "Chapter 2", "Episode Two:",
// "Ep. 3 -", "Part IV" style prefix that some sources (and some networks'
// official titles) put in front of the real episode name.
var reLeadingOrdinalPrefix = regexp.MustCompile(`(?i)^\s*(?:chapter|chap\.?|episode|ep\.?|part|pt\.?)\s*` +
	`(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\b` +
	`\s*[:\-_.,–—]*\s*`)

// stripLeadingOrdinalPrefix removes a leading ordinal prefix (see
// reLeadingOrdinalPrefix). When nothing would remain (the whole title is just
// "Part Two" or "Episode 2") the title is returned unchanged.
func stripLeadingOrdinalPrefix(s string) string {
	stripped := reLeadingOrdinalPrefix.ReplaceAllString(s, "")
	if normTitle(stripped) == "" {
		return s
	}
	return stripped
}

// tokenContainmentScore is the similarity assigned when one title appears as
// a contiguous whole-word run inside the other. It is deliberately below the
// 0.90 "confident" bar used for season/episode correction, but above the 0.60
// mismatch bar, so containment alone can confirm an episode at the parsed S/E
// but never relocate a file to a different episode.
const tokenContainmentScore = 0.85

// tokenContainmentSimilarity returns tokenContainmentScore when the shorter
// normalized title (at least two words, so a lone "Pilot" can't match
// anything containing it) appears as a contiguous word sequence in the longer
// one, and 0 otherwise. A shorter title that is nothing but an ordinal
// ("Episode 2", "Chapter Two") never counts — it carries no identifying text.
func tokenContainmentSimilarity(a, b string) float64 {
	ta := strings.Fields(normTitle(normalizeEpisodePartSuffix(a)))
	tb := strings.Fields(normTitle(normalizeEpisodePartSuffix(b)))
	shorter := a
	if len(ta) > len(tb) {
		ta, tb = tb, ta
		shorter = b
	}
	if len(ta) < 2 || len(ta) == len(tb) {
		return 0
	}
	if normTitle(reLeadingOrdinalPrefix.ReplaceAllString(shorter, "")) == "" {
		return 0
	}
	for i := 0; i+len(ta) <= len(tb); i++ {
		match := true
		for j := range ta {
			if tb[i+j] != ta[j] {
				match = false
				break
			}
		}
		if match {
			return tokenContainmentScore
		}
	}
	return 0
}

// reSeriesYearSuffix matches the " (YYYY)" TVDB appends to a series name to
// disambiguate remakes, e.g. "Treasure Island (2026)".
var reSeriesYearSuffix = regexp.MustCompile(`\s*\((\d{4})\)\s*$`)

// splitSeriesYearSuffix returns name without a trailing " (YYYY)"
// disambiguator and the year it carried (0 when there was none).
func splitSeriesYearSuffix(name string) (string, int) {
	m := reSeriesYearSuffix.FindStringSubmatchIndex(name)
	if m == nil {
		return name, 0
	}
	year, _ := strconv.Atoi(name[m[2]:m[3]])
	return name[:m[0]], year
}

// TVDBResult holds the matched metadata from a successful TVDB lookup.
type TVDBResult struct {
	SeriesID       int
	SeriesName     string
	FirstAiredYear int
	Network        string
	EpisodeFound   bool
	Season         int // resolved season (may differ from parsed after reconciliation)
	Episode        int // resolved episode number (may differ from parsed after reconciliation)
	EpisodeTitle   string
	EpisodeAirDate string
	Confidence     float64

	// Episode2 is set when the filename's episode title suggests this single
	// file actually contains two consecutive TVDB episodes (e.g. a combined
	// series-premiere two-parter numbered as one file) and a duration check
	// against TVDB's per-episode runtimes confirmed it. 0 when not applicable.
	Episode2 int
	// MultiPartUnconfirmed is set when the filename's title suggests a
	// combined multi-part episode but the duration check could not confirm
	// it (no runtime data from TVDB, or no probe duration available). The
	// caller should route to Review Queue rather than guess — see Lookup's
	// doc comment.
	MultiPartUnconfirmed bool
	// TitleMismatchUnresolved is set when the filename's episode title didn't
	// match the episode found at the parsed S/E, and reconciliation (adjacent-
	// season check + whole-series title search) failed to find a confident
	// replacement. EpisodeTitle/Season/Episode still reflect the ORIGINAL
	// (mismatched) episode in this case — callers must not treat it as a
	// normal match; route to Review Queue instead.
	TitleMismatchUnresolved bool
	// SeriesAmbiguous is set when the filename carries an episode title and
	// one or more other series candidates scored within seriesAmbiguityMargin
	// of the chosen one, i.e. the pick came down to TVDB's result order rather
	// than evidence. Callers must not treat the result as a normal match.
	SeriesAmbiguous bool
	// Alternates holds other series candidates (each with its episode at the
	// parsed S/E) for the caller to offer an LLM or a human when the match is
	// SeriesAmbiguous or TitleMismatchUnresolved. Empty otherwise.
	Alternates []*TVDBResult
}

// seriesAmbiguityMargin is how close a runner-up's selection score must be to
// the winner's for the series choice to count as ambiguous.
const seriesAmbiguityMargin = 0.05

// maxSeriesAlternates caps how many alternate series are carried on a result.
const maxSeriesAlternates = 5

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
func (c *TVDBClient) Lookup(ctx context.Context, parsed *ParsedFilename, probeDuration time.Duration) (*TVDBResult, error) {
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

	logger.Debug("TVDB: series candidates returned",
		"query", parsed.Title, "count", len(candidates),
		"filename_episode", parsed.ParsedEpisodeTitle,
		"season", parsed.Season, "episode", parsed.Episode)

	scored, validated := c.scoreSeriesCandidates(ctx, candidates, parsed)
	best, selScore := scored[0].s, scored[0].score
	logger.Debug("TVDB: best series candidate",
		"name", best.Name, "year", best.Year,
		"tvdb_id", best.TVDBIDStr, "selection_score", fmt.Sprintf("%.2f", selScore))

	result := &TVDBResult{
		SeriesID:       best.intID(),
		SeriesName:     best.Name,
		FirstAiredYear: best.parsedYear(),
		Network:        best.Network,
	}

	// With an episode title to check against, a near-tie means the pick came
	// down to TVDB's result order, not evidence — flag it rather than guess.
	if validated && len(scored) > 1 && selScore-scored[1].score < seriesAmbiguityMargin {
		result.SeriesAmbiguous = true
		logger.Warn("TVDB: series match ambiguous, top candidates scored within margin",
			"best", best.Name, "best_score", fmt.Sprintf("%.2f", selScore),
			"runner_up", scored[1].s.Name, "runner_up_score", fmt.Sprintf("%.2f", scored[1].score))
	}

	if parsed.IsTV && parsed.Season > 0 && parsed.Episode > 0 {
		var epRuntime int // minutes; tracks whichever episode result currently reflects

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
			result.Season = ep.SeasonNumber
			result.Episode = ep.Number
			result.EpisodeTitle = ep.Name
			result.EpisodeAirDate = ep.Aired
			epRuntime = ep.Runtime
			logger.Debug("TVDB: episode found", "title", ep.Name, "aired", ep.Aired)
		}

		// Episode-name reconciliation: when the filename carries an episode title but the
		// episode at the given S/E is missing or its name disagrees, the source S/E number
		// is likely wrong (e.g. a streaming service that numbers seasons differently). Trust
		// the episode name: search the whole series for it and, on a confident unambiguous
		// hit, correct the season/episode rather than filing under the wrong episode.
		if parsed.ParsedEpisodeTitle != "" {
			mismatch := !result.EpisodeFound ||
				bestTitleSimilarity(result.EpisodeTitle, parsed.ParsedEpisodeTitle) < 0.60
			if mismatch {
				logger.Debug("TVDB: episode name mismatch, searching series for title",
					"series", best.Name, "tvdb_id", best.intID(),
					"filename_episode", parsed.ParsedEpisodeTitle,
					"season_episode_title", result.EpisodeTitle)
				// Try the immediate season neighbors first (cheap: at most 2 requests) —
				// catches a whole show being numbered a season off across the board,
				// which the whole-series scan can miss on shows with many episodes/pages
				// or titles that are only near-identical.
				match, ok := c.findAdjacentSeasonEpisode(ctx, best.intID(), parsed.Season, parsed.Episode, parsed.ParsedEpisodeTitle)
				if !ok {
					match, ok = c.findEpisodeByTitle(ctx, best.intID(), parsed.ParsedEpisodeTitle)
				}
				if ok {
					logger.Info("TVDB: corrected season/episode by episode name",
						"series", best.Name,
						"filename_episode", parsed.ParsedEpisodeTitle,
						"old_season", parsed.Season, "old_episode", parsed.Episode,
						"new_season", match.SeasonNumber, "new_episode", match.Number,
						"matched_title", match.Name)
					result.EpisodeFound = true
					result.Season = match.SeasonNumber
					result.Episode = match.Number
					result.EpisodeTitle = match.Name
					result.EpisodeAirDate = match.Aired
					epRuntime = match.Runtime
				} else {
					// Reconciliation couldn't find a confident replacement. Do NOT let
					// the originally-fetched (mismatched) episode stand as if it were a
					// normal match — it already proved it's the wrong episode; keeping
					// its title/number and scoring it normally previously let a clearly
					// wrong match (e.g. filename "Politics" silently confidence-scored
					// against TVDB's "There But For the Grace of God") sail past the
					// review threshold on name+year+"an episode exists here" alone,
					// with the 15%-weighted title mismatch too small to catch it. Flag
					// it instead so the caller routes to Review Queue — trust the title,
					// and if we can't find where it really belongs, don't guess.
					logger.Warn("TVDB: episode title mismatch could not be resolved, flagging for review",
						"series", best.Name,
						"filename_episode", parsed.ParsedEpisodeTitle,
						"season", result.Season, "episode", result.Episode,
						"unresolved_tvdb_title", result.EpisodeTitle)
					result.TitleMismatchUnresolved = true
				}
			}
		}

		// Combined multi-part episode detection: the filename's episode title
		// (e.g. "Children of the Gods Parts 1 & 2") suggests this one file
		// actually contains two consecutive TVDB episodes, numbered as a single
		// episode by the source. Only act on a text-signal match if a duration
		// check against TVDB's runtimes for episode N and N+1 also confirms it
		// — never guess from the title alone (CLAUDE.md: no silent failures).
		if result.EpisodeFound && parsed.ParsedEpisodeTitle != "" && isMultiPartTitle(parsed.ParsedEpisodeTitle) {
			nextEp, nextErr := c.fetchEpisode(ctx, best.intID(), result.Season, result.Episode+1)
			if nextErr != nil {
				logger.Info("TVDB: possible combined multi-part episode, could not fetch next episode to confirm",
					"series", best.Name, "season", result.Season, "episode", result.Episode, "error", nextErr)
				result.MultiPartUnconfirmed = true
			} else {
				confirmed := false
				if probeDuration > 0 && epRuntime > 0 && nextEp.Runtime > 0 {
					expected := time.Duration(epRuntime+nextEp.Runtime) * time.Minute
					diff := probeDuration - expected
					if diff < 0 {
						diff = -diff
					}
					if diff <= expected*multiPartRuntimeTolerancePct/100 {
						confirmed = true
					}
				}
				if confirmed {
					result.Episode2 = nextEp.Number
					logger.Info("TVDB: confirmed combined multi-part episode",
						"series", best.Name, "season", result.Season, "episode", result.Episode,
						"episode2", nextEp.Number, "probe_duration", probeDuration)
				} else {
					result.MultiPartUnconfirmed = true
					logger.Info("TVDB: possible combined multi-part episode, duration unconfirmed",
						"series", best.Name, "season", result.Season, "episode", result.Episode,
						"next_episode", nextEp.Number, "probe_duration", probeDuration,
						"episode_runtime_min", epRuntime, "next_episode_runtime_min", nextEp.Runtime)
				}
			}
		}
	}

	result.Confidence = computeTVDBConfidence(parsed, result)
	logger.Debug("TVDB: confidence", "score", fmt.Sprintf("%.2f", result.Confidence))

	if result.SeriesAmbiguous || result.TitleMismatchUnresolved {
		result.Alternates = seriesAlternates(parsed, scored, result.TitleMismatchUnresolved)
	}
	return result, nil
}

// seriesAlternates builds alternate results from the runner-up candidates whose
// episode at the parsed S/E was fetched during selection. When the episode
// title couldn't be resolved on the chosen series, any runner-up may be the
// real show, so the top few are offered; when the choice was merely a near-tie,
// only the candidates inside the ambiguity margin are.
func seriesAlternates(parsed *ParsedFilename, scored []scoredSeries, anyRunnerUp bool) []*TVDBResult {
	var alts []*TVDBResult
	for _, sc := range scored[1:] {
		if len(alts) >= maxSeriesAlternates {
			break
		}
		if !anyRunnerUp && scored[0].score-sc.score >= seriesAmbiguityMargin {
			break
		}
		if sc.ep == nil {
			continue
		}
		alt := &TVDBResult{
			SeriesID:       sc.s.intID(),
			SeriesName:     sc.s.Name,
			FirstAiredYear: sc.s.parsedYear(),
			Network:        sc.s.Network,
			EpisodeFound:   true,
			Season:         sc.ep.SeasonNumber,
			Episode:        sc.ep.Number,
			EpisodeTitle:   sc.ep.Name,
			EpisodeAirDate: sc.ep.Aired,
		}
		alt.Confidence = computeTVDBConfidence(parsed, alt)
		alts = append(alts, alt)
	}
	return alts
}

// computeTVDBConfidence scores a TVDB result against the parsed filename using a
// 4-component weighted formula.  See Lookup for the component weights and override rules.
func computeTVDBConfidence(parsed *ParsedFilename, r *TVDBResult) float64 {
	// Component 1: show name match (40%). TVDB's " (YYYY)" remake
	// disambiguator isn't part of the show's name, so it's compared without it.
	seriesName, _ := splitSeriesYearSuffix(r.SeriesName)
	nameSim := stringSimilarity(seriesName, parsed.Title)
	var nameScore float64
	switch {
	case nameSim == 1.0:
		nameScore = 0.40
	case nameSim >= 0.80:
		nameScore = 0.30
	default:
		// Substring containment is a strong signal even when edit-distance similarity
		// is below 0.80 (e.g. "Walking Dead" ⊂ "The Walking Dead", sim ≈ 0.75).
		a := strings.ToLower(seriesName)
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
		titleScore = bestTitleSimilarity(r.EpisodeTitle, parsed.ParsedEpisodeTitle) * 0.15
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
			if bestTitleSimilarity(r.EpisodeTitle, parsed.ParsedEpisodeTitle) >= 0.90 {
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

// baseSeriesScore scores a candidate on show name and premiere year only.
// queryNorm must already be normalized via normTitle — comparing raw lowercased
// strings misses matches like "20/20" vs "20 20" (punctuation-only differences),
// which let unrelated candidates with a bogus/garbage year outscore the real show.
func baseSeriesScore(s tvdbSearchResult, parsed *ParsedFilename, queryNorm string) float64 {
	score := 0.50
	nameNorm := normTitle(s.Name)
	// TVDB disambiguates remakes by appending the premiere year to the name
	// ("Treasure Island (2026)"), which used to cost the real show its exact-name
	// bonus. When that suffix year is the filename's year, the name is exactly
	// what the filename said ("Treasure Island (2026)") and scores as an exact
	// match. Otherwise it scores just below one, so an un-suffixed original still
	// edges out remakes when nothing else (year, episode title) tells them apart.
	baseName, nameYear := splitSeriesYearSuffix(s.Name)

	switch {
	case nameNorm == queryNorm:
		score += 0.30
	case nameYear > 0 && normTitle(baseName) == queryNorm && nameYear == parsed.Year:
		score += 0.30
	case nameYear > 0 && normTitle(baseName) == queryNorm:
		score += 0.25
	case strings.Contains(nameNorm, queryNorm) || strings.Contains(queryNorm, nameNorm):
		score += 0.10
	}

	// Award the year bonus when the series premiere year <= the filename year.
	// The year in a TV filename is the episode/season year, not the premiere year,
	// so an exact match is not expected — we only need the series to have existed.
	// Guard against implausible/garbage years (e.g. a mis-parsed "0099") getting a
	// free bonus just because they're trivially <= the filename year.
	seriesYear := s.parsedYear()
	if parsed.Year > 0 && seriesYear >= 1900 && seriesYear <= parsed.Year {
		score += 0.10
	}
	// An exact year match is additional evidence on top of that: when the
	// filename's year is the premiere year (the usual "Show (YYYY)" convention),
	// a remake from that year beats older same-named series instead of tying.
	if parsed.Year > 0 && (seriesYear == parsed.Year || nameYear == parsed.Year) {
		score += 0.10
	}

	return score
}

// scoredSeries is one series candidate with its selection score and, when the
// episode title was validated, the episode TVDB has at the parsed S/E.
type scoredSeries struct {
	s     tvdbSearchResult
	score float64
	ep    *tvdbEpisode // nil when not fetched or the fetch failed
	epSim float64
}

// selectBestSeries picks the candidate with the highest confidence score.
// See scoreSeriesCandidates for how candidates are scored.
func (c *TVDBClient) selectBestSeries(ctx context.Context, candidates []tvdbSearchResult, parsed *ParsedFilename) (tvdbSearchResult, float64) {
	scored, _ := c.scoreSeriesCandidates(ctx, candidates, parsed)
	return scored[0].s, scored[0].score
}

// scoreSeriesCandidates scores every candidate and returns them best first.
// The second return value reports whether episode titles were validated.
// candidates must be non-empty.
//
// When the filename carries an episode title (and a season/episode number), the
// episode name is the primary differentiator between similarly-named shows: for
// each candidate the target episode is fetched from TVDB and its name compared
// to the filename's episode title. A close match strongly boosts the candidate;
// a mismatch (or a missing episode) penalizes it. This is what distinguishes
// e.g. "The Office (2005) S01E01 Pilot" (US, S01E01="Pilot") from the 2001 UK
// series (S01E01="Downsize").
//
// The base name+year score is used alone when no episode title is available.
// Equal scores are ordered by episode-title similarity, then by TVDB's order.
func (c *TVDBClient) scoreSeriesCandidates(ctx context.Context, candidates []tvdbSearchResult, parsed *ParsedFilename) ([]scoredSeries, bool) {
	queryNorm := normTitle(parsed.Title)
	validateEpisode := parsed.IsTV && parsed.Season > 0 && parsed.Episode > 0 && parsed.ParsedEpisodeTitle != ""

	scored := make([]scoredSeries, 0, len(candidates))
	for _, s := range candidates {
		sc := scoredSeries{s: s, score: baseSeriesScore(s, parsed, queryNorm)}

		if validateEpisode {
			ep, err := c.fetchEpisode(ctx, s.intID(), parsed.Season, parsed.Episode)
			switch {
			case err != nil:
				// Missing episode is evidence against this being the right series.
				sc.score -= 0.20
				logger.Debug("TVDB: candidate episode fetch failed, penalizing",
					"candidate", s.Name, "year", s.Year, "tvdb_id", s.TVDBIDStr,
					"season", parsed.Season, "episode", parsed.Episode, "error", err)
			default:
				sim := bestTitleSimilarity(ep.Name, parsed.ParsedEpisodeTitle)
				var epAdj float64
				switch {
				case sim >= 0.90:
					epAdj = 0.40 // exact / near-exact episode name match
				case sim >= 0.60:
					epAdj = 0.15 // partial match
				default:
					epAdj = -0.30 // episode name disagrees → wrong series
				}
				sc.score += epAdj
				sc.ep, sc.epSim = ep, sim
				logger.Debug("TVDB: candidate episode name compared",
					"candidate", s.Name, "year", s.Year, "tvdb_id", s.TVDBIDStr,
					"filename_episode", parsed.ParsedEpisodeTitle, "tvdb_episode", ep.Name,
					"similarity", fmt.Sprintf("%.2f", sim),
					"episode_score_adj", fmt.Sprintf("%+.2f", epAdj))
			}
		}

		logger.Debug("TVDB: candidate scored",
			"candidate", s.Name, "year", s.Year, "tvdb_id", s.TVDBIDStr,
			"score", fmt.Sprintf("%.2f", sc.score))
		scored = append(scored, sc)
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].epSim > scored[j].epSim
	})
	return scored, validateEpisode
}

// findAdjacentSeasonEpisode checks season+1 and season-1 (same episode number)
// for a strong title match. Some sources number an entire show's seasons off by
// one across the board (e.g. a syndication numbering that doesn't match TVDB's
// season boundaries) — trying the immediate neighbors first is cheap (at most 2
// requests) and reliably catches that common case.
func (c *TVDBClient) findAdjacentSeasonEpisode(ctx context.Context, seriesID, season, episode int, title string) (*tvdbEpisode, bool) {
	for _, s := range []int{season + 1, season - 1} {
		if s <= 0 {
			continue
		}
		ep, err := c.fetchEpisode(ctx, seriesID, s, episode)
		if err != nil {
			continue
		}
		if sim := bestTitleSimilarity(ep.Name, title); sim >= 0.90 {
			logger.Debug("TVDB: adjacent-season episode name match",
				"series_id", seriesID, "tried_season", s, "episode", episode,
				"tvdb_episode", ep.Name, "similarity", fmt.Sprintf("%.2f", sim))
			return ep, true
		}
	}
	return nil, false
}

// tvdbEpisode is the per-episode shape from GET /v4/series/{id}/episodes/default?page={n}.
type tvdbEpisode struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Aired        string `json:"aired"`
	SeasonNumber int    `json:"seasonNumber"`
	Number       int    `json:"number"`
	Runtime      int    `json:"runtime"` // minutes; 0 if TVDB has no runtime data for this episode
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

// findEpisodeByTitle searches every episode of a series for one whose name matches
// title, paginating GET /v4/series/{id}/episodes/default?page={n}. It returns the
// single best match only when its similarity is strong (>= 0.90) and unambiguous
// (clearly ahead of the runner-up) — otherwise ok is false and the caller should
// fall back to the Review Queue rather than guess. Special/season-0 episodes are
// ignored so they don't shadow the real numbered episode.
func (c *TVDBClient) findEpisodeByTitle(ctx context.Context, seriesID int, title string) (*tvdbEpisode, bool) {
	const maxPages = 20

	var best tvdbEpisode
	bestSim, runnerUpSim := -1.0, -1.0
	pagesScanned, episodesScanned := 0, 0

	for page := 0; page < maxPages; page++ {
		url := fmt.Sprintf("%s/v4/series/%d/episodes/default", tvdbBaseURL, seriesID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			logger.Debug("TVDB: findEpisodeByTitle request build failed", "series_id", seriesID, "page", page, "error", err)
			return nil, false
		}
		q := req.URL.Query()
		q.Set("page", strconv.Itoa(page))
		req.URL.RawQuery = q.Encode()
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			logger.Debug("TVDB: findEpisodeByTitle request failed", "series_id", seriesID, "page", page, "error", err)
			return nil, false
		}
		var payload struct {
			Data struct {
				Episodes []tvdbEpisode `json:"episodes"`
			} `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if resp.StatusCode != http.StatusOK {
			logger.Debug("TVDB: findEpisodeByTitle non-200 response", "series_id", seriesID, "page", page, "status", resp.StatusCode)
			resp.Body.Close()
			return nil, false
		}
		decErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if decErr != nil {
			logger.Debug("TVDB: findEpisodeByTitle response decode failed", "series_id", seriesID, "page", page, "error", decErr)
			return nil, false
		}

		pagesScanned++
		episodesScanned += len(payload.Data.Episodes)
		for i := range payload.Data.Episodes {
			ep := &payload.Data.Episodes[i]
			if ep.SeasonNumber <= 0 || ep.Name == "" {
				continue // skip specials / unnamed
			}
			sim := bestTitleSimilarity(ep.Name, title)
			switch {
			case sim > bestSim:
				runnerUpSim = bestSim
				best, bestSim = *ep, sim
			case sim > runnerUpSim:
				runnerUpSim = sim
			}
		}

		if payload.Links.Next == "" || len(payload.Data.Episodes) == 0 {
			break
		}
	}

	// Require a strong match that is clearly ahead of the next-best candidate so we
	// don't silently pick one of several similarly-named episodes.
	if bestSim >= 0.90 && (runnerUpSim < 0 || bestSim-runnerUpSim >= 0.10) {
		return &best, true
	}
	logger.Debug("TVDB: findEpisodeByTitle no confident match",
		"series_id", seriesID, "title", title,
		"pages_scanned", pagesScanned, "episodes_scanned", episodesScanned,
		"best_match", best.Name, "best_similarity", fmt.Sprintf("%.2f", bestSim),
		"runner_up_similarity", fmt.Sprintf("%.2f", runnerUpSim))
	return nil, false
}
