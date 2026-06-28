package ffmpeg

import "strings"

// mkvCompatibleCodecs lists subtitle codecs that can be muxed to MKV.
// Based on FFmpeg's matroska.c ff_mkv_codec_tags mapping.
// See: https://github.com/FFmpeg/FFmpeg/blob/master/libavformat/matroska.c
var mkvCompatibleCodecs = map[string]bool{
	"subrip":             true, // S_TEXT/UTF8
	"srt":                true, // Alias for subrip
	"ass":                true, // S_TEXT/ASS
	"ssa":                true, // S_TEXT/SSA
	"text":               true, // S_TEXT/UTF8
	"dvd_subtitle":       true, // S_VOBSUB
	"dvb_subtitle":       true, // S_DVBSUB
	"hdmv_pgs_subtitle":  true, // S_HDMV/PGS (Blu-ray)
	"hdmv_text_subtitle": true, // S_HDMV/TEXTST
	"arib_caption":       true, // S_ARIBSUB (Japanese)
	"webvtt":             true, // D_WEBVTT/*
}

// mp4CompatibleCodecs lists subtitle codecs that ffmpeg can convert to mov_text for MP4.
// Image-based codecs (PGS, VOBSUB, DVB) are excluded because ffmpeg cannot convert
// them to mov_text and will error if you try.
var mp4CompatibleCodecs = map[string]bool{
	"subrip":             true, // SRT text
	"srt":                true, // Alias for subrip
	"ass":                true, // SSA/ASS (formatting is lost on conversion)
	"ssa":                true,
	"text":               true,
	"webvtt":             true,
	"mov_text":           true, // Already MP4-native, passes through cleanly
	"hdmv_text_subtitle": true, // Blu-ray text subtitles (TEXTST)
}

// IsMKVCompatible returns true if the subtitle codec can be muxed to MKV.
// Normalizes to lowercase and trims whitespace for safety.
// Unknown codecs return false for safety (better to drop than fail transcode).
func IsMKVCompatible(codecName string) bool {
	return mkvCompatibleCodecs[strings.ToLower(strings.TrimSpace(codecName))]
}

// IsMP4Compatible returns true if the subtitle codec can be converted to mov_text for MP4.
// Image-based codecs (PGS, VOBSUB, DVB) return false because ffmpeg cannot convert them.
func IsMP4Compatible(codecName string) bool {
	return mp4CompatibleCodecs[strings.ToLower(strings.TrimSpace(codecName))]
}

// FilterMP4Compatible partitions subtitle streams into MP4-convertible and incompatible.
// Returns indices of convertible streams (for -map 0:N arguments with -c:s mov_text) and
// unique codec names of dropped streams (for logging, de-duplicated).
//
// IMPORTANT: Return value semantics match FilterMKVCompatible:
//   - nil input  → nil output  (no subtitle streams exist)
//   - non-nil input → non-nil output (possibly empty slice if all incompatible)
func FilterMP4Compatible(streams []SubtitleStream) (compatibleIndices []int, droppedCodecs []string) {
	if streams == nil {
		return nil, nil
	}

	compatibleIndices = make([]int, 0, len(streams))
	seenCodecs := make(map[string]bool)

	for _, s := range streams {
		if IsMP4Compatible(s.CodecName) {
			compatibleIndices = append(compatibleIndices, s.Index)
			continue
		}
		if !seenCodecs[s.CodecName] {
			seenCodecs[s.CodecName] = true
			droppedCodecs = append(droppedCodecs, s.CodecName)
		}
	}
	return compatibleIndices, droppedCodecs
}

// isMovText returns true if the codec is the MP4-native text subtitle format
// that requires conversion to subrip (srt) when muxing into MKV.
func isMovText(codecName string) bool {
	return strings.ToLower(strings.TrimSpace(codecName)) == "mov_text"
}

// FilterMKVSubtitles partitions subtitle streams into three groups:
//   - copyIndices: streams that can be muxed into MKV with -c:s copy
//   - convertIndices: mov_text streams that must be converted to subrip (-c:s srt)
//   - droppedCodecs: unique codec names of streams that cannot be included at all
//
// mov_text is losslessly convertible to subrip (same content, MKV-compatible container).
//
// IMPORTANT: Return value semantics match FilterMKVCompatible:
//   - nil input → nil, nil, nil (no subtitle streams exist)
//   - non-nil input → non-nil copyIndices (possibly empty) even if all streams are dropped
func FilterMKVSubtitles(streams []SubtitleStream) (copyIndices []int, convertIndices []int, droppedCodecs []string) {
	if streams == nil {
		return nil, nil, nil
	}

	copyIndices = make([]int, 0, len(streams))
	convertIndices = make([]int, 0)
	seenDropped := make(map[string]bool)

	for _, s := range streams {
		if IsMKVCompatible(s.CodecName) {
			copyIndices = append(copyIndices, s.Index)
			continue
		}
		if isMovText(s.CodecName) {
			convertIndices = append(convertIndices, s.Index)
			continue
		}
		norm := strings.ToLower(strings.TrimSpace(s.CodecName))
		if !seenDropped[norm] {
			seenDropped[norm] = true
			droppedCodecs = append(droppedCodecs, s.CodecName)
		}
	}
	return copyIndices, convertIndices, droppedCodecs
}

// FilterMKVCompatible partitions subtitle streams into compatible and incompatible.
// Returns indices of compatible streams (for -map 0:N arguments) and unique codec names
// of dropped streams (for logging warnings to the user, de-duplicated to avoid log spam).
//
// IMPORTANT: Return value semantics for worker logic:
//   - nil input → nil output (no subtitle streams exist)
//   - non-nil input → non-nil output (possibly empty slice if all incompatible)
//
// The worker uses nil to mean "map all" and empty slice to mean "map none".
func FilterMKVCompatible(streams []SubtitleStream) (compatibleIndices []int, droppedCodecs []string) {
	if streams == nil {
		return nil, nil
	}

	// Pre-allocate to ensure we return empty slice, not nil, when all are incompatible
	compatibleIndices = make([]int, 0, len(streams))
	seenCodecs := make(map[string]bool)

	for _, s := range streams {
		if IsMKVCompatible(s.CodecName) {
			compatibleIndices = append(compatibleIndices, s.Index)
			continue
		}
		// De-duplicate dropped codecs for cleaner log output
		if !seenCodecs[s.CodecName] {
			seenCodecs[s.CodecName] = true
			droppedCodecs = append(droppedCodecs, s.CodecName)
		}
	}
	return compatibleIndices, droppedCodecs
}
