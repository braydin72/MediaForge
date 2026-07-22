package vmaf

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/braydin72/mediaforge/internal/logger"
)

const (
	// minModRange is the minimum modifier range worth searching.
	// Below 5% bitrate difference, quality changes are imperceptible.
	minModRange = 0.05

	// maxSearchIters caps search iterations to prevent thrashing near the
	// boundary due to sampling noise.
	maxSearchIters = 6
)

// QualityRange defines the search bounds for an encoder
type QualityRange struct {
	Min         int     // Minimum quality value (best quality)
	Max         int     // Maximum quality value (most compression)
	UsesBitrate bool    // True for VideoToolbox (bitrate modifier)
	MinMod      float64 // Min bitrate modifier (most compression)
	MaxMod      float64 // Max bitrate modifier (best quality)
}

// SearchResult holds the result of the search
type SearchResult struct {
	Quality        int     // CRF/CQ/QP value
	Modifier       float64 // Bitrate modifier (for VideoToolbox)
	VMafScore      float64 // Achieved VMAF score
	Iterations     int     // Number of quality levels tested (each test encodes all samples)
	BestEffort     bool    // True if no CRF met the threshold; Quality/VMafScore reflect best found
	BestEffortBase int     // Original best-effort CRF before plateau fallback climbed higher (0 if unchanged/not applicable)

	// CeilingVMAF and EffectiveThreshold are only set by AdaptiveBinarySearch.
	// CeilingVMAF is the near-lossless VMAF ceiling probed before the search;
	// EffectiveThreshold is the (possibly lowered) threshold actually used.
	CeilingVMAF        float64
	EffectiveThreshold float64
}

// EncodeSampleFunc is a function that encodes a sample at the given quality
// and returns the path to the encoded file
type EncodeSampleFunc func(ctx context.Context, samplePath string, quality int, modifier float64) (string, error)

// BinarySearch finds the optimal quality setting via interpolated binary search.
// Uses linear interpolation between data points to converge faster than pure binary search.
func BinarySearch(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	qRange QualityRange, threshold float64, height int, encodeSample EncodeSampleFunc) (*SearchResult, error) {

	// Validate inputs
	if len(referenceSamples) == 0 {
		return nil, fmt.Errorf("no reference samples provided")
	}

	// Create the encode+score function that both search modes share
	scorer := newSampleScorer(ctx, ffmpegPath, referenceSamples, height, encodeSample)

	if qRange.UsesBitrate {
		if qRange.MinMod >= qRange.MaxMod {
			return nil, fmt.Errorf("invalid bitrate range: min %.3f >= max %.3f", qRange.MinMod, qRange.MaxMod)
		}
		return interpolatedSearchBitrate(scorer, qRange, threshold)
	}

	if qRange.Min >= qRange.Max {
		return nil, fmt.Errorf("invalid CRF range: min %d >= max %d", qRange.Min, qRange.Max)
	}
	return interpolatedSearchCRF(scorer, qRange, threshold)
}

// ceilingSearchMargin is how far (in VMAF points) below the probed ceiling
// the adaptive search's effective threshold is set. Without this margin,
// requiring a score exactly equal to the ceiling would only ever be met at
// (or very near) the same near-lossless quality used for the ceiling probe
// itself, defeating the purpose of searching for a smaller file. This is
// intentionally a separate constant from bestEffortFallbackTolerance: that
// one governs a post-hoc fallback step, this one drives the primary search
// target from the start.
const ceilingSearchMargin = 2.0

// computeEffectiveThreshold returns the threshold the adaptive search should
// actually target: the tier's threshold if the content's ceiling clears it
// (unchanged behavior), or the ceiling minus a small margin if the ceiling
// falls short (so the search can converge on the highest CRF that still
// scores close to what this content can ever achieve, instead of endlessly
// chasing an unreachable tier threshold).
func computeEffectiveThreshold(tierThreshold, ceilingScore, margin float64) float64 {
	if ceilingScore-margin < tierThreshold {
		return ceilingScore - margin
	}
	return tierThreshold
}

