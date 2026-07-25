package intake

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/braydin72/mediaforge/internal/config"
)

// resolveLibraryPath builds the intended post-encode destination path in the
// library for an AVC file, using the naming templates in cfg and the parsed
// filename metadata. ext should be the output container extension (e.g. ".mkv").
// Returns an empty string if not enough metadata is available to build a path.
func resolveLibraryPath(cfg *config.IntakeConfig, parsed *ParsedFilename, ext string) string {
	if parsed.Title == "" {
		return ""
	}

	switch parsed.MediaType {
	case "tv":
		if parsed.Season == 0 {
			return ""
		}
		folderTmpl := cfg.Naming.ShowFolder
		if folderTmpl == "" {
			folderTmpl = "{show} ({year})"
		}
		fileTmpl := cfg.Naming.EpisodeFile
		if fileTmpl == "" {
			fileTmpl = "{show} - S{season:02d}E{episode:02d} - {episode_title}"
		}
		showFolder := applyNamingTemplate(folderTmpl, parsed)
		episodeFile := applyNamingTemplate(fileTmpl, parsed)
		seasonDir := fmt.Sprintf("Season %02d", parsed.Season)
		return filepath.Join(cfg.Library.TVShows, showFolder, seasonDir, episodeFile+ext)

	default: // "movie"
		folderTmpl := cfg.Naming.MovieFolder
		if folderTmpl == "" {
			folderTmpl = "{title} ({year})"
		}
		fileTmpl := cfg.Naming.MovieFile
		if fileTmpl == "" {
			fileTmpl = "{title} ({year})"
		}
		movieFolder := applyNamingTemplate(folderTmpl, parsed)
		movieFile := applyNamingTemplate(fileTmpl, parsed)
		return filepath.Join(cfg.Library.Movies, movieFolder, movieFile+ext)
	}
}

// buildCorrectedFilename renders just the file-name component (no folder path)
// that resolveLibraryPath would use, given corrected metadata in parsed. Used to
// rename a Review Queue entry's stored Filename after a successful metadata
// lookup so ParseFilename re-parses the correction instead of the raw source
// name. Returns "" under the same conditions resolveLibraryPath would.
func buildCorrectedFilename(cfg *config.IntakeConfig, parsed *ParsedFilename, ext string) string {
	if parsed.Title == "" {
		return ""
	}
	switch parsed.MediaType {
	case "tv":
		if parsed.Season == 0 {
			return ""
		}
		fileTmpl := cfg.Naming.EpisodeFile
		if fileTmpl == "" {
			fileTmpl = "{show} - S{season:02d}E{episode:02d} - {episode_title}"
		}
		return applyNamingTemplate(fileTmpl, parsed) + ext
	default: // "movie"
		fileTmpl := cfg.Naming.MovieFile
		if fileTmpl == "" {
			fileTmpl = "{title} ({year})"
		}
		return applyNamingTemplate(fileTmpl, parsed) + ext
	}
}

// ResolveLibraryPath is an exported wrapper around resolveLibraryPath for callers
// outside the package (e.g. the API resolving a manually-picked match to a
// destination path). Returns "" when there is not enough metadata to build a path.
func ResolveLibraryPath(cfg *config.IntakeConfig, parsed *ParsedFilename, ext string) string {
	return resolveLibraryPath(cfg, parsed, ext)
}

// applyNamingTemplate replaces template tokens in tmpl with values from parsed.
// Supported tokens: {title}, {show}, {year}, {season:02d}, {episode:02d}, {episode_title}.
func applyNamingTemplate(tmpl string, parsed *ParsedFilename) string {
	var yearStr string
	if parsed.Year > 0 {
		yearStr = fmt.Sprintf("%d", parsed.Year)
	}

	// A file covering two consecutive episodes (either the filename itself
	// encoded a range like S01E01E02, or TVDB reconciliation confirmed a
	// combined multi-part episode) renders as "E01-E02" instead of just "E01"
	// so the second episode isn't silently dropped from the destination name.
	episodeStr := fmt.Sprintf("%02d", parsed.Episode)
	if parsed.Episode2 > 0 {
		episodeStr = fmt.Sprintf("%02d-E%02d", parsed.Episode, parsed.Episode2)
	}

	replacer := strings.NewReplacer(
		"{title}", sanitizePathComponent(parsed.Title),
		"{show}", sanitizePathComponent(parsed.Title),
		"{year}", yearStr,
		"{season:02d}", fmt.Sprintf("%02d", parsed.Season),
		"{episode:02d}", episodeStr,
		"{episode_title}", sanitizePathComponent(parsed.EpisodeTitle),
	)
	result := replacer.Replace(tmpl)

	// Clean up parentheses around an empty year: "Title ()" → "Title"
	result = strings.ReplaceAll(result, " ()", "")
	result = strings.TrimSpace(result)
	return result
}

// sanitizePathComponent removes characters that are invalid in directory or file names.
// Colons are replaced with " - " (matching common media-server naming conventions,
// e.g. "9-1-1: Nashville" -> "9-1-1 - Nashville") rather than an underscore, since
// a colon in a title is almost always a subtitle separator, not stray punctuation.
func sanitizePathComponent(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = strings.ReplaceAll(s, ":", " - ")

	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	result := b.String()
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}
