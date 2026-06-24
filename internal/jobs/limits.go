package jobs

// Worker count limits
const (
	MinWorkers = 1
	MaxWorkers = 6
)

// VMAF analysis concurrency limits
const (
	MinConcurrentAnalyses = 1
	MaxConcurrentAnalyses = 3
)

// ClampWorkerCount ensures the worker count is within valid bounds.
func ClampWorkerCount(n int) int {
	if n < MinWorkers {
		return MinWorkers
	}
	if n > MaxWorkers {
		return MaxWorkers
	}
	return n
}

// ClampAnalysisCount ensures the analysis count is within valid bounds.
func ClampAnalysisCount(n int) int {
	if n < MinConcurrentAnalyses {
		return MinConcurrentAnalyses
	}
	if n > MaxConcurrentAnalyses {
		return MaxConcurrentAnalyses
	}
	return n
}

// IsValidAnalysisCount returns true if the analysis count is within valid bounds.
func IsValidAnalysisCount(n int) bool {
	return n >= MinConcurrentAnalyses && n <= MaxConcurrentAnalyses
}

// SmartShrink quality tier validation

// ValidSmartShrinkQualities contains the valid quality tier names.
var ValidSmartShrinkQualities = []string{"acceptable", "good", "excellent"}

// IsValidSmartShrinkQuality returns true if the quality tier is valid.
func IsValidSmartShrinkQuality(quality string) bool {
	for _, valid := range ValidSmartShrinkQualities {
		if quality == valid {
			return true
		}
	}
	return false
}