// AdaptiveBinarySearch is like BinarySearch, but first probes the content's
// real achievable VMAF ceiling (scoring at the best-quality end of qRange)
// and searches against min(threshold, ceiling-margin) instead of always the
// raw tier threshold. This avoids the plain search wastefully narrowing
// toward near-lossless CRFs chasing a threshold the content can never clear,
// and lets it converge directly on the smallest file that's genuinely close
// to this content's own quality ceiling.
func AdaptiveBinarySearch(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	qRange QualityRange, threshold float64, height int, encodeSample EncodeSampleFunc) (*SearchResult, error) {

	if len(referenceSamples) == 0 {
		return nil, fmt.Errorf("no reference samples provided")
	}

	scorer := newSampleScorer(ctx, ffmpegPath, referenceSamples, height, encodeSample)

	var ceilingScore float64
	var err error
	if qRange.UsesBitrate {
		if qRange.MinMod >= qRange.MaxMod {
			return nil, fmt.Errorf("invalid bitrate range: min %.3f >= max %.3f", qRange.MinMod, qRange.MaxMod)
		}
		ceilingScore, err = scorer.scoreModifier(qRange.MaxMod)
	} else {
		if qRange.Min >= qRange.Max {
			return nil, fmt.Errorf("invalid CRF range: min %d >= max %d", qRange.Min, qRange.Max)
		}
		ceilingScore, err = scorer.scoreCRF(qRange.Min)
	}
	if err != nil {
		return nil, fmt.Errorf("ceiling probe: %w", err)
	}

	effectiveThreshold := computeEffectiveThreshold(threshold, ceilingScore, ceilingSearchMargin)

	logger.Info("Adaptive VMAF ceiling probe complete",
		"ceiling_vmaf", fmt.Sprintf("%.2f", ceilingScore),
		"tier_threshold", threshold,
		"effective_threshold", fmt.Sprintf("%.2f", effectiveThreshold))

	var result *SearchResult
	if qRange.UsesBitrate {
		result, err = interpolatedSearchBitrate(scorer, qRange, effectiveThreshold)
	} else {
		result, err = interpolatedSearchCRF(scorer, qRange, effectiveThreshold)
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}

	result.CeilingVMAF = ceilingScore
	result.EffectiveThreshold = effectiveThreshold
	return result, nil
}

// sampleScorer handles encoding and VMAF scoring
type sampleScorer struct {
	ctx              context.Context
	ffmpegPath       string
	referenceSamples []*Sample
	height           int
	encodeSample     EncodeSampleFunc
	testCount        int // Number of quality levels tested
}

func newSampleScorer(ctx context.Context, ffmpegPath string, referenceSamples []*Sample,
	height int, encodeSample EncodeSampleFunc) *sampleScorer {
	return &sampleScorer{
		ctx:              ctx,
		ffmpegPath:       ffmpegPath,
		referenceSamples: referenceSamples,
		height:           height,
		encodeSample:     encodeSample,
	}
}

// scoreCRF encodes at a CRF value and returns the VMAF score
func (s *sampleScorer) scoreCRF(crf int) (float64, error) {
	s.testCount++
	encodeStart := time.Now()

	distortedSamples := make([]*Sample, 0, len(s.referenceSamples))
	var encodeErr error

	for i, ref := range s.referenceSamples {
		distPath, err := s.encodeSample(s.ctx, ref.Path, crf, 0)
		if err != nil {
			encodeErr = fmt.Errorf("encoding sample %d at CRF %d: %w", i, crf, err)
			break
		}
		distortedSamples = append(distortedSamples, &Sample{Path: distPath})
	}

	// Cleanup on encode failure
	if encodeErr != nil {
		CleanupSamples(distortedSamples)
		return 0, encodeErr
	}

	encodeDuration := time.Since(encodeStart)

	scoreStart := time.Now()
	score, err := ScoreSamples(s.ctx, s.ffmpegPath, s.referenceSamples, distortedSamples, s.height)
	scoreDuration := time.Since(scoreStart)

	CleanupSamples(distortedSamples)

	if err != nil {
		return 0, fmt.Errorf("scoring at CRF %d: %w", crf, err)
	}

	logger.Info("VMAF search iteration",
		"crf", crf,
		"vmaf", fmt.Sprintf("%.2f", score),
		"encode_time", encodeDuration.String(),
		"score_time", scoreDuration.String())

	return score, nil
}

