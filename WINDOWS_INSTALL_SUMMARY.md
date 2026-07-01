# Windows Installation Fix — Updated Plan Summary

## What Changed From Original Plan

### Process Management (Critical)

**Old:** mediaforge.exe runs as Windows service
**New:** Tray app manages mediaforge.exe lifecycle

```
Old:                          New:
mediaforge.exe (service)      tray.exe (user process)
    ↓                             ↓
[always running]              Launches mediaforge.exe
                              [Controls start/stop/reload]
                              Kills mediaforge.exe on tray exit
```

**Why this matters:**
- Services run as LocalSystem (no network share access)
- User processes inherit user credentials (can access network shares)
- Tray can restart app to reload config without manual intervention
- Cleaner shutdown (no orphaned processes)

---

## Tray Menu — Full Feature Set

```
MediaForge Tray Icon
├─ [Open Dashboard]     → Browser to http://localhost:8080
├─ [Start Pipeline]     → Resume encode queue
├─ [Pause Pipeline]     → Pause (don't stop in-progress jobs)
├─ [Stop Pipeline]      → Stop all jobs immediately
├─ [Reload Config]      → Kill app + restart (reloads mediaforge.yaml)
├─ [View Logs]          → Open %APPDATA%\MediaForge\config\logs\mediaforge.log
├─ [Settings]           → Open mediaforge.yaml in default editor
└─ [Exit]               → Kill mediaforge.exe + close tray
```

**Pipeline controls:** Start/Pause/Stop are API calls, not tray operations
- Allows fine-grained control without restarting app
- Start/Pause/Stop require these API endpoints:
  - `POST /api/queue/start` → workerPool.Unpause()
  - `POST /api/queue/pause` → workerPool.Pause()
  - `POST /api/queue/stop` → workerPool.StopAll()

**Reload Config:** Unique to tray (not available in web UI)
- User edits mediaforge.yaml manually
- Clicks "Reload Config" in tray menu
- Tray kills mediaforge.exe
- Tray relaunches mediaforge.exe with fresh config loaded

**Exit:** Properly shuts down
- Kills mediaforge.exe first
- Then closes tray
- No orphaned processes left running

---

## Process Lifecycle

### Startup

```
User logs in
    ↓
Windows Run key executes: C:\Program Files\MediaForge\tray.exe
    ↓
Tray app starts (shows icon in system tray)
    ├─ Check %APPDATA%\MediaForge\mediaforge.yaml exists?
    │
    ├─ NO (first run):
    │  ├─ Show config modal (Fyne GUI)
    │  ├─ User picks: watch folder, movies library, TV library, API keys
    │  ├─ Save to %APPDATA%\MediaForge\mediaforge.yaml
    │  └─ Launch mediaforge.exe
    │
    └─ YES (subsequent runs):
       └─ Launch mediaforge.exe immediately
       
    ↓
Tray shows menu (Dashboard, Start, Pause, Stop, Reload, etc.)
Browser opens to http://localhost:8080 (2 second delay to let app start)
```

### Reload Config (User clicks tray menu)

```
User clicks "Reload Config"
    ↓
tray.exe calls stopMediaForge()
    ├─ mediaforgeProcess.Kill() ← kills mediaforge.exe
    └─ mediaforgeProcess.Wait()
    ↓
    [1 second delay]
    ↓
tray.exe calls launchMediaForge()
    ├─ Start mediaforge.exe with CREATE_NO_WINDOW
    ├─ Store process handle in global mediaforgeProcess
    └─ mediaforge.exe loads fresh config from disk
```

### Exit (User clicks tray menu)

```
User clicks "Exit"
    ↓
tray.exe calls stopMediaForge()
    └─ mediaforgeProcess.Kill() ← kills mediaforge.exe
    ↓
tray.exe calls systray.Quit()
tray.exe calls os.Exit(0)
    ↓
[All processes terminated]
```

---

## File Locations

```
C:\Program Files\MediaForge\
├─ mediaforge.exe          (main app, launched by tray)
├─ tray.exe                (system tray, owns mediaforge process)
└─ web/                    (UI static files)

%APPDATA%\MediaForge\      (user home directory)
├─ mediaforge.yaml         (config, created by tray setup modal)
├─ config/
│  └─ logs/
│     └─ mediaforge.log    (session log, created by mediaforge.exe at startup)
└─ poster_cache/           (optional, TMDB poster downloads)
```

**Why this split:**
- Program Files: executable binaries (write-protected)
- %APPDATA%: user-writable data (config, logs, cache)
- Clean uninstall: remove Program Files, keep %APPDATA%

---

## Installer

**installer/mediaforge.iss (Inno Setup)**

```ini
[Setup]
DefaultDirName={pf}\MediaForge      ← Program Files\MediaForge

[Files]
Source: "dist\mediaforge.exe"; DestDir: "{app}"
Source: "dist\tray.exe"; DestDir: "{app}"
Source: "web\*"; DestDir: "{app}\web"; recursesubdirs
; NO config/ or logs/ folders created

[Registry]
Root: HKCU; Subkey: "...\Run"; 
  ValueName: "MediaForge"; 
  ValueData: "{app}\tray.exe"      ← User-level startup, not HKLM

[Run]
Filename: "{app}\tray.exe"; Parameters: "--first-run"
; No browser launch (tray handles it)
```

