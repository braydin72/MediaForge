// Package upgrade implements the "Intelligent Duplicate Resolution" rules from
// MEDIAFORGE_SPEC.md: deciding whether an incoming file is an unambiguous
// upgrade over an existing file at the same library destination, without
// requiring a manual Review Queue decision.
//
// It has no dependency on internal/intake or internal/jobs so both packages
// (which cannot import each other) can share the same decision logic for
// their respective duplicate-at-destination checks.
package upgrade

import (
	"fmt"
	"strings"
)

// FileInfo holds the metadata needed to compare two files for an upgrade decision.
type FileInfo struct {
	Codec      string
	Width      int
	Height     int
	BitrateBps int64
}

// Decision is the outcome of comparing an incoming file against an existing one.
type Decision int

const (
	Review  Decision = iota // ambiguous — send to Review Queue for manual decision
	Replace                 // incoming is an unambiguous upgrade — replace existing
	Keep                    // existing is an unambiguous upgrade — discard incoming
)

// modernCodecs are treated as an upgrade tier over everything else at equal resolution.
var modernCodecs = map[string]struct{}{
	"hevc": {}, "h265": {}, "x265": {}, "av1": {}, "libaom-av1": {}, "libsvtav1": {},
}

// tier classifies a codec as "modern" or "other" for codec-tier comparison.
// Unknown/empty codecs are reported as "" so callers can treat them as inconclusive.
func tier(codec string) string {
	if codec == "" {
		return ""
	}
	if _, ok := modernCodecs[strings.ToLower(codec)]; ok {
		return "modern"
	}
	return "other"
}

// Decide compares incoming against existing and returns whether the difference
// is unambiguous enough to resolve automatically. bitrateThreshold is the
// fractional bitrate increase required, at equal resolution/codec tier, to
// treat one file as a bitrate upgrade over the other (e.g. 0.25 = 25%).
func Decide(incoming, existing FileInfo, bitrateThreshold float64) (Decision, string) {
	incomingPixels := incoming.Width * incoming.Height
	existingPixels := existing.Width * existing.Height

	if incomingPixels > 0 && existingPixels > 0 && incomingPixels != existingPixels {
		if incomingPixels > existingPixels {
			return Replace, fmt.Sprintf("auto-resolved: incoming resolution %dx%d is higher than existing %dx%d",
				incoming.Width, incoming.Height, existing.Width, existing.Height)
		}
		return Keep, fmt.Sprintf("auto-resolved: existing resolution %dx%d is higher than incoming %dx%d",
			existing.Width, existing.Height, incoming.Width, incoming.Height)
	}

	incomingTier := tier(incoming.Codec)
	existingTier := tier(existing.Codec)
	if incomingTier != "" && existingTier != "" && incomingTier != existingTier {
		if incomingTier == "modern" {
			return Replace, fmt.Sprintf("auto-resolved: incoming codec %s is a modern-codec upgrade over existing %s at equal resolution",
				incoming.Codec, existing.Codec)
		}
		return Keep, fmt.Sprintf("auto-resolved: existing codec %s is a modern-codec upgrade over incoming %s at equal resolution",
			existing.Codec, incoming.Codec)
	}

	if incoming.BitrateBps > 0 && existing.BitrateBps > 0 && bitrateThreshold > 0 {
		incomingBr := float64(incoming.BitrateBps)
		existingBr := float64(existing.BitrateBps)
		if incomingBr >= existingBr*(1+bitrateThreshold) {
			return Replace, fmt.Sprintf("auto-resolved: incoming bitrate %d bps is >=%.0f%% higher than existing %d bps at equal resolution/codec",
				incoming.BitrateBps, bitrateThreshold*100, existing.BitrateBps)
		}
		if existingBr >= incomingBr*(1+bitrateThreshold) {
			return Keep, fmt.Sprintf("auto-resolved: existing bitrate %d bps is >=%.0f%% higher than incoming %d bps at equal resolution/codec",
				existing.BitrateBps, bitrateThreshold*100, incoming.BitrateBps)
		}
	}

	return Review, "duplicate: unable to automatically determine which file is the upgrade"
}
