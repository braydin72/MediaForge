package config

// ValidTonemapAlgorithms contains the supported HDR tonemapping algorithms.
// These map to FFmpeg's zscale tonemap filter options.
var ValidTonemapAlgorithms = []string{
	"hable",    // Filmic tonemapping, good for movies (default)
	"bt2390",   // ITU-R BT.2390 EETF, broadcast standard
	"reinhard", // Simple Reinhard operator
	"mobius",   // Mobius function, smooth highlight rolloff
	"clip",     // Hard clip (simple but can lose detail)
	"linear",   // Linear scaling (preserves ratios)
	"gamma",    // Gamma correction only
}

// DefaultTonemapAlgorithm is the default tonemapping algorithm.
const DefaultTonemapAlgorithm = "hable"

// IsValidTonemapAlgorithm returns true if the algorithm name is valid.
func IsValidTonemapAlgorithm(algorithm string) bool {
	for _, valid := range ValidTonemapAlgorithms {
		if algorithm == valid {
			return true
		}
	}
	return false
}

// ValidateTonemapAlgorithm returns the algorithm if valid, or the default if invalid.
func ValidateTonemapAlgorithm(algorithm string) string {
	if IsValidTonemapAlgorithm(algorithm) {
		return algorithm
	}
	return DefaultTonemapAlgorithm
}
