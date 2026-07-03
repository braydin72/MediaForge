# Known Issues & Fixes

Issues grouped by module and prioritized for immediate action. These are bugs and broken features that need fixing before the next release.

---

## Critical Priority

### Tray App Process Management

**Issue #1: Tray doesn't kill mediaforge on exit**
- **Description:** Clicking tray menu Exit sometimes closes only the tray, leaving mediaforge.exe running as an orphaned process.
- **Impact:** App doesn't actually shut down; port stays in use; next launch fails to bind.
- **Fix:** Ensure tray's `killMediaForge()` is called before `systray.Quit()`. Add retry logic (taskkill /F with timeout) and verify process is gone before exiting.
- **Files:** `cmd/tray/main.go` (tray exit handler)

**Issue #8: Black console window stays open on tray launch**
- **Description:** Blank command prompt window appears when tray launches mediaforge.exe and doesn't close.
- **Impact:** User sees unsightly console window; confusing for non-technical users.
- **Fix:** Verify `CREATE_NO_WINDOW` flag (0x08000000) is set correctly in `SysProcAttr`. Test on Windows 10/11.
- **Files:** `cmd/tray/main.go` (launchMediaForge function)

**Issue #9: View Logs fails with "exit status 1"**
- **Description:** Tray menu "View Logs" crashes with error `openFile failed: exit status 1`. The black console window mentioned in Issue #8 shows the error.
- **Root Cause:** Log file path may be incorrect or file doesn't exist yet.
- **Fix:** Verify log path resolves to `%APPDATA%\MediaForge\logs\mediaforge.log`. Create the file on first run if missing. Test with different user paths.
- **Files:** `cmd/tray/main.go` (openFile helper, logsPath function)

**Issue #5: First-run config modal overlapped by installer finish button**
- **Description:** Tray's first-run setup modal launches, but the "Save & Launch" button is hidden or shifted behind other windows (installer finish screen).
- **Impact:** User can't see or click the Save button; setup appears to hang.
- **Fix:** Ensure modal is topmost window. Add window focus/raise call after launching the PowerShell wizard. Test full installer flow.
- **Files:** `cmd/tray/setup_windows.go` (runSetupWizard function)

### Logging

**Issue #10: Missing version banner and encoder info in logs**
- **Description:** Startup log missing: name/version banner and active encoders detected (NVIDIA/AMD/Intel/CPU).
- **Impact:** No way to verify what build is running or what hardware acceleration is available.
- **Fix:** Add startup log entries: banner with version and build number; encoder detection results from hwaccel probe.
- **Files:** `cmd/mediaforge/main.go` (startup logging), `internal/hwaccel/detect.go` (encoder detection)

