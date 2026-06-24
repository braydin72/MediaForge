// Package util provides shared utility functions across the application.
package util

import (
	"fmt"
	"time"
)

// FormatBytes formats a byte count as a human-readable string (e.g., "1.5 GB").
// Uses binary units (1024 bytes = 1 KB).
func FormatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatDuration formats a duration as a human-readable string (e.g., "1h 30m").
// Returns empty string for negative durations.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		return ""
	}

	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
