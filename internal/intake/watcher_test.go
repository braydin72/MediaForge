package intake

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/store"
)

func TestClassifyCodec(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// Modern codecs — route to library
		{"hevc", "hevc"},
		{"HEVC", "hevc"},
		{"h265", "hevc"},
		{"x265", "hevc"},
		{"av1", "hevc"},
		{"libaom-av1", "hevc"},
		{"libsvtav1", "hevc"},
		// H.264 family — encode queue
		{"h264", "encode"},
		{"H264", "encode"},
		{"avc", "encode"},
		{"x264", "encode"},
		// Explicitly requested codecs — encode queue
		{"mpeg2video", "encode"},
		{"mpeg4", "encode"},
		{"vc1", "encode"},
		{"vp9", "encode"},
		// Other known video codecs — encode queue
		{"vp8", "encode"},
		{"wmv3", "encode"},
		{"theora", "encode"},
		{"flv1", "encode"},
		// Unrecognized — review queue
		{"", "unknown"},
		{"somethingweird", "unknown"},
		{"data", "unknown"},
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

// TestResolveAndGateLowConfidenceToReview verifies the AVC/HEVC shared gate routes a
// below-review-threshold match to the Review Queue and returns ok=false, so the file is
// never staged or enqueued. A TV filename with only a series-name match (no episode, no
// year, no episode title) scores 0.40 — below the 0.60 review threshold.
func TestResolveAndGateLowConfidenceToReview(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	// TVDB mock: series matches, but the requested episode is absent (404) → no episode,
	// no year → confidence 0.40.
	tvdb := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, breakingBadSearchBody) },
		"/v4/series": func(r *http.Request) *http.Response {
			return jsonResp(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{"episodes": []map[string]interface{}{}},
			})
		},
	}))

	cfg := config.IntakeConfig{}
	w := NewWatcher(&cfg, "ffprobe", st)
	w.Orchestrator = &Orchestrator{TVDB: tvdb}

	parsed := ParseFilename("Breaking Bad - S01E99.mkv")
	result, ok := w.resolveAndGate(context.Background(), "/incoming/Breaking Bad - S01E99.mkv", &parsed, nil)
	if ok {
		t.Fatalf("resolveAndGate: want ok=false for low confidence, got true (result=%+v)", result)
	}

	count, err := st.GetReviewQueueCount()
	if err != nil {
		t.Fatalf("GetReviewQueueCount: %v", err)
	}
	if count != 1 {
		t.Errorf("review queue count: want 1, got %d", count)
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

// TestMoveHEVCToLibrary_DuplicateReviewEntryUsesCorrectedFilename reproduces the
// real-world bug: TVDB corrects a season/episode numbering offset (filename says
// S48E30 "I Have Killed For You", TVDB's real episode is S49E30 "Her Last Call"),
// but the destination already has a file, so the corrected file is routed to the
// Review Queue instead of moved. Before the fix, sendDuplicateToReviewQueue stored
// the raw, uncorrected source filename — so a later "Re-encode Custom" / resolve,
// which re-parses ReviewEntry.Filename via ParseFilename, would reproduce the wrong
// season/episode. The stored Filename must reflect the correction.
func TestMoveHEVCToLibrary_DuplicateReviewEntryUsesCorrectedFilename(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	searchBody := map[string]interface{}{
		"data": []map[string]interface{}{
			{"tvdb_id": "72289", "name": "20/20", "year": "1978", "network": "ABC"},
		},
	}
	tvdb := newMockTVDBClient("validkey", routeByPath(map[string]func(*http.Request) *http.Response{
		"/v4/login":  func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, loginOKBody) },
		"/v4/search": func(r *http.Request) *http.Response { return jsonResp(http.StatusOK, searchBody) },
		"/v4/series": func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/page/") {
				return jsonResp(http.StatusOK, map[string]interface{}{
					"data":  map[string]interface{}{"episodes": []map[string]interface{}{}},
					"links": map[string]interface{}{"next": ""},
				})
			}
			switch r.URL.Query().Get("season") {
			case "48":
				return jsonResp(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"episodes": []map[string]interface{}{
							{"id": 1, "name": "I Have Killed For You", "aired": "2025-06-06", "seasonNumber": 48, "number": 30},
						},
					},
				})
			case "49":
				return jsonResp(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"episodes": []map[string]interface{}{
							{"id": 2, "name": "Her Last Call", "aired": "2023-03-10", "seasonNumber": 49, "number": 30},
						},
					},
				})
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}
			}
		},
	}))

	cfg := config.IntakeConfig{}
	cfg.Library.TVShows = filepath.Join(dir, "TV Shows")

	w := NewWatcher(&cfg, "ffprobe", st)
	w.Orchestrator = &Orchestrator{TVDB: tvdb}

	// Pre-create the corrected destination file so checkDuplicate routes to review
	// instead of moving.
	destDir := filepath.Join(cfg.Library.TVShows, "20_20 (1978)", "Season 49")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatal(err)
	}
	destFile := filepath.Join(destDir, "20_20 - S49E30 - Her Last Call.mkv")
	if err := os.WriteFile(destFile, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	incomingPath := filepath.Join(dir, "20_20 (1978) - S48E30 - Her Last Call.mkv")
	probe := &ffmpeg.ProbeResult{VideoCodec: "hevc"}

	w.moveHEVCToLibrary(context.Background(), incomingPath, probe)

	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 review queue entry, got %d", len(entries))
	}

	e := entries[0]
	if strings.Contains(e.Filename, "S48E30") {
		t.Errorf("Filename = %q, still contains uncorrected S48E30", e.Filename)
	}
	if !strings.Contains(e.Filename, "S49E30") {
		t.Errorf("Filename = %q, want it to contain corrected S49E30", e.Filename)
	}
	if strings.Contains(e.Filename, "I Have Killed For You") {
		t.Errorf("Filename = %q, still contains uncorrected episode title", e.Filename)
	}
	if !strings.Contains(e.Filename, "Her Last Call") {
		t.Errorf("Filename = %q, want it to contain corrected episode title", e.Filename)
	}
	if e.DuplicateInfo == "" {
		t.Error("expected DuplicateInfo to be set for a duplicate-conflict entry")
	}

	// Reproduce what ResolveReviewEntry/ResubmitReviewEntry do with the stored
	// Filename to prove the round-trip yields the corrected season/episode.
	reparsed := ParseFilename(e.Filename)
	if reparsed.Season != 49 || reparsed.Episode != 30 {
		t.Errorf("re-parsed season/episode: want S49E30, got S%02dE%02d", reparsed.Season, reparsed.Episode)
	}
}
