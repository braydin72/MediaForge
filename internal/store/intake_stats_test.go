package store

import (
	"path/filepath"
	"testing"
	"time"
)

func makeRecord(id, outcome, failureReason string, origBytes, outBytes int64, reductionPct, fps float64) *ProcessingRecord {
	return &ProcessingRecord{
		ID:                id,
		Filename:          id + ".mkv",
		SourcePath:        "/incoming/" + id + ".mkv",
		DetectedCodec:     "h264",
		Container:         "matroska",
		Resolution:        "1920x1080",
		DurationSecs:      3600,
		PipelineMode:      "full",
		OriginalSizeBytes: origBytes,
		OutputSizeBytes:   outBytes,
		SizeReductionPct:  reductionPct,
		EncodeFPS:         fps,
		Outcome:           outcome,
		FailureReason:     failureReason,
		CreatedAt:         time.Now().UTC(),
	}
}

func TestAddAndGetProcessingRecord(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	r := makeRecord("file-1", OutcomeSuccess, "", 1_000_000_000, 400_000_000, 60.0, 120.5)
	r.MatchedTitle = "The Matrix"
	r.MatchedYear = 1999
	r.TMDBId = "603"
	r.EncodePreset = "good"
	r.EncodeSpeed = "medium"
	r.EncodeGPU = "nvidia"
	r.EncodeDurationSecs = 300

	if err := st.AddProcessingRecord(r); err != nil {
		t.Fatalf("AddProcessingRecord: %v", err)
	}

	records, err := st.GetProcessingRecords(10, 0)
	if err != nil {
		t.Fatalf("GetProcessingRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}

	got := records[0]
	if got.ID != r.ID {
		t.Errorf("ID = %q, want %q", got.ID, r.ID)
	}
	if got.MatchedTitle != r.MatchedTitle {
		t.Errorf("MatchedTitle = %q, want %q", got.MatchedTitle, r.MatchedTitle)
	}
	if got.MatchedYear != r.MatchedYear {
		t.Errorf("MatchedYear = %d, want %d", got.MatchedYear, r.MatchedYear)
	}
	if got.TMDBId != r.TMDBId {
		t.Errorf("TMDBId = %q, want %q", got.TMDBId, r.TMDBId)
	}
	if got.OriginalSizeBytes != r.OriginalSizeBytes {
		t.Errorf("OriginalSizeBytes = %d, want %d", got.OriginalSizeBytes, r.OriginalSizeBytes)
	}
	if got.Outcome != OutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeSuccess)
	}
}

func TestGetProcessingRecords_Pagination(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	for i := range 5 {
		r := makeRecord("file-pg-"+string(rune('0'+i)), OutcomeSuccess, "", 1e9, 4e8, 60.0, 100.0)
		if err := st.AddProcessingRecord(r); err != nil {
			t.Fatalf("AddProcessingRecord: %v", err)
		}
	}

	page, err := st.GetProcessingRecords(3, 0)
	if err != nil {
		t.Fatalf("GetProcessingRecords page 0: %v", err)
	}
	if len(page) != 3 {
		t.Errorf("page 0 len = %d, want 3", len(page))
	}

	page2, err := st.GetProcessingRecords(3, 3)
	if err != nil {
		t.Fatalf("GetProcessingRecords page 1: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 1 len = %d, want 2", len(page2))
	}
}

func TestGetIntakeStats_Empty(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	stats, err := st.GetIntakeStats()
	if err != nil {
		t.Fatalf("GetIntakeStats: %v", err)
	}

	if stats.LifetimeFilesProcessed != 0 {
		t.Errorf("LifetimeFilesProcessed = %d, want 0", stats.LifetimeFilesProcessed)
	}
	if stats.LifetimeStorageSavedBytes != 0 {
		t.Errorf("LifetimeStorageSavedBytes = %d, want 0", stats.LifetimeStorageSavedBytes)
	}
	if stats.MostCommonFailureReason != "" {
		t.Errorf("MostCommonFailureReason = %q, want empty", stats.MostCommonFailureReason)
	}
}

