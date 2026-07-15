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
			got := SamplePositions(tt.duration, "seed.mkv")
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
			got := SamplePositions(tt.duration, "seed.mkv")
			if len(got) != tt.wantLen {
				t.Errorf("SamplePositions(%v) = %v, want len %d", tt.duration, got, tt.wantLen)
			}
		})
	}
}

// TestSamplePositionsJitterBounds verifies jittered positions stay within
// their anchor windows and don't overlap adjacent anchors.
func TestSamplePositionsJitterBounds(t *testing.T) {
	anchors := []float64{0.25, 0.50, 0.75}
	for _, seedKey := range []string{"a.mkv", "b.mkv", "S01E01.mkv", "S01E02.mkv"} {
		got := SamplePositions(2*time.Hour, seedKey)
		if len(got) != 3 {
			t.Fatalf("SamplePositions(%q) len = %d, want 3", seedKey, len(got))
		}
		for i, pos := range got {
			if pos < anchors[i]-sampleJitterRange || pos > anchors[i]+sampleJitterRange {
				t.Errorf("SamplePositions(%q)[%d] = %v, outside jitter window around %v", seedKey, i, pos, anchors[i])
			}
		}
	}
}

// TestSamplePositionsDeterministic verifies the same seedKey always produces
// the same positions, and different seed keys produce different positions.
func TestSamplePositionsDeterministic(t *testing.T) {
	a1 := SamplePositions(2*time.Hour, "episode1.mkv")
	a2 := SamplePositions(2*time.Hour, "episode1.mkv")
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Errorf("SamplePositions not deterministic: %v vs %v", a1, a2)
		}
	}

	b := SamplePositions(2*time.Hour, "episode2.mkv")
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
