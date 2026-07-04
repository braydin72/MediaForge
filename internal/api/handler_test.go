package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/braydin72/mediaforge/internal/browse"
	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/intake"
	"github.com/braydin72/mediaforge/internal/jobs"
	"github.com/braydin72/mediaforge/internal/store"
)

func setupTestHandler(t *testing.T) (*Handler, string) {
	tmpDir := t.TempDir()

	// Create test directory structure
	tvDir := filepath.Join(tmpDir, "TV Shows", "Test Show", "Season 1")
	if err := os.MkdirAll(tvDir, 0755); err != nil {
		t.Fatalf("failed to create test dirs: %v", err)
	}

	// Create fake video files
	for _, name := range []string{"episode1.mkv", "episode2.mkv"} {
		path := filepath.Join(tvDir, name)
		if err := os.WriteFile(path, []byte("fake video"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	cfg := &config.Config{
		MediaPath:        tmpDir,
		OriginalHandling: "replace",
		Workers:          1,
		FFmpegPath:       "ffmpeg",
		FFprobePath:      "ffprobe",
	}

	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)
	queue := jobs.NewQueue()
	pool := jobs.NewWorkerPool(queue, cfg, nil)

	handler := NewHandler(browser, queue, pool, cfg, "")

	return handler, tmpDir
}

func TestBrowseEndpoint(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	// Test browsing root
	req := httptest.NewRequest("GET", "/api/browse", nil)
	w := httptest.NewRecorder()

	handler.Browse(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result browse.BrowseResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if result.Path != filepath.ToSlash(tmpDir) {
		t.Errorf("expected path %s, got %s", filepath.ToSlash(tmpDir), result.Path)
	}

	if len(result.Entries) == 0 {
		t.Error("expected at least one entry")
	}

	t.Logf("Browse response: %d entries", len(result.Entries))
}

func TestBrowseWithPath(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	tvDir := filepath.Join(tmpDir, "TV Shows")

	req := httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(tvDir), nil)
	w := httptest.NewRecorder()

	handler.Browse(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result browse.BrowseResult
	json.Unmarshal(w.Body.Bytes(), &result)

	if result.Path != filepath.ToSlash(tvDir) {
		t.Errorf("expected path %s, got %s", filepath.ToSlash(tvDir), result.Path)
	}

	if result.Parent != filepath.ToSlash(tmpDir) {
		t.Errorf("expected parent %s, got %s", filepath.ToSlash(tmpDir), result.Parent)
	}
}

func TestPresetsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := httptest.NewRequest("GET", "/api/presets", nil)
	w := httptest.NewRecorder()

	handler.Presets(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var presets []*ffmpeg.Preset
	if err := json.Unmarshal(w.Body.Bytes(), &presets); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(presets) != 4 {
		t.Errorf("expected 4 presets, got %d", len(presets))
	}

	t.Logf("Presets: %v", presets)
}

func TestJobsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Initially should have no jobs
	req := httptest.NewRequest("GET", "/api/jobs", nil)
	w := httptest.NewRecorder()

	handler.ListJobs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)

	jobs, _ := result["jobs"].([]interface{})
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestCreateJobsEndpoint(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	// Create jobs for the test show
	showDir := filepath.Join(tmpDir, "TV Shows", "Test Show", "Season 1")

	reqBody := CreateJobsRequest{
		Paths:    []string{showDir},
		PresetID: "compress-hevc",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.CreateJobs(w, req)

	// Will fail because fake video files can't be probed
	// But we can at least check it tries
	t.Logf("Create jobs response: %d - %s", w.Code, w.Body.String())
}

func TestConfigEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Get config
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()

	handler.GetConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var cfg map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &cfg)

	if cfg["original_handling"] != "replace" {
		t.Errorf("expected original_handling 'replace', got %v", cfg["original_handling"])
	}

	t.Logf("Config: %v", cfg)
}

func TestUpdateConfigEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Update config
	keepVal := "keep"
	reqBody := UpdateConfigRequest{
		OriginalHandling: &keepVal,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.UpdateConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Verify it changed
	req = httptest.NewRequest("GET", "/api/config", nil)
	w = httptest.NewRecorder()

	handler.GetConfig(w, req)

	var cfg map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &cfg)

	if cfg["original_handling"] != "keep" {
		t.Errorf("expected original_handling 'keep', got %v", cfg["original_handling"])
	}
}

