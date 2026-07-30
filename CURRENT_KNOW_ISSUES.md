# Known Issues & Fixes

Issues grouped by module and prioritized for immediate action. These are bugs and broken features that need fixing before the next release.

Most of the backlog that used to live in this file (tray process management, logging, Review Queue UI/retry/custom-encode, dashboard queue controls, wizard help text, installer AppData cleanup, tray/web pause sync, TVDB year disambiguation) has since been fixed or superseded by how the Review Queue actually evolved (bulk + individual re-encode, per-entry match/search resolution). See `CHANGELOG.md` for the specific fixes. Only genuinely open items remain below.

---

## High Priority

### Intake Pipeline

**Issue #14: Ingest pipeline doesn't clean up staging directories**
- **Description:** After moving a file from staging to library, empty show/season folders are left behind.
- **Impact:** Staging directory grows with orphaned folders; disk clutter.
- **Fix:** After post-encode move, recursively delete empty directories: season folder first, then show folder. Verify directory is empty before deleting (guard against concurrency).
- **Files:** `internal/intake/watcher.go` (post-encode move path)

---

## Roadmap / Needs Design

### Pause & Stop Rework

The queue/intake control redesign (separated Play/Pause/Stop transport bar,
Pause Intake, file-disposal prompts, Skip, ReQueue, tray "Stop MediaForge",
Manual Add Add-to-Queue/Start-Encode split, naming-lookup toggle) was
implemented — see `CHANGELOG.md` [Unreleased] and `CURRENT_STATE.md`. One
item from the original design-stage list remains genuinely unscoped:

**True mid-encode pause (not just queue pause)**
- **Description:** "Pause" still only stops the worker pool from picking up *new* jobs — a job already running keeps encoding to completion (Stop All/Skip cancel it outright instead of pausing it). There is no way to suspend-and-later-resume an in-flight FFmpeg encode itself, unlike apps such as HandBrake which can suspend/resume the active encode.
- **Why it's not a quick fix:** Not clear this is straightforwardly possible with the ffmpeg CLI the way MediaForge invokes it today — needs research into whether ffmpeg supports a real pause/resume of an in-progress encode (vs. process suspend, which has its own caveats on Windows) before any implementation decision.
- **Status:** Not scoped yet. This is a "figure out the desired workflow first" item — needs a decision on what "pause" should mean beyond queue-level (which now exists) — process-level suspend vs. suspend-and-resume — before it becomes an actionable issue.
