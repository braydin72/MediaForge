package intake

import (
	"context"
	"strings"
	"time"
)

// LookupResult is the unified output from the orchestrator, normalised from
// whichever source (TVDB, TMDB, OMDb) provided the winning result.
type LookupResult struct {
	Source         string // "tvdb" | "tmdb" | "omdb"
	MediaType      string // "movie" | "tv"
	Title          string // show or movie title
	Year           int
	RuntimeMinutes int    // movie only; 0 for TV
	EpisodeTitle   string // TV only
	EpisodeAirDate string // TV only
	ImdbID         string // populated from OMDb
	TMDBId         int    // populated from TMDB
	TVDBSeriesID   int    // populated from TVDB
	TVDBNetwork    string // populated from TVDB
	PosterPath     string // populated from TMDB
	Confidence     float64
}

// NoMatchError is returned when all configured lookup sources fail or produce
// no usable candidates. Reasons contains one entry per failed source.
type NoMatchError struct {
	Reasons []string
}

func (e *NoMatchError) Error() string {
	return "no metadata match found: " + strings.Join(e.Reasons, "; ")
}

// Orchestrator runs the tiered metadata lookup chain. Any client field may be
// nil — that source is silently skipped.
type Orchestrator struct {
	TVDB *TVDBClient
	TMDB *TMDBClient
	OMDb *OMDbClient
}

// NewOrchestrator creates a lookup orchestrator. Pass nil for any client whose
// API key is not configured.
func NewOrchestrator(tvdb *TVDBClient, tmdb *TMDBClient, omdb *OMDbClient) *Orchestrator {
	return &Orchestrator{TVDB: tvdb, TMDB: tmdb, OMDb: omdb}
}

// LookupMovie runs TMDB → OMDb. It stops as soon as a result meets or exceeds
// reviewThreshold. If no source meets the threshold, the best candidate is
// returned. If every source errors (no candidates found), NoMatchError is returned.
func (o *Orchestrator) LookupMovie(ctx context.Context, parsed *ParsedFilename, probeDuration time.Duration, reviewThreshold float64) (*LookupResult, error) {
	var best *LookupResult
	var reasons []string

	if o.TMDB != nil {
		raw, err := o.TMDB.LookupMovie(ctx, parsed, probeDuration)
		if err != nil {
			reasons = append(reasons, "TMDB: "+err.Error())
		} else {
			r := fromTMDBMovie(raw, parsed, probeDuration)
			if r.Confidence >= reviewThreshold {
				return r, nil
			}
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}
	}

	if o.OMDb != nil {
		raw, err := o.OMDb.LookupMovie(ctx, parsed, probeDuration)
		if err != nil {
			reasons = append(reasons, "OMDb: "+err.Error())
		} else {
			r := fromOMDbMovie(raw, parsed, probeDuration)
			if r.Confidence >= reviewThreshold {
				return r, nil
			}
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}
	}

	if best != nil {
		return best, nil
	}
	return nil, &NoMatchError{Reasons: reasons}
}

// LookupTV runs TVDB → TMDB → OMDb. Same stop/fallback semantics as LookupMovie.
func (o *Orchestrator) LookupTV(ctx context.Context, parsed *ParsedFilename, reviewThreshold float64) (*LookupResult, error) {
	var best *LookupResult
	var reasons []string

	if o.TVDB != nil {
		raw, err := o.TVDB.Lookup(ctx, parsed)
		if err != nil {
			reasons = append(reasons, "TVDB: "+err.Error())
		} else {
			r := fromTVDB(raw, parsed)
			if r.Confidence >= reviewThreshold {
				return r, nil
			}
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}
	}

	if o.TMDB != nil {
		raw, err := o.TMDB.LookupTV(ctx, parsed)
		if err != nil {
			reasons = append(reasons, "TMDB: "+err.Error())
		} else {
			r := fromTMDBTV(raw, parsed)
			if r.Confidence >= reviewThreshold {
				return r, nil
			}
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}
	}

	if o.OMDb != nil {
		raw, err := o.OMDb.LookupTV(ctx, parsed)
		if err != nil {
			reasons = append(reasons, "OMDb: "+err.Error())
		} else {
			r := fromOMDbTV(raw, parsed)
			if r.Confidence >= reviewThreshold {
				return r, nil
			}
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}
	}

	if best != nil {
		return best, nil
	}
	return nil, &NoMatchError{Reasons: reasons}
}

