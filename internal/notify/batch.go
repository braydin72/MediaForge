package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// BatchCollector accumulates events and sends a digest on a configurable schedule.
// The ticker interval is set at construction; stop and recreate to change it.
type BatchCollector struct {
	mu       sync.Mutex
	events   []Event
	notifier Notifier
	baseURL  *string // pointer into config so base URL changes propagate
	ticker   *time.Ticker
	done     chan struct{}
	stopped  bool
}

// NewBatchCollector creates and starts a collector that flushes every intervalMinutes.
func NewBatchCollector(n Notifier, intervalMinutes int, baseURL *string) *BatchCollector {
	if intervalMinutes < 1 {
		intervalMinutes = 60
	}
	bc := &BatchCollector{
		notifier: n,
		baseURL:  baseURL,
		ticker:   time.NewTicker(time.Duration(intervalMinutes) * time.Minute),
		done:     make(chan struct{}),
	}
	go bc.run()
	return bc
}

// Add enqueues an event for the next digest.
func (bc *BatchCollector) Add(e *Event) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.events = append(bc.events, *e)
}

// Stop shuts down the background ticker. Safe to call more than once.
func (bc *BatchCollector) Stop() {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.stopped {
		return
	}
	bc.stopped = true
	bc.ticker.Stop()
	close(bc.done)
}

// FlushNow forces an immediate digest send regardless of the schedule.
// Intended for testing.
func (bc *BatchCollector) FlushNow() {
	bc.flush()
}

func (bc *BatchCollector) run() {
	for {
		select {
		case <-bc.done:
			return
		case <-bc.ticker.C:
			bc.flush()
		}
	}
}

func (bc *BatchCollector) flush() {
	bc.mu.Lock()
	events := bc.events
	bc.events = nil
	bc.mu.Unlock()

	if len(events) == 0 {
		return
	}

	baseURL := ""
	if bc.baseURL != nil {
		baseURL = *bc.baseURL
	}
	subject, body := buildDigest(events, baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = bc.notifier.Send(ctx, subject, body)
}

func buildDigest(events []Event, baseURL string) (subject, body string) {
	subject = fmt.Sprintf("MediaForge: %d notification(s)", len(events))

	var sb strings.Builder
	fmt.Fprintf(&sb, "MediaForge digest — %d event(s)\n\n", len(events))

	for i, e := range events {
		fmt.Fprintf(&sb, "[%d] %s — %s\n", i+1, e.Type, e.Filename)
		if e.Reason != "" {
			fmt.Fprintf(&sb, "    Reason: %s\n", e.Reason)
		}
		fmt.Fprintf(&sb, "    %s\n\n", e.Timestamp.Format(time.RFC1123))
	}

	if baseURL != "" {
		fmt.Fprintf(&sb, "Open MediaForge: %s\n", baseURL)
	}

	body = sb.String()
	return subject, body
}
