# MediaForge — Comprehensive Status & Roadmap

**Release Date:** v1.0.0 on 2026-06-24 (latest release: v1.7.1 on 2026-07-26 — see CHANGELOG.md)  
**Current Session Date:** 2026-07-27

---

## Planning Added 2026-07-25 — Review Queue Actions & Pipeline Control Buttons

Two items requested by the user for future implementation, based on live testing
of v1.4.x/v1.5.0. **No code was written for either — plan only, pending review.**

### A. Review Queue: context-aware actions + bulk actions beyond Discard

**Problems observed live:**
- Every non-duplicate Review Queue entry renders the same four buttons —
  Pick Selected / Search Manually / Discard / Re-encode Custom
  (`actionsHTML` in `web/templates/index.html`, ~line 5930) — regardless of
  *why* the entry is there. For an entry created by an encode failure (e.g.
  "SmartShrink: no viable encode"), "Pick Selected" is dead: `reviewPick()`
  (~line 6078) always alerts "Select a candidate first" because no candidate
  list was ever populated for that entry type — clicking it does nothing
  useful, exactly as reported.
- Duplicate entries already get the right, narrower button set (Replace /
  Keep Existing) via a client-side check: `isDuplicate = e.reason.startsWith('duplicate:')`
  (~line 5890). This is the only category the UI distinguishes today.
- Bulk selection exists (checkboxes + `reviewUpdateBulk()`), but the only
  bulk action wired up is `reviewDiscardSelected()` (~line 6166). No bulk
  Replace, no bulk Re-encode Custom — every other resolution is one-by-one.

**Root cause:** `ReviewEntry` (`internal/store/sqlite.go` ~line 130) has no
structured category — only a free-text `Reason string`. `sendToReviewQueue`
(`internal/intake/watcher.go`) and `SendToReviewQueue`
(`internal/jobs/worker.go`) are called from ~15 distinct sites with
different reason strings and no shared taxonomy for the frontend to key off.

**Real categories found in the current code** (a starting taxonomy, not
final):
1. **Duplicate at destination** — already handled correctly (Replace / Keep Existing).
2. **Metadata/identification failure** — no TVDB/TMDB/OMDb match, low
   confidence, LLM rejected or unavailable. Needs Pick Selected / Search
   Manually / Discard. Re-encode Custom doesn't apply — nothing has been
   transcoded yet.
3. **Encode/transcode failure** — SmartShrink no viable encode, fixed-
   reduction target unachievable, post-encode move failed. Needs Re-encode
   Custom / Discard. Pick Selected is the dead button reported live.
