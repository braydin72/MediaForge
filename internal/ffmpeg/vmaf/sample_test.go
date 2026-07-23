package vmaf

import (
	"context"
	"testing"
	"time"
)

func TestExtractSamplesSignature(t *testing.T) {
	// Verify the function signature compiles
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ExtractSamples(ctx, "ffmpeg", "input.mkv", "/tmp", 60*time.Second, []float64{0.5})
	// Error expected due to cancelled context
	_ = err
}

func TestSamplePositions(t *testing.T) {
	tests := []struct {
		name       string
		duration   time.Duration
		wantLen    int
		wantSingle bool
	}{
		{"very short", 10 * time.Second, 1, true},
		{"short video", 45 * time.Second, 1, true},
		{"normal video", 120 * time.Second, 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SamplePositions(tt.duration, "seed.mkv", 3)
			if len(got) != tt.wantLen {
				t.Errorf("SamplePositions() len = %d, want %d", len(got), tt.wantLen)
				return
			}
			if tt.wantSingle && got[0] != 0.5 {
				t.Errorf("SamplePositions()[0] = %v, want 0.5", got[0])
			}
		})
	}
}

func TestSamplePositionsEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		wantLen  int
	}{
		{"zero duration", 0, 1},
		{"negative duration", -5 * time.Second, 1},
		{"exactly 59s", 59 * time.Second, 1},
		{"exactly 60s", 60 * time.Second, 3},
		{"very long video", 3600 * time.Second, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SamplePositions(tt.duration, "seed.mkv", 3)
			if len(got) != tt.wantLen {
				t.Errorf("SamplePositions(%v) = %v, want len %d", tt.duration, got, tt.wantLen)
			}
		})
	}
}

// evenAnchors returns the expected unjittered anchor positions for count
// evenly spaced samples, matching SamplePositions' own formula.
func evenAnchors(count int) []float64 {
	anchors := make([]float64, count)
	for i := range anchors {
		anchors[i] = (float64(i) + 0.5) / float64(count)
	}
	return anchors
}

// TestSamplePositionsJitterBounds verifies jittered positions stay within
// their anchor windows and don't overlap adjacent anchors, across several
// sample counts.
func TestSamplePositionsJitterBounds(t *testing.T) {
	for _, count := range []int{3, 4, 5} {
		anchors := evenAnchors(count)
		jitterRange := sampleJitterRange
		if maxRange := 0.4 / float64(count); maxRange < jitterRange {
			jitterRange = maxRange
		}
		for _, seedKey := range []string{"a.mkv", "b.mkv", "S01E01.mkv", "S01E02.mkv"} {
			got := SamplePositions(2*time.Hour, seedKey, count)
			if len(got) != count {
				t.Fatalf("SamplePositions(%q, %d) len = %d, want %d", seedKey, count, len(got), count)
			}
			for i, pos := range got {
				if pos < anchors[i]-jitterRange || pos > anchors[i]+jitterRange {
					t.Errorf("SamplePositions(%q, %d)[%d] = %v, outside jitter window around %v", seedKey, count, i, pos, anchors[i])
				}
			}
		}
	}
}

// TestSamplePositionsNoOverlap verifies adjacent sample windows never overlap
// regardless of sample count, so no two samples can land on the same clip.
func TestSamplePositionsNoOverlap(t *testing.T) {
	for _, count := range []int{3, 4, 5, 6} {
		got := SamplePositions(2*time.Hour, "overlap-check.mkv", count)
		for i := 1; i < len(got); i++ {
			if got[i] <= got[i-1] {
				t.Errorf("count=%d: positions not strictly increasing: %v", count, got)
			}
		}
	}
}

// TestSamplePositionsDeterministic verifies the same seedKey always produces
// the same positions, and different seed keys produce different positions.
func TestSamplePositionsDeterministic(t *testing.T) {
	a1 := SamplePositions(2*time.Hour, "episode1.mkv", 4)
	a2 := SamplePositions(2*time.Hour, "episode1.mkv", 4)
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Errorf("SamplePositions not deterministic: %v vs %v", a1, a2)
		}
	}

	b := SamplePositions(2*time.Hour, "episode2.mkv", 4)
	same := true
	for i := range a1 {
		if a1[i] != b[i] {
			same = false
		}
	}
	if same {
		t.Errorf("SamplePositions gave identical positions for different seed keys: %v", a1)
	}
}
