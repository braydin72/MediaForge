//go:build windows

// Package main is the MediaForge system tray companion. It ensures the config
// exists (running the first-run setup if not), launches the mediaforge.exe
// server as a hidden background process, and then hosts the tray icon/menu.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
	"fyne.io/systray"
)

// hideConsole is the Windows CreateProcess flag (CREATE_NO_WINDOW) that starts
// mediaforge.exe without allocating a console window.
const hideConsole = 0x08000000

// baseURL is the local MediaForge server the tray talks to.
const baseURL = "http://127.0.0.1:8080"

func main() {
	// 1-3. Ensure a config exists; run first-run setup if it doesn't.
	if !configExists() {
		setupConfig()
	}

	// 4-5. Launch the server hidden; launchMediaForge waits for it to bind and
	// opens the web UI.
	launchMediaForge()

	// 6-7. Host the tray until the user chooses Exit.
	systray.Run(onReady, onExit)
}

// onReady sets the icon/tooltip and hands off to buildTrayMenu.
func onReady() {
	if icon, err := loadIcon(); err == nil {
		systray.SetIcon(icon)
	}
	systray.SetTooltip("MediaForge")
	// Left-click (primary tap) opens the dashboard. Right-click still opens the
	// menu (systray falls back to showing the menu when no secondary handler is
	// set).
	systray.SetOnTapped(func() { openBrowser(baseURL) })
	buildTrayMenu()
}

// onExit is invoked when systray shuts down. Nothing to clean up yet.
func onExit() {}

// configExists reports whether the MediaForge config file is present.
func configExists() bool {
	return fileExists(configPath())
}

// configPath returns the resolved path to mediaforge.yaml
// (%APPDATA%\MediaForge\mediaforge.yaml on Windows).
func configPath() string {
	return config.ResolveConfigPath("")
}

// launchMediaForge starts mediaforge.exe as a hidden, windowless background
// process. It looks for the binary alongside this executable first, falling
// back to the working directory / PATH.
func launchMediaForge() {
	cmd := exec.Command(mediaForgePath())
	// Windows only: start the server without allocating a console window.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: hideConsole}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "tray: failed to start mediaforge.exe: %v\n", err)
		return
	}
	// Give the server time to bind its port, then open the web UI.
	time.Sleep(2 * time.Second)
	openBrowser(baseURL)
}

// mediaForgePath resolves the mediaforge.exe location, preferring the directory
// of the running tray executable so the installed layout works regardless of
// the current working directory.
func mediaForgePath() string {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "mediaforge.exe")
		if fileExists(candidate) {
			return candidate
		}
	}
	return "mediaforge.exe"
}

// buildTrayMenu constructs the tray menu items, wires their click handlers, and
// starts the background goroutine that refreshes the queue-depth label.
func buildTrayMenu() {
	systray.SetTitle("MediaForge")
	systray.SetTooltip("MediaForge — Media Management")

	// 1. Pipeline toggle (checked = running).
	mPipeline := systray.AddMenuItemCheckbox("Pipeline", "Toggle the intake/transcode pipeline", true)

	// 2. Queue depth — display only.
	mQueue := systray.AddMenuItem("Transcode Queue (0)", "Number of jobs pending/running")
	mQueue.Disable()

	systray.AddSeparator() // 3

	// 4-6. Explicit queue controls.
	mStart := systray.AddMenuItem("Start Queue", "Resume processing the transcode queue")
	// Checked = queue paused. Toggling drives the pause/start API and keeps the
	// Pipeline checkbox visually in sync (checked Pipeline = running).
	mPause := systray.AddMenuItemCheckbox("Pause Queue", "Pause processing the transcode queue", false)
	mStop := systray.AddMenuItem("Stop Queue", "Stop the transcode queue")

	systray.AddSeparator() // 7

	// 8-10. Config, restart, logs.
	mConfig := systray.AddMenuItem("Config", "Open mediaforge.yaml")
	mRestart := systray.AddMenuItem("Restart MediaForge", "Restart the mediaforge.exe server")
	mLogs := systray.AddMenuItem("View Logs", "Open the current session log")

	systray.AddSeparator() // 11

	mExit := systray.AddMenuItem("Exit", "Stop MediaForge and close this icon") // 12

	// Background: refresh the queue-depth label every 2 seconds.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			mQueue.SetTitle(fmt.Sprintf("Transcode Queue (%d)", getQueueCount()))
		}
	}()

	// Menu handler loop.
	go func() {
		for {
			select {
			case <-mPipeline.ClickedCh:
				if mPipeline.Checked() {
					mPipeline.Uncheck()
					mPause.Check()
					callQueueAPI("pause")
				} else {
					mPipeline.Check()
					mPause.Uncheck()
					callQueueAPI("start")
				}
			case <-mStart.ClickedCh:
				mPipeline.Check()
				mPause.Uncheck()
				callQueueAPI("start")
			case <-mPause.ClickedCh:
				if mPause.Checked() {
					// Was paused, now unchecked -> resume.
					mPause.Uncheck()
					mPipeline.Check()
					callQueueAPI("start")
				} else {
					// Now checked -> pause.
					mPause.Check()
					mPipeline.Uncheck()
					callQueueAPI("pause")
				}
			case <-mStop.ClickedCh:
				mPipeline.Uncheck()
				mPause.Check()
				callQueueAPI("stop")
			case <-mConfig.ClickedCh:
				openFile(configPath())
			case <-mRestart.ClickedCh:
				fmt.Fprintln(os.Stderr, "tray: restarting mediaforge.exe")
				killMediaForge()
				time.Sleep(1 * time.Second)
				launchMediaForge()
			case <-mLogs.ClickedCh:
				openLog()
			case <-mExit.ClickedCh:
				killMediaForge()
				systray.Quit()
				os.Exit(0)
			}
		}
	}()
}