4. **Unresolved multi-part episode / unresolved title mismatch** (new in
   this session's TVDB reconciliation work) — metadata partially resolved
   but ambiguous. Needs Search Manually / Discard, and possibly a dedicated
   "confirm this is/isn't a combined episode" action rather than a generic
   candidate picker.
5. **System/path failures** — codec detection failed, staging move failed,
   queue-add failed, library move failed, could not resolve library path.
   Usually retry-oriented. Note: an earlier session removed a "Retry"/
   "Re-add" review action (see `internal/api/handler.go` history/
   `CURRENT_STATE.md`) — understand why before reintroducing something
   similar for this category, don't just re-add blindly.

**Proposed direction (needs decision before implementing):**
- Add a `Category` field (string enum) to `ReviewEntry`, set explicitly at
  each `sendToReviewQueue`/`SendToReviewQueue` call site instead of the
  frontend sniffing the reason string.
- `actionsHTML` switches on `e.category` instead of the current single
  `isDuplicate` ternary, rendering the right button set per category.
- Add `reviewReplaceSelected()` for bulk duplicate resolution, and a bulk
  re-encode flow — this needs a shared settings panel (preset/quality/CRF/
  speed/format) applied to every selected item at once, since today's
  Re-encode Custom form (`reviewToggleCustom`, ~line 6105) is per-card and
  inline, not designed to apply to multiple entries.
- Decide UX for a mixed-category bulk selection (e.g. a duplicate and an
  encode-failure entry selected together) — likely: only show actions valid
  for every selected entry's category, hide/disable the rest.

**Status:** Done — implemented 2026-07-26. See CURRENT_STATE.md /
CHANGELOG.md `[Unreleased]` for details (Category field, context-aware
actions, bulk Replace/Re-encode Custom endpoints).

### B. Pipeline/queue control buttons — Pause/Resume, Stop Pipeline/Start, Stop Everything/Restart

**Problems observed live:**
- Clicking "Pause Queue" gives no feedback that a pause is pending while the
  current encode finishes — the button "flashes" then nothing visibly
  changes until the running job completes and it flips to "Resume". No
  "Pausing… (finishing current job)" intermediate state exists.
- The footer "Stop All" button (`pauseQueue()` in `web/templates/index.html`)
  calls the *same* `/api/queue/pause` endpoint as the header toggle. Its
  confirm-dialog text was corrected this session to stop overpromising a
  hard cancel it doesn't perform, but the underlying redundancy — two
  differently-labeled buttons doing the identical soft-pause action — was
  not resolved.

**Current real controls, as they exist right now:**
- Header "Pause Queue"/"Resume Encode Queue" toggle → `POST /api/queue/pause`
  / `/api/queue/start` (encode queue only, soft pause — fixed this session,
  `WorkerPool.Pause()`/`Unpause()`).
- Footer "Stop All" → same `/api/queue/pause` endpoint, misleadingly labeled
  relative to what it actually does.
- Tray "Stop Pipeline" checkbox → `POST /api/intake/pause` /
  `/api/intake/resume` (intake only, added this session via
  `Watcher.Pause()`/`Resume()`) — **web UI has no equivalent control at all.**
- `WorkerPool.StopAll()` / `POST /api/queue/stop` — unimplemented no-op
  stub (`// TODO: define final semantics`). A true hard-cancel does not
  exist anywhere in the app today.

**User's requested target design — three independent pause/resume pairs,
replacing the current header toggle + footer "Stop All":**
1. **Pause Queue / Resume Queue** — blocks new jobs from starting; the
   currently-running job (if any) finishes normally. Backend already
   correct (`WorkerPool.Pause()`/`Unpause()`, fixed this session) — this is
   a UI-only gap: add a visible "Pausing… (finishing current job)" state
   between click and the queue actually going idle, instead of silence.
2. **Stop Pipeline / Start Pipeline** — stops intake from moving new files
   out of the Incoming folder. Backend already correct and exposed in the
   tray (`Watcher.Pause()`/`Resume()` via `/api/intake/pause`/`/resume`,
   added this session) — needs the equivalent buttons added to the web UI,
   which currently has zero intake control.
3. **Stop Everything / Restart** — a genuine hard stop: cancel the
   currently-running job AND stop intake. Does not exist anywhere yet.
   `WorkerPool.StopAll()` needs real implementation — likely reintroducing
   the cancel-and-requeue logic that was deliberately removed from
   `Pause()` earlier this session, but as its own explicit action, combined
   with calling `Watcher.Pause()`. Open design question: does "Stop
   Everything" discard the current job's progress entirely (requeue to
   pending, matching the old pre-this-session `Pause()` behavior) or mark
   it failed? Recommend requeue, to match the semantics the code already
   had before this session repurposed `Pause()`.

Once the web UI catches up, the tray menu (`cmd/tray/main.go`) should
probably grow the same third "Stop Everything" control — it currently only
has 2 of the 3 concepts (Stop Pipeline, Pause Queue).

**Status:** Not started — plan only, including the hard-stop semantics
decision above, which needs your call before implementation.

---

## Session Summary (Planning Chat)

This session produced detailed plans for 6 major improvement areas. No code was written; all output is planning documentation.

---

## Completed Work (Previous Session)

✅ **Windows System Tray App**
- Tray app exists (cmd/tray/main_windows.go + icon_windows.go)
- Updated to use config.ResolveConfigPath()
- Finds config in %APPDATA%\MediaForge\ instead of relative path

