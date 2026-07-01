CURRENT STATE NOTE

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