**Issue #11: Directory cache timeout warning on network shares**
- **Description:** Log shows "Directory count cache warm did not complete" with 30s timeout on SMB shares. Breaks the stats dashboard initialization.
- **Impact:** Stats gauge doesn't show accurate directory count; user sees timeout warnings repeatedly.
- **Fix:** Investigate if 30s timeout is too short for large shares. Consider making timeout configurable or increasing default to 60s. Add early-exit if share is unreachable (don't keep retrying).
- **Files:** `internal/browse/browse.go` (WarmCountCache context timeout)

---

## High Priority

### Review Queue UI

**Issue #12: Review Queue entries too small to read**
- **Description:** Queue resizes based on entry count; with many items, each entry becomes unreadable.
- **Impact:** Users can't triage queue entries; can't click actions or read details.
- **Fix:** Make Review Queue scrollable. Use fixed-height card layout (not flex-shrink). Add "Show 10 per page" pagination or infinite scroll.
- **Files:** `web/templates/index.html` (review-queue div styling)

**Issue #3: No retry individual items in Review Queue**
- **Description:** Review Queue only has "Retry All" and "Discard Selected" bulk actions. No per-entry retry button.
- **Impact:** User must discard low-confidence matches individually but can't retry a codec error without retrying everything.
- **Fix:** Add [Retry] button per entry. On click, re-run the pipeline from the failed step (e.g., re-run codec detection, re-run TVDB lookup).
- **Files:** `web/templates/index.html` (review-queue entry actions), `internal/api/handler.go` (RetryReviewQueueItem endpoint)

**Issue #4: Review Queue lacks custom encode options per entry**
- **Description:** Review Queue entries for failed encodes have no way to retry with different settings (quality preset, speed, target reduction).
- **Impact:** User can't tune encode parameters if first attempt failed; must discard and re-add via Manual Add UI.
- **Fix:** Add "Re-encode with custom settings" action to Review Queue. Opens settings modal pre-populated with original file name and current settings; user can adjust and retry.
- **Files:** `web/templates/index.html` (review-queue encode failure actions), `internal/api/handler.go` (custom retry endpoint)

### Dashboard UI

**Issue #12 (UI):** Pause pipeline slider position
- **Description:** Pipeline pause/resume slider is buried in settings panel; should be top of dashboard queue controls.
- **Impact:** User can't quickly pause intake without opening Settings.
- **Fix:** Move pause slider to dashboard queue panel header (next to Start/Pause/Stop queue buttons).
- **Files:** `web/templates/index.html` (dashboard layout)

**Issue #13: Missing pause/resume encode queue button on dashboard**
- **Description:** Dashboard has no button to pause/resume the encode queue.
- **Impact:** User must open tray menu to pause encodes; no in-web-UI control.
- **Fix:** Add "Pause Queue" / "Resume Queue" toggle button in dashboard queue panel header (next to pipeline slider). Calls `POST /api/queue/pause` and `POST /api/queue/start`.
- **Files:** `web/templates/index.html` (dashboard queue controls), `internal/api/handler.go` (queue pause/resume endpoints)

### Intake Pipeline

**Issue #14: Ingest pipeline doesn't clean up staging directories**
- **Description:** After moving a file from staging to library, empty show/season folders are left behind.
- **Impact:** Staging directory grows with orphaned folders; disk clutter.
- **Fix:** After post-encode move, recursively delete empty directories: season folder first, then show folder. Verify directory is empty before deleting (guard against concurrency).
- **Files:** `internal/intake/move.go` (post-encode move handler)

### First-Run Wizard

**Issue #16: Missing UNC path documentation in first-run**
- **Description:** Setup wizard has no warning that mapped drives (M:\) don't work; users should use UNC paths (\\SERVER\SHARE).
- **Impact:** User configures mapped drive, service fails silently, app appears broken.
- **Fix:** Add help text under "Intake Paths" section: "Note: Use UNC paths for network shares (e.g., \\\\SERVER\\SHARE\\FOLDER). Mapped drives are not supported."
- **Files:** `cmd/tray/setup_wizard.ps1` (wizard form labels)

**Issue #17: Missing SSD recommendation for transcode directory**
- **Description:** Setup wizard doesn't mention that transcode (working) directory should be on fast storage.
- **Impact:** User picks slow secondary drive for working directory; encodes bottleneck on disk I/O.
- **Fix:** Add tooltip/help text next to "Transcode Working Directory": "Tip: Use a fast SSD for best performance. Staging and working files are temporary."
- **Files:** `cmd/tray/setup_wizard.ps1` (wizard form labels)

**Issue #18: Unclear that API keys are optional**
- **Description:** Setup wizard doesn't explain that API keys are optional; users may skip them thinking they're required.
- **Impact:** User doesn't add API keys; identification fails, Review Queue fills with "no metadata match".
- **Fix:** Add note near API key inputs: "API Keys are optional. Without them, all files will be routed to Review Queue for manual matching."
- **Files:** `cmd/tray/setup_wizard.ps1` (wizard form labels)

### Installer

**Issue #15: Uninstall doesn't remove AppData**
- **Description:** Inno Setup uninstall removes binaries only; config, logs, and cache left in `%APPDATA%\MediaForge`.
- **Impact:** User reinstalls and old config is used; confusing for fresh starts.
- **Fix:** Add uninstall option: "Remove application data (config, logs, cache)?". If yes, delete `%APPDATA%\MediaForge` directory.
- **Files:** `installer/mediaforge.iss` (UninstallRun section)

---

## Medium Priority

### Tray Menu Features

**Issue #2: Tray missing left-click to open dashboard**
- **Description:** Tray icon has right-click menu but left-click doesn't do anything (should open dashboard in browser).
- **Impact:** Non-obvious how to open the web UI from tray; users must right-click and find "View Dashboard" (if it exists).
- **Fix:** Add left-click handler to tray icon: calls `openBrowser("http://localhost:8080")`.
- **Files:** `cmd/tray/main.go` (systray.Run onReady callback)

**Issue #7: Tray pause doesn't sync with web UI pipeline slider**
- **Description:** Tray menu has Pipeline Pause/Resume checkbox. Web UI has a separate pipeline slider. Toggling one doesn't update the other; they drift out of sync.
- **Impact:** User pauses intake in tray, but web UI slider shows running (or vice versa).
- **Fix:** Both tray and web UI should call the same API endpoint (`POST /api/queue/pause` or `POST /api/queue/start`). Fetch current state from API on tray menu rebuild (every 2s).
- **Files:** `cmd/tray/main.go` (buildTrayMenu), `internal/api/handler.go` (queue endpoints must return current state)

### Encode Quality

**Issue #6: Pause doesn't actually pause encoding**
- **Description:** Pause queue in tray or web UI stops the encoder immediately and clears the queue, forcing restart on resume.
- **Impact:** User loses encoding progress; must re-encode from scratch.
- **Fix:** Implement true pause: worker goroutine stops accepting new jobs but current encode continues. On resume, worker unpauses and picks up next job. Requires worker pool changes.
- **Files:** `internal/jobs/worker.go` (WorkerPool.Pause, Unpause logic)

---

## Low Priority (Polish)

### TVDB Confidence Scoring

**Issue #19: Multi-show disambiguation (The Office UK vs US)**
- **Description:** TVDB lookup for "The Office" returns UK 2001 instead of US 2005 even when filename says 2005.
- **Root Cause:** Code doesn't filter candidates by premiere year before picking the first result.
- **Impact:** Wrong episode metadata assigned to file; moved to wrong library folder.
- **Fix:** After TVDB search, filter candidates where `premiere_year == filename_year ± 1`. Require episode title match. If no match after filtering, route to LLM review (confidence < 0.60).
- **Files:** `internal/intake/tvdb.go` (TVDBClient search and scoring logic)

---

## Summary by Effort

| Priority | Issue | Effort | Owner |
|---|---|---|---|
| Critical | Tray doesn't kill mediaforge on exit | 1h | Tray |
| Critical | Black console window on launch | 0.5h | Tray |
| Critical | View Logs fails | 1h | Tray |
| Critical | First-run modal overlapped | 0.5h | Tray |
| Critical | Missing version/encoder logs | 1h | Logging |
| Critical | Directory cache timeout | 1h | Browse |
| High | Review Queue scrolling | 1h | UI |
| High | Retry individual items | 2h | API + UI |
| High | Custom encode options | 2h | API + UI |
| High | Pause slider position | 0.5h | UI |
| High | Pause encode queue button | 1h | API + UI |
| High | Clean up staging dirs | 1h | Pipeline |
| High | UNC path warning | 0.5h | Wizard |
| High | SSD tip | 0.5h | Wizard |
| High | API key optional note | 0.5h | Wizard |
| High | Uninstall remove AppData | 1h | Installer |
| Medium | Left-click open dashboard | 0.5h | Tray |
| Medium | Sync tray and web UI state | 1h | API + Tray |
| Medium | True pause (not stop) | 3h | Worker |
| Low | TVDB year disambiguation | 2h | Intake |

**Estimated total effort (critical + high): ~21 hours**