// scoreModifier encodes at a bitrate modifier and returns the VMAF score
func (s *sampleScorer) scoreModifier(mod float64) (float64, error) {
	s.testCount++
	encodeStart := time.Now()

	distortedSamples := make([]*Sample, 0, len(s.referenceSamples))
	var encodeErr error

	for i, ref := range s.referenceSamples {
		distPath, err := s.encodeSample(s.ctx, ref.Path, 0, mod)
		if err != nil {
			encodeErr = fmt.Errorf("encoding sample %d at modifier %.3f: %w", i, mod, err)
			break
		}
		distortedSamples = append(distortedSamples, &Sample{Path: distPath})
	}

	// Cleanup on encode failure
	if encodeErr != nil {
		CleanupSamples(distortedSamples)
		return 0, encodeErr
	}

	encodeDuration := time.Since(encodeStart)

	scoreStart := time.Now()
	score, err := ScoreSamples(s.ctx, s.ffmpegPath, s.referenceSamples, distortedSamples, s.height)
	scoreDuration := time.Since(scoreStart)

	CleanupSamples(distortedSamples)

	if err != nil {
		return 0, fmt.Errorf("scoring at modifier %.3f: %w", mod, err)
	}

	logger.Info("VMAF search iteration",
		"modifier", fmt.Sprintf("%.3f", mod),
		"vmaf", fmt.Sprintf("%.2f", score),
		"encode_time", encodeDuration.String(),
		"score_time", scoreDuration.String())

	return score, nil
}

// crfAttempt stores a CRF value and its VMAF score for interpolation
type crfAttempt struct {
	crf   int
	score float64
}

// bestEffortFallbackTolerance is how far (in VMAF points) a higher CRF may
// score below a best-effort ceiling and still be treated as "no real quality
// cost" — used to claim free size savings when content has a hard VMAF
// ceiling below the tier's threshold (see applyBestEffortFallback).
const bestEffortFallbackTolerance = 2.0

// bestEffortFallbackMaxProbes caps how many extra CRF steps are probed
// looking for plateau-edge size savings beyond a best-effort result.
const bestEffortFallbackMaxProbes = 3

// bestEffortFallbackStep is the CRF increment used for each plateau probe.
const bestEffortFallbackStep = 2

// applyBestEffortFallback walks CRF upward (more compression, smaller file)
// from a best-effort attempt that never cleared the VMAF threshold, looking
// for free size reduction. Content with a hard VMAF ceiling (e.g. film
// grain/video noise no CRF can fully preserve) often scores nearly the same
// across a range of CRFs, but interpolatedSearchCRF's failure handling only
// ever narrows toward minCRF (larger files) once it decides quality is
// insufficient — it never tests whether a smaller file would cost anything.
// This fills that gap: returns the highest CRF within
// bestEffortFallbackTolerance VMAF points of the original best-effort score,
// or the original attempt unchanged if even the first probe step drops
// quality meaningfully.
func applyBestEffortFallback(s *sampleScorer, best crfAttempt, maxCRF int) crfAttempt {
	current := best
	for i := 0; i < bestEffortFallbackMaxProbes; i++ {
		candidateCRF := current.crf + bestEffortFallbackStep
		if candidateCRF > maxCRF {
			break
		}
		score, err := s.scoreCRF(candidateCRF)
		if err != nil {
			break
		}
		if bestEffortFallbackAccepts(best.score, score) {
			current = crfAttempt{crf: candidateCRF, score: score}
		} else {
			break
		}
	}
	return current
}

