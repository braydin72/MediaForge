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
