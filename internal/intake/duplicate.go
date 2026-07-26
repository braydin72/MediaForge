package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/logger"
	"github.com/braydin72/mediaforge/internal/upgrade"
)

// DuplicateFileInfo holds display metadata for one side of a duplicate comparison.
type DuplicateFileInfo struct {
	Path       string `json:"path"`
	Codec      string `json:"codec"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	BitrateBps int64  `json:"bitrate_bps"`
	FileSizeB  int64  `json:"filesize_bytes"`
}

// DuplicateContext is stored in ReviewEntry.DuplicateInfo for duplicate-file review entries.
// It carries both files' details so the UI can offer two actions:
// Replace (overwrite existing with incoming) or Keep Existing (discard incoming).
type DuplicateContext struct {
	Incoming DuplicateFileInfo `json:"incoming"`
	Existing DuplicateFileInfo `json:"existing"`
}

// duplicateDecision is the outcome of a duplicate check.
type duplicateDecision int

const (
	dupNone         duplicateDecision = iota // destination does not exist
	dupSendReview                            // send to Review Queue for user decision
	dupAutoReplace                           // incoming is an unambiguous upgrade — replace existing
	dupAutoKeep                              // existing is an unambiguous upgrade — discard incoming
)

// dupResult is the output of checkDuplicate.
type dupResult struct {
	decision duplicateDecision
	reason   string            // human-readable; set for dupSendReview, dupAutoReplace, dupAutoKeep
	ctx      *DuplicateContext // nil for dupNone
}

// decideDuplicateAction compares an incoming file against the existing file at
// the destination and decides whether the upgrade is unambiguous enough to
// resolve automatically, per the shared internal/upgrade rules.
func decideDuplicateAction(incoming, existing DuplicateFileInfo, bitrateThreshold float64) (duplicateDecision, string) {
	decision, reason := upgrade.Decide(
		upgrade.FileInfo{Codec: incoming.Codec, Width: incoming.Width, Height: incoming.Height, BitrateBps: incoming.BitrateBps},
		upgrade.FileInfo{Codec: existing.Codec, Width: existing.Width, Height: existing.Height, BitrateBps: existing.BitrateBps},
		bitrateThreshold,
	)
	switch decision {
	case upgrade.Replace:
		return dupAutoReplace, reason
	case upgrade.Keep:
		return dupAutoKeep, reason
	default:
		return dupSendReview, reason
	}
}

// checkDuplicate returns dupNone if destPath does not exist.
// If it does exist and duplicateResolution is "manual", it always returns
// dupSendReview — the user must decide whether to replace or keep the
// existing file. If duplicateResolution is "auto" (the default), unambiguous
// upgrades are resolved automatically (dupAutoReplace/dupAutoKeep) per
// decideDuplicateAction; only ambiguous cases fall back to dupSendReview.
// incomingProbe must be non-nil (already-completed probe of the incoming file).
func checkDuplicate(ctx context.Context, prober *ffmpeg.Prober, incomingPath, destPath string, incomingProbe *ffmpeg.ProbeResult, duplicateResolution string, bitrateThreshold float64) dupResult {
	existingInfo, statErr := os.Stat(destPath)
	if os.IsNotExist(statErr) {
		return dupResult{decision: dupNone}
	}
	if statErr != nil {
		return dupResult{
			decision: dupSendReview,
			reason:   fmt.Sprintf("duplicate: file already exists at destination %s", destPath),
		}
	}

	incoming := probeToFileInfo(incomingPath, incomingProbe)

	// Best-effort probe of the existing file for UI display — failure is non-fatal.
	existing := DuplicateFileInfo{Path: destPath, FileSizeB: existingInfo.Size()}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if existingProbe, err := prober.Probe(probeCtx, destPath); err == nil {
		existing = probeToFileInfo(destPath, existingProbe)
	} else {
		logger.Warn("Intake: duplicate — ffprobe failed on existing file", "path", destPath, "error", err)
	}

	dupCtx := &DuplicateContext{
		Incoming: incoming,
		Existing: existing,
	}

	if duplicateResolution != "manual" {
		if decision, reason := decideDuplicateAction(incoming, existing, bitrateThreshold); decision != dupSendReview {
			return dupResult{decision: decision, reason: reason, ctx: dupCtx}
		}
	}

	return dupResult{
		decision: dupSendReview,
		reason:   fmt.Sprintf("duplicate: file already exists at destination %s", destPath),
		ctx:      dupCtx,
	}
}

// probeToFileInfo builds a DuplicateFileInfo from a ProbeResult and the file path.
func probeToFileInfo(path string, probe *ffmpeg.ProbeResult) DuplicateFileInfo {
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	return DuplicateFileInfo{
		Path:       path,
		Codec:      probe.VideoCodec,
		Width:      probe.Width,
		Height:     probe.Height,
		BitrateBps: probe.Bitrate,
		FileSizeB:  size,
	}
}

// marshalDuplicateContext serialises a DuplicateContext to a JSON string for DB storage.
func marshalDuplicateContext(d *DuplicateContext) string {
	if d == nil {
		return ""
	}
	b, _ := json.Marshal(d)
	return string(b)
}