// bestEffortFallbackAccepts reports whether a candidate CRF's score is close
// enough to the original best-effort score to count as "no real quality
// cost" — i.e. within bestEffortFallbackTolerance VMAF points.
func bestEffortFallbackAccepts(bestScore, candidateScore float64) bool {
	return bestScore-candidateScore <= bestEffortFallbackTolerance
}

// interpolatedSearchCRF finds optimal CRF using ab-av1 style interpolated search.
// Algorithm: Start with midpoint, track all attempts, use interpolation to converge.
// For CRF: lower value = better quality, higher value = more compression.
func interpolatedSearchCRF(s *sampleScorer, qRange QualityRange, threshold float64) (*SearchResult, error) {
	minCRF := qRange.Min
	maxCRF := qRange.Max

	// Track all attempts for interpolation (ab-av1 style)
	attempts := make([]crfAttempt, 0, maxSearchIters+2)

	// Start with midpoint - don't test both bounds first
	crf := (minCRF + maxCRF) / 2

	for run := 1; run <= maxSearchIters+2; run++ {
		score, err := s.scoreCRF(crf)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, crfAttempt{crf: crf, score: score})

		if score >= threshold {
			// Good: score meets threshold
			// Check early termination: within tolerance of threshold
			tolerance := 0.5 + float64(run-1)*0.5
			if score < threshold+tolerance {
				return &SearchResult{
					Quality:    crf,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			}

			// Find upper bound: attempt with crf > current crf (more compression, lower quality)
			upperBound := findUpperBound(attempts, crf)

			if upperBound != nil && upperBound.crf == crf+1 {
				// Adjacent - can't improve further
				return &SearchResult{
					Quality:    crf,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			}

			if upperBound != nil {
				// Interpolate between current (good) and upper (bad)
				crf = vmafLerpCRF(threshold, upperBound.crf, upperBound.score, crf, score)
			} else if run == 1 && crf+1 < maxCRF {
				// First iteration, no upper bound yet: 40/60 cut toward compression
				// This is the ab-av1 "cut_on_iter2" optimization
				crf = int(math.Round(float64(crf)*0.4 + float64(maxCRF)*0.6))
			} else if crf == maxCRF {
				// Already at max compression and it's good - we're done
				return &SearchResult{
					Quality:    crf,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			} else {
				// No upper bound, go to max
				crf = maxCRF
			}
		} else {
			// Not good enough: score below threshold
			if crf == minCRF {
				// Even best quality fails — use best-effort result
				if best := findAbsoluteBestCRFAttempt(attempts); best != nil {
					fallback := applyBestEffortFallback(s, *best, maxCRF)
					return &SearchResult{Quality: fallback.crf, VMafScore: fallback.score, Iterations: s.testCount, BestEffort: true, BestEffortBase: best.crf}, nil
				}
				return nil, nil
			}

			// Find lower bound: attempt with crf < current crf (less compression, higher quality)
			lowerBound := findLowerBound(attempts, crf)

			if lowerBound != nil && lowerBound.crf+1 == crf {
				// Adjacent - lower bound is the best we can do
				if lowerBound.score >= threshold {
					return &SearchResult{
						Quality:    lowerBound.crf,
						VMafScore:  lowerBound.score,
						Iterations: s.testCount,
					}, nil
				}
				// Both adjacent points fail — use best-effort result
				if best := findAbsoluteBestCRFAttempt(attempts); best != nil {
					fallback := applyBestEffortFallback(s, *best, maxCRF)
					return &SearchResult{Quality: fallback.crf, VMafScore: fallback.score, Iterations: s.testCount, BestEffort: true, BestEffortBase: best.crf}, nil
				}
				return nil, nil
			}

			if lowerBound != nil {
				// Interpolate between lower (good) and current (bad)
				crf = vmafLerpCRF(threshold, crf, score, lowerBound.crf, lowerBound.score)
			} else if run == 1 && crf > minCRF+1 {
				// First iteration, no lower bound yet: 40/60 cut toward quality
				crf = int(math.Round(float64(crf)*0.4 + float64(minCRF)*0.6))
			} else {
				// No lower bound, go to min
				crf = minCRF
			}
		}

		// Clamp to valid range
		if crf < minCRF {
			crf = minCRF
		}
		if crf > maxCRF {
			crf = maxCRF
		}

		// Skip if we've already tested this CRF
		if hasAttempt(attempts, crf) {
			// Find best result that meets threshold
			best := findBestAttempt(attempts, threshold)
			if best != nil {
				return &SearchResult{
					Quality:    best.crf,
					VMafScore:  best.score,
					Iterations: s.testCount,
				}, nil
			}
			// No threshold-meeting result — use best-effort
			if absBest := findAbsoluteBestCRFAttempt(attempts); absBest != nil {
				fallback := applyBestEffortFallback(s, *absBest, maxCRF)
				return &SearchResult{Quality: fallback.crf, VMafScore: fallback.score, Iterations: s.testCount, BestEffort: true, BestEffortBase: absBest.crf}, nil
			}
			return nil, nil
		}
	}

	// Return best result that meets threshold
	best := findBestAttempt(attempts, threshold)
	if best != nil {
		return &SearchResult{
			Quality:    best.crf,
			VMafScore:  best.score,
			Iterations: s.testCount,
		}, nil
	}
	// No threshold-meeting result — use best-effort
	if absBest := findAbsoluteBestCRFAttempt(attempts); absBest != nil {
		fallback := applyBestEffortFallback(s, *absBest, maxCRF)
		return &SearchResult{Quality: fallback.crf, VMafScore: fallback.score, Iterations: s.testCount, BestEffort: true, BestEffortBase: absBest.crf}, nil
	}
	return nil, nil
}

// findUpperBound finds the attempt with the smallest CRF greater than the given CRF
func findUpperBound(attempts []crfAttempt, crf int) *crfAttempt {
	var best *crfAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.crf > crf && (best == nil || a.crf < best.crf) {
			best = a
		}
	}
	return best
}

// findLowerBound finds the attempt with the largest CRF less than the given CRF
func findLowerBound(attempts []crfAttempt, crf int) *crfAttempt {
	var best *crfAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.crf < crf && (best == nil || a.crf > best.crf) {
			best = a
		}
	}
	return best
}

