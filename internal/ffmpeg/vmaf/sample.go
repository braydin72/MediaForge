package vmaf

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/braydin72/mediaforge/internal/logger"
)

// lastLines returns the last n non-empty lines from output
func lastLines(output string, n int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Sample represents an extracted video sample
type Sample struct {
	Path     string        // Path to extracted sample file
	Position time.Duration // Position in source video
	Duration time.Duration // Sample duration
}

// sampleJitterRange is the maximum fraction of total duration each sample
// position may shift from its anchor (0.25/0.50/0.75). Anchors are spaced
// 0.25 apart, so a range of 0.08 keeps windows well clear of overlapping.
const sampleJitterRange = 0.08

// SamplePositions returns the positions to sample, anchored at count evenly
// spaced points across video duration but jittered within a window around
// each anchor. The jitter is seeded deterministically from seedKey (typically
// the input file path), so re-analyzing the same file yields the same
// positions, but different files don't all land on the exact same relative
// timestamp.
//
// Fixed anchors previously let a recurring low-motion structural element at
// a consistent relative position (e.g. a mid-episode recap card or bumper,
// common in syndicated TV) score near-perfect VMAF regardless of CRF on
// every episode of a series, skewing the averaged score high and masking
// the real quality/CRF tradeoff of the actual content.
func SamplePositions(videoDuration time.Duration, seedKey string, count int) []float64 {
	seconds := videoDuration.Seconds()

	// Handle zero/negative duration
	if seconds <= 0 {
		return []float64{0.5}
	}

	// Very short videos (<60s): single sample at 50%
	if seconds < 60 {
		return []float64{0.5}
	}

	if count < 1 {
		count = 1
	}

	anchors := make([]float64, count)
	for i := range anchors {
		anchors[i] = (float64(i) + 0.5) / float64(count)
	}

	// Scale jitter down as count grows so adjacent anchor windows never
	// overlap (anchors are 1/count apart; keep the window comfortably
	// inside half that spacing).
	jitterRange := sampleJitterRange
	if maxRange := 0.4 / float64(count); maxRange < jitterRange {
		jitterRange = maxRange
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(seedKey))
	rng := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // deterministic sample-position jitter, not security

	positions := make([]float64, len(anchors))
	for i, anchor := range anchors {
		jitter := (rng.Float64()*2 - 1) * jitterRange
		positions[i] = anchor + jitter
	}
	return positions
}

// SampleDuration is the fixed duration for each sample (20 seconds).
// Longer samples provide more representative quality measurement.
const SampleDuration = 20

// ExtractSamples extracts video samples at specified positions using stream copy.
// This is fast (remux only, no decode/encode) but results in keyframe-aligned cuts.
// Tonemapping is NOT applied here - it's handled during VMAF scoring instead.
func ExtractSamples(ctx context.Context, ffmpegPath, inputPath, tempDir string,
	videoDuration time.Duration, positions []float64) ([]*Sample, error) {

	samples := make([]*Sample, 0, len(positions))

	for i, pos := range positions {
		startTime := time.Duration(float64(videoDuration) * pos)

		// Ensure we don't go past end of video
		if startTime+time.Duration(SampleDuration)*time.Second > videoDuration {
			startTime = videoDuration - time.Duration(SampleDuration)*time.Second
			if startTime < 0 {
				startTime = 0
			}
		}

		samplePath := filepath.Join(tempDir, fmt.Sprintf("sample_%d.mkv", i))

		// Stream copy extraction - fast, keyframe-aligned
		args := []string{
			"-ss", fmt.Sprintf("%.3f", startTime.Seconds()),
			"-i", inputPath,
			"-t", fmt.Sprintf("%d", SampleDuration),
			"-c:v", "copy",
			"-an", "-sn",
			"-y",
			samplePath,
		}

		cmd := exec.CommandContext(ctx, ffmpegPath, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("FFmpeg sample extraction failed", "sample", i, "error", err, "stderr", lastLines(string(output), 5))
			// Clean up any created samples
			for _, s := range samples {
				os.Remove(s.Path)
			}
			return nil, fmt.Errorf("failed to extract sample %d: %w (%s)", i, err, lastLines(string(output), 3))
		}

		samples = append(samples, &Sample{
			Path:     samplePath,
			Position: startTime,
			Duration: time.Duration(SampleDuration) * time.Second,
		})
	}

	return samples, nil
}

// ExtractSamplesAccurate extracts video samples at specified positions using
// a lossless re-encode instead of stream copy. Slower than ExtractSamples,
// but frame-accurate: with -ss before -i, ffmpeg only guarantees exact-time
// seeking when re-encoding (it decodes from the nearest preceding keyframe
// and discards frames until reaching the target time); stream copy instead
// snaps to that keyframe itself. Two independently-encoded files (e.g. a
// source and its transcoded output) place keyframes on unrelated schedules,
// so stream-copy extraction from each can land on genuinely different
// content at the "same" position — use this instead whenever samples from
// two different files need to correspond to the same real timestamps (see
// Worker.verifyPostEncodeVMAF). -crf 0 keeps the re-encode mathematically
// lossless so it doesn't introduce its own quality distortion into the
// VMAF comparison.
func ExtractSamplesAccurate(ctx context.Context, ffmpegPath, inputPath, tempDir string,
	videoDuration time.Duration, positions []float64) ([]*Sample, error) {

	samples := make([]*Sample, 0, len(positions))

	for i, pos := range positions {
		startTime := time.Duration(float64(videoDuration) * pos)

		// Ensure we don't go past end of video
		if startTime+time.Duration(SampleDuration)*time.Second > videoDuration {
			startTime = videoDuration - time.Duration(SampleDuration)*time.Second
			if startTime < 0 {
				startTime = 0
			}
		}

		samplePath := filepath.Join(tempDir, fmt.Sprintf("sample_%d.mkv", i))

		args := []string{
			"-ss", fmt.Sprintf("%.3f", startTime.Seconds()),
			"-i", inputPath,
			"-t", fmt.Sprintf("%d", SampleDuration),
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-crf", "0",
			"-an", "-sn",
			"-y",
			samplePath,
		}

		cmd := exec.CommandContext(ctx, ffmpegPath, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("FFmpeg accurate sample extraction failed", "sample", i, "error", err, "stderr", lastLines(string(output), 5))
			for _, s := range samples {
				os.Remove(s.Path)
			}
			return nil, fmt.Errorf("failed to extract sample %d: %w (%s)", i, err, lastLines(string(output), 3))
		}

		samples = append(samples, &Sample{
			Path:     samplePath,
			Position: startTime,
			Duration: time.Duration(SampleDuration) * time.Second,
		})
	}

	return samples, nil
}

// CleanupSamples removes all sample files
func CleanupSamples(samples []*Sample) {
	for _, s := range samples {
		os.Remove(s.Path)
	}
}
