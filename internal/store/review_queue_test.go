package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewQueueCRUD(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	e := ReviewEntry{
		ID:           "test-id-1",
		OriginalPath: "/incoming/movie.mkv",
		Filename:     "movie.mkv",
		Reason:       "codec detection failed: unrecognized codec \"vp9\"",
		FFProbeInfo:  `{"video_codec":"vp9"}`,
		Category:     string(ReviewCategorySystemFailure),
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}

	// Add
	if err := st.AddToReviewQueue(&e); err != nil {
		t.Fatalf("AddToReviewQueue: %v", err)
	}

	// Count
	count, err := st.GetReviewQueueCount()
	if err != nil {
		t.Fatalf("GetReviewQueueCount: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Duplicate is silently ignored
	if err := st.AddToReviewQueue(&e); err != nil {
		t.Fatalf("duplicate AddToReviewQueue: %v", err)
	}
	count, _ = st.GetReviewQueueCount()
	if count != 1 {
		t.Errorf("after duplicate add, count = %d, want 1", count)
	}

	// GetReviewQueue
	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Reason != e.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, e.Reason)
	}
	if got.FFProbeInfo != e.FFProbeInfo {
		t.Errorf("FFProbeInfo = %q, want %q", got.FFProbeInfo, e.FFProbeInfo)
	}
	if got.Category != e.Category {
		t.Errorf("Category = %q, want %q", got.Category, e.Category)
	}

	// GetReviewEntry round-trips Category too
	single, err := st.GetReviewEntry(e.ID)
	if err != nil {
		t.Fatalf("GetReviewEntry: %v", err)
	}
	if single == nil || single.Category != e.Category {
		t.Errorf("GetReviewEntry Category = %v, want %q", single, e.Category)
	}

	// UpdateReviewQueueStatus
	if err := st.UpdateReviewQueueStatus(e.ID, "discarded"); err != nil {
		t.Fatalf("UpdateReviewQueueStatus: %v", err)
	}
	count, _ = st.GetReviewQueueCount()
	if count != 0 {
		t.Errorf("after discard, pending count = %d, want 0", count)
	}

	// GetReviewQueue still returns all entries regardless of status
	entries, _ = st.GetReviewQueue()
	if len(entries) != 1 {
		t.Errorf("total entries after discard = %d, want 1", len(entries))
	}
	if entries[0].Status != "discarded" {
		t.Errorf("status = %q, want %q", entries[0].Status, "discarded")
	}

	// A new pending entry for the same path can be added after it's discarded
	e2 := e
	e2.ID = "test-id-2"
	if err := st.AddToReviewQueue(&e2); err != nil {
		t.Fatalf("AddToReviewQueue after discard: %v", err)
	}
	count, _ = st.GetReviewQueueCount()
	if count != 1 {
		t.Errorf("count after re-add = %d, want 1", count)
	}

	_ = os.RemoveAll(dir)
}

func TestHasPendingReviewEntry(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	path := "/incoming/movie.mkv"

	if pending, err := st.HasPendingReviewEntry(path); err != nil || pending {
		t.Fatalf("HasPendingReviewEntry before any entry exists = (%v, %v), want (false, nil)", pending, err)
	}

	e := ReviewEntry{
		ID:           "test-id-1",
		OriginalPath: path,
		Filename:     "movie.mkv",
		Reason:       "duplicate: file already exists at destination",
		Category:     string(ReviewCategoryDuplicate),
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.AddToReviewQueue(&e); err != nil {
		t.Fatalf("AddToReviewQueue: %v", err)
	}

	if pending, err := st.HasPendingReviewEntry(path); err != nil || !pending {
		t.Fatalf("HasPendingReviewEntry with a pending entry = (%v, %v), want (true, nil)", pending, err)
	}

	if err := st.UpdateReviewQueueStatus(e.ID, "resolved"); err != nil {
		t.Fatalf("UpdateReviewQueueStatus: %v", err)
	}

	if pending, err := st.HasPendingReviewEntry(path); err != nil || pending {
		t.Fatalf("HasPendingReviewEntry after resolution = (%v, %v), want (false, nil)", pending, err)
	}
}

func TestReviewQueue_EmptyCategoryRoundTrips(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	// Simulates a row written before categories existed: Category left unset.
	e := ReviewEntry{
		ID:           "legacy-1",
		OriginalPath: "/incoming/legacy.mkv",
		Filename:     "legacy.mkv",
		Reason:       "no metadata match found",
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.AddToReviewQueue(&e); err != nil {
		t.Fatalf("AddToReviewQueue: %v", err)
	}

	got, err := st.GetReviewEntry(e.ID)
	if err != nil {
		t.Fatalf("GetReviewEntry: %v", err)
	}
	if got == nil || got.Category != "" {
		t.Errorf("Category = %v, want empty string for legacy row", got)
	}
}

func TestBulkUpdateReviewQueueStatus(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ids := []string{"bulk-1", "bulk-2", "bulk-3"}
	for _, id := range ids {
		e := ReviewEntry{
			ID:           id,
			OriginalPath: "/incoming/" + id + ".mkv",
			Filename:     id + ".mkv",
			Reason:       "no viable encode found",
			Category:     string(ReviewCategoryEncodeFailure),
			Status:       "pending",
			CreatedAt:    time.Now().UTC(),
		}
		if err := st.AddToReviewQueue(&e); err != nil {
			t.Fatalf("AddToReviewQueue(%s): %v", id, err)
		}
	}

	// Empty slice is a no-op, not an error.
	if err := st.BulkUpdateReviewQueueStatus(nil, "discarded"); err != nil {
		t.Fatalf("BulkUpdateReviewQueueStatus(nil): %v", err)
	}

	// Update two of the three, plus one nonexistent id (should be silently ignored).
	if err := st.BulkUpdateReviewQueueStatus([]string{"bulk-1", "bulk-2", "does-not-exist"}, "discarded"); err != nil {
		t.Fatalf("BulkUpdateReviewQueueStatus: %v", err)
	}

	entries, err := st.GetReviewQueue()
	if err != nil {
		t.Fatalf("GetReviewQueue: %v", err)
	}
	statusByID := make(map[string]string, len(entries))
	for _, e := range entries {
		statusByID[e.ID] = e.Status
	}
	if statusByID["bulk-1"] != "discarded" || statusByID["bulk-2"] != "discarded" {
		t.Errorf("bulk-1/bulk-2 not updated: %+v", statusByID)
	}
	if statusByID["bulk-3"] != "pending" {
		t.Errorf("bulk-3 should be untouched, got %q", statusByID["bulk-3"])
	}
}