// hasAttempt checks if a CRF value has already been tested
func hasAttempt(attempts []crfAttempt, crf int) bool {
	for _, a := range attempts {
		if a.crf == crf {
			return true
		}
	}
	return false
}

// findBestAttempt finds the attempt with highest CRF (most compression) that meets threshold
func findBestAttempt(attempts []crfAttempt, threshold float64) *crfAttempt {
	var best *crfAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.score >= threshold && (best == nil || a.crf > best.crf) {
			best = a
		}
	}
	return best
}

// findAbsoluteBestCRFAttempt returns the attempt with highest VMAF score regardless of threshold.
// Ties broken by preferring higher CRF (more compression → smaller file).
func findAbsoluteBestCRFAttempt(attempts []crfAttempt) *crfAttempt {
	var best *crfAttempt
	for i := range attempts {
		a := &attempts[i]
		if best == nil || a.score > best.score || (a.score == best.score && a.crf > best.crf) {
			best = a
		}
	}
	return best
}

// vmafLerpCRF interpolates to find CRF that should produce the threshold score.
// worseCRF has score < threshold, betterCRF has score >= threshold.
// Returns a CRF clamped to strictly between the two inputs.
func vmafLerpCRF(threshold float64, worseCRF int, worseScore float64, betterCRF int, betterScore float64) int {
	scoreDiff := betterScore - worseScore
	if scoreDiff <= 0 {
		return (worseCRF + betterCRF) / 2
	}

	// Linear interpolation: where does threshold fall proportionally?
	factor := (threshold - worseScore) / scoreDiff
	crfDiff := worseCRF - betterCRF
	lerp := float64(worseCRF) - float64(crfDiff)*factor

	// Clamp to strictly between bounds to guarantee progress
	result := int(math.Round(lerp))
	if result <= betterCRF {
		result = betterCRF + 1
	}
	if result >= worseCRF {
		result = worseCRF - 1
	}
	return result
}

