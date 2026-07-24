//go:build windows

// Package main is the MediaForge system tray companion. It ensures the config
// exists (running the first-run setup if not), launches the mediaforge.exe
// server as a hidden background process, and then hosts the tray icon/menu.
package main

import (
	"log"
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

// init routes the tray's own diagnostics to %APPDATA%\MediaForge\logs\tray.log.
// The tray is linked with -H windowsgui (no console), so stderr goes nowhere;
// without this file there is no way to see why the tray misbehaves. The log dir
// is created first because on a fresh install it may not exist yet, and we keep
// the default stderr output if the file can't be opened (SetOutput(nil) would
// make every later log call panic on a nil writer).
func init() {
	logDir := filepath.Join(os.Getenv("APPDATA"), "MediaForge", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	logFile, err := os.OpenFile(
		filepath.Join(logDir, "tray.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return
	}
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("tray: ")
	log.Println("----- tray started -----")
}

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
		log.Printf("failed to start mediaforge.exe: %v", err)
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
//
// "Stop Pipeline" and "Pause Queue" are deliberately separate, independent
// controls, not two views onto the same toggle:
//   - Stop Pipeline (/api/intake/pause, /api/intake/resume) only stops files
//     from being pulled out of the Incoming folder into the pipeline. It does
//     not touch the transcode queue — jobs already queued or encoding are
//     completely unaffected.
//   - Pause Queue (/api/queue/pause, /api/queue/start) only stops new jobs
//     from starting. It does not touch intake — files can still be added to
//     the queue while paused — and it does not cancel whatever job is
//     currently encoding; that job finishes normally. Cancelling one specific
//     queued/running job is done per-item from the web UI, not from here.
//
// An earlier version of this menu collapsed both concepts into one
// checkbox (toggling "Pipeline" also silently paused the queue, and vice
// versa), which meant there was no way to stop intake without also halting
// the encoder or vice versa.
func buildTrayMenu() {
	systray.SetTitle("MediaForge")
	systray.SetTooltip("MediaForge — Media Management")

	// 1. Intake control — checked = paused (Incoming folder not being scanned).
	mIntake := systray.AddMenuItemCheckbox("Stop Pipeline", "Stop pulling new files from the Incoming folder", false)

	// 2. Encode queue control — checked = paused (no new jobs starting).
	mPauseQueue := systray.AddMenuItemCheckbox("Pause Queue", "Stop starting new jobs from the transcode queue", false)

	// 3. Queue depth — display only.
	mQueue := systray.AddMenuItem("Transcode Queue (0)", "Number of jobs pending/running")
	mQueue.Disable()

	systray.AddSeparator() // 4

	// 5-7. Config, restart, logs.
	mConfig := systray.AddMenuItem("Config", "Open mediaforge.yaml")
	mRestart := systray.AddMenuItem("Restart MediaForge", "Restart the mediaforge.exe server")
	mLogs := systray.AddMenuItem("View Logs", "Open the current session log")

	systray.AddSeparator() // 8

	mExit := systray.AddMenuItem("Exit", "Stop MediaForge and close this icon") // 9

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
			case <-mIntake.ClickedCh:
				if mIntake.Checked() {
					mIntake.Uncheck()
					callIntakeAPI("resume")
				} else {
					mIntake.Check()
					callIntakeAPI("pause")
				}
			case <-mPauseQueue.ClickedCh:
				if mPauseQueue.Checked() {
					mPauseQueue.Uncheck()
					callQueueAPI("start")
				} else {
					mPauseQueue.Check()
					callQueueAPI("pause")
				}
			case <-mConfig.ClickedCh:
				openFile(configPath())
			case <-mRestart.ClickedCh:
				log.Println("restarting mediaforge.exe")
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
			log.Printf("could not create log dir %s: %v", filepath.Dir(path), err)
		}
		showMessage("MediaForge — Logs",
			fmt.Sprintf("No log file exists yet at:\n\n%s\n\n"+
				"It will be created once MediaForge writes its first log entry.", path))
		return
	}
	// Open with Notepad directly rather than via "start". The .log extension
	// usually has no default file association, so "cmd /c start" just flashes a
	// console window and opens nothing. Notepad always handles a text log.
	if err := exec.Command("notepad.exe", path).Start(); err != nil {
		log.Printf("openLog notepad %s failed: %v", path, err)
		showMessage("MediaForge — Logs",
			fmt.Sprintf("Could not open the log file:\n\n%s\n\n%v", path, err))
	}
}

// postAPI POSTs to path (e.g. "/api/queue/pause") with no body. It fails
// silently (logging to stderr) so a missing server never surfaces a dialog
// to the user.
func postAPI(path string) error {
	url := baseURL + path
	resp, err := http.Post(url, "application/json", nil) //nolint:noctx
	if err != nil {
		log.Printf("POST %s failed: %v", url, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("POST %s returned HTTP %d", url, resp.StatusCode)
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// callQueueAPI POSTs to /api/queue/{endpoint} — controls the transcode queue
// only (see buildTrayMenu's doc comment for the intake/queue distinction).
func callQueueAPI(endpoint string) error {
	return postAPI("/api/queue/" + endpoint)
}

// callIntakeAPI POSTs to /api/intake/{endpoint} — controls the intake
// watcher only (see buildTrayMenu's doc comment for the intake/queue
// distinction).
func callIntakeAPI(endpoint string) error {
	return postAPI("/api/intake/" + endpoint)
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

// openFile opens a text file (config, logs) in Notepad. We invoke notepad.exe
// directly rather than "cmd /c start": the tray is a -H windowsgui process with
// no console, so the "start" builtin just spawns a throwaway cmd that flashes
// and dies before handing off. Notepad has no console dependency.
func openFile(path string) {
	if err := exec.Command("notepad.exe", path).Start(); err != nil {
		log.Printf("openFile %s failed: %v", path, err)
	}
}

// openBrowser opens a URL in the default web browser.
func openBrowser(url string) {
	if err := exec.Command("cmd", "/c", "start", "", url).Run(); err != nil {
		log.Printf("openBrowser %s failed: %v", url, err)
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
			log.Printf("taskkill mediaforge.exe: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if !mediaForgeRunning() {
			return
		}
		if time.Now().After(deadline) {
			log.Println("mediaforge.exe still running after 5s kill timeout")
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