**Key points:**
- Copies binaries only (no config/logs)
- Adds tray.exe to HKCU\Run (user login trigger)
- Passes `--first-run` flag on fresh install
- Installer doesn't launch browser or show wizard

---

## Tray App Code Highlights

### Global Process Management

```go
var mediaforgeProcess *os.Process

func launchMediaForge() {
    cmd := exec.Command(exePath)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        CreationFlags: 0x08000000, // CREATE_NO_WINDOW (no blank console)
    }
    cmd.Start()
    mediaforgeProcess = cmd.Process
    
    // Open browser after delay
    time.Sleep(2 * time.Second)
    openBrowser("http://localhost:8080")
}

func stopMediaForge() {
    if mediaforgeProcess != nil {
        mediaforgeProcess.Kill()
        mediaforgeProcess.Wait()
        mediaforgeProcess = nil
    }
}

func reloadConfig() {
    stopMediaForge()
    time.Sleep(1 * time.Second)
    launchMediaForge()
}
```

### Tray Menu

```go
func buildTrayMenu() {
    mStart := systray.AddMenuItem("Start Pipeline", "")
    mPause := systray.AddMenuItem("Pause Pipeline", "")
    mStop := systray.AddMenuItem("Stop Pipeline", "")
    mReload := systray.AddMenuItem("Reload Config", "")
    mExit := systray.AddMenuItem("Exit", "")
    
    go func() {
        for {
            select {
            case <-mStart.ClickedCh:
                httpRequest("POST", "http://localhost:8080/api/queue/start", nil)
            case <-mReload.ClickedCh:
                reloadConfig()  // Kill + relaunch
            case <-mExit.ClickedCh:
                stopMediaForge()  // Kill app first
                systray.Quit()
                os.Exit(0)
            }
        }
    }()
}
```

---

## API Endpoints Required

### New Endpoints (in internal/api/handler.go)

```go
// StartPipeline — POST /api/queue/start
func (h *Handler) StartPipeline(w http.ResponseWriter, r *http.Request) {
    h.workerPool.Unpause()
    writeJSON(w, http.StatusOK, map[string]string{"status": "pipeline started"})
}

// PausePipeline — POST /api/queue/pause
func (h *Handler) PausePipeline(w http.ResponseWriter, r *http.Request) {
    h.workerPool.Pause()
    writeJSON(w, http.StatusOK, map[string]string{"status": "pipeline paused"})
}

// StopPipeline — POST /api/queue/stop
func (h *Handler) StopPipeline(w http.ResponseWriter, r *http.Request) {
    h.workerPool.StopAll()  // Cancel in-progress jobs
    writeJSON(w, http.StatusOK, map[string]string{"status": "pipeline stopped"})
}
```

**Prerequisites:**
- workerPool must have Pause(), Unpause(), StopAll() methods
- Should already exist if queue pause/resume is implemented

---

## Implementation Checklist

### Phase 1: Config Path Resolution (2 hours)
- [ ] Update internal/config/config.go
  - LoadConfig() checks %APPDATA% first, then app dir
  - SaveConfig() creates %APPDATA%\MediaForge\ directory
  - Logs go to %APPDATA%\MediaForge\config\logs\

### Phase 1.5: API Endpoints (1 hour)
- [ ] Add StartPipeline, PausePipeline, StopPipeline handlers
- [ ] Register routes
- [ ] Verify workerPool has required methods

### Phase 2: Tray App (5 hours)
- [ ] Create cmd/tray/main.go
- [ ] Process management: launch, stop, reload
- [ ] First-run config modal (Fyne)
  - Path pickers for watch folder, movies, TV
  - Optional text fields for API keys
  - Save button → write YAML + launch mediaforge.exe
  - Cancel button → exit
- [ ] Tray menu (8 items: Dashboard, Start, Pause, Stop, Reload, Logs, Settings, Exit)
- [ ] Global mediaforgeProcess variable
- [ ] CREATE_NO_WINDOW flag on launch
- [ ] Browser open after 2 second delay
- [ ] Embed tray icon (16×16 PNG)

### Phase 3: Installer (2 hours)
- [ ] Rewrite installer/mediaforge.iss
- [ ] Copy only binaries (no config)
- [ ] Add tray to HKCU\Run with --first-run flag
- [ ] Remove wizard and browser launch

### Phase 4: Main App (1 hour)
- [ ] Remove wizard
- [ ] Use new config path resolution
- [ ] Test startup without tray

### Phase 5: Testing (2 hours)
- [ ] Fresh install: first-run modal works
- [ ] Config saves to %APPDATA%
- [ ] Browser opens after setup
- [ ] Tray menu all working (Start, Pause, Stop, Reload, etc.)
- [ ] Reload Config kills and restarts app
- [ ] Exit kills app
- [ ] No blank windows
- [ ] Reinstall finds existing config

**Total: 13 hours**

---

## Why This Design

1. **Tray is mandatory** — Windows has no elegant way to run headless services for per-user apps
2. **No service registration** — Services run as LocalSystem (can't access user shares)
3. **Process management** — Tray can reload config without user intervention
4. **Clean shutdown** — Tray owns the process, can clean up on exit
5. **GUI for config** — Fyne provides folder pickers (better UX than CLI)
6. **API-driven pipeline control** — Start/Pause/Stop don't need process restart
