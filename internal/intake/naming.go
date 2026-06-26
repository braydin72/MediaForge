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
func resolveLibraryPath(cfg config.IntakeConfig, parsed ParsedFilename, ext string) string {
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

// applyNamingTemplate replaces template tokens in tmpl with values from parsed.
// Supported tokens: {title}, {show}, {year}, {season:02d}, {episode:02d}, {episode_title}.
func applyNamingTemplate(tmpl string, parsed ParsedFilename) string {
	var yearStr string
	if parsed.Year > 0 {
		yearStr = fmt.Sprintf("%d", parsed.Year)
	}

	replacer := strings.NewReplacer(
		"{title}", sanitizePathComponent(parsed.Title),
		"{show}", sanitizePathComponent(parsed.Title),
		"{year}", yearStr,
		"{season:02d}", fmt.Sprintf("%02d", parsed.Season),
		"{episode:02d}", fmt.Sprintf("%02d", parsed.Episode),
		"{episode_title}", "",
	)
	result := replacer.Replace(tmpl)

	// Clean up parentheses around an empty year: "Title ()" → "Title"
	result = strings.ReplaceAll(result, " ()", "")
	result = strings.TrimSpace(result)
	return result
}

// sanitizePathComponent removes characters that are invalid in directory or file names.
func sanitizePathComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