✅ **Windows Config Path Resolution**
- ResolveConfigPath() checks %APPDATA% first, falls back to ./config/
- EnsureWindowsDirs() creates needed directories on Windows
- Wired into mediaforge.exe, service handler, and tray app

✅ **UNC Path Validation Fix**
- media_path now editable in Settings panel (no longer wizard-only)
- Browser.SetRoot() refreshes paths without restart
- Supports local drives, UNC paths (\\server\share), and relative paths

---

## Planning Work Completed This Session (6 Documents)

### 1. SMTP Test Button + Notification Check Bug Fix
**File:** `MEDIAFORGE_ISSUES_PLAN.md` (Issue 1)

**Problem:** "Test All Notifications" reports no notifications when only SMTP configured.

**Solution (3 phases, 4 hours total):**
- Add SMTP config struct to internal/config/config.go
- Create internal/smtp/client.go with IsConfigured() and Test() methods
- Add POST /api/smtp/test endpoint in handler
- Update frontend Settings UI with SMTP fields and test button

**Status:** Ready to implement

---

### 2. Retry Reliability Fix (Videos Going Missing)
**File:** `MEDIAFORGE_ISSUES_PLAN.md` (Issue 2)

**Problem:** Retry logic unreliable; files sometimes disappear or end up in unknown state.

**Root Causes Identified:**
- No pre-flight checks before retry
- File may be deleted/moved/locked between failure and retry
- Transactional safety issues (old job removed before new job confirmed)
- Orphaned temp files from first attempt

**Solution (3 phases, 6 hours total):**
- Pre-flight checks: file exists, locked check, orphaned temp detection
- Probe with 15s timeout to detect network issues early
- Transactional: add new job BEFORE removing old job
- Better error messages ("file not found" vs "file in use")
- Frontend: disable button during retry, show specific errors

**Status:** Ready to implement

---

### 3. SmartShrink Retry System Redesign
**File:** `MEDIAFORGE_ISSUES_PLAN.md` (Issue 3)

**Problem:** Current linear CRF retry loop is inefficient, wastes time on impossible cases.

**Solution (4 phases, 12 hours total):**
- Quick first-pass estimate (10s clip at 480p) to predict final size
- Binary search on CRF instead of linear loop
- Fallback cascade: Excellent → Good → Acceptable (no Review Queue for preset fallback)
- Configurable VMAF sample count (quick mode for retries, thorough for initial)

**Status:** Ready to implement, marked as "lower priority"

---

### 4. Smart Duplicate Comparison & Auto-Resolution
**File:** `DUPLICATE_COMPARISON_PLAN.md`

**Problem:** All duplicates go to Review Queue; no automatic resolution logic.

**Solution (4 phases, 11 hours total):**
- Decision tree: codec first (prefer HEVC) → resolution (prefer highest) → bitrate (prefer higher)
- Only tied files (same codec, resolution, within 5% bitrate) go to Review Queue
- Auto-replace when incoming is objectively better
- Auto-keep when existing is objectively better
- New file: internal/intake/duplicate.go with CompareDuplicates() function

**Status:** Ready to implement

---

### 5. Review Queue UI Evaluation
**File:** `REVIEW_QUEUE_UI_EVAL.md`

**Problem:** Current UI is cluttered, hard to triage, slow to navigate.

**Decision:** **Keep current UI for v1.0**
- Functional but not optimized
- Other fixes (retry reliability, smart duplicates) will reduce entry volume naturally
- Rework is polish, not blocking
- Post-release scope unless bug fix naturally requires UI changes

**Post-Release Plan (11 hours):**
- Compact card list grouped by reason (codec error, low-confidence, duplicates, move failed)
- Duplicate comparison modal (side-by-side comparison)
- Low-confidence match picker workflow
- Batch actions (retry all, discard all selected)
- Sorting/filtering by reason, confidence score

**Status:** Deferred to v1.1 (post-release)

---

### 6. Windows Installation & Configuration Fix
**File:** `WINDOWS_INSTALL_FIX.md` + `WINDOWS_INSTALL_SUMMARY.md`

