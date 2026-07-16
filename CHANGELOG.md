# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Review Queue "Re-encode Custom" resubmits that target a file already sitting
  in the library (e.g. re-encoding an existing AVC library file to HEVC) were
  incorrectly flagged as a duplicate at the destination, even though the
  encode was intentionally replacing that exact file. Root cause:
  `ffmpeg.FinalizeTranscode`, with `OriginalHandling: "replace"`, deletes the
  input file and writes the new encode to the same path when the input is
  already at its final library location — so `finalPath` and
  `job.LibraryPath` (`internal/jobs/worker.go` `processJob`) resolved to the
  identical file, and the post-encode "does the destination already exist"
  check was comparing the just-written file to itself, always finding a
  "duplicate" (and showing identical Incoming/Existing details in the Review
  Queue UI, since both probes hit the same path). Fix: new `samePath` helper
  in `internal/jobs/worker.go` (case-insensitive on Windows) short-circuits
  the duplicate check and move step when `finalPath` already equals
  `job.LibraryPath`, since the replace has already completed in place. Files
  modified: `internal/jobs/worker.go`, `internal/jobs/worker_test.go`.

- Encode-complete/failed Pushover and email notifications were sent once per
  connected SSE client (browser tab), not once per event — `JobStream`
  (`internal/api/sse.go`) runs its handler loop once per connection to
  `/api/jobs/stream`, and `dispatchEncodeEvent` was called directly inside
  that per-connection loop with no dedup, so a user with e.g. 3 open browser
  tabs got 3 identical notifications per encode-complete/failed event. Fix:
  `jobs.Queue` gained an `OnTerminalEvent` callback invoked exactly once per
  `broadcast()` call, regardless of subscriber count; `dispatchEncodeEvent`
  is now wired there (in `NewHandler`) instead of inside the per-connection
  SSE loop. Files modified: `internal/jobs/queue.go`,
  `internal/api/handler.go`, `internal/api/sse.go`,
  `internal/jobs/queue_test.go` (new regression test with 3 simulated
  subscribers asserting exactly 1 dispatch).

### Changed
- SmartShrink previously routed any file whose content never cleared the
  configured VMAF tier threshold (a hard quality ceiling, e.g. from film
  grain/video noise) straight to Review Queue via "no viable encode found",
  with no size information — even when a smaller file existed that cost no
  additional measurable quality. Root cause: `interpolatedSearchCRF`'s
  failure handling only ever narrows toward `qRange.Min` (larger files) once
  it decides quality is insufficient, so the best-effort CRF it lands on is
  biased toward near-lossless regardless of whether a higher CRF would have
  scored almost identically; the size-retry loop then compared every step
  against the absolute tier threshold, which content already known to be
  below that threshold can never pass. Now: when analysis returns a
  best-effort result (`AnalysisResult.BestEffort`), a new plateau-fallback
  probes a few CRF steps upward and accepts any step within 2.0 VMAF points
  of the original best-effort ceiling (`bestEffortFallbackTolerance` in
  `internal/ffmpeg/vmaf/search.go`), and the worker's size-retry loop
  (`internal/jobs/worker.go`) steps using that same tolerance instead of the
  absolute threshold. The job completes automatically with the
  closest-achievable-quality, smallest-available file instead of failing to
  Review Queue — this is a deliberate, explicit exception to the "every
  failure routes to Review Queue" rule for this one case, confirmed with the
  user, since a content quality ceiling isn't something a human decision can
  fix. Clearly logged as a fallback (`ceiling_vmaf`, `tier_threshold`, chosen
  CRF) so it's distinguishable from a normal threshold-met accept. The
  existing "still not smaller than source" safety net is unchanged and still
  routes to Review Queue. Files modified: `internal/ffmpeg/vmaf/search.go`,
  `internal/ffmpeg/vmaf/vmaf.go`, `internal/ffmpeg/vmaf/analyze.go`,
  `internal/jobs/worker.go`, `internal/ffmpeg/vmaf/search_test.go`.

