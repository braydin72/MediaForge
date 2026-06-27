package intake

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ParsedFilename holds the result of parsing a media filename.
type ParsedFilename struct {
	Raw          string // filename stem without extension
	Title        string // cleaned title guess
	Year         int    // 0 if not found
	IsTV         bool
	Season       int    // 0 if not TV
	Episode      int    // 0 if not TV
	Episode2     int    // second episode for multi-episode files (S01E01E02); 0 if single
	MediaType    string // "movie" | "tv"
	EpisodeTitle string // confirmed episode title from metadata lookup; empty if not yet resolved
}

// Compiled regexes are package-level to avoid recompilation on every call.
var (
	// TV episode patterns — checked most-specific first to avoid SxxExx matching
	// the start of SxxExxExx.
	reTVMultiConcat = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,2})E(\d{1,2})`)
	reTVMultiHyphen = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,2})-E(\d{1,2})`)
	reTVSingle      = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,2})`)

	// Fansub and quality bracket groups: [SubGroup], [1080p], etc.
	reBracketed = regexp.MustCompile(`\[[^\]]*\]`)

	// reSceneToken matches a single normalized (space-separated) token that is a
	// known scene/release tag. Anchored at both ends so partial matches are rejected.
	reSceneToken = buildSceneTokenRe()
)

func buildSceneTokenRe() *regexp.Regexp {
	alts := strings.Join([]string{
		// Resolution
		`1080[pi]`, `720p`, `480p`, `2160p`, `4k`, `uhd`,
		// HDR / color
		`hdr10\+?`, `hdr`, `sdr`, `hlg`,
		// Source
		`blu-?ray`, `bdrip`, `brrip`, `webrip`, `web-dl`, `webdl`,
		`hdtv`, `dvdrip`, `dvdscr`, `dvd`, `hdrip`, `pdtv`,
		// Streaming services
		`amzn`, `nf`, `hulu`, `dsnp`, `atvp`, `pcok`, `cr`, `stan`,
		// Video codec
		`x264`, `x265`, `hevc`, `avc`, `xvid`, `divx`, `vp9`, `av1`,
		`h264`, `h265`,
		// Audio codec
		`dts-hd`, `dts`, `truehd`, `atmos`, `flac`, `eac3`, `ac3`, `aac`, `mp3`,
		// Quality / release flags
		`10bit`, `8bit`, `remux`, `proper`, `repack`,
		`extended`, `theatrical`, `unrated`, `limited`, `imax`, `hq`,
	}, "|")
	return regexp.MustCompile(`(?i)^(?:` + alts + `)$`)
}

// ParseFilename parses a video filename and returns structured metadata.
// The filename may include a directory path and extension — only the base name
// is parsed. filepath.Base and filepath.Ext are used so both / and \ are
// handled correctly on all platforms.
func ParseFilename(filename string) ParsedFilename {
	base := filepath.Base(filename)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	result := ParsedFilename{Raw: stem}

	// Remove bracketed fansub/quality groups before any further processing.
	work := reBracketed.ReplaceAllString(stem, " ")

	// 1. Detect TV episode pattern on the pre-normalization string so that
	//    compact tokens like "S01E01" are not split by dot-normalization.
	if titleEnd := detectTVPattern(work, &result); titleEnd >= 0 {
		result.MediaType = "tv"
		result.Title = normalizeTitle(work[:titleEnd])
		return result
	}

	// 2. Movie path: normalize dot/underscore separators to spaces, then scan
	//    tokens left-to-right.  We use a two-pass strategy:
	//      Pass 1 — locate the first scene tag (hard boundary for title end).
	//      Pass 2 — find the last year token before that boundary.
	//    This correctly handles titles that begin with a year (e.g. "2001 A
	//    Space Odyssey") by preferring the later year closest to the tags.
	result.MediaType = "movie"
	norm := normalizeSeparators(work)
	tokens := strings.Fields(norm)

	// Pass 1: first scene-tag position.
	tagIdx := len(tokens)
	for i, tok := range tokens {
		if reSceneToken.MatchString(tok) {
			tagIdx = i
			break
		}
	}

	// Pass 2: last year token before the tag boundary.
	yearIdx := -1
	for i := 0; i < tagIdx; i++ {
		if y, ok := parseYearToken(tokens[i]); ok {
			result.Year = y
			yearIdx = i
		}
	}

	end := tagIdx
	if yearIdx >= 0 {
		end = yearIdx
	}

	result.Title = cleanTitle(tokens[:end])
	return result
}

// detectTVPattern searches s for a TV episode marker.  On a match it populates
// r and returns the byte index in s where the title ends (start of the marker).
// Returns -1 if no TV pattern is found.
func detectTVPattern(s string, r *ParsedFilename) int {
	type pat struct {
		re    *regexp.Regexp
		multi bool
	}
	for _, p := range []pat{
		{reTVMultiConcat, true},
		{reTVMultiHyphen, true},
		{reTVSingle, false},
	} {
		m := p.re.FindStringSubmatchIndex(s)
		if m == nil {
			continue
		}
		r.IsTV = true
		r.Season = mustAtoi(s[m[2]:m[3]])
		r.Episode = mustAtoi(s[m[4]:m[5]])
		if p.multi {
			r.Episode2 = mustAtoi(s[m[6]:m[7]])
		}
		return m[0]
	}
	return -1
}

// parseYearToken returns the year and true if tok is a bare year (2008) or
// a parenthesized year ((2008)) in the range 1900–2099.
func parseYearToken(tok string) (int, bool) {
	switch len(tok) {
	case 4:
		if y, err := strconv.Atoi(tok); err == nil && y >= 1900 && y <= 2099 {
			return y, true
		}
	case 6:
		if tok[0] == '(' && tok[5] == ')' {
			if y, err := strconv.Atoi(tok[1:5]); err == nil && y >= 1900 && y <= 2099 {
				return y, true
			}
		}
	}
	return 0, false
}

// normalizeSeparators replaces dots and underscores with spaces.
// Hyphens are preserved so that "Spider-Man" and "blu-ray" remain intact.
func normalizeSeparators(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '_' {
			return ' '
		}
		return r
	}, s)
}

// normalizeTitle normalizes separators and trims trailing punctuation from a
// raw pre-normalization title slice (the portion before the TV episode marker).
func normalizeTitle(s string) string {
	return cleanTitle(strings.Fields(normalizeSeparators(s)))
}

// cleanTitle joins tokens and trims trailing separator tokens (bare hyphens).
func cleanTitle(tokens []string) string {
	for len(tokens) > 0 && tokens[len(tokens)-1] == "-" {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " ")
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
