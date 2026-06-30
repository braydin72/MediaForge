//go:build windows

// Package main provides a Windows system tray companion for the MediaForge server.
// It polls the server API for status updates, provides quick-access menu items,
// and surfaces Windows toast notifications for encode completions and review queue additions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/getlantern/systray"
)

const (
	baseURL   = "http://127.0.0.1:8080"
	pollDelay = 10 * time.Second
)

var logFile string

type trayState struct {
	mu              sync.Mutex
	intakePaused    bool
	prevComplete    int
	prevFailed      int
	prevReviewCount int
}

var state trayState

func main() {
	configFlag := flag.String("config", "", "Path to MediaForge config file (default: %APPDATA%\\MediaForge\\mediaforge.yaml)")
	flag.Parse()

	cfgPath := config.ResolveConfigPath(*configFlag)
	configDir := filepath.Dir(cfgPath)
	if configDir == "." {
		// Fallback: use APPDATA dir or "config" if APPDATA is unset.
		if base := config.EnsureWindowsDirs(); base != "" {
			configDir = base
		} else {
			configDir = "config"
		}
	}
	logFile = filepath.Join(configDir, "logs", "mediaforge.log")

	systray.Run(onReady, func() {})
}

func onReady() {
	if icon, err := loadIcon(); err == nil {
		systray.SetIcon(icon)
	}
	systray.SetTooltip("MediaForge")

	mOpen := systray.AddMenuItem("Open MediaForge", "Open the web UI in your browser")
	mIntake := systray.AddMenuItem("Pause Intake", "Pause the intake watch folder")
	systray.AddSeparator()
	mLogs := systray.AddMenuItem("View Logs", "Open the current session log file")
	systray.AddSeparator()
	mExit := systray.AddMenuItem("Exit", "Stop MediaForge and close this icon")

	// Seed counts from server so startup doesn't fire spurious notifications.
	if st, err := getStats(); err == nil {
		state.mu.Lock()
		state.prevComplete = st.Complete
		state.prevFailed = st.Failed
		state.mu.Unlock()
	}
	if rc, err := getReviewCount(); err == nil {
		state.mu.Lock()
		state.prevReviewCount = rc
		state.mu.Unlock()
	}

	// Sync initial intake pause state.
	if s, err := getIntakeStatus(); err == nil {
		state.mu.Lock()
		state.intakePaused = s.Paused
		state.mu.Unlock()
		syncIntakeLabel(mIntake, s.Paused)
	}

	go pollLoop(mIntake)

	for {
		select {
		case <-mOpen.ClickedCh:
			openURL(baseURL)
		case <-mIntake.ClickedCh:
			state.mu.Lock()
			paused := state.intakePaused
			state.mu.Unlock()
			if paused {
				if postAPI("/api/intake/resume") == nil {
					state.mu.Lock()
					state.intakePaused = false
					state.mu.Unlock()
					syncIntakeLabel(mIntake, false)
				}
			} else {
				if postAPI("/api/intake/pause") == nil {
					state.mu.Lock()
					state.intakePaused = true
					state.mu.Unlock()
					syncIntakeLabel(mIntake, true)
				}
			}
		case <-mLogs.ClickedCh:
			openFile(logFile)
		case <-mExit.ClickedCh:
			// Attempt graceful service stop; ignore errors (may not be running as service).
			exec.Command("sc", "stop", "MediaForge").Start() //nolint:errcheck
			systray.Quit()
		}
	}
}

func syncIntakeLabel(m *systray.MenuItem, paused bool) {
	if paused {
		m.SetTitle("Resume Intake")
		m.SetTooltip("Resume watching for new files")
	} else {
		m.SetTitle("Pause Intake")
		m.SetTooltip("Pause watching for new files")
	}
}

