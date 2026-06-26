package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
)

// mockNotifier records calls to Send.
type mockNotifier struct {
	mu       sync.Mutex
	name     string
	calls    []sentCall
	sendErr  error
}

type sentCall struct {
	subject string
	body    string
}

func (m *mockNotifier) Name() string { return m.name }
func (m *mockNotifier) IsConfigured() bool { return true }
func (m *mockNotifier) Send(_ context.Context, subject, body string) error {
	m.mu.Lock()
	m.calls = append(m.calls, sentCall{subject: subject, body: body})
	m.mu.Unlock()
	return m.sendErr
}

func (m *mockNotifier) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockNotifier) lastCall() (sentCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return sentCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

func newTestDispatcher(events config.NotificationEventsConfig) (*Dispatcher, *config.NotificationsConfig) {
	cfg := &config.NotificationsConfig{
		BaseURL: "http://localhost:8080",
		Events:  events,
		Email: config.EmailNotificationConfig{
			Mode:            "per_file",
			IntervalMinutes: 60,
		},
	}
	return NewDispatcher(cfg), cfg
}

func TestDispatcher_DispatchPerFile(t *testing.T) {
	events := config.NotificationEventsConfig{EncodeFailed: true}
	d, _ := newTestDispatcher(events)
	mn := &mockNotifier{name: "test"}
	d.AddChannel(mn, 60)

	d.Dispatch(context.Background(), Event{
		Type:     EventEncodeFailed,
		Filename: "movie.mkv",
		Reason:   "output too large",
	})

	if mn.callCount() != 1 {
		t.Fatalf("expected 1 Send call, got %d", mn.callCount())
	}
	call, _ := mn.lastCall()
	if !containsStr(call.subject, "Encode Failed") {
		t.Errorf("subject %q missing expected text", call.subject)
	}
	if !containsStr(call.body, "movie.mkv") {
		t.Errorf("body missing filename")
	}
}

func TestDispatcher_DisabledEventSkipped(t *testing.T) {
	events := config.NotificationEventsConfig{EncodeFailed: false}
	d, _ := newTestDispatcher(events)
	mn := &mockNotifier{name: "test"}
	d.AddChannel(mn, 60)

	d.Dispatch(context.Background(), Event{Type: EventEncodeFailed, Filename: "a.mkv"})

	if mn.callCount() != 0 {
		t.Errorf("expected 0 calls for disabled event, got %d", mn.callCount())
	}
}

func TestDispatcher_DispatchTest(t *testing.T) {
	events := config.NotificationEventsConfig{}
	d, _ := newTestDispatcher(events)
	mn := &mockNotifier{name: "test"}
	d.AddChannel(mn, 60)

	if err := d.DispatchTest(context.Background()); err != nil {
		t.Fatalf("DispatchTest returned error: %v", err)
	}
	if mn.callCount() != 1 {
		t.Fatalf("expected 1 Send for test, got %d", mn.callCount())
	}
	call, _ := mn.lastCall()
	if !containsStr(call.subject, "Test") {
		t.Errorf("subject %q missing 'Test'", call.subject)
	}
}

func TestDispatcher_DispatchTestNoChannels(t *testing.T) {
	d, _ := newTestDispatcher(config.NotificationEventsConfig{})
	if err := d.DispatchTest(context.Background()); err == nil {
		t.Error("expected error when no channels configured")
	}
}

func TestDispatcher_BaseURLInBody(t *testing.T) {
	events := config.NotificationEventsConfig{EncodeComplete: true}
	d, _ := newTestDispatcher(events)
	mn := &mockNotifier{name: "test"}
	d.AddChannel(mn, 60)

	d.Dispatch(context.Background(), Event{
		Type:      EventEncodeComplete,
		Filename:  "movie.mkv",
		Timestamp: time.Now(),
	})

	call, ok := mn.lastCall()
	if !ok {
		t.Fatal("no call made")
	}
	if !containsStr(call.body, "http://localhost:8080") {
		t.Errorf("body missing base URL, got: %s", call.body)
	}
}
