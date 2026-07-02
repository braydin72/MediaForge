CURRENT STATE NOTE

=== Latest session: fix tray first-run config not persisting (UTF-8 BOM) ===

- Symptom: first-run setup modal always opened with defaults; after "Save &
  Launch" the form closed, nothing launched, and restart re-showed the modal
  with defaults (config never written to %APPDATA%\MediaForge\mediaforge.yaml).
- Root cause: setup_wizard.ps1 wrote result.json via `Set-Content -Encoding
  UTF8`, which in Windows PowerShell 5.1 prepends a UTF-8 BOM. Go's
  encoding/json rejects a leading BOM, so runSetupWizard()'s json.Unmarshal
  failed and returned ok=false -> setupConfig() treated it as Cancel and
  os.Exit(0). The config-save path (applyAndSaveConfig -> cfg.Save) was never
  reached.
- Fix: cmd/tray/setup_windows.go strips a leading UTF-8 BOM before Unmarshal
  (bytes.TrimPrefix). Also changed setup_wizard.ps1 to write UTF-8 without a
  BOM via [System.IO.File]::WriteAllText for good measure.
- Also removed a stray untracked cmd/tray/main_windows.go (poller-only tray
  duplicate) that redeclared main/onReady/baseURL/openFile and broke the
  Windows build. It was never committed; HEAD was unaffected.
- Verified: `GOOS=windows go build ./...` and `go build ./...` both pass.

=== Prior session: fix nil panic in WarmCountCache (SMB shares) ===

- internal/browse/browse.go WarmCountCache(): the ancestor-propagation loop
  assumed dirCounts[dir] was always present ("WalkDir visits parents before
  children"). On SMB/UNC shares a parent dir can be skipped mid-walk (nil
  DirEntry or access error), leaving dirCounts[dir]==nil -> dc.fileCount++
  panicked. Now creates the dirCount on demand instead of dereferencing nil.
- Added a 30s context.WithTimeout around the WalkDir so an unreachable share
  logs a warning instead of hanging the warm goroutine forever. New warning
  log ("Directory count cache warm did not complete") when ctx cancels/times
  out before completion.
- Note: the nil-DirEntry guard the original report asked for was already
  present; the real crash was the ancestor-loop deref above.
- Verified: `go build ./internal/browse/` compiles clean. Could NOT run full
  build/tests this session — C: drive is 100% full (~40MB free), so linking
  the mediaforge/tray binaries and go test both fail on disk space, unrelated
  to this change. Re-run `go build ./... && go test ./...` after freeing disk.

=== Prior session: tray launch opens browser ===

- cmd/tray/main.go launchMediaForge(): now logs to stderr on cmd.Start()
  failure and, after the 2s bind wait, opens the web UI via new openBrowser()
  helper. Console-hiding (hideConsole/syscall) was already present, unchanged.
- Removed the redundant time.Sleep(2s) in main() (the wait now lives inside
  launchMediaForge, so startup no longer double-waits to 4s).
- openBrowser() added (retires the earlier unused-helper note). Kept the robust
  mediaForgePath() (os.Executable) rather than os.Args[0].
- OPEN BEHAVIOR: launchMediaForge is also called by the Restart menu item, so
  Restart now also reopens the browser after 2s. Left as-is; split into a
  startup-only browser open if that's unwanted.

=== Prior session: installer switched to launcher model ===

- installer/mediaforge.iss reworked from the Windows-service model to the tray
  launcher model (tray starts mediaforge.exe; no service).
- Added [Registry]: HKCU\...\Run "MediaForge" = "{app}\mediaforge-tray.exe"
  (ValueType string, uninsdeletevalue). Single autostart entry.
- Removed the [Tasks] (startupservice/traystartup) and the {userstartup}
  shortcut that duplicated autostart. Removed [Run] service --install + sc
  start and the [UninstallRun] service stop/--uninstall. Removed the unused
  MyServiceName define and the service-sleep [Code] block.
- [Run] now launches {app}\mediaforge-tray.exe --first-run (nowait). NOTE: the
  tray does not parse --first-run (no flag.Parse); it auto-detects first run via
  configExists(), so the flag is currently a harmless no-op.
- [UninstallRun] now taskkills mediaforge-tray.exe and mediaforge.exe so files
  can be removed.
- Used correct binary name mediaforge-tray.exe throughout (prompt said tray.exe,
  which this installer never produced).
- CAVEAT (ISCC warning): PrivilegesRequired=admin + HKCU Run key — in an admin
  install HKCU is the elevated user's hive. Fine when the logged-in user is the
  admin (typical); unreliable if a standard user installs with separate admin
  creds. Follow-up options: switch Root to HKA/HKLM, or make it a per-user
  ({localappdata}) install to drop admin. Also pre-existing warning: x64 arch id
  deprecated (use x64compatible).