// logsPath returns the current session log file
// (%APPDATA%\MediaForge\logs\mediaforge.log).
func logsPath() string {
	return filepath.Join(filepath.Dir(configPath()), "logs", "mediaforge.log")
}

// openLog opens the current session log with its default handler. If the log
// file (or its parent directory) does not yet exist, it creates the directory
// and shows an explanatory error dialog with the full path instead of failing
// silently with "exit status 1".
func openLog() {
	path := logsPath()
	if !fileExists(path) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "tray: could not create log dir %s: %v\n", filepath.Dir(path), err)
		}
		showMessage("MediaForge — Logs",
			fmt.Sprintf("No log file exists yet at:\n\n%s\n\n"+
				"It will be created once MediaForge writes its first log entry.", path))
		return
	}
	openFile(path)
}

// callQueueAPI POSTs to /api/queue/{endpoint}. It fails silently (logging to
// stderr) so a missing server never surfaces a dialog to the user.
func callQueueAPI(endpoint string) error {
	url := baseURL + "/api/queue/" + endpoint
	resp, err := http.Post(url, "application/json", nil) //nolint:noctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "tray: POST %s failed: %v\n", url, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "tray: POST %s returned HTTP %d\n", url, resp.StatusCode)
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// getQueueCount returns the current queue depth (pending + running) from
// /api/stats, or 0 if the server is unreachable.
func getQueueCount() int {
	resp, err := http.Get(baseURL + "/api/stats") //nolint:noctx
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var s struct {
		Pending int `json:"pending"`
		Running int `json:"running"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return 0
	}
	return s.Pending + s.Running
}

// openFile opens a file with its default handler (Explorer's "start").
func openFile(path string) {
	if err := exec.Command("cmd", "/c", "start", "", path).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tray: openFile %s failed: %v\n", path, err)
	}
}

// openBrowser opens a URL in the default web browser.
func openBrowser(url string) {
	if err := exec.Command("cmd", "/c", "start", "", url).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tray: openBrowser %s failed: %v\n", url, err)
	}
}

// killMediaForge force-terminates any running mediaforge.exe and waits up to
// 5 seconds, confirming the process is actually gone before returning. taskkill
// is retried on each poll in case a child/respawned process is still holding on.
// Failures are logged to stderr. Callers (Exit, Restart) rely on this so the
// server is never left orphaned.
func killMediaForge() {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if out, err := exec.Command("taskkill", "/F", "/IM", "mediaforge.exe").CombinedOutput(); err != nil {
			// A non-zero exit usually means "process not found" — treat a
			// confirmed-absent process as success.
			if !mediaForgeRunning() {
				return
			}
			fmt.Fprintf(os.Stderr, "tray: taskkill mediaforge.exe: %v: %s\n", err, strings.TrimSpace(string(out)))
		}
		if !mediaForgeRunning() {
			return
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "tray: mediaforge.exe still running after 5s kill timeout")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// mediaForgeRunning reports whether a mediaforge.exe process is currently alive.
func mediaForgeRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq mediaforge.exe", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "mediaforge.exe")
}

// fileExists reports whether path exists and is not a directory error.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