**Problems:**
- Installer puts files in Program Files (no write permissions for config)
- First-run wizard fails with default /media path (Linux, not Windows)
- Browser doesn't launch after install
- Blank command window appears
- Service registration complexity

**Architecture Change:**
- Tray app becomes primary process manager (mandatory on Windows)
- No Windows Service registration (use HKCU\Run instead)
- Tray owns mediaforge.exe lifecycle
- First-run config modal in tray app (Fyne GUI with folder pickers)

**Solution (5 phases, 13 hours total):**
1. Config path resolution (2h) — %APPDATA% with fallback
2. API endpoints (1h) — /api/queue/start, pause, stop
3. Tray app rewrite (5h) — process management, config modal, menu
4. Installer rewrite (2h) — Inno Setup, minimal (no config)
5. Testing (2h) — fresh install, startup, config reload

**Tray Menu:**
- Open Dashboard
- Start Pipeline → POST /api/queue/start
- Pause Pipeline → POST /api/queue/pause
- Stop Pipeline → POST /api/queue/stop
- Reload Config → kill app + restart
- View Logs
- Settings
- Exit → kill app + close tray

**Status:** Ready to implement (highest priority per user)

---

### 7. TVDB Matching Fix (Bonus)
**File:** `TVDB_MATCHING_FIX.md`

**Problem:** "The Office" (2005 US) matched to "The Office" (2001 UK).

**Root Cause:** Year weighting (40%) not heavy enough to break tie between identical episode records.

**Solution (5 phases, 5 hours total):**
1. Year penalty function (1h) — stricter year matching (>5yr diff = heavy penalty)
2. Network weighting (1h) — increase from 5% to 15%, add networkMatch() function
3. Search result ranking (1h) — deprioritize old shows when newer version exists
4. Logging & debugging (1h) — detailed score breakdown for each candidate
5. Testing (1h) — The Office US/UK, other multi-version shows

**Confidence Scoring Changes:**
- Before: (name 40%, year 40%, episode 15%, network 5%)
- After: (name 35%, year 35% with stricter penalty, episode 15%, network 15%)

**Result:**
- UK (2001) vs 2005 = 0.30 penalty → total score 0.52 (rejected)
- US (2005) vs 2005 = 1.0 penalty → total score 0.975 (selected)

**Status:** Ready to implement

---

## Priority Queue for Implementation

### 🔴 CRITICAL (Blocking v1.0.x)

1. **Windows Installation Fix** (13 hours)
   - User explicitly prioritized this as "#1"
   - Fixes broken installer, blank window issue, tray process management
   - Requires: config path (done), API endpoints (new)

### 🟠 HIGH (v1.0.x or v1.1)

2. **Retry Reliability Fix** (6 hours)
   - Fixes "videos going missing" critical bug
   - User reported this as major issue
   - Unblocks confident use of retry feature

3. **SMTP Test Button + Notification Bug** (4 hours)
   - Fixes user-facing feature
   - Low effort, high value
   - Complements Windows install (SMTP email notifications)

4. **TVDB Matching Fix** (5 hours)
   - Solves misidentification of "The Office"
   - Improves confidence scoring for all multi-version shows
   - Can run alongside other work

### 🟡 MEDIUM (v1.1)

5. **Smart Duplicate Resolution** (11 hours)
   - Reduces Review Queue clutter
   - Auto-resolves obvious cases
   - Improves user experience
   - Nice-to-have but not blocking

6. **SmartShrink Redesign** (12 hours)
   - Improves encode efficiency
   - Not user-facing, mostly internal optimization
   - Lower priority than other fixes

7. **Review Queue UI Redesign** (11 hours)
   - Polish, not blocking
   - Other fixes reduce entry volume first
   - Gather user feedback, then redesign

---

## Effort Summary by Category