func TestGetIntakeStats_Aggregation(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	// 2 successful encodes saving 600 MB and 400 MB respectively
	if err := st.AddProcessingRecord(makeRecord("s1", OutcomeSuccess, "", 1_000_000_000, 400_000_000, 60.0, 120.0)); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProcessingRecord(makeRecord("s2", OutcomeSuccess, "", 1_000_000_000, 600_000_000, 40.0, 80.0)); err != nil {
		t.Fatal(err)
	}
	// 1 file in review queue — no encode, no savings
	if err := st.AddProcessingRecord(makeRecord("r1", OutcomeReviewQueue, "no metadata match", 500_000_000, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}

	stats, err := st.GetIntakeStats()
	if err != nil {
		t.Fatalf("GetIntakeStats: %v", err)
	}

	if stats.LifetimeFilesProcessed != 3 {
		t.Errorf("LifetimeFilesProcessed = %d, want 3", stats.LifetimeFilesProcessed)
	}

	// savings = (1e9-4e8) + (1e9-6e8) = 600e6 + 400e6 = 1e9
	wantSaved := int64(1_000_000_000)
	if stats.LifetimeStorageSavedBytes != wantSaved {
		t.Errorf("LifetimeStorageSavedBytes = %d, want %d", stats.LifetimeStorageSavedBytes, wantSaved)
	}

	// avg reduction of encoded files only: (60+40)/2 = 50
	if stats.LifetimeAvgReductionPct < 49.9 || stats.LifetimeAvgReductionPct > 50.1 {
		t.Errorf("LifetimeAvgReductionPct = %.2f, want ~50.0", stats.LifetimeAvgReductionPct)
	}

	// success rate: 2/3 * 100 ≈ 66.67
	if stats.LifetimeEncodeSuccessRate < 66.0 || stats.LifetimeEncodeSuccessRate > 67.0 {
		t.Errorf("LifetimeEncodeSuccessRate = %.2f, want ~66.67", stats.LifetimeEncodeSuccessRate)
	}

	// avg fps of encoded files: (120+80)/2 = 100
	if stats.LifetimeAvgEncodeFPS < 99.9 || stats.LifetimeAvgEncodeFPS > 100.1 {
		t.Errorf("LifetimeAvgEncodeFPS = %.2f, want 100.0", stats.LifetimeAvgEncodeFPS)
	}
}

func TestGetIntakeStats_MostCommonFailure(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	reasons := []string{
		"no metadata match",
		"no metadata match",
		"codec detection failed",
	}
	for i, reason := range reasons {
		r := makeRecord("f"+string(rune('0'+i)), OutcomeReviewQueue, reason, 1e9, 0, 0, 0)
		if err := st.AddProcessingRecord(r); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := st.GetIntakeStats()
	if err != nil {
		t.Fatalf("GetIntakeStats: %v", err)
	}
	if stats.MostCommonFailureReason != "no metadata match" {
		t.Errorf("MostCommonFailureReason = %q, want %q", stats.MostCommonFailureReason, "no metadata match")
	}
	if stats.MostCommonFailureCount != 2 {
		t.Errorf("MostCommonFailureCount = %d, want 2", stats.MostCommonFailureCount)
	}
}

func TestResetIntakeLifetime(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	// Use explicit timestamps to avoid second-precision RFC3339 collisions.
	now := time.Now().UTC()
	past := now.Add(-2 * time.Second)
	future := now.Add(2 * time.Second)

	// Add a record clearly before the reset.
	old := makeRecord("old", OutcomeSuccess, "", 1e9, 4e8, 60.0, 100.0)
	old.CreatedAt = past
	if err := st.AddProcessingRecord(old); err != nil {
		t.Fatal(err)
	}

	before, err := st.GetIntakeStats()
	if err != nil {
		t.Fatal(err)
	}
	if before.LifetimeFilesProcessed != 1 {
		t.Fatalf("before reset: LifetimeFilesProcessed = %d, want 1", before.LifetimeFilesProcessed)
	}

	if err := st.ResetIntakeLifetime(); err != nil {
		t.Fatalf("ResetIntakeLifetime: %v", err)
	}

	after, err := st.GetIntakeStats()
	if err != nil {
		t.Fatal(err)
	}
	if after.LifetimeFilesProcessed != 0 {
		t.Errorf("after reset: LifetimeFilesProcessed = %d, want 0", after.LifetimeFilesProcessed)
	}
	if after.LifetimeResetAt.IsZero() {
		t.Error("LifetimeResetAt should be non-zero after reset")
	}

	// Record clearly after the reset should count.
	newR := makeRecord("new", OutcomeSuccess, "", 1e9, 4e8, 60.0, 100.0)
	newR.CreatedAt = future
	if err := st.AddProcessingRecord(newR); err != nil {
		t.Fatal(err)
	}
	final, err := st.GetIntakeStats()
	if err != nil {
		t.Fatal(err)
	}
	if final.LifetimeFilesProcessed != 1 {
		t.Errorf("after new record: LifetimeFilesProcessed = %d, want 1", final.LifetimeFilesProcessed)
	}
}

func TestResetIntakePeriod(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	// Record clearly before the reset.
	pre := makeRecord("pre", OutcomeSuccess, "", 1e9, 4e8, 60.0, 100.0)
	pre.CreatedAt = time.Now().UTC().Add(-2 * time.Second)
	if err := st.AddProcessingRecord(pre); err != nil {
		t.Fatal(err)
	}

	if err := st.ResetIntakePeriod(); err != nil {
		t.Fatalf("ResetIntakePeriod: %v", err)
	}

	after, err := st.GetIntakeStats()
	if err != nil {
		t.Fatal(err)
	}
	// Period count is 0 (pre-reset record excluded); lifetime count is still 1.
	if after.PeriodFilesProcessed != 0 {
		t.Errorf("PeriodFilesProcessed = %d, want 0", after.PeriodFilesProcessed)
	}
	if after.LifetimeFilesProcessed != 1 {
		t.Errorf("LifetimeFilesProcessed = %d, want 1", after.LifetimeFilesProcessed)
	}
	if after.PeriodResetAt.IsZero() {
		t.Error("PeriodResetAt should be non-zero after reset")
	}
}

func TestUpdateProcessingRecord(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	r := makeRecord("enc-1", OutcomeSuccess, "", 2_000_000_000, 0, 0, 0)
	if err := st.AddProcessingRecord(r); err != nil {
		t.Fatalf("AddProcessingRecord: %v", err)
	}

	if err := st.UpdateProcessingRecord(r.ID, 800_000_000, 60.0, 600, 95.5, OutcomeSuccess, ""); err != nil {
		t.Fatalf("UpdateProcessingRecord: %v", err)
	}

	records, err := st.GetProcessingRecords(1, 0)
	if err != nil {
		t.Fatalf("GetProcessingRecords: %v", err)
	}
	got := records[0]
	if got.OutputSizeBytes != 800_000_000 {
		t.Errorf("OutputSizeBytes = %d, want 800000000", got.OutputSizeBytes)
	}
	if got.SizeReductionPct != 60.0 {
		t.Errorf("SizeReductionPct = %.1f, want 60.0", got.SizeReductionPct)
	}
	if got.EncodeDurationSecs != 600 {
		t.Errorf("EncodeDurationSecs = %d, want 600", got.EncodeDurationSecs)
	}
	if got.EncodeFPS != 95.5 {
		t.Errorf("EncodeFPS = %.1f, want 95.5", got.EncodeFPS)
	}
}