- Verified: ISCC compiles mediaforge.iss successfully.

=== Prior session: tray menu implementation ===

- Implemented buildTrayMenu() in cmd/tray/main.go. Menu (in order): Pipeline
  (checkbox, checked=running; toggles /api/queue/start|pause), Transcode Queue
  (N) (disabled label, refreshed every 2s from /api/stats pending+running),
  sep, Start/Pause/Stop Queue (POST /api/queue/*), sep, Config (opens
  mediaforge.yaml), Restart MediaForge (killMediaForge -> sleep 1s ->
  launchMediaForge), View Logs, sep, Exit (kill + systray.Quit + os.Exit).
- Helpers added: callQueueAPI (silent-fail POST, logs to stderr), getQueueCount
  (GET /api/stats), openFile (cmd /c start), killMediaForge (taskkill /F +
  wait up to 3s via tasklist), mediaForgeRunning, logsPath.
- Corrected log path vs the prompt: real path is
  %APPDATA%\MediaForge\logs\mediaforge.log (logger uses {configDir}/logs), NOT
  ...\config\logs\... . Derived from configPath() rather than hardcoded.
- Kept the existing real embedded icon (loadIcon) instead of a blue-square
  placeholder. Omitted the openBrowser helper from the spec — no menu item uses
  it and it would trip the unused-func linter; add it if an "Open Web UI" item
  is introduced later.
- Verified: GOOS=windows go build/vet ./cmd/tray clean; queue handlers ignore
  body and return 200 (callQueueAPI nil body OK); /api/queue/stop exists.
  GUI modal/tray interaction can't be driven headlessly — needs manual click.

=== Prior session: first-run setup wizard (tray, Windows) ===

- Implemented setupConfig() for the tray (Windows). Chose a native Windows Forms
  dialog driven via PowerShell instead of Fyne — this environment has no gcc and
  CGO_ENABLED=0, so Fyne (CGO+OpenGL) would break the pure-Go build.
- New files:
  * cmd/tray/setup_wizard.ps1 — embedded (go:embed) WinForms wizard. Sections:
    Intake Paths (4 required path rows w/ Browse), API Keys, LLM, Notifications
    (email checkbox enables/disables SMTP fields), Advanced. Save & Launch writes
    JSON to -OutPath and exits 0; Cancel/close exits 1. Folder browse detects a
    mapped network drive and offers Convert-to-UNC / Keep / Cancel.
  * cmd/tray/setup_windows.go — runSetupWizard() (temp files + powershell),
    applyAndSaveConfig() (builds config.DefaultConfig(), overrides fields, creates
    local dirs, writes via cfg.Save), validateLibraryPath()/isLocalPath()/driveOf(),
    convertMappedDriveToUNC() (parses `net use X:`), showMessage() error dialog.
  * cmd/tray/setup_check_test.go — unit tests for the non-GUI helpers (pass).
- Config is built from config.DefaultConfig() + Save() (not hand-written YAML), so
  all template fields are covered. Required paths must be non-empty; mapped network
  drive letters (M:\) are rejected with a "use UNC" error; UNC paths accepted and
  validated at runtime. intake.enabled=true on wizard completion.
- LINUX/DOCKER: NOT changed — the browser first-run wizard already exists
  (setup.WizardHandler wired in cmd/mediaforge/main.go), and config.Load creates a
  default config when none exists. No duplication needed.
- Verified: GOOS=windows go build/vet ./cmd/tray clean; PS script parses; helper
  tests pass. The GUI modal itself needs a manual click-through (can't run headless).

=== Prior session: build number injection + tray rewrite ===

Build number tooling:
- Added build.ps1 (repo root): builds dist/mediaforge.exe and dist/tray.exe
  with the build number injected into internal/version.Build via -ldflags -X.
  Defaults to git commit count; override with -Build <n>.
- .github/workflows/release.yml: Windows jobs (mediaforge.exe + tray.exe) and
  the docker job now inject the build number from github.run_number (matching
  Dockerfile / dev-build.yml). Bumped setup-go 1.21 -> 1.25 in both Windows
  jobs to match go.mod (was previously broken).

=== Tray app rewrite (scaffold) ===

- REWROTE the system tray app as a launcher. Deleted the old
  cmd/tray/main_windows.go (which assumed mediaforge already ran as a Windows
  service and built a full menu). Replaced with cmd/tray/main.go (//go:build
  windows).
- New flow in main(): configExists() → setupConfig() if missing → launchMediaForge()
  (starts mediaforge.exe hidden via SysProcAttr CreationFlags 0x08000000) →
  sleep 2s → systray.Run(onReady, onExit).
- mediaForgePath() prefers mediaforge.exe next to the tray exe, else falls back
  to "mediaforge.exe" on PATH.
- STUBS (not yet implemented): buildTrayMenu() (no-op → tray shows icon, empty
  menu) and setupConfig() (no-op). These are intentional placeholders for the
  next steps — the previous menu/polling/toast logic was NOT carried over and
  needs to be rebuilt.
- cmd/tray/icon_windows.go kept unchanged (loadIcon used by onReady).
- Verified: GOOS=windows go build -o dist/tray.exe ./cmd/tray is clean.

=== Prior session ===

As of this session, the following were completed:

1. Windows system tray app (cmd/tray/main_windows.go + icon_windows.go) — ALREADY
   existed from commit 861f2e0. Updated to use config.ResolveConfigPath() so it
   finds the config file in %APPDATA%\MediaForge\ instead of relying on a hardcoded
   relative path.

2. Windows config path resolution (internal/config/config.go):
   - Added ResolveConfigPath(explicit string) — checks %APPDATA%\MediaForge\mediaforge.yaml
     on Windows before falling back to ./config/mediaforge.yaml.
   - Added EnsureWindowsDirs() — creates %APPDATA%\MediaForge\ and \logs\ at startup
     on Windows (no-op on Linux/Docker).
   - Wired into cmd/mediaforge/main.go, internal/winsvc/service.go, and
     cmd/tray/main_windows.go.

3. UNC path validation fix:
   - Root cause: media_path was returned in GET /api/config but had no field in
     UpdateConfigRequest and no input in the Settings panel — it could only be set
     during the first-run wizard.
   - Added MediaPath *string to UpdateConfigRequest in internal/api/handler.go.
   - UpdateConfig() now accepts any non-empty path (local, UNC, relative) and calls
     browser.SetRoot() so the change takes effect immediately without restart.
   - Added Browser.SetRoot() in internal/browse/browse.go — clears both caches and
     updates mediaRoot atomically.
   - Added "Media browser root" text input to the Settings panel (Intake section,
     after TV Shows library) in web/templates/index.html.
   - Updated loadSettings() JS to populate the new field from config.media_path.

NOT yet started / still pending:
- SmartShrink quality cascade (Excellent -> Good -> Acceptable fallback)
- CRF search range expansion (28 down to 16)
- VMAF sample count configurable
- Inno Setup installer (installer/mediaforge.iss + installer/default-config.yaml
  written, NOT yet tested or wired into CI)

Verified this session (no code changes):
- Ran mediaforge.exe on Windows: config auto-created at
  %APPDATA%\MediaForge\mediaforge.yaml, logs at
  %APPDATA%\MediaForge\logs\mediaforge.log, SQLite store at
  %APPDATA%\MediaForge\mediaforge.db — all as expected from commits
  ee5ff4f and f44e935.

Key files changed this session:
- internal/config/config.go         (ResolveConfigPath, EnsureWindowsDirs)
- cmd/mediaforge/main.go            (use new config helpers)
- internal/winsvc/service.go        (use new config helpers)
- cmd/tray/main_windows.go          (use new config helpers)
- internal/browse/browse.go         (Browser.SetRoot)
- internal/api/handler.go           (MediaPath in UpdateConfigRequest + UpdateConfig)
- web/templates/index.html          (media_path Settings field + loadSettings)

ALWAYS run "git log --oneline -10" at the start of a new session to see
what actually landed vs what was requested, before assuming anything is
done or not done.

## Update: queue control API (Start/Pause/Stop) for tray app

This session added the three queue-control endpoints the tray app menu will
call. The tray app itself was not modified — wiring the menu items is the
next step.

Changes:
- internal/api/handler.go: renamed PauseQueue→PausePipeline,
  ResumeQueue→StartPipeline. Added new StopPipeline handler that calls
  h.workerPool.StopAll().
- internal/api/router.go: routes are now
  POST /api/queue/start  → StartPipeline (workerPool.Unpause)
  POST /api/queue/pause  → PausePipeline (workerPool.Pause)
  POST /api/queue/stop   → StopPipeline  (workerPool.StopAll)
- internal/jobs/worker.go: added WorkerPool.StopAll() as a no-op stub.
  Final semantics to be decided — options listed in the source comment
  (requeue vs fail vs clear).
- web/templates/index.html: web UI resume button now calls
  /api/queue/start (was /api/queue/resume). Pause button unchanged.

WorkerPool method status:
- Pause()     — existed, unchanged.
- Unpause()   — existed, unchanged.
- StopAll()   — ADDED as a no-op stub.

Verified: go build ./... and go test ./... both clean.