| Category | Hours | Priority | Status |
|---|---|---|---|
| Windows Install Fix | 13 | 🔴 CRITICAL | Ready to code |
| Retry Reliability | 6 | 🟠 HIGH | Ready to code |
| SMTP Notifications | 4 | 🟠 HIGH | Ready to code |
| TVDB Matching | 5 | 🟠 HIGH | Ready to code |
| Duplicate Resolution | 11 | 🟡 MEDIUM | Ready to code |
| Review Queue UI | 11 | 🟡 MEDIUM | Deferred to v1.1 |
| SmartShrink Redesign | 12 | 🟡 MEDIUM | Ready to code, lower priority |
| **TOTAL** | **62 hours** | | |

---

## Release Strategy & Sprint Breakdown

### v1.0.x — Immediate Fixes (8 hours)

These are blockers before v1.1. Already planned:

**Retry Reliability (6 hours)**
- Pre-flight checks: file exists, locked status, orphaned temp files
- Transactional safety: add new job before removing old
- Better error messages: distinguish "not found" from "in use"
- Users can trust retry; no more mysterious disappearing files

**SMTP Test Button (2 hours)**
- POST /api/smtp/test endpoint
- Test button in Settings UI
- Users verify SMTP config works before relying on notifications

**TVDB Disambiguation (0 hours — done)**
- 4-component scoring implemented (commit fc4e7d8)
- "The Office" (2005 US) no longer mismatches to UK version

### v1.1 Sprint (19 hours)

Features for next release, prioritized by value + effort.

**SmartShrink Quality Cascade (3 hours)**
- When Excellent preset fails, auto-fallback to Good → Acceptable before Review Queue
- Fewer queue entries; users get usable files instead of manual intervention

**CRF Search Range Expansion (2 hours)**
- Lower floor from 28 to 16 for better compression on high-motion content
- More headroom for action/sports; better results overall

**Configurable VMAF Sample Count (2 hours)**
- Let users tune accuracy vs speed (default 5 samples)
- Power users want control; higher samples = better precision

**Intelligent Duplicate Resolution (6 hours)**
- Auto-resolve duplicates by codec tier, resolution, bitrate
- Only tied files go to Review Queue (80% of duplicates are obvious: higher res wins, HEVC beats AVC)
- Decision tree:
  1. Resolution mismatch → keep higher
  2. Same res + same codec → compare bitrate
  3. Same res + different codec → prefer HEVC
  4. Tied → Review Queue with full context

**Review Queue UI Rework (6 hours)**
- Group entries by reason (Codec Error, Low Confidence, Duplicate, Move Failed)
- Scrollable card layout, per-entry actions, pagination
- Current UI unreadable with >20 entries; users can't triage

### v1.2 Sprint (24 hours)

Strategic improvements for the release after v1.1.

**Subtitle File Handling (3 hours)**
- Move .srt, .ass, .vtt files alongside video during library move
- Subtitles no longer orphaned when video renamed

**Per-Show Naming Template Override (4 hours)**
- Allow per-show naming customization (Show - S01E01 Title.mkv vs 01x01 - Title.mkv)
- Organize libraries with show-specific formatting

**Automatic Library Scanning (4 hours)**
- Scan library on startup and index existing files
- Users can migrate existing library and get stats immediately (no re-ingest)

**Stats Dashboard Gauge with Trend (4 hours)**
- Circular gauge (speedometer style) showing storage saved + 30-day trend line
- More visual than bar chart; easier to see progress at a glance

**Mobile-Friendly UI (5 hours)**
- Responsive design for tablet/phone dashboards and settings
- Users want to monitor encodes on mobile

**Webhook Outbound Notifications (4 hours)**
- Send JSON webhooks to custom URLs for Home Assistant, Discord, Slack
- Power users want to trigger automation based on MediaForge events

---

## Implementation Timeline

### Recommended Order

1. **Immediate** (8h) — unblock v1.0.x
2. **v1.1 Reliability** (6h) — SmartShrink + Duplicates (highest value)
3. **v1.1 Polish** (13h) — Review Queue UI + VMAF config + CRF range
4. **v1.2** (24h) — mobile, webhooks, library scanning

Release v1.1 after immediate + reliability (14h).  
Release v1.2 after v1.2 sprint (24h).

### Effort Summary by Release