// pollLoop runs every pollDelay and refreshes tooltip, intake state, and notifications.
func pollLoop(mIntake *systray.MenuItem) {
	ticker := time.NewTicker(pollDelay)
	defer ticker.Stop()
	for range ticker.C {
		// Stats → tooltip + encode/fail notifications.
		if st, err := getStats(); err == nil {
			inProgress := st.Pending + st.Running
			if inProgress > 0 {
				systray.SetTooltip(fmt.Sprintf("MediaForge — %d file(s) in queue", inProgress))
			} else {
				systray.SetTooltip("MediaForge")
			}

			state.mu.Lock()
			newComplete := st.Complete - state.prevComplete
			newFailed := st.Failed - state.prevFailed
			state.prevComplete = st.Complete
			state.prevFailed = st.Failed
			state.mu.Unlock()

			if newComplete > 0 {
				showToast("MediaForge", fmt.Sprintf("%d file(s) encoded successfully", newComplete))
			}
			if newFailed > 0 {
				showToast("MediaForge", fmt.Sprintf("%d encode job(s) failed — check the queue", newFailed))
			}
		}

		// Intake status → keep menu label in sync with server state.
		if s, err := getIntakeStatus(); err == nil {
			state.mu.Lock()
			changed := s.Paused != state.intakePaused
			state.intakePaused = s.Paused
			state.mu.Unlock()
			if changed {
				syncIntakeLabel(mIntake, s.Paused)
			}
		}

		// Review queue count → notify on new additions.
		if rc, err := getReviewCount(); err == nil {
			state.mu.Lock()
			prev := state.prevReviewCount
			state.prevReviewCount = rc
			state.mu.Unlock()
			if rc > prev {
				showToast("MediaForge", fmt.Sprintf("%d new item(s) added to Review Queue", rc-prev))
			}
		}
	}
}

// --- API helpers ---

type statsResponse struct {
	Pending  int `json:"pending"`
	Running  int `json:"running"`
	Complete int `json:"complete"`
	Failed   int `json:"failed"`
}

type intakeStatusResponse struct {
	Enabled bool `json:"enabled"`
	Paused  bool `json:"paused"`
}

func getStats() (*statsResponse, error) {
	resp, err := http.Get(baseURL + "/api/stats") //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s statsResponse
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func getIntakeStatus() (*intakeStatusResponse, error) {
	resp, err := http.Get(baseURL + "/api/intake/status") //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s intakeStatusResponse
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func getReviewCount() (int, error) {
	resp, err := http.Get(baseURL + "/api/review/count") //nolint:noctx
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var rc struct {
		Count int `json:"count"`
	}
	return rc.Count, json.NewDecoder(resp.Body).Decode(&rc)
}

func postAPI(path string) error {
	req, err := http.NewRequest(http.MethodPost, baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// --- Shell helpers ---

func openURL(url string) {
	exec.Command("cmd", "/c", "start", url).Start() //nolint:errcheck
}

func openFile(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		showToast("MediaForge", "Log file not found at: "+path)
		return
	}
	exec.Command("cmd", "/c", "start", "", path).Start() //nolint:errcheck
}

// showToast displays a Windows 10/11 toast notification via PowerShell WinRT APIs.
// Errors are silently ignored — tray functionality is not impacted if toasts fail.
func showToast(title, message string) {
	safe := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	// Use the PowerShell AUMID so no app registration is required.
	script := fmt.Sprintf(`
$ErrorActionPreference = 'SilentlyContinue'
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
$t = [Windows.UI.Notifications.ToastTemplateType]::ToastText02
$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent($t)
$xml.GetElementsByTagName('text')[0].AppendChild($xml.CreateTextNode('%s')) | Out-Null
$xml.GetElementsByTagName('text')[1].AppendChild($xml.CreateTextNode('%s')) | Out-Null
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app)
$notifier.Show([Windows.UI.Notifications.ToastNotification]::new($xml))
`, safe(title), safe(message))
	exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", script).Start() //nolint:errcheck
}
