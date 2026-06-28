package intake

import (
	"time"
)

// ScoreInput carries the lookup-result fields used by the unified confidence scorer.
type ScoreInput struct {
	Title        string // returned series or movie title
	Year         int    // returned year (first-air for TV, release for movies)
	RuntimeMin   int    // minutes (movies only; 0 = unknown or TV)
	EpisodeFound bool   // TV only: true when an episode title was returned
}

// ScoreMovie computes a unified confidence score for a movie lookup result.
// probeDuration is from ProbeResult.Duration; pass 0 to skip the runtime cross-check.
//
// Scoring:
//   - Exact title match (case-insensitive): +0.40
//   - Fuzzy title match (Levenshtein similarity >= 0.80): +0.25
//   - Year match exact:  +0.25
//   - Year match ±1:     +0.10
//   - Runtime within 5 min: +0.20
//   - Runtime within 10 min: +0.10
//   - Result capped at 1.0
func ScoreMovie(parsed *ParsedFilename, input ScoreInput, probeDuration time.Duration) float64 {
	var score float64

	sim := stringSimilarity(input.Title, parsed.Title)
	switch {
	case sim == 1.0:
		score += 0.40
	case sim >= 0.80:
		score += 0.25
	}

	if parsed.Year > 0 && input.Year > 0 {
		switch absInt(input.Year - parsed.Year) {
		case 0:
			score += 0.25
		case 1:
			score += 0.10
		}
	}

	if probeDuration > 0 && input.RuntimeMin > 0 {
		_, delta := RuntimeCrossCheck(probeDuration.Seconds(), input.RuntimeMin)
		switch {
		case delta <= 5:
			score += 0.20
		case delta <= 10:
			score += 0.10
		}
	}

	return min(score, 1.0)
}

// ScoreTV computes a unified confidence score for a TV lookup result.
//
// Scoring:
//   - Exact series name match: +0.40
//   - Fuzzy series name (similarity >= 0.80): +0.25
//   - Year match (exact only): +0.20
//   - Episode record found (non-empty episode title): +0.25
//   - Result capped at 1.0
func ScoreTV(parsed *ParsedFilename, input ScoreInput) float64 {
	var score float64

	sim := stringSimilarity(input.Title, parsed.Title)
	switch {
	case sim == 1.0:
		score += 0.40
	case sim >= 0.80:
		score += 0.25
	}

	if parsed.Year > 0 && input.Year > 0 && input.Year == parsed.Year {
		score += 0.20
	}

	if input.EpisodeFound {
		score += 0.25
	}

	return min(score, 1.0)
}

// RuntimeCrossCheck reports whether a ffprobe duration and an API runtime
// are within 5 minutes of each other. It also returns the absolute delta in minutes.
//
// probeDurationSecs comes from ProbeResult.Duration.Seconds().
// apiRuntimeMinutes comes from the lookup result (e.g. TMDBResult.RuntimeMinutes).
func RuntimeCrossCheck(probeDurationSecs float64, apiRuntimeMinutes int) (withinFive bool, deltaMinutes int) {
	probeMin := int(probeDurationSecs/60 + 0.5) // round to nearest minute
	delta := probeMin - apiRuntimeMinutes
	if delta < 0 {
		delta = -delta
	}
	return delta <= 5, delta
}

// stringSimilarity returns a value in [0, 1] representing how similar two strings
// are, using normalised Levenshtein distance over Unicode runes. Both strings are
// lowercased and trimmed before comparison.
func stringSimilarity(a, b string) float64 {
	a = normTitle(a)
	b = normTitle(b)
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}
	ra, rb := []rune(a), []rune(b)
	dist := levenshtein(ra, rb)
	maxLen := len(ra)
	if len(rb) > maxLen {
		maxLen = len(rb)
	}
	return 1.0 - float64(dist)/float64(maxLen)
}

// levenshtein computes the edit distance between two rune slices using O(n) space DP.
func levenshtein(a, b []rune) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	row := make([]int, lb+1)
	for j := range row {
		row[j] = j
	}

	for i := 1; i <= la; i++ {
		prev := row[0]
		row[0] = i
		for j := 1; j <= lb; j++ {
			tmp := row[j]
			if a[i-1] == b[j-1] {
				row[j] = prev
			} else {
				row[j] = 1 + min(prev, min(row[j], row[j-1]))
			}
			prev = tmp
		}
	}
	return row[lb]
}
