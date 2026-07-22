package vmaf

// AnalysisResult holds the results of VMAF analysis
type AnalysisResult struct {
	OptimalCRF  int     // CRF/CQ/QP value (0 if bitrate-based)
	QualityMod  float64 // Bitrate modifier for VideoToolbox (0 if CRF-based)
	VMafScore   float64 // Achieved VMAF score
	ShouldSkip  bool    // True if file should be skipped
	SkipReason  string  // Reason for skip
	SamplesUsed int     // Number of samples analyzed
	BestEffort  bool    // True if no CRF cleared the threshold; VMafScore is the best achievable ceiling

	// CeilingVMAF and EffectiveThreshold are only set in adaptive-target mode
	// (Analyzer.Adaptive). CeilingVMAF is the content's near-lossless VMAF
	// ceiling; EffectiveThreshold is min(tier_threshold, CeilingVMAF-margin),
	// the value actually used to drive the search.
	CeilingVMAF        float64
	EffectiveThreshold float64
}