func TestUpdateConfigLogLevel(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Verify log_level appears in GET response
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handler.GetConfig(w, req)

	var cfg map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if _, ok := cfg["log_level"]; !ok {
		t.Fatal("log_level missing from GET /api/config response")
	}

	// Update log level to debug
	debugVal := "debug"
	reqBody := UpdateConfigRequest{
		LogLevel: &debugVal,
	}
	body, _ := json.Marshal(reqBody)

	req = httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.UpdateConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Verify it changed in config
	req = httptest.NewRequest("GET", "/api/config", nil)
	w = httptest.NewRecorder()
	handler.GetConfig(w, req)

	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg["log_level"] != "debug" {
		t.Errorf("expected log_level 'debug', got %v", cfg["log_level"])
	}
}

func TestUpdateConfigLogLevelInvalid(t *testing.T) {
	handler, _ := setupTestHandler(t)

	badVal := "verbose"
	reqBody := UpdateConfigRequest{
		LogLevel: &badVal,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.UpdateConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid log level, got %d", w.Code)
	}
}

func TestStatsEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()

	handler.Stats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats jobs.Stats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("failed to parse stats: %v", err)
	}

	if stats.Total != 0 {
		t.Errorf("expected 0 total jobs, got %d", stats.Total)
	}

	t.Logf("Stats: %+v", stats)
}

func TestJobStreamEndpoint(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Create a request with a context that will timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest("GET", "/api/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Run in goroutine since it blocks
	done := make(chan bool)
	go func() {
		handler.JobStream(w, req)
		done <- true
	}()

	// Wait for timeout or completion
	select {
	case <-done:
		// Good - context cancelled
	case <-time.After(time.Second):
		t.Error("SSE handler didn't respect context cancellation")
	}

	// Should have SSE headers
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream content type, got %s", w.Header().Get("Content-Type"))
	}

	// Should have initial data
	if !bytes.Contains(w.Body.Bytes(), []byte("data:")) {
		t.Error("expected SSE data in response")
	}

	t.Logf("SSE response: %s", w.Body.String()[:min(200, len(w.Body.String()))])
}

func TestTestNotificationsNoChannels(t *testing.T) {
	handler, _ := setupTestHandler(t)
	// Default config has no email credentials, so no channels should be configured.
	req := httptest.NewRequest("POST", "/api/notifications/test", nil)
	w := httptest.NewRecorder()
	handler.TestNotifications(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no channels configured, got %d", w.Code)
	}
}

func TestTestNotificationsWithChannel(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Wire a working mock notifier via the exported method.
	sent := 0
	mn := &handlerTestNotifier{sendFn: func() error { sent++; return nil }}
	handler.dispatcher.AddPerFileNotifier(mn)

	req := httptest.NewRequest("POST", "/api/notifications/test", nil)
	w := httptest.NewRecorder()
	handler.TestNotifications(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when channel configured, got %d body=%s", w.Code, w.Body.String())
	}
	if sent != 1 {
		t.Errorf("expected 1 notification sent, got %d", sent)
	}
}

// handlerTestNotifier is a minimal Notifier for handler tests.
type handlerTestNotifier struct {
	sendFn func() error
}

func (n *handlerTestNotifier) Name() string       { return "test" }
func (n *handlerTestNotifier) IsConfigured() bool { return true }
func (n *handlerTestNotifier) Send(_ context.Context, _, _ string) error {
	return n.sendFn()
}

// mockReviewStore is a minimal in-memory ReviewQueueStore for handler tests.
type mockReviewStore struct {
	entries map[string]*store.ReviewEntry
}

func newMockReviewStore(entries ...*store.ReviewEntry) *mockReviewStore {
	m := &mockReviewStore{entries: map[string]*store.ReviewEntry{}}
	for _, e := range entries {
		m.entries[e.ID] = e
	}
	return m
}

func (m *mockReviewStore) GetReviewQueue() ([]store.ReviewEntry, error) {
	out := make([]store.ReviewEntry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, *e)
	}
	return out, nil
}
func (m *mockReviewStore) GetReviewEntry(id string) (*store.ReviewEntry, error) {
	return m.entries[id], nil
}
func (m *mockReviewStore) GetReviewQueueCount() (int, error) { return len(m.entries), nil }
func (m *mockReviewStore) UpdateReviewQueueStatus(id, status string) error {
	if e := m.entries[id]; e != nil {
		e.Status = status
	}
	return nil
}
func (m *mockReviewStore) UpdateReviewEntryReason(id, reason string) error {
	if e := m.entries[id]; e != nil {
		e.Reason = reason
	}
	return nil
}