// --- result converters ---
// Each converter translates a client-specific result into a LookupResult and
// recomputes confidence using the unified scorer.

func fromTVDB(r *TVDBResult, parsed *ParsedFilename) *LookupResult {
	score := ScoreTV(parsed, ScoreInput{
		Title:        r.SeriesName,
		Year:         r.FirstAiredYear,
		EpisodeFound: r.EpisodeTitle != "",
	})
	return &LookupResult{
		Source:         "tvdb",
		MediaType:      "tv",
		Title:          r.SeriesName,
		Year:           r.FirstAiredYear,
		TVDBSeriesID:   r.SeriesID,
		TVDBNetwork:    r.Network,
		EpisodeTitle:   r.EpisodeTitle,
		EpisodeAirDate: r.EpisodeAirDate,
		Confidence:     score,
	}
}

func fromTMDBMovie(r *TMDBResult, parsed *ParsedFilename, probeDuration time.Duration) *LookupResult {
	score := ScoreMovie(parsed, ScoreInput{
		Title:      r.Title,
		Year:       r.Year,
		RuntimeMin: r.RuntimeMinutes,
	}, probeDuration)
	return &LookupResult{
		Source:         "tmdb",
		MediaType:      "movie",
		Title:          r.Title,
		Year:           r.Year,
		RuntimeMinutes: r.RuntimeMinutes,
		PosterPath:     r.PosterPath,
		TMDBId:         r.TMDBID,
		Confidence:     score,
	}
}

func fromTMDBTV(r *TMDBResult, parsed *ParsedFilename) *LookupResult {
	score := ScoreTV(parsed, ScoreInput{
		Title:        r.Title,
		Year:         r.Year,
		EpisodeFound: r.EpisodeTitle != "",
	})
	return &LookupResult{
		Source:         "tmdb",
		MediaType:      "tv",
		Title:          r.Title,
		Year:           r.Year,
		TMDBId:         r.TMDBID,
		PosterPath:     r.PosterPath,
		EpisodeTitle:   r.EpisodeTitle,
		EpisodeAirDate: r.EpisodeAirDate,
		Confidence:     score,
	}
}

func fromOMDbMovie(r *OMDbResult, parsed *ParsedFilename, probeDuration time.Duration) *LookupResult {
	score := ScoreMovie(parsed, ScoreInput{
		Title:      r.Title,
		Year:       r.Year,
		RuntimeMin: r.RuntimeMinutes,
	}, probeDuration)
	return &LookupResult{
		Source:         "omdb",
		MediaType:      "movie",
		Title:          r.Title,
		Year:           r.Year,
		RuntimeMinutes: r.RuntimeMinutes,
		ImdbID:         r.ImdbID,
		Confidence:     score,
	}
}

func fromOMDbTV(r *OMDbResult, parsed *ParsedFilename) *LookupResult {
	// OMDb TV: r.Title is the queried show title (OMDb does not echo the series name).
	// A successful response confirms the title matched, so similarity = 1.0.
	score := ScoreTV(parsed, ScoreInput{
		Title:        r.Title,
		Year:         r.Year,
		EpisodeFound: r.EpisodeTitle != "",
	})
	return &LookupResult{
		Source:         "omdb",
		MediaType:      "tv",
		Title:          r.Title,
		Year:           r.Year,
		ImdbID:         r.ImdbID,
		EpisodeTitle:   r.EpisodeTitle,
		EpisodeAirDate: r.EpisodeAirDate,
		Confidence:     score,
	}
}
