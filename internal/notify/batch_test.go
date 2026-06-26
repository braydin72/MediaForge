package notify

import (
	"testing"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
)

func TestBatchCollector_CollectsAndSendsDigest(t *testing.T) {
	mn := &mockNotifier{name: "test"}
	baseURL := "http://localhost:8080"
	bc := NewBatchCollector(mn, 999, &baseURL) // very long interval; flush manually
	defer bc.Stop()

	bc.Add(&Event{Type: EventEncodeFailed, Filename: "a.mkv", Reason: "too big", Timestamp: time.Now()})
	bc.Add(&Event{Type: EventEncodeComplete, Filename: "b.mkv", Timestamp: time.Now()})

	bc.FlushNow()

	if mn.callCount() != 1 {
		t.Fatalf("expected 1 digest send, got %d", mn.callCount())
	}
	call, _ := mn.lastCall()
	if !containsStr(call.subject, "2 notification") {
		t.Errorf("subject %q does not mention count", call.subject)
	}
	if !containsStr(call.body, "a.mkv") || !containsStr(call.body, "b.mkv") {
		t.Errorf("digest body missing filenames: %s", call.body)
	}
	if !containsStr(call.body, "http://localhost:8080") {
		t.Errorf("digest body missing base URL")
	}
}

func TestBatchCollector_EmptyBatchDoesNotSend(t *testing.T) {
	mn := &mockNotifier{name: "test"}
	baseURL := ""
	bc := NewBatchCollector(mn, 999, &baseURL)
	defer bc.Stop()

	bc.FlushNow()

	if mn.callCount() != 0 {
		t.Errorf("expected no send for empty batch, got %d calls", mn.callCount())
	}
}

func TestBatchCollector_EventsClearedAfterFlush(t *testing.T) {
	mn := &mockNotifier{name: "test"}
	baseURL := ""
	bc := NewBatchCollector(mn, 999, &baseURL)
	defer bc.Stop()

	bc.Add(&Event{Type: EventEncodeFailed, Filename: "x.mkv", Timestamp: time.Now()})
	bc.FlushNow()
	bc.FlushNow() // second flush should send nothing

	if mn.callCount() != 1 {
		t.Errorf("expected 1 send total (second flush empty), got %d", mn.callCount())
	}
}

func TestDispatcher_BatchedMode(t *testing.T) {
	cfg := &config.NotificationsConfig{
		Events: config.NotificationEventsConfig{EncodeFailed: true},
		Email: config.EmailNotificationConfig{
			Mode:            "batched",
			IntervalMinutes: 999,
		},
	}
	d := NewDispatcher(cfg)
	defer d.Stop()

	mn := &mockNotifier{name: "email"}
	d.channels = append(d.channels, channelEntry{
		notifier: mn,
		batch:    NewBatchCollector(mn, 999, &cfg.BaseURL),
	})

	d.Dispatch(nil, &Event{Type: EventEncodeFailed, Filename: "file.mkv", Timestamp: time.Now()}) //nolint:staticcheck

	// Event should be batched, not sent yet.
	if mn.callCount() != 0 {
		t.Errorf("expected 0 immediate sends in batched mode, got %d", mn.callCount())
	}

	// Force flush via the batch collector.
	d.channels[0].batch.FlushNow()

	if mn.callCount() != 1 {
		t.Errorf("expected 1 send after flush, got %d", mn.callCount())
	}
}