| Item | Hours | Release | Status |
|---|---|---|---|
| Retry reliability | 6 | v1.0.x | Ready |
| SMTP test button | 2 | v1.0.x | Ready |
| TVDB disambiguation | 0 | v1.0.x | ✅ Done |
| SmartShrink cascade | 3 | v1.1 | Ready |
| CRF range expansion | 2 | v1.1 | Ready |
| VMAF sample config | 2 | v1.1 | Ready |
| Duplicate resolution | 6 | v1.1 | Ready |
| Review Queue UI | 6 | v1.1 | Ready |
| Subtitles | 3 | v1.2 | Ready |
| Per-show templates | 4 | v1.2 | Ready |
| Library scanning | 4 | v1.2 | Ready |
| Stats gauge | 4 | v1.2 | Ready |
| Mobile UI | 5 | v1.2 | Ready |
| Webhooks | 4 | v1.2 | Ready |
| **TOTAL** | **51 hours** | | |

---

## Backlog (Lower Priority)

- Multi-file episode detection (complex; deferred unless user demand)
- Per-job VMAF sample override (covered by global config)
- Metadata caching layer (optimization if API rate limits hit)

---

## Per-Issue Checklist

### Issue 1: SMTP Notifications (4 hours)

**Checklist:**
- [ ] Add SMTPConfig struct to internal/config/config.go
- [ ] Create internal/smtp/client.go with IsConfigured(), Test(), Send()
- [ ] Add POST /api/smtp/test endpoint
- [ ] Add SMTP fields to UpdateConfigRequest
- [ ] Update /api/config response to include smtp_configured
- [ ] Add SMTP section to Settings UI (host, port, TLS, username, password, from, to)
- [ ] Add "Test SMTP" button
- [ ] Test: send actual email, catch auth errors, handle network errors
- [ ] Update notification status to show both Pushover and SMTP

**Files to modify:**
- internal/config/config.go (config struct)
- internal/smtp/client.go (new file)
- internal/api/handler.go (endpoint + UpdateConfigRequest)
- web/templates/index.html (Settings UI)

---

### Issue 2: Retry Reliability (6 hours)

**Checklist:**
- [ ] Add pre-flight checks: file exists, probe timeout, orphaned temp
- [ ] Implement transactional add→remove (new job confirmed before old removed)
- [ ] Update RetryJob handler with better error messages
- [ ] Test: retry when file locked, file deleted, file available
- [ ] Test: successful retry followed by encode completion
- [ ] Frontend: disable button, show spinner, specific error messages
- [ ] Verify logs show decision tree (why retry succeeded/failed)

**Files to modify:**
- internal/api/handler.go (RetryJob handler)
- internal/jobs/queue.go (CanRetry helper, transactional logic)
- web/templates/index.html (retry button UX)
- internal/logger/logger.go (enhanced logging)

---

### Issue 3: TVDB Matching (5 hours)

**Checklist:**
- [ ] Add yearPenalty() function to internal/intake/tvdb.go
- [ ] Add networkMatch() function
- [ ] Update confidence calculation to use new year penalty
- [ ] Update weighting coefficients (35, 35, 15, 15)
- [ ] Add deduplicateAndRank() for search result ordering
- [ ] Add detailed logging to show score breakdown
- [ ] Test The Office US/UK (should prefer US with 2005 in filename)
- [ ] Test other multi-version shows (Sherlock, etc.)
- [ ] Verify Review Queue entries still show all candidates

**Files to modify:**
- internal/intake/tvdb.go (year penalty, network match, ranking, logging)

---

### Issue 4: Windows Install (13 hours)

**Checklist:**
- [ ] Config path resolution (ALREADY DONE, verified working)
- [ ] API endpoints (new):
  - [ ] POST /api/queue/start → workerPool.Unpause()
  - [ ] POST /api/queue/pause → workerPool.Pause()
  - [ ] POST /api/queue/stop → workerPool.StopAll()
