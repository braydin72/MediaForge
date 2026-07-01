# MediaForge — Comprehensive Status & Roadmap

**Release Date:** v1.0.0 on 2026-06-24  
**Current Session Date:** 2026-07-01

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

## Suggested Implementation Roadmap

### Immediate (Next 1-2 sprints)

**Sprint 1: Critical Path (19 hours)**
- Windows Install Fix (13h)
- SMTP Notifications (4h)
- TVDB Matching (5h) — can run in parallel with install testing

**Result:** v1.0.1 release with:
- ✅ Working Windows installer and tray app
- ✅ SMTP email notifications + test button
- ✅ Better show identification (fixes The Office, etc.)
- ✅ Stable foundation for next work

### Near-term (Sprint 2-3)

**Sprint 2: Reliability & UX (17 hours)**
- Retry Reliability Fix (6h)
- Smart Duplicate Resolution (11h)

**Result:** v1.1.0 release with:
- ✅ Trustworthy retry feature (no more missing videos)
- ✅ Cleaner Review Queue (auto-resolved duplicates)
- ✅ Better user confidence in automation

### Later (Sprint 3+)

**Sprint 3: Polish (23 hours)**
- SmartShrink Redesign (12h)
- Review Queue UI Rework (11h)

**Result:** v1.2.0 release with:
- ✅ Faster, smarter encode queue
- ✅ Polished UI for Review Queue triage

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
