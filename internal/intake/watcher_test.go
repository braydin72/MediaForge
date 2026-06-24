package intake

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/store"
)

func TestClassifyCodec(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"hevc", "hevc"},
		{"HEVC", "hevc"},
		{"h265", "hevc"},
		{"x265", "hevc"},
		{"h264", "h264"},
		{"H264", "h264"},
		{"avc", "h264"},
		{"x264", "h264"},
		{"av1", "unknown"},
		{"vp9", "unknown"},
		{"mpeg4", "unknown"},
		{"", "unknown"},
	}

	for _, c := range cases {
		got := classifyCodec(c.raw)
		if got != c.want {
			t.Errorf("classifyCodec(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// TestWatcherStabilityCheck verifies that a file is detected and sent to the
// review queue after it stabilises (ffprobe will fail on a fake file, which is
// the expected path we can observe without a real video file).
func TestWatcherStabilityCheck(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	watchDir := filepath.Join(dir, "watch")
	if err := os.MkdirAll(watchDir, 0755); err != nil {
		t.Fatal(err)
	}

	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	cfg := config.IntakeConfig{
		Enabled:       true,
		WatchFolder:   watchDir,
		StagingFolder: filepath.Join(dir, "staging"),
		StabilityCheck: config.IntakeStabilityConfig{
			IntervalSeconds: 0, // 0 → treated as <1, clamped to 1 by Load, but we set directly
			PassesRequired:  2,
		},
	}
	// Force sub-second stability interval for the test.
	cfg.StabilityCheck.IntervalSeconds = 1

	w := NewWatcher(&cfg, "ffprobe", st)
	w.ScanInterval = 200 * time.Millisecond

	// Use a very short stability interval by patching the config.
	// 1-second interval * 2 passes = ~3 seconds to stabilise.
	// Use 100ms instead.
	cfg.StabilityCheck.IntervalSeconds = 0 // will be overridden below
	w.cfg = cfg

	// Override stability interval to 100ms by replacing cfg inside the watcher.
	// We achieve this by setting IntervalSeconds=0 and patching waitForStability
	// via the public ScanInterval path.  Since we can't easily inject the stability
	// duration, we instead set IntervalSeconds to a real value and accept the ~2s wait.
	w.cfg.StabilityCheck.IntervalSeconds = 1
	w.cfg.StabilityCheck.PassesRequired = 2

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go w.Start(ctx)

	// Write a fake .mkv file (not a real video — ffprobe will fail).
	fakePath := filepath.Join(watchDir, "test_movie.mkv")
	if err := os.WriteFile(fakePath, []byte("not a real video file"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for the file to be processed. The pipeline is:
	//   scan (200ms) → stability check (1s × 2 passes ≈ 3s) → ffprobe (fails) → review queue
	// Allow up to 12 seconds.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		count, err := st.GetReviewQueueCount()
		if err != nil {
			t.Fatalf("GetReviewQueueCount: %v", err)
		}
		if count > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	count, err := st.GetReviewQueueCount()
	if err != nil {
		t.Fatalf("GetReviewQueueCount: %v", err)
	}
	if count == 0 {
		t.Fatal("expected file to appear in review queue after ffprobe failure, but queue is empty")
	}

	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no review queue entries")
	}

	e := entries[0]
	t.Logf("review queue entry: filename=%q reason=%q status=%q", e.Filename, e.Reason, e.Status)

	if e.Filename != "test_movie.mkv" {
		t.Errorf("Filename = %q, want %q", e.Filename, "test_movie.mkv")
	}
	if e.Status != "pending" {
		t.Errorf("Status = %q, want pending", e.Status)
	}
	if e.Reason == "" {
		t.Error("Reason is empty, want a codec detection failure message")
	}
}

// TestWatcherIgnoresNonVideo verifies that non-video files are ignored.
func TestWatcherIgnoresNonVideo(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	watchDir := filepath.Join(dir, "watch")
	if err := os.MkdirAll(watchDir, 0755); err != nil {
		t.Fatal(err)
	}

	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	cfg := config.IntakeConfig{
		WatchFolder: watchDir,
		StabilityCheck: config.IntakeStabilityConfig{
			IntervalSeconds: 1,
			PassesRequired:  1,
		},
	}

	w := NewWatcher(&cfg, "ffprobe", st)
	w.ScanInterval = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Write a non-video file.
	if err := os.WriteFile(filepath.Join(watchDir, "readme.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	go w.Start(ctx)
	<-ctx.Done()

	count, _ := st.GetReviewQueueCount()
	if count != 0 {
		t.Errorf("expected 0 review queue entries for non-video file, got %d", count)
	}

	w.mu.Lock()
	knownCount := len(w.known)
	w.mu.Unlock()
	if knownCount != 0 {
		t.Errorf("expected 0 known files for non-video file, got %d", knownCount)
	}
}

// TestWatcherMissingFolder verifies the watcher does not panic on a missing watch folder.
func TestWatcherMissingFolder(t *testing.T) {
	dir := t.TempDir()

	st, err := store.NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.IntakeConfig{
		WatchFolder: filepath.Join(dir, "nonexistent"),
		StabilityCheck: config.IntakeStabilityConfig{
			IntervalSeconds: 1,
			PassesRequired:  1,
		},
	}

	w := NewWatcher(&cfg, "ffprobe", st)
	w.ScanInterval = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Should not panic.
	w.Start(ctx)
}