- [ ] Tray app updates:
  - [ ] Global mediaforgeProcess variable
  - [ ] launchMediaForge() with CREATE_NO_WINDOW flag
  - [ ] stopMediaForge() → kill process
  - [ ] reloadConfig() → stop + restart
  - [ ] First-run config modal (Fyne GUI)
  - [ ] Tray menu: 8 items (Dashboard, Start, Pause, Stop, Reload, Logs, Settings, Exit)
  - [ ] httpRequest() helper for API calls
- [ ] Installer (Inno Setup):
  - [ ] Copy binaries only (no config/logs)
  - [ ] Add tray to HKCU\Run
  - [ ] Pass --first-run flag
  - [ ] Remove wizard and browser launch
- [ ] Testing:
  - [ ] Fresh install, first-run modal
  - [ ] Config saves to %APPDATA%
  - [ ] Browser opens
  - [ ] Tray menu works
  - [ ] Reload Config works
  - [ ] Exit kills app
  - [ ] Reinstall finds existing config

**Files to modify:**
- internal/api/handler.go (3 new endpoints + router registration)
- cmd/tray/main_windows.go (process management, modal, menu)
- installer/mediaforge.iss (rewrite)

---

### Issue 5: Duplicate Resolution (11 hours)

**Checklist:**
- [ ] Add internal/intake/duplicate.go with CompareDuplicates()
- [ ] Codec tier mapping (HEVC > AV1 > VP9 > H.264)
- [ ] Resolution comparison (prefer higher pixels)
- [ ] Bitrate comparison (within 5% = tied)
- [ ] Integrate into file routing logic
- [ ] Add DuplicateInfo to Review Queue entry
- [ ] API handler for duplicate actions (Replace / Keep Existing)
- [ ] Add config options: duplicate_resolution mode, bitrate_tolerance, discard_folder
- [ ] Frontend: show duplicate panel for tied files
- [ ] Test all auto-resolve cases + tied cases

**Files to modify:**
- internal/intake/duplicate.go (new file)
- internal/intake/router.go or internal/jobs/worker.go (integration)
- internal/api/handler.go (duplicate action endpoint)
- internal/store/ or internal/db/ (add DuplicateInfo to Review Queue struct)
- internal/config/config.go (duplicate resolution options)
- web/templates/index.html (duplicate comparison panel)

---

## Session Output Files

All planning documents are in `/mnt/user-data/outputs/`:

1. **MEDIAFORGE_ISSUES_PLAN.md** — Issues 1-3 (SMTP, Retry, SmartShrink)
2. **DUPLICATE_COMPARISON_PLAN.md** — Issue 4 (Duplicate auto-resolution)
3. **REVIEW_QUEUE_UI_EVAL.md** — Issue 5 (Review Queue UI)
4. **WINDOWS_INSTALL_FIX.md** — Issue 6 detailed plan (Windows installation)
5. **WINDOWS_INSTALL_SUMMARY.md** — Issue 6 summary with code examples
6. **TVDB_MATCHING_FIX.md** — Bonus (TVDB matching for The Office)

---

## Next Steps

1. **Review this roadmap** — confirm priorities align with your goals
2. **Pick a starting point:**
   - Most urgent: Windows Install Fix (critical blocker)
   - Quick win: SMTP Notifications (4 hours, high impact)
   - High value: Retry Reliability (6 hours, fixes major bug)
3. **For each issue:** Detailed implementation plan is in corresponding document
4. **Start coding:** Plans are detailed enough to implement directly (pseudocode included in most)

---

## Questions for Next Session

1. Should we start with Windows Install Fix immediately, or gather more feedback on current v1.0?
2. Retry Reliability vs SMTP Notifications — which do you want first?
3. Should SmartShrink redesign wait for v1.2, or do you want it sooner?
4. Do you have early testers we should coordinate with for Windows installer testing?
5. Should we open an issues tracker to let early users report The Office / TVDB matching problems?

---

## References

- **Specification:** MEDIAFORGE_SPEC.md (v2.0, comprehensive design doc)
- **GitHub:** github.com/BRAYDIN72/MediaForge (develop branch)
- **Docker:** ghcr.io/braydin72/mediaforge:dev
- **Release:** v1.0.0 on 2026-06-24