### Fixed
- SmartShrink VMAF sampling used fixed positions (25%/50%/75% of duration)
  for every file. Real production logs showed the 50% sample scoring 15-30
  VMAF points above the other two on nearly every episode of the same show
  (e.g. `[75.8, 96.3, 74.9]`) — a recurring low-motion structural element
  (recap card/bumper, common in syndicated TV) at a consistent relative
  timestamp inflated the averaged score, which is very likely why the
  interpolated CRF search bottomed out near CRF 16 (near-lossless, huge
  first-try output) instead of a size-friendlier CRF that still meets the
  quality floor on the actual content. `SamplePositions` (`internal/ffmpeg/vmaf/sample.go`)
  now jitters each of the 3 sample positions within ±8% of its anchor,
  seeded deterministically per input file path (`fnv` hash + `math/rand`),
  so re-analyzing the same file is reproducible but different episodes of a
  series no longer all land on the same relative timestamp. Files modified:
  `internal/ffmpeg/vmaf/sample.go`, `internal/ffmpeg/vmaf/analyze.go`,
  `internal/jobs/worker.go`, `internal/ffmpeg/vmaf/sample_test.go`.

- LLM verification pass (triggered when TVDB/TMDB confidence lands between
  `review_threshold` and `confidence_threshold`) was silent in the logs on
  success — `LLMClient.Verify()` never logged, so there was no way to tell
  from the log file whether the LLM was queried at all, or what it returned,
  when a low-confidence match was accepted into the library anyway (its
  confidence silently overwrote the deterministic score). Fix: `watcher.go`'s
  `resolveAndGate()` now logs `Intake: querying LLM for verification` before
  the call and `Intake: LLM verification result` (candidate_id, confidence,
  reasoning) after it, plus a log line for the previously-silent
  `llmResult.Disabled` (LLM not configured) branch. Files modified:
  `internal/intake/watcher.go`.

## [1.2.3] - 2026-07-15

### Fixed
- Review Queue manual search treated queries containing a season/episode
  pattern (e.g. "MST3K - S04E23 - Bride of the Monster") as a movie lookup,
  because `reviewDoSearch()` (`web/templates/index.html`) never extracted
  season/episode from the query string, even though the backend's
  `SearchReviewEntry` handler (`internal/api/handler.go`) already supports
  `season`/`episode` query params and sets `IsTV = true` when they're
  present. Fix: `reviewDoSearch()` now matches the query against
  `[Ss](\d{1,2})[Ee](\d{1,2})`; on a match it appends `season`/`episode`
  params and forces `type=tv` (overriding the user-selected type dropdown).
  Files modified: `web/templates/index.html`.