// modAttempt stores a modifier value and its VMAF score for interpolation
type modAttempt struct {
	mod   float64
	score float64
}

// interpolatedSearchBitrate finds optimal bitrate modifier using ab-av1 style interpolated search.
// Algorithm: Start with midpoint, track all attempts, use interpolation to converge.
// For bitrate modifier: higher value = better quality, lower value = more compression.
func interpolatedSearchBitrate(s *sampleScorer, qRange QualityRange, threshold float64) (*SearchResult, error) {
	minMod := qRange.MinMod // Most compression
	maxMod := qRange.MaxMod // Best quality

	// Track all attempts for interpolation
	attempts := make([]modAttempt, 0, maxSearchIters+2)

	// Start with midpoint
	mod := (minMod + maxMod) / 2

	for run := 1; run <= maxSearchIters+2; run++ {
		score, err := s.scoreModifier(mod)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, modAttempt{mod: mod, score: score})

		if score >= threshold {
			// Good: score meets threshold
			tolerance := 0.5 + float64(run-1)*0.5
			if score < threshold+tolerance {
				return &SearchResult{
					Modifier:   mod,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			}

			// Find lower bound: attempt with mod < current mod (more compression)
			lowerBound := findModLowerBound(attempts, mod)

			if lowerBound != nil && math.Abs(lowerBound.mod-mod) <= minModRange {
				// Adjacent - can't improve further
				return &SearchResult{
					Modifier:   mod,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			}

			if lowerBound != nil {
				// Interpolate between current (good) and lower (bad)
				mod = vmafLerpMod(threshold, lowerBound.mod, lowerBound.score, mod, score)
			} else if run == 1 && mod-minModRange > minMod {
				// First iteration: 40/60 cut toward compression (lower modifier)
				mod = mod*0.4 + minMod*0.6
			} else if math.Abs(mod-minMod) <= minModRange {
				// Already at min and it's good
				return &SearchResult{
					Modifier:   mod,
					VMafScore:  score,
					Iterations: s.testCount,
				}, nil
			} else {
				mod = minMod
			}
		} else {
			// Not good enough
			if math.Abs(mod-maxMod) <= minModRange {
				// Even best quality fails — use best-effort result
				if best := findAbsoluteBestModAttempt(attempts); best != nil {
					return &SearchResult{Modifier: best.mod, VMafScore: best.score, Iterations: s.testCount, BestEffort: true}, nil
				}
				return nil, nil
			}

			// Find upper bound: attempt with mod > current mod (better quality)
			upperBound := findModUpperBound(attempts, mod)

			if upperBound != nil && math.Abs(upperBound.mod-mod) <= minModRange {
				// Adjacent
				if upperBound.score >= threshold {
					return &SearchResult{
						Modifier:   upperBound.mod,
						VMafScore:  upperBound.score,
						Iterations: s.testCount,
					}, nil
				}
				// Both adjacent points fail — use best-effort result
				if best := findAbsoluteBestModAttempt(attempts); best != nil {
					return &SearchResult{Modifier: best.mod, VMafScore: best.score, Iterations: s.testCount, BestEffort: true}, nil
				}
				return nil, nil
			}

			if upperBound != nil {
				// Interpolate between current (bad) and upper (good)
				mod = vmafLerpMod(threshold, mod, score, upperBound.mod, upperBound.score)
			} else if run == 1 && mod+minModRange < maxMod {
				// First iteration: 40/60 cut toward quality (higher modifier)
				mod = mod*0.4 + maxMod*0.6
			} else {
				mod = maxMod
			}
		}

		// Clamp to valid range
		if mod < minMod {
			mod = minMod
		}
		if mod > maxMod {
			mod = maxMod
		}

		// Skip if already tested (within tolerance)
		if hasModAttempt(attempts, mod) {
			best := findBestModAttempt(attempts, threshold)
			if best != nil {
				return &SearchResult{
					Modifier:   best.mod,
					VMafScore:  best.score,
					Iterations: s.testCount,
				}, nil
			}
			// No threshold-meeting result — use best-effort
			if absBest := findAbsoluteBestModAttempt(attempts); absBest != nil {
				return &SearchResult{Modifier: absBest.mod, VMafScore: absBest.score, Iterations: s.testCount, BestEffort: true}, nil
			}
			return nil, nil
		}
	}

	best := findBestModAttempt(attempts, threshold)
	if best != nil {
		return &SearchResult{
			Modifier:   best.mod,
			VMafScore:  best.score,
			Iterations: s.testCount,
		}, nil
	}
	// No threshold-meeting result — use best-effort
	if absBest := findAbsoluteBestModAttempt(attempts); absBest != nil {
		return &SearchResult{Modifier: absBest.mod, VMafScore: absBest.score, Iterations: s.testCount, BestEffort: true}, nil
	}
	return nil, nil
}

// findModUpperBound finds the attempt with the smallest modifier greater than given
func findModUpperBound(attempts []modAttempt, mod float64) *modAttempt {
	var best *modAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.mod > mod && (best == nil || a.mod < best.mod) {
			best = a
		}
	}
	return best
}

