package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
)

// EventType identifies a notification event.
type EventType string

const (
	EventEncodeComplete  EventType = "encode_complete"
	EventEncodeFailed    EventType = "encode_failed"
	EventReviewQueueItem EventType = "review_queue_item"
	EventDailySummary    EventType = "daily_summary"
	EventWeeklySummary   EventType = "weekly_summary"
	EventTest            EventType = "test"
)

// StatsSnapshot holds brief stats for inclusion in notifications.
type StatsSnapshot struct {
	FilesProcessedToday int
	StorageSavedToday   string
}

// Event is a single notification event.
type Event struct {
	Type      EventType
	Filename  string
	Reason    string
	Stats     StatsSnapshot
	Timestamp time.Time
}

// Notifier is the interface implemented by each notification channel.
type Notifier interface {
	Name() string
	IsConfigured() bool
	Send(ctx context.Context, subject, body string) error
}

// Dispatcher routes events to configured notification channels.
// It reads event-enable flags and channel mode from cfg on every call so
// config changes take effect immediately without restart.
type Dispatcher struct {
	cfg      *config.NotificationsConfig
	mu       sync.Mutex
	channels []channelEntry
}

type channelEntry struct {
	notifier Notifier
	batch    *BatchCollector
}

// NewDispatcher creates a Dispatcher backed by the given config pointer.
func NewDispatcher(cfg *config.NotificationsConfig) *Dispatcher {
	return &Dispatcher{cfg: cfg}
}

// AddChannel registers a notifier. If the notifier is not configured it is
// silently skipped.  intervalMinutes is only used when the channel starts in
// batched mode; if the user later switches to per_file the batch collector
// drains on the next tick but new events are sent immediately.
func (d *Dispatcher) AddChannel(n Notifier, intervalMinutes int) {
	if !n.IsConfigured() {
		return
	}
	bc := NewBatchCollector(n, intervalMinutes, &d.cfg.BaseURL)
	d.mu.Lock()
	d.channels = append(d.channels, channelEntry{notifier: n, batch: bc})
	d.mu.Unlock()
}

// Dispatch sends an event to all configured channels that have the event
// type enabled.
func (d *Dispatcher) Dispatch(ctx context.Context, e *Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if !d.eventEnabled(e.Type) {
		return
	}
	subject, body := formatEvent(e, d.cfg.BaseURL)
	d.mu.Lock()
	channels := d.channels
	d.mu.Unlock()
	for i := range channels {
		ch := &channels[i]
		if d.channelMode(ch.notifier) == "batched" {
			ch.batch.Add(e)
		} else {
			_ = ch.notifier.Send(ctx, subject, body)
		}
	}
}

// DispatchTest sends a test message to all configured channels regardless
// of per-event toggles.
func (d *Dispatcher) DispatchTest(ctx context.Context) error {
	d.mu.Lock()
	channels := d.channels
	d.mu.Unlock()
	if len(channels) == 0 {
		return fmt.Errorf("no notification channels configured")
	}
	e := &Event{
		Type:      EventTest,
		Filename:  "test.mkv",
		Timestamp: time.Now(),
	}
	subject, body := formatEvent(e, d.cfg.BaseURL)
	var errs []string
	for i := range channels {
		ch := &channels[i]
		if err := ch.notifier.Send(ctx, subject, body); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ch.notifier.Name(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// AddPerFileNotifier registers a notifier that always sends immediately (no batching).
// Intended for testing.
func (d *Dispatcher) AddPerFileNotifier(n Notifier) {
	if !n.IsConfigured() {
		return
	}
	baseURL := ""
	d.mu.Lock()
	d.channels = append(d.channels, channelEntry{
		notifier: n,
		batch:    NewBatchCollector(n, 999999, &baseURL),
	})
	d.mu.Unlock()
}

// IsAnyConfigured returns true if at least one channel is active.
func (d *Dispatcher) IsAnyConfigured() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.channels) > 0
}

// Stop shuts down background batch collectors.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.channels {
		d.channels[i].batch.Stop()
	}
}

func (d *Dispatcher) eventEnabled(t EventType) bool {
	ev := &d.cfg.Events
	switch t {
	case EventEncodeComplete:
		return ev.EncodeComplete
	case EventEncodeFailed:
		return ev.EncodeFailed
	case EventReviewQueueItem:
		return ev.ReviewQueueItem
	case EventDailySummary:
		return ev.DailySummary
	case EventWeeklySummary:
		return ev.WeeklySummary
	case EventTest:
		return true
	}
	return false
}

func (d *Dispatcher) channelMode(n Notifier) string {
	if n.Name() == "email" {
		return d.cfg.Email.Mode
	}
	return "per_file"
}

func formatEvent(e *Event, baseURL string) (subject, body string) {
	switch e.Type {
	case EventTest:
		subject = "MediaForge: Test Notification"
		body = "This is a test notification from MediaForge.\n\nIf you received this, your notification settings are working correctly."
	case EventEncodeComplete:
		subject = fmt.Sprintf("MediaForge: Encode Complete — %s", e.Filename)
		body = fmt.Sprintf("Encode completed successfully.\n\nFile: %s\n", e.Filename)
	case EventEncodeFailed:
		subject = fmt.Sprintf("MediaForge: Encode Failed — %s", e.Filename)
		body = fmt.Sprintf("Encode failed.\n\nFile: %s\nReason: %s\n", e.Filename, e.Reason)
	case EventReviewQueueItem:
		subject = fmt.Sprintf("MediaForge: Review Queue — %s", e.Filename)
		body = fmt.Sprintf("A file was added to the Review Queue.\n\nFile: %s\nReason: %s\n", e.Filename, e.Reason)
	default:
		subject = fmt.Sprintf("MediaForge: %s", e.Type)
		body = fmt.Sprintf("Event: %s\nFile: %s\n", e.Type, e.Filename)
	}
	body += appendStats(e.Stats)
	if baseURL != "" {
		body += fmt.Sprintf("\nOpen MediaForge: %s\n", baseURL)
	}
	body += fmt.Sprintf("\n%s", e.Timestamp.Format(time.RFC1123))
	return subject, body
}

func appendStats(s StatsSnapshot) string {
	if s.FilesProcessedToday == 0 && s.StorageSavedToday == "" {
		return ""
	}
	return fmt.Sprintf("\nStats today: %d files processed, %s saved\n", s.FilesProcessedToday, s.StorageSavedToday)
}