- Second, deeper bug in the same feature (found via real log after the fix
  above): even with `season`/`episode`/`type=tv` correctly sent,
  `SearchReviewEntry` (`internal/api/handler.go`) used the raw `q` string
  verbatim as the TMDB/TVDB search title — e.g. it searched TMDB/TVDB for
  the literal string "Mystery Science Theater 3000 - S04E23 - Bride of the
  Monster" instead of just "Mystery Science Theater 3000", so no candidates
  were ever found. The normal intake pipeline avoids this because it always
  calls `intake.ParseFilename()` first to split title from season/episode;
  the manual-search handler never did. Fix: `SearchReviewEntry` now runs
  `q` through `intake.ParseFilename()` and uses its extracted `Title` (and
  `Year`/`Season`/`Episode`/`IsTV` as fallbacks when the corresponding query
  param wasn't supplied) instead of `q` itself. Files modified:
  `internal/api/handler.go`.
- Third bug in the same feature: after the fixes above, manual search found
  the correct TV episode candidate, but clicking "Pick Selected" failed with
  "could not build library path ... (missing title/season?)". Root cause:
  the candidate JSON built in `SearchReviewEntry` never included the
  resolved `season`/`episode` (even though `intake.LookupResult` already
  carries them), so `ResolveReviewEntry`'s overlay had nothing to use except
  whatever `intake.ParseFilename(entry.Filename)` could extract from the
  Review Queue entry's *original* stored filename — which is frequently
  unparseable (that's often why the entry needed manual search in the first
  place), yielding `Season == 0` and tripping `resolveLibraryPath`'s TV
  guard. Fix: `SearchReviewEntry`'s candidate JSON now includes `season` and
  `episode` from the lookup result, so the picked candidate itself supplies
  them regardless of the original filename. Files modified:
  `internal/api/handler.go`.

## [1.2.2] - 2026-07-08

### Fixed
- Review Queue duplicate-conflict entries lost metadata corrections found
  during intake (e.g. TVDB season/episode reconciliation, title/year
  correction) — the stored `ReviewEntry.Filename` was always the raw,
  uncorrected source basename, even when `resolveAndGate` had already
  identified the correct metadata before the duplicate check ran. Since
  `ResolveReviewEntry`/`ResubmitReviewEntry` re-derive metadata by
  re-parsing `entry.Filename`, a later "Re-encode Custom" or manual
  resolve would reproduce the original wrong title/year/season/episode.
  Fix: `sendToReviewQueue`/`sendDuplicateToReviewQueue`
  (`internal/intake/watcher.go`) now accept a `correctedFilename` built
  from the already-identified metadata (`buildCorrectedFilename`,
  `internal/intake/naming.go`) once `resolveAndGate` succeeds, so the
  stored filename carries the correction. The post-encode duplicate path
  (`internal/jobs/worker.go` `processJob`) now derives the review-entry
  filename from `job.LibraryPath` (already metadata-corrected) instead of
  `filepath.Base(finalPath)` (the raw transcoded file's name, unchanged by
  encoding). No new staging folder or physical file move — the file stays
  at its existing location; only the filename string used for
  identification/re-parsing changed.
  - Files modified: internal/intake/watcher.go, internal/intake/naming.go,
    internal/intake/watcher_test.go, internal/jobs/worker.go
- Two remaining gaps in the fix above, found from a real run (Revolution
  S02E12: first landed as "Revolution (2013) - S02E12 -.mp4" — wrong year,
  missing episode title):
  1. `internal/jobs/worker.go` SmartShrink "no viable encode" → Review Queue
     path (~line 926) was a call site the first fix missed — still used
     `filepath.Base(job.InputPath)` (raw, uncorrected name) instead of
     `job.LibraryPath` (already metadata-corrected during intake). This is
     why the wrong year (2013 instead of TVDB's 2012) survived into the
     Review Queue entry.
  2. `internal/api/handler.go` `ResubmitReviewEntry` (and defensively
     `ResolveReviewEntry`) called `intake.ParseFilename(entry.Filename)`,
     which only ever populates `ParsedEpisodeTitle` (the raw text parsed
     from the filename) — never `EpisodeTitle`, the field naming templates
     actually read. Every "Re-encode Custom" resubmit for a TV show was
     silently dropping the episode title from the rebuilt library path,
     independent of whether the entry's filename was corrected. Both
     handlers now fall back to `ParsedEpisodeTitle` when `EpisodeTitle` is
     empty.
  - Files modified: internal/jobs/worker.go, internal/api/handler.go

## [1.2.1] - 2026-07-08

### Fixed
- Review Queue "Re-encode Custom" (`ResubmitReviewEntry`) completed encodes
  successfully but never moved the output into the library — the file was
  left behind in the staging/transcode folder. Root cause: the worker's
  post-encode library-move step (`internal/jobs/worker.go`) is gated on
  `Job.LibraryPath`, which is only ever set by the file-watcher intake path
  (`internal/intake/watcher.go`); the Review Queue resubmit path re-enqueued
  jobs via `AddMultiple` but never set `LibraryPath` on them. Fix rebuilds
  the intended library path from the review entry's filename (same approach
  as `ResolveReviewEntry`) and calls `queue.SetLibraryPath` on the resubmitted
  job(s).
  - Files modified: internal/api/handler.go (ResubmitReviewEntry)
- Colon character in show/movie titles (e.g. `9-1-1: Nashville`) was being
  replaced with an underscore (`9-1-1_ Nashville`) when building library
  folder/file paths, instead of the intended space-dash separator. Colons
  are now replaced with " - " while other filesystem-illegal characters
  (`/ \ * ? " < >  |`) still map to `_`.
  - Files modified: internal/intake/naming.go, internal/intake/naming_test.go

## [1.2.0] - 2026-07-07

### Added
- Logs viewer tab in the web UI (`GET /api/logs?file=current|1|2&lines=N`):
  lets a device on the LAN view the app's rotated log files remotely instead
  of requiring local filesystem access to `%APPDATA%\Mediaforge\logs`. Includes
  a file picker (current/previous/2-sessions-ago), a line-count selector
  (50/200/500/1000, tail-based), and an optional 5s auto-refresh.
  - Files modified: internal/api/handler.go, internal/api/router.go,
    web/templates/index.html
- Custom Encode CRF override for Compress presets (compress-hevc/compress-av1):
  a per-job CRF field in both the main file browser UI and the Review Queue's
  "Re-encode Custom" form, sent as `encode_quality_crf` to `/api/jobs` and
  `/api/review/{id}/resubmit`. Falls back to the global quality_hevc/quality_av1
  setting when left blank; validated to the same ranges as the Settings sliders
  (16-30 HEVC, 18-35 AV1) and rejected for SmartShrink presets.
  - Files modified: internal/jobs/job.go, internal/jobs/queue.go,
    internal/jobs/worker.go, internal/api/handler.go, web/templates/index.html

### Changed
- App name in the header now renders in Cinzel; rest of the UI is unchanged
  (DM Sans). SmartShrink quality dropdown (Acceptable/Good/Excellent) is
  narrower (160px min-width) to match its shorter option labels.
  - Files modified: web/templates/index.html

### Fixed
- Review Queue resubmit ("Re-encode Custom") silently failed for entries whose
  original path was in the staging/transcode working directory (e.g. jobs that
  failed during SmartShrink encoding), instead of the media library — clicking
  resubmit bounced the entry back to `pending` with no encode ever enqueued.
  Root cause: `ResubmitReviewEntry` probed the file via
  `browser.GetVideoFilesWithProgress`, which is scoped to the configured media
  browse root and silently drops any path outside it (returns 0 results, nil
  error). Fixed by probing via `browser.ProbeFile` (direct ffprobe call, no
  root restriction) instead, matching the pattern already used by `RetryJob`.
  - Files modified: internal/api/handler.go
- Additional mobile responsiveness fixes below 768px, continuing the prior
  pass: `.header` now wraps (`flex-wrap`, `row-gap`) instead of clipping when
  its contents don't fit one row; `.view-nav` moves to its own row
  (`order: 3`, full width) with horizontal scroll instead of squeezing the
  tab labels; `.review-bulk-bar` (480px block) now wraps instead of
  overflowing.
  - Files modified: web/templates/index.html
- Mobile responsiveness gaps below 768px: the Review Queue duplicate-file
  compare (`.review-dup-compare`) stayed side-by-side and got cramped on
  phone-width screens; buttons and the review-card checkbox were under the
  ~44px touch-target guideline. Added a new `@media (max-width: 480px)`
  block that stacks the duplicate compare into a column and bumps `.btn`
  min-height to 44px and `.review-card-check` to 20x20px (`.btn-sm` left
  untouched — those are deliberately compact secondary actions).
  - Files modified: web/templates/index.html
- Review Queue "Re-encode Custom" silently corrupted Windows/UNC file paths
  (every backslash except the first was dropped) because the original path
  was interpolated raw into an inline `onclick="..."` attribute, which the
  browser then parsed as a JS string literal — lone backslashes were consumed
  as invalid escape sequences. Fixed by passing the path via a `data-*`
  attribute instead. Also: a failed resubmit (bad path, missing file) used to
  mark the entry `resolved` and silently drop it from the queue with no
  encode ever created; it now reverts the entry to `pending` with an
  actionable reason instead of disappearing silently.
  - Files modified: internal/api/handler.go, web/templates/index.html
- App-reported version was hardcoded to `1.0.0` in `internal/version/version.go`
  and never bumped for tagged releases, so a binary built from the `v1.1.0` tag
  still logged `version=v1.0.0+build.N`. `Version` is now injected via
  `-ldflags` (same mechanism already used for `Build`), derived from the git
  tag in CI (`.github/workflows/release.yml`), the Docker build
  (`Dockerfile`, new `APP_VERSION` build-arg), and local builds
  (`build.ps1`, via `git describe --tags`). The hardcoded constant remains
  only as a fallback for non-tagged/dev builds.
  - Files modified: internal/version/version.go, build.ps1, Dockerfile,
    .github/workflows/release.yml

## V1.1.0

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

## V1.0.2

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
  
## V1.0.1

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

[Unreleased]: https://github.com/braydin72/MediaForge/compare/v1.2.0...develop
[1.2.0]: https://github.com/braydin72/MediaForge/compare/v1.1.0...v1.2.0
[1.0.0]: https://github.com/braydin72/MediaForge/releases/tag/v1.0.0