// findModLowerBound finds the attempt with the largest modifier less than given
func findModLowerBound(attempts []modAttempt, mod float64) *modAttempt {
	var best *modAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.mod < mod && (best == nil || a.mod > best.mod) {
			best = a
		}
	}
	return best
}

// hasModAttempt checks if a modifier has already been tested (within tolerance)
func hasModAttempt(attempts []modAttempt, mod float64) bool {
	for _, a := range attempts {
		if math.Abs(a.mod-mod) < minModRange/10 {
			return true
		}
	}
	return false
}

// findBestModAttempt finds the attempt with lowest modifier (most compression) that meets threshold
func findBestModAttempt(attempts []modAttempt, threshold float64) *modAttempt {
	var best *modAttempt
	for i := range attempts {
		a := &attempts[i]
		if a.score >= threshold && (best == nil || a.mod < best.mod) {
			best = a
		}
	}
	return best
}

// findAbsoluteBestModAttempt returns the attempt with highest VMAF score regardless of threshold.
// Ties broken by preferring lower modifier (more compression → smaller file).
func findAbsoluteBestModAttempt(attempts []modAttempt) *modAttempt {
	var best *modAttempt
	for i := range attempts {
		a := &attempts[i]
		if best == nil || a.score > best.score || (a.score == best.score && a.mod < best.mod) {
			best = a
		}
	}
	return best
}

// vmafLerpMod interpolates to find modifier that should produce the threshold score.
// worseMod has score < threshold (lower modifier), betterMod has score >= threshold (higher modifier).
func vmafLerpMod(threshold float64, worseMod, worseScore, betterMod, betterScore float64) float64 {
	scoreDiff := betterScore - worseScore
	if scoreDiff <= 0 {
		return (worseMod + betterMod) / 2
	}

	factor := (threshold - worseScore) / scoreDiff
	modDiff := betterMod - worseMod
	lerp := worseMod + modDiff*factor

	// Clamp to interior with small margin
	margin := minModRange / 2
	if lerp <= worseMod+margin {
		lerp = worseMod + margin
	}
	if lerp >= betterMod-margin {
		lerp = betterMod - margin
	}
	return lerp
}

