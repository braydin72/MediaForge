# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Review Queue pagination with a "per page" selector (default 10, options
  10/25/50/100) and Prev/Next controls; cards no longer shrink to fit, so a long
  queue scrolls instead of squishing (Issue #12).
- "Re-encode Custom" action on Review Queue entries: pick Standard HEVC or
  SmartShrink (with quality tier), plus encoder speed and output container, then
  resubmit to the encode queue. /api/review/{id}/resubmit now accepts
  encode_speed, encode_output_format, and smartshrink_quality (Issue #4).
- "Pause/Resume Encode Queue" toggle with a Running/Paused state badge in the
  queue panel header. The badge is driven by the authoritative worker-pool state,
  now exposed as `paused` in GET /api/stats (Issue #13).
  - Files modified: web/templates/index.html, internal/api/handler.go

### Added
- TVDB episode-title reconciliation now tries the immediate season neighbors
  (season+1, season-1, same episode number) before falling back to the
  whole-series title scan. Confirmed real-world cause: a source numbers an
  entire show's seasons one off from TVDB across the board (e.g. filename
  S48E30 is actually TVDB's S49E30) — the direct neighbor check catches this in
  at most 2 extra requests instead of depending on the paginated scan, which
  can miss on long-running shows or when the runner-up title is close enough
  to trip the "unambiguous" guard.
  - Files modified: internal/intake/tvdb.go (new findAdjacentSeasonEpisode),
    internal/intake/tvdb_test.go (new TestTVDBLookup_ReconcileAdjacentSeasonOffset)
- TVDB episode-title reconciliation (findEpisodeByTitle) now logs its outcome at
  debug level even when it does NOT find a confident match — the best candidate
  name/similarity, the runner-up similarity, and how many pages/episodes were
  scanned. Previously a failed reconciliation was silent (only the success path
  logged), so there was no way to tell from the log whether reconciliation ran
  and found nothing usable, or didn't run at all.
  - Files modified: internal/intake/tvdb.go

### Fixed
- Removed the Review Queue "Retry" and "Re-add" actions: Retry only marked the
  entry resolved without re-running identification or re-enqueuing anything
  (a no-op for non-post-encode entries — it just removed the card), and Re-add
  duplicated the same /api/review/{id}/resubmit call that "Re-encode Custom"
  already covers, but without its error feedback. "Re-encode Custom" is now the
  single re-processing action on a Review Queue entry.
  - Files modified: web/templates/index.html (removed reviewRetry,
    reviewRetryAll, reviewResubmit, their buttons, and the "Retry All" bulk
    action), internal/api/handler.go (removed RetryReviewEntry),
    internal/api/router.go (removed PUT /api/review/{id}/retry)
- `normTitle` (internal/intake/tmdb.go, shared by TMDB and — as of this same
  session — TVDB series scoring) deleted punctuation instead of treating it as a
  word boundary, so "20/20" collapsed to "2020" while a filename-derived title
  like "20 20" stayed two tokens — they never compared equal, silently breaking
  every exact/Contains-based title match on punctuated names. Now punctuation
  becomes a space (then whitespace is collapsed), so "20/20" normalizes to
  "20 20" and correctly matches. This was the real reason the TVDB
  selectBestSeries fix earlier in this session didn't actually resolve the
  "20/20" mis-identification in production: the name-normalization it relied on
  was itself broken.
  - Files modified: internal/intake/tmdb.go, internal/intake/tvdb_test.go (added
    TestNormTitle_PunctuationIsWordBoundary; strengthened
    TestSelectBestSeries_PunctuationNormalizedForNameMatch with a decoy that has
    a plausible, non-garbage year — the earlier test passed on a coincidental
    tie-break and didn't actually exercise the name-match bonus)
- TVDB series candidate selection (selectBestSeries) now normalizes punctuation
  before comparing names, e.g. "20 20" (parsed from a filename) vs "20/20" (TVDB's
  listing) — previously a raw case-only comparison missed this as an exact match,
  so the real show scored no better than unrelated decoys. Combined with the
  episode-mismatch penalty (-0.30, applied when a show's season/episode exists but
  the name disagrees) being harsher than the fetch-failure penalty (-0.20, applied
  when a show doesn't have that season at all), an unrelated decoy with a garbage/
  mis-parsed year outscored the real "20/20" and won selection — so the later
  episode-title reconciliation ran against the wrong series and always failed,
  falling back to TMDB with no reconciliation and the wrong episode ("Her Last
  Call" filed as "I Have Killed For You"). Also added a sanity floor (year >= 1900)
  on the premiere-year bonus so implausible years can no longer earn it for free.
  - Files modified: internal/intake/tvdb.go, internal/intake/tvdb_test.go
- Review Queue "Keep Existing" (discard a duplicate-conflict entry) now deletes the
  incoming file from disk. Previously DiscardReviewEntry only flipped the entry to
  "discarded" and left the losing file sitting in the intake/staging directory
  forever (a silent-failure bug — the file didn't reappear in the queue, but it
  never got cleaned up either). Deletion is scoped to entries with duplicate info
  (the incoming path from the stored DuplicateContext); a missing file is treated
  as success, and non-duplicate discards are unaffected.
  - Files modified: internal/api/handler.go, internal/api/handler_test.go
- AVC intake now applies confidence gating identical to the HEVC path. Previously
  `stageAndEnqueue` logged the match confidence but never acted on it, so a
  low-confidence match (e.g. a 0.43 episode-title mismatch) was silently staged and
  added to the encode queue under the wrong metadata instead of routing to the Review
  Queue — a silent-failure bug. Metadata lookup and gating now run on the source file
  BEFORE staging: below the review threshold → Review Queue; in the LLM gray zone →
  LLM verification then Review; only an accepted match is staged and enqueued. The
  gate is now a shared `resolveAndGate` helper used by both codec paths so they stay
  in lockstep.
- TVDB lookup now reconciles a wrong season/episode by episode name. When the
  filename carries an episode title but the episode at the given S/E is missing or its
  name disagrees (e.g. a streaming service that numbers seasons differently), the
  whole series is searched for that episode name and, on a confident unambiguous hit,
  the season/episode is corrected — so "S48E30 - Her Last Call" is filed at the real
  S45E12 rather than under the wrong S48E30 title. If the name is found nowhere, the
  low confidence routes the file to the Review Queue.
  - Files modified: internal/intake/tvdb.go, internal/intake/orchestrator.go,
    internal/intake/watcher.go, internal/intake/tvdb_test.go,
    internal/intake/watcher_test.go
- Review Queue "Pick Selected" (manual match) now actually files the movie.
  Previously ResolveReviewEntry ignored the picked candidate and only flipped the
  entry to "resolved" — the file was never moved into the library and was left
  stranded at its intake path (a silent-failure bug). Resolve now builds the
  library destination from the chosen candidate and moves the file there, marking
  the entry resolved only on a successful move; it refuses to overwrite an
  existing destination file and leaves the entry pending with an updated reason on
  any failure (source missing, duplicate at destination, move error). The web UI
  retains the selected candidate object (auto and manual-search results) so the
  full metadata is sent, and surfaces resolve errors instead of dropping the card.
  - Files modified: internal/api/handler.go, internal/intake/naming.go
    (new exported ResolveLibraryPath), web/templates/index.html,
    internal/api/handler_test.go
- TVDB series misidentification for same-named shows (e.g. "The Office (2005)
  S01E01 Pilot" resolving to the 2001 UK series). selectBestSeries now fetches
  the target episode for each candidate and compares its TVDB episode title to
  the filename's episode title before scoring: a close match strongly boosts the
  candidate, a mismatch (or missing episode) penalizes it, making episode-name
  agreement the primary differentiator when show names are similar. Added debug
  logging across the TVDB and TMDB identification paths (search counts, per-
  candidate scores, episode-name comparisons, best pick).
  - Files modified: internal/intake/tvdb.go, internal/intake/tmdb.go,
    internal/intake/tvdb_test.go
- Setup/installer UX polish (#15, #16, #17, #18): first-run wizard now shows a
  UNC-path note under Intake Paths (mapped drives unsupported as a service), an
  SSD recommendation under the Staging Folder, and a note that API keys are
  optional (missing keys route files to the Review Queue). The uninstaller now
  prompts to remove application data (%APPDATA%\MediaForge) instead of always
  preserving it.
  Files modified: cmd/tray/setup_wizard.ps1, installer/mediaforge.iss

- Startup logging (#10): mediaforge.exe now writes a version/build banner
  ("MediaForge v{VERSION}+build.{BUILD} starting") as the first log line, and a
  human-readable summary of detected hardware encoders (NVENC, VAAPI, Quick
  Sync, VideoToolbox, CPU fallback) after encoder detection. Both go through a
  new logger.Banner() that bypasses the level filter, so they appear in the log
  file at every log level (including warn/error) and in headless (tray) runs.
  Files modified: cmd/mediaforge/main.go, internal/logger/logger.go

### Added
- Windows system tray application (tray launcher model) with process lifecycle management
- First-run setup wizard via native Windows Forms dialog with path validation and UNC conversion
- Full tray menu with pipeline control, queue management, config reload, and log viewing
- Build number injection into Windows executables (tracked via `internal/version`)
- Queue control API endpoints: `POST /api/queue/{start,pause,stop}` for tray integration
- Pause Queue checkbox in tray menu (wired to queue API)
- Windows Inno Setup installer with HKCU autostart registry entry
- Config path resolution: Windows uses `%APPDATA%\MediaForge\`, Unix uses `./config`
- UNC path support for network shares in media browser
- Media browser root path setting in Settings panel with live update
- Recursive subfolder watching in intake pipeline
- Extended codec routing: mpeg2video, mpeg4, vc1 queue for encode; vp9, av1 pass through as HEVC-equivalent
- DVD subtitle passthrough to MKV output (dropped with warning when output is MP4)
- Post-encode move retry logic without re-detecting codec
- Session file logging to `mediaforge.log` with rotation (2 backups)
- Windows service support via `--install`, `--uninstall`, `--service` flags
- Duplicate file handling: always routes to Review Queue with codec/resolution/bitrate comparison
- Duplicate-aware Review Queue actions: Replace, Keep Existing
- Full LLM verification pipeline (Anthropic, OpenAI, Ollama backends)
- TVDB API v4 client with token caching and episode detail lookup
- TMDB movie and TV search with poster URLs
- OMDb last-resort fallback for movies and TV shows
- Confidence scoring with runtime cross-check
- SmartShrink mode: VMAF-driven CRF iteration with 60% reduction target
- Fixed Reduction mode: CRF adjusted to hit user-specified size target
- NVIDIA NVENC, AMD VAAPI, Intel VAAPI, and CPU fallback encoding support
- Encoder speed presets (Slowest to Fast)
- Container format selection (mkv, mp4, preserve)
- Stability check: configurable interval and passes required before processing
- Review Queue with per-entry actions: retry, discard, pick candidate, manual search, re-add with custom settings
- Dashboard with active queue, progress bars, storage savings gauge, stats bar
- Manual add UI with folder picker and per-job encode settings override
- Settings panel with all configuration options
- SMTP email notifications (Gmail with App Password, self-hosted SMTP) with per-event toggles and batched digest
- Stats tracking per-file and aggregated (lifetime / period reset points)

### Changed
- Windows architecture: tray launcher model replaces service-based architecture
- Config file location on Windows: now uses `%APPDATA%\MediaForge\mediaforge.yaml` (cross-version compatible)
- TVDB confidence scoring: rewritten as 4-component weighted formula (series name, premiere year, episode record, network)
- TMDB movie confidence scoring: year removed as API filter; now used only for post-search validation (±0/±1 year tiers)
- Default output format changed from mkv to preserve (source container maintained)
- Default stability passes increased to 6 (from 3)
- Pause pipeline: now a checkbox in tray menu; semantics split into Pause (don't stop in-progress jobs) vs Stop (cancel all)
- Web UI pipeline pause slider moved to top of queue panel header
- Review Queue resized entries based on content (compact cards, scrollable)

### Fixed
- Tray "View Logs" and "Config" flashed a console window and opened nothing: the tray is a `-H windowsgui` process with no console, so `cmd /c start` spawned a throwaway `cmd` that died before handing off to the file's default handler. Both now launch the file directly in `notepad.exe` (no console dependency) (`cmd/tray/main.go`)
- Installer no longer offered a choice of install location: switching to a per-user install (`PrivilegesRequired=lowest`) made Inno Setup's `DisableDirPage=auto` suppress the "Select Destination Location" page for the `{autopf}` default. Added `DisableDirPage=no` to restore the page while keeping the per-user, no-elevation model (`installer/mediaforge.iss`)
- Tray errors were invisible: the tray is linked with `-H windowsgui` (no console) so all `fmt.Fprintf(os.Stderr, ...)` diagnostics were discarded. Tray diagnostics now go to `%APPDATA%\MediaForge\logs\tray.log` via the `log` package; `init()` creates the log dir first and keeps default output if the file can't be opened (avoids a nil-writer panic from `log.SetOutput(nil)`) (`cmd/tray/main.go`)
- Directory count cache warm timed out on large SMB shares (fixed 30s cap): the timeout is now configurable via `intake.cache_timeout_seconds` (default 60) and `WarmCountCache` early-exits with a warning if the media root is unreachable instead of burning the whole window (`internal/browse/browse.go`, `internal/config/config.go`, `internal/api/handler.go`, `web/templates/index.html`, `cmd/mediaforge/main.go`, `internal/winsvc/service.go`)
- Tray Exit left `mediaforge.exe` running as an orphan: `killMediaForge()` now polls up to 5s, re-issuing `taskkill /F` and confirming the process is gone before returning (`cmd/tray/main.go`)
- Tray executable launched with a visible console window: tray is now linked with `-H windowsgui` (GUI subsystem, no console) (`build.ps1`, `.github/workflows/release.yml`)
- Tray "View Logs" crashed with "exit status 1" when no log file existed: now creates the log directory and shows a dialog with the full `%APPDATA%\MediaForge\logs\mediaforge.log` path instead of failing silently (`cmd/tray/main.go`)
- Left-click on the tray icon did nothing: switched from `getlantern/systray` to `fyne.io/systray` and wired `SetOnTapped` so left-click opens the dashboard; right-click still opens the menu (`cmd/tray/main.go`, `go.mod`, `go.sum`)
- Installer deprecated `x64` architecture id and admin/HKCU autostart hive mismatch: `mediaforge.iss` now uses `x64compatible` and a per-user install (`PrivilegesRequired=lowest`) so the install dir and autostart Run key share the same user hive (`installer/mediaforge.iss`)
- PowerShell UTF-8 BOM handling: `setup_wizard.ps1` now writes UTF-8 without BOM; Go-side strips any leading BOM before JSON unmarshal
- Tray app console window visibility: added `CREATE_NO_WINDOW` flag on mediaforge.exe launch
- Nil pointer panic in `WarmCountCache` when SMB shares become unavailable mid-walk
- Browse API path validation for mapped network drives and UNC paths
- Windows path separator normalization throughout codebase (forward slashes in breadcrumbs/API responses, backslashes in OS operations)
- TVDB multi-show disambiguation: now filters by premiere year (±1 match) before episode lookup
- TVDB year scoring: changed from exact-year requirement to premiere_year <= filename_year
- Ollama request timeout increased to 120s (from default) to accommodate cold-start model load
- Context-canceled errors in ffprobe during manual Full Pipeline runs
- Full Pipeline mode now works without `intake.enabled`
- Cross-device move handling: `os.Rename` EXDEV errors caught and retried with copy+rename
- Subtitle conversion: best-effort encoding (drop problematic streams with warning)
- Punctuation normalization in string similarity scoring (removes accents, special chars before comparison)
- Scene-tag stripping: handles trailing year directly attached to title with no space (2001: A Space Odyssey)
- Intake watcher: added exclusive-open check to stability detection on Windows
- Encoder detection results now logged for debugging

### Removed
- Windows service model (replaced by tray launcher)
- Browser wizard from main app (tray handles first-run configuration on Windows; browser wizard retained for Docker)
- Unused browsing helper function stubs

## [1.0.0] - Initial Release (forked from Shrinkray)
  Initial release of MediaForge, forked from Shrinkray (gwlsn/shrinkray)
  *See SHRINKRAY_CHANGELOG.md for pre-fork history*
### Added
- MediaForge forked from shrinkray repository
- Complete media ingest pipeline: watch, identify, transcode, organize
- Codec detection via ffprobe (HEVC, H.264, and fallback to Review Queue)
- Filename parsing: title/year extraction, SxxExx TV detection, multi-episode support
- File stability check before processing (prevent acting on partially-written files)
- TVDB integration for TV show metadata
- TMDB integration for movie metadata and TV fallback
- OMDb integration for last-resort metadata lookup
- Confidence scoring system with configurable thresholds
- LLM verification for ambiguous matches (Anthropic, OpenAI, Ollama support)
- Review Queue for all failures and low-confidence results
- HEVC direct library move (Plex/Jellyfin-compatible naming)
- AVC staging and encode queue
- SmartShrink quality-driven encoding with VMAF measurement
- Fixed Reduction size-driven encoding
- Hardware accelerated transcoding (NVIDIA NVENC, AMD VAAPI, Intel VAAPI)
- Cross-device move handling (EXDEV fallback to copy+rename)
- Dashboard with active queue, progress tracking, and storage savings
- Settings UI for all configuration options
- Review Queue UI with candidate selection, manual search, retry, discard
- Duplicate detection and comparison
- SMTP email notifications
- Per-file statistics and aggregate tracking
- Docker support with multi-stage build
- GitHub Actions CI/CD pipeline

[Unreleased]: https://github.com/braydin72/MediaForge/compare/v1.0.0...develop
[1.0.0]: https://github.com/braydin72/MediaForge/releases/tag/v1.0.0