// resolveTestSetup wires a handler with a library dir and a single pending review
// entry whose source file exists on disk. Returns the handler, the mock store, the
// entry, and the expected library destination.
func resolveTestSetup(t *testing.T) (*Handler, *mockReviewStore, *store.ReviewEntry, string) {
	t.Helper()
	handler, tmpDir := setupTestHandler(t)

	moviesDir := filepath.Join(tmpDir, "Library", "Movies")
	handler.cfg.Intake.Library.Movies = moviesDir

	src := filepath.Join(tmpDir, "incoming", "Big Movie 2020.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatalf("mkdir incoming: %v", err)
	}
	if err := os.WriteFile(src, []byte("hevc bytes"), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	entry := &store.ReviewEntry{
		ID:           "entry-1",
		OriginalPath: src,
		Filename:     "Big Movie 2020.mkv",
		Reason:       "low confidence match",
		Status:       "pending",
	}
	ms := newMockReviewStore(entry)
	handler.SetReviewStore(ms)

	wantDst := filepath.Join(moviesDir, "Big Movie (2020)", "Big Movie (2020).mkv")
	return handler, ms, entry, wantDst
}

func resolveRequest(id string, candidate map[string]interface{}) *http.Request {
	body, _ := json.Marshal(map[string]interface{}{"candidate": candidate})
	req := httptest.NewRequest("PUT", "/api/review/"+id+"/resolve", bytes.NewReader(body))
	req.SetPathValue("id", id)
	return req
}

func TestResolveReviewEntry_MovesFile(t *testing.T) {
	handler, ms, entry, wantDst := resolveTestSetup(t)

	req := resolveRequest(entry.ID, map[string]interface{}{
		"title": "Big Movie", "year": 2020, "media_type": "movie",
	})
	w := httptest.NewRecorder()
	handler.ResolveReviewEntry(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(wantDst); err != nil {
		t.Errorf("expected file moved to %s, stat err: %v", wantDst, err)
	}
	if _, err := os.Stat(entry.OriginalPath); !os.IsNotExist(err) {
		t.Errorf("expected source removed, stat err: %v", err)
	}
	if ms.entries[entry.ID].Status != "resolved" {
		t.Errorf("expected entry resolved, got %s", ms.entries[entry.ID].Status)
	}
}

func TestResolveReviewEntry_MissingTitle(t *testing.T) {
	handler, _, entry, _ := resolveTestSetup(t)
	req := resolveRequest(entry.ID, map[string]interface{}{"year": 2020})
	w := httptest.NewRecorder()
	handler.ResolveReviewEntry(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing title, got %d", w.Code)
	}
}

func TestResolveReviewEntry_DuplicateAtDestination(t *testing.T) {
	handler, ms, entry, wantDst := resolveTestSetup(t)

	// Pre-create the destination file so the move must refuse.
	if err := os.MkdirAll(filepath.Dir(wantDst), 0755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(wantDst, []byte("existing"), 0644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	req := resolveRequest(entry.ID, map[string]interface{}{
		"title": "Big Movie", "year": 2020, "media_type": "movie",
	})
	w := httptest.NewRecorder()
	handler.ResolveReviewEntry(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate destination, got %d body=%s", w.Code, w.Body.String())
	}
	// Entry stays pending; source is untouched (no silent loss).
	if ms.entries[entry.ID].Status != "pending" {
		t.Errorf("expected entry still pending, got %s", ms.entries[entry.ID].Status)
	}
	if _, err := os.Stat(entry.OriginalPath); err != nil {
		t.Errorf("expected source untouched, stat err: %v", err)
	}
}

func TestResolveReviewEntry_SourceMissing(t *testing.T) {
	handler, ms, entry, _ := resolveTestSetup(t)
	if err := os.Remove(entry.OriginalPath); err != nil {
		t.Fatalf("remove src: %v", err)
	}
	req := resolveRequest(entry.ID, map[string]interface{}{
		"title": "Big Movie", "year": 2020, "media_type": "movie",
	})
	w := httptest.NewRecorder()
	handler.ResolveReviewEntry(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for missing source, got %d", w.Code)
	}
	if ms.entries[entry.ID].Status != "pending" {
		t.Errorf("expected entry still pending, got %s", ms.entries[entry.ID].Status)
	}
}

func discardRequest(id string) *http.Request {
	req := httptest.NewRequest("PUT", "/api/review/"+id+"/discard", nil)
	req.SetPathValue("id", id)
	return req
}

// TestDiscardReviewEntry_DuplicateDeletesIncomingFile covers the "Keep Existing"
// action: discarding a duplicate-conflict entry must delete the incoming file
// from disk, not just mark the entry discarded.
func TestDiscardReviewEntry_DuplicateDeletesIncomingFile(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	incoming := filepath.Join(tmpDir, "incoming", "Big Movie 2020.mkv")
	if err := os.MkdirAll(filepath.Dir(incoming), 0755); err != nil {
		t.Fatalf("mkdir incoming: %v", err)
	}
	if err := os.WriteFile(incoming, []byte("incoming bytes"), 0644); err != nil {
		t.Fatalf("write incoming: %v", err)
	}
	existing := filepath.Join(tmpDir, "Library", "Big Movie (2020).mkv")
	if err := os.MkdirAll(filepath.Dir(existing), 0755); err != nil {
		t.Fatalf("mkdir library: %v", err)
	}
	if err := os.WriteFile(existing, []byte("existing bytes"), 0644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	dupCtx := intake.DuplicateContext{
		Incoming: intake.DuplicateFileInfo{Path: incoming},
		Existing: intake.DuplicateFileInfo{Path: existing},
	}
	b, _ := json.Marshal(dupCtx)
	entry := &store.ReviewEntry{
		ID:            "entry-dup",
		OriginalPath:  incoming,
		Filename:      "Big Movie 2020.mkv",
		Reason:        "duplicate: file already exists at destination",
		DuplicateInfo: string(b),
		Status:        "pending",
	}
	ms := newMockReviewStore(entry)
	handler.SetReviewStore(ms)

	w := httptest.NewRecorder()
	handler.DiscardReviewEntry(w, discardRequest(entry.ID))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(incoming); !os.IsNotExist(err) {
		t.Errorf("expected incoming file deleted, stat err: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Errorf("expected existing library file untouched, stat err: %v", err)
	}
	if ms.entries[entry.ID].Status != "discarded" {
		t.Errorf("expected entry discarded, got %s", ms.entries[entry.ID].Status)
	}
}

// TestDiscardReviewEntry_NonDuplicateNoFileTouched ensures a plain (non-duplicate)
// discard does not attempt to delete any file — there is no incoming path to delete
// against, and the source file for a normal low-confidence entry should be left
// alone (the user may want to Search Manually instead).
func TestDiscardReviewEntry_NonDuplicateNoFileTouched(t *testing.T) {
	handler, ms, entry, _ := resolveTestSetup(t)

	w := httptest.NewRecorder()
	handler.DiscardReviewEntry(w, discardRequest(entry.ID))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(entry.OriginalPath); err != nil {
		t.Errorf("expected source file untouched, stat err: %v", err)
	}
	if ms.entries[entry.ID].Status != "discarded" {
		t.Errorf("expected entry discarded, got %s", ms.entries[entry.ID].Status)
	}
}

// TestDiscardReviewEntry_IncomingAlreadyGone ensures discarding a duplicate entry
// whose incoming file was already removed (e.g. a repeat click) does not error.
func TestDiscardReviewEntry_IncomingAlreadyGone(t *testing.T) {
	handler, tmpDir := setupTestHandler(t)

	incoming := filepath.Join(tmpDir, "incoming", "Gone Movie 2020.mkv")
	dupCtx := intake.DuplicateContext{
		Incoming: intake.DuplicateFileInfo{Path: incoming},
		Existing: intake.DuplicateFileInfo{Path: filepath.Join(tmpDir, "Library", "Gone Movie (2020).mkv")},
	}
	b, _ := json.Marshal(dupCtx)
	entry := &store.ReviewEntry{
		ID:            "entry-gone",
		OriginalPath:  incoming,
		Filename:      "Gone Movie 2020.mkv",
		DuplicateInfo: string(b),
		Status:        "pending",
	}
	ms := newMockReviewStore(entry)
	handler.SetReviewStore(ms)

	w := httptest.NewRecorder()
	handler.DiscardReviewEntry(w, discardRequest(entry.ID))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even when incoming file is already gone, got %d body=%s", w.Code, w.Body.String())
	}
	if ms.entries[entry.ID].Status != "discarded" {
		t.Errorf("expected entry discarded, got %s", ms.entries[entry.ID].Status)
	}
}
