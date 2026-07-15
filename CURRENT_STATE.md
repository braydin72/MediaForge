CURRENT STATE NOTE

=== Latest session: LLM verification pass had no logging ===

User was debugging why a low-confidence TVDB match (e.g. "MST3K (1989) -
S13E12 - The Bubble.mp4", confidence=0.60) moved straight to the library
instead of Review Queue, and initially misread this as the LLM-verification
gate (review_threshold..confidence_threshold gray zone, defaults 0.60/0.85)
being skipped. Root cause: it wasn't skipped — `LLMClient.Verify()`
(internal/intake/llm.go) never logged anything on success, and the
`llmResult.Disabled` branch in `resolveAndGate()` (internal/intake/watcher.go)
also logged nothing, so there was no way to tell from the log file whether
the LLM was queried, or what it returned, when a low-confidence match got
silently accepted (LLM confidence overwrites the deterministic score at
`result.Confidence = llmResult.Confidence`).

Fix (internal/intake/watcher.go, resolveAndGate only): added
`logger.Info("Intake: querying LLM for verification", ...)` immediately
before the `Verify()` call, `logger.Info("Intake: LLM verification result",
...)` (candidate_id, confidence, reasoning) immediately after a successful
call, and a `logger.Warn` for the previously-silent `llmResult.Disabled`
(LLM not configured) case. No behavior change — logging only.

Files modified: internal/intake/watcher.go, CHANGELOG.md.

Verified: go build ./... and go test ./internal/intake/... pass. User
re-tested against a live instance by renaming a file into the gray zone
(confidence 0.60) and confirmed the new log lines appear correctly:
"Intake: querying LLM for verification" followed by "Intake: LLM
verification result" with candidate_id/confidence/reasoning, then the file
moved to the library as expected.

Separately identified but NOT fixed this session: `computeTVDBConfidence`
(internal/intake/tvdb.go) scores show-name match as 0 for acronym filenames
like "MST3K" against the full TVDB series name "Mystery Science Theater
3000" — neither the Levenshtein-similarity fuzzy check nor the substring
-containment fallback recognize the acronym as equivalent, costing 40% of
the confidence score on every MST3K file regardless of episode-title/year
match. This is why MST3K episodes repeatedly land in the LLM gray zone or
Review Queue rather than the 0.90+ exact-name-match shortcut. User has not
yet asked for this to be fixed — flagged for a future session if they want
an alias/acronym table added to the name-matching logic.

=== Prior session: manual search in Review Queue didn't extract season/episode for TV queries ===

Symptom: searching "MST3K - S04E23 - Bride of the Monster" in a Review Queue
entry's manual search dialog returned a movie-search error ("no TMDB movie
found for..."). Root cause: `reviewDoSearch()` (web/templates/index.html)
sent the raw query string as `q` plus `year`/`type` but never parsed out a
season/episode pattern, so the backend's `SearchReviewEntry`
(internal/api/handler.go) defaulted to a movie lookup — that handler already
supports `season`/`episode` params and sets `IsTV = true` when present, this
was purely a frontend gap.

Fix (web/templates/index.html, reviewDoSearch() only): matches the query
against `/[Ss](\d{1,2})[Ee](\d{1,2})/`; on match appends `season`/`episode`
params (parsed as ints) and forces `type = 'tv'`, overriding whatever the
type dropdown had selected. Full query string is left intact — backend
handles it as before.

Files modified: web/templates/index.html, CHANGELOG.md.

User re-tested against a live deployed instance and reported it still
failed, with a real debug log: `TMDB: TV search query="Mystery Science
Theater 3000 - S04E23 - Bride of the Monster"` /
`TMDB: no TV candidates returned` — same full string. Traced the second,
deeper bug: `SearchReviewEntry` (internal/api/handler.go) used the raw `q`
query param VERBATIM as `parsed.Title` for the TMDB/TVDB search — it never
parsed the query at all, so `season`/`episode`/`type=tv` being sent
correctly (part 1's fix) didn't matter; the search title itself still
contained the whole "Title - SxxExx - Episode Title" string, which no
TMDB/TVDB search matches. The user separately confirmed TVDB lookup itself
works correctly for this exact show/episode — renaming the file and letting
it re-enter the normal intake pipeline correctly identified and moved it
via TVDB (S04E23 "Bride of the Monster", confidence=0.90) — because that
path calls `intake.ParseFilename()` on the filename first, which correctly
splits "Mystery Science Theater 3000" (title) from "S04E23" and "Bride of
the Monster" (episode title). `SearchReviewEntry` never did this.

Fix (internal/api/handler.go, SearchReviewEntry): now runs `q` through
`intake.ParseFilename(q)` and uses the resulting `Title` as the search
title (falling back to raw `q` only if parsing yields an empty title).
`Year`/`Season`/`Episode`/`IsTV` from the parse are used as fallbacks only
when the corresponding query param wasn't already supplied by the frontend
(explicit params from the form still win). No frontend change needed for
this part — `reviewDoSearch()` already sends the full query as `q`, per the
original task's explicit instruction to keep the query string intact and
let the backend handle it; that assumption just wasn't true until this fix.

Files modified: internal/api/handler.go, CHANGELOG.md.

User rebuilt/redeployed and confirmed this part worked — search now returns
a real candidate. But clicking "Pick Selected" then failed: "Resolve
failed: could not build library path for "Mystery Science Theater 3000"
(missing title/season?)". Third bug in this same feature, found by tracing
ResolveReviewEntry (internal/api/handler.go): it seeds `parsed` from
`intake.ParseFilename(entry.Filename)` (the Review Queue entry's ORIGINAL
stored filename, not the search query), then overlays only the fields the
picked candidate JSON supplies. `SearchReviewEntry`'s candidate JSON never
included `season`/`episode` (checked: `intake.LookupResult` already carries
both, populated by TVDB/TMDB — the JSON builder in SearchReviewEntry just
never surfaced them). So the candidate overlay had nothing to give
`parsed.Season`, leaving whatever `ParseFilename(entry.Filename)` produced
from the entry's original filename — which, for entries that needed manual
search in the first place, is often exactly the case where the original
filename ISN'T cleanly parseable, so Season came out 0.
`resolveLibraryPath` (internal/intake/naming.go) refuses to build a TV path
when `parsed.Season == 0`, which is the exact error string the user saw.

Fix (internal/api/handler.go, SearchReviewEntry): candidate JSON now
includes `"season": result.Season, "episode": result.Episode`. No other
handler needed changes — `ResolveReviewEntry`'s existing overlay logic
already does `if req.Candidate.Season > 0 { parsed.Season = ... }` /
same for Episode; it just never received nonzero values to overlay before
this fix.

Files modified: internal/api/handler.go, CHANGELOG.md.

Verified: go build ./..., go vet ./..., go test ./... all pass (full
suite). NOT re-verified against a live instance this round — recommend the
user rebuild/redeploy once more, redo the same manual search, click "Pick
Selected", and confirm the file actually moves to the library this time
(check for a destination path in the response/log, not just the absence of
an error).

=== Prior session (cont.): two more real-log-confirmed gaps in the metadata-correction fix ===

User re-deployed the fix below via update-mediaforge.ps1, then hit the exact
same symptom on a real run and reported it back with a real log excerpt.
Traced the log precisely (file: "Revolution (2013) - S02E12 - Captain
Trips.mp4"):
- Job `-1` (original SmartShrink attempt, VMAF threshold=94) correctly
  resolved the library path to "Revolution (2012) - S02E12 - Captain
  Trips.mp4" (TVDB correction worked, confidence=1.0), but failed the
  quality bar ("no viable encode found") and went to Review Queue.
- User did "Re-encode Custom" with a lower quality tier (job `-2`,
  threshold=85). This job's post-encode move landed at "Revolution (2013)
  - S02E12 -.mp4" — wrong year, no episode title.

Two distinct root causes, neither fixed by the earlier session's change:
1. `internal/jobs/worker.go` line ~926, the SmartShrink "no viable encode"
   → Review Queue call site, was missed by the earlier fix — still used
   `filepath.Base(job.InputPath)` (raw staging filename, still says 2013)
   instead of `job.LibraryPath` (already correctly resolved to 2012 during
   intake, before this SmartShrink retry loop runs). Fixed: now uses
   `filepath.Base(job.LibraryPath)` when LibraryPath is set, matching the
   pattern used at the two call sites fixed in the prior session.
2. `internal/api/handler.go` `ResubmitReviewEntry`: `intake.ParseFilename
   (entry.Filename)` only ever populates `ParsedEpisodeTitle` (raw text
   parsed from the filename) — never `EpisodeTitle`, which is the field
   `ResolveLibraryPath`/`applyNamingTemplate` actually reads for the
   `{episode_title}` naming-template token. This meant EVERY "Re-encode
   Custom" resubmit for a TV show silently dropped the episode title,
   independent of bug 1 above and independent of whether entry.Filename
   itself was ever corrected. `ResolveReviewEntry` ("Pick Selected") mostly
   avoided this because it explicitly overlays `req.Candidate.EpisodeTitle`
   when the picked candidate supplies one — but had no fallback if it
   didn't. Fixed: both handlers now do
   `if parsed.EpisodeTitle == "" { parsed.EpisodeTitle =
   parsed.ParsedEpisodeTitle }` (ResolveReviewEntry's fallback only applies
   when the candidate also didn't supply one).

User explicitly asked to fix these directly rather than switch to the
originally-proposed physical-staging-folder design — confirmed via
investigation that a staging folder would not have prevented either bug
(bug 1 is a missed call site, bug 2 is a field-mapping gap in a handler),
so the existing filename-carries-metadata architecture was kept and these
two gaps closed instead.

Verified: go build ./..., go vet ./..., go test ./... all pass (full
suite, including the intake test added in the prior session). Did NOT add
a new automated test for either fix — `ResubmitReviewEntry` only exercises
the fixed code inside a goroutine gated on a real `ProbeFile` (ffprobe)
call, matching the same "not worth the flakiness" reasoning from an
earlier session's CURRENT_STATE.md entry for the same handler; the
`ParsedEpisodeTitle` extraction itself is already covered by existing
`internal/intake/parse_test.go` cases. NOT yet redeployed/re-verified
against a live run — recommend rebuilding via update-mediaforge.ps1 and
re-running a TV file through the exact failure path (SmartShrink fails
quality bar → Review Queue → "Re-encode Custom" with a different quality
tier) to confirm the final library path now has both the corrected year
and the episode title.

=== Prior session: fixed Review Queue duplicate entries losing metadata corrections ===

User reported (via a task prompt describing symptoms, not a live repro): a
file with a wrong year/season/episode in its filename gets corrected by
intake's metadata lookup, then gets flagged as a duplicate at the library
destination and routed to Review Queue — but the correction is lost, so a
later "Re-encode Custom" reproduces the original wrong metadata in the
output filename/path.

Root cause, confirmed by reading the code (not a guess): ReviewEntry has no
dedicated metadata fields — `ResolveReviewEntry`/`ResubmitReviewEntry`
(internal/api/handler.go) already re-derive title/year/season/episode by
calling `intake.ParseFilename(entry.Filename)`, i.e. the stored Filename
string IS the metadata source of truth for resubmit/resolve (this was
already the existing design, not something added this session). The bug:
`entry.Filename` was always set via `filepath.Base(path)` — the raw,
uncorrected source/output basename — even in the two places where
corrected metadata was already known before routing to review:
1. `internal/intake/watcher.go` `moveHEVCToLibrary`: `resolveAndGate`
   merges corrected title/year/season/episode into `parsed`, but if a
   duplicate is then found at the destination, `sendDuplicateToReviewQueue`
   was called with the raw basename, discarding `parsed`.
2. `internal/jobs/worker.go` `processJob` post-encode duplicate check
   (~line 965): `job.LibraryPath` already has the corrected name baked in
   (set during intake's `stageAndEnqueue`), but the review entry's filename
   was built from `filepath.Base(finalPath)` — the transcoded file's name,
   unchanged by encoding, so still the original uncorrected name.

Fix — no new staging folder, no physical file move, no DB schema change
(the initial task prompt proposed a `D:\MediaForge\ReviewQueue\` staging
folder with files physically moved there; investigation showed the
filename-as-metadata-carrier pattern already exists and just needed to
actually carry the correction):
- `internal/intake/watcher.go`: `sendToReviewQueue`/
  `sendDuplicateToReviewQueue` gained a `correctedFilename string` param
  (falls back to `filepath.Base(path)` when empty). `moveHEVCToLibrary` and
  `stageAndEnqueue` now build one via the new `buildCorrectedFilename`
  helper once `resolveAndGate` has succeeded, and pass it through at every
  review-queue call site in those two functions. Call sites still inside
  `resolveAndGate` itself pass `""` (no correction exists yet at that
  point — that's the expected review case).
- `internal/intake/naming.go`: new `buildCorrectedFilename(cfg, parsed,
  ext)` — renders just the filename component (no folder) using the same
  naming-template logic `resolveLibraryPath` already uses, so the result
  round-trips cleanly through `ParseFilename`.
- `internal/jobs/worker.go`: the two post-encode `SendDuplicateToReviewQueue`/
  `SendToReviewQueue` calls now use `filepath.Base(job.LibraryPath)`
  instead of `filepath.Base(finalPath)`.
- New test: `internal/intake/watcher_test.go`
  `TestMoveHEVCToLibrary_DuplicateReviewEntryUsesCorrectedFilename` —
  reproduces the exact real-world case from an earlier session's TVDB
  adjacent-season-offset bug (filename S48E30 "I Have Killed For You" →
  corrected S49E30 "Her Last Call") with a pre-existing file at the
  corrected destination, and asserts the resulting ReviewEntry.Filename
  carries the correction and re-parses correctly via ParseFilename.

Did NOT add a worker.go-level test for the post-encode duplicate branch —
consistent with an earlier session's decision (see below), that code path
only completes via a real ffmpeg transcode through the worker pool, so a
meaningful test would need a real encode-to-completion run; the fix there
is a one-line field swap (`finalPath` → `job.LibraryPath`) verified by
reading the code, not by a new automated test.

Verified: go build ./..., go vet ./..., go test ./... (full suite,
including the new test) all pass. gofmt -l flags these files, but that is
a pre-existing repo-wide CRLF-line-ending artifact (confirmed by running
gofmt -l against untouched files too, e.g. internal/intake/parse.go) — not
something introduced by or relevant to this change. NOT verified against a
live intake run with a real duplicate file — recommend re-running the
original 20/20-style scenario (or any file that hits a duplicate at the
destination after a metadata correction) and confirming the Review Queue
entry's displayed filename shows the corrected title/year/season/episode,
then confirming "Re-encode Custom" from that entry lands at the corrected
path.

=== Prior session: fixed Review Queue custom re-encode never moving file to library ===

User reported: TV shows re-encoded from the Review Queue via "Re-encode
Custom" completed successfully (log showed "Job started"/"Job complete" with
normal duration/saved-bytes) but the file was left in the staging folder
(D:\MediaForge\Transcode\) instead of landing in the library.

Root cause: the worker's post-encode library-move step
(internal/jobs/worker.go processJob, ~line 957) is gated on `job.LibraryPath
!= ""`. That field is set in exactly one place in the codebase —
internal/intake/watcher.go, during the normal file-watcher intake flow.
ResubmitReviewEntry (internal/api/handler.go, the only handler behind Review
Queue re-encodes since RetryReviewEntry/"Re-add" were removed in an earlier
session) re-enqueues the job via queue.AddMultiple but never calls
queue.SetLibraryPath — so every Review Queue resubmit (not just ones using
custom CRF/speed/format overrides) skipped the library move silently. The
"Job complete" log line fires unconditionally regardless of whether the move
happened, which is why this looked like success in the logs.

Fix (internal/api/handler.go ResubmitReviewEntry): now fetches the
ReviewEntry up front (previously only req.OriginalPath/PresetID were used),
and after AddMultiple, rebuilds the intended library path the same way
ResolveReviewEntry already does — intake.ParseFilename(entry.Filename) +
intake.ResolveLibraryPath(&h.cfg.Intake, &parsed, ext) — with the output
extension resolved via ffmpeg.ResolveOutputFormat using the same
config-then-override precedence the worker uses for OutputFormat. Calls
queue.SetLibraryPath(job.ID, libraryPath) on each resubmitted job. If the
library path can't be rebuilt (e.g. unparseable filename), logs a warning
and leaves the job to complete without an auto-move rather than failing the
resubmit outright.

Verified: go build ./..., go vet ./..., go test ./... (full suite) all pass.
Did NOT add a new automated test — ResubmitReviewEntry probes the file via a
real ffprobe call and the job only completes via a real ffmpeg transcode
through the worker pool, so a meaningful test would require a real
encode-to-completion run rather than a mock; considered not worth the
flakiness for this fix. Recommend the user re-run "Re-encode Custom" on a
real Review Queue TV show entry and confirm the file actually lands in the
configured TV library path (not just that the job logs "Job complete").

=== Prior session: fixed colon-in-title sanitization (":" -> "_") for library naming ===

User reported: shows like "9-1-1: Nashville" were getting library folder/file
names like "9-1-1_ Nashville" instead of the expected "9-1-1 - Nashville".
Root cause: internal/intake/naming.go sanitizePathComponent() mapped every
filesystem-illegal character, including ':', to a bare underscore.

Fix (internal/intake/naming.go): sanitizePathComponent now special-cases the
colon before the generic illegal-char loop — replaces ": " and ":" with
" - " (space-dash-space), then still maps the other illegal chars
(/ \ * ? " < > |) to "_" as before. Also collapses any resulting double
spaces and trims. Comment explains why colon gets special treatment (title
subtitle separator, not stray punctuation).

Test (new internal/intake/naming_test.go): TestSanitizePathComponent covers
colon-with-space, colon-no-space, and all the other illegal characters
individually; TestApplyNamingTemplateColonInShowTitle exercises the full
{show} ({year}) template with "9-1-1: Nashville" end-to-end, asserting the
folder name comes out "9-1-1 - Nashville (2025)".

Verified: go build ./... and go test ./... (full suite) both pass. Not
tested against a live intake run — this is naming-template logic only, no
filesystem/network dependency, so unit coverage is adequate.

=== Latest session (cont.): additional mobile CSS fixes below 768px ===

Continuation of the mobile responsiveness pass from the prior commit
(796057c). web/templates/index.html, inside the existing 768px block: `.header`
gets `flex-wrap`/`row-gap` so it wraps instead of clipping when contents
overflow one row; new `.view-nav` rule moves the tab nav to its own full-width
row (`order: 3`) with `overflow-x: auto` instead of squeezing tab labels.
Inside the 480px block: `.review-bulk-bar` gets `flex-wrap`. No JS/backend
changes. Not click-tested in a browser this session (CSS-only, reviewed by
reading the rules against the existing layout).

=== Latest session: fixed Review Queue resubmit silently failing for staging-path entries ===

User reported: an encode-failure Review Queue entry (SmartShrink couldn't hit
the VMAF threshold for any CRF, so it was routed to Review Queue) would, on
clicking "Re-encode Custom", flash back to the queue screen with no encode
ever starting. Log showed `Review resubmit: probe failed ... error=<nil>` —
a nil error with zero probes is the tell.

Root cause: `ResubmitReviewEntry` (internal/api/handler.go) probed the file via
`h.browser.GetVideoFilesWithProgress`, which is scoped to the configured media
browse root (`internal/browse/browse.go` normalizePath/isUnderRoot) and
silently drops any path outside that root — returns an empty slice with a nil
error, no logged reason. For entries that failed during encoding (not intake
identification), `OriginalPath` is the staging/transcode working path (e.g.
`D:\MediaForge\Transcode\...`), which normally lives outside the media library
root, so every resubmit attempt silently probed zero files and the entry got
reverted to `pending`.

Fix: switched to `h.browser.ProbeFile(ctx, req.OriginalPath)` — a direct
ffprobe call with no root restriction, same pattern already used by
`RetryJob` (internal/api/handler.go). Appropriate here because OriginalPath
comes from a server-stored review entry, not untrusted user browse input, so
the root-scoping check was never the right tool for this call site.

Files modified: internal/api/handler.go (ResubmitReviewEntry), CHANGELOG.md.

Verified: go build ./..., go vet ./..., go test ./... all pass. NOT yet
re-verified against a live encode-failure-to-resubmit run (no test file in
this environment reproduces a full SmartShrink failure end-to-end) — recommend
the user re-run "Re-encode Custom" on the real failed entry and confirm the
job actually starts (job_id appears, "Job started" log line) instead of the
entry reverting to pending again.

=== Prior session: fixed Review Queue resubmit path corruption + added Logs viewer tab; mobile CSS pass in progress ===

User found (real-world, not synthetic): "Re-encode Custom" in the Review
Queue removed the entry from the queue but never actually enqueued an
encode. Root cause: `web/templates/index.html`'s Re-encode button embedded
the file's raw Windows/UNC path directly into an inline `onclick="..."`
attribute; the browser parses that attribute as a JS string literal, so
every lone backslash (all but the leading `\\`) was silently dropped as an
invalid escape sequence — e.g. `\\TOWER\Media\TV Shows\...` became
`\TOWERMediaTV Shows...`. Fixed by passing the path via a `data-original-path`
attribute (HTML-escaped via the existing `escReview()` helper) instead of an
inline JS string. Separately, `ResubmitReviewEntry` in
`internal/api/handler.go` used to mark the entry `resolved` unconditionally
before probing the file in a background goroutine — if the probe failed
(corrupted path, missing file, etc.) the entry just vanished with only a
server-log warning, violating the "no silent failures" principle in
MEDIAFORGE_SPEC.md. Now reverts the entry to `pending` with a reason via
`UpdateReviewEntryReason` when the probe fails, so it stays visible and
actionable instead of disappearing.

Also added a Logs viewer to the web UI (user asked "I can't see the log from
a remote device" — logs previously only existed as a local file at
`%APPDATA%\Mediaforge\logs\mediaforge.log`, unreachable from a phone/other
LAN device hitting the web UI):
- New `GET /api/logs?file=current|1|2&lines=N` endpoint
  (`internal/api/handler.go`, registered in `internal/api/router.go`). Reuses
  the existing `h.cfgPath` field to derive the log directory the same way
  `cmd/mediaforge/main.go` does. `lines` clamps 1-1000 (default 200); returns
  JSON with `lines`, `totalLines`, and an `available` map so the frontend can
  gray out file-picker options for backups that don't exist yet (fresh
  installs only have `current`, no `.1`/`.2`).
- New third top-level nav tab "Logs" (`web/templates/index.html`), alongside
  the existing Queue/Review tabs — `#logs-view` section with a file picker
  (current/previous/2-sessions-ago), a line-count selector (50/200/500/1000),
  and an "Auto-refresh" checkbox (client-side 5s `setInterval` poll — no SSE/
  live-tail in v1, deliberately: no file-watcher abstraction exists in this
  codebase and a plain refresh is adequate for a personal LAN tool).
  `switchView()` was generalized from a two-branch (queue/review) to a
  three-branch if to support the new tab.

Verified end-to-end against a real running instance (not just unit tests):
built a scratch `mediaforge.exe`, confirmed via `curl` that `/api/logs`'s
tail output exactly matches the real file's tail on disk, confirmed
`file=1`/`file=2` return distinct real content vs. 404 when absent, confirmed
`lines=5000` doesn't error (clamped), and drove the UI via headless Chrome
over CDP (Node's built-in `WebSocket`, no extra deps) to confirm the Logs tab
shows real content, the file picker's disabled state matches `available`,
switching files changes displayed content, navigating away hides the tab and
resets auto-refresh, and the auto-refresh timer fires a real second fetch
after 5s.

Cleanup note for future sessions: when killing a disposable headless Chrome
test instance, track its specific PID(s) at launch and kill only those (or
kill by the scratch app's listening port via `netstat -ano`) — a blanket
`taskkill /F /IM chrome.exe` earlier in this project's history killed the
user's real browser windows by accident. Same caution applies to
`mediaforge.exe`: check `netstat -ano | grep :<scratch-port>` before killing,
since the user may have the real deployed instance running concurrently.

Part 2 (mobile CSS pass) is now also done, in a separate commit. Added a new
`@media (max-width: 480px)` block right after the existing 768px block in
`web/templates/index.html`: `.review-dup-compare` switches to
`flex-direction: column` (was a cramped two-column layout on phones),
`.btn` gets `min-height: 44px`, `.review-card-check` grows to 20x20px
(`.btn-sm` deliberately left alone — Prev/Next and other compact actions
rely on the smaller size). Note: `.file-name` already had proper ellipsis
truncation before this session — that was a false lead from initial
research, confirmed already-fine by reading the CSS directly, no work
needed there.

Verified via the same scratch-build + headless-Chrome-over-CDP approach:
measured `.review-dup-compare` stacking (injected a synthetic instance of
the markup since no real duplicate entry existed in the scratch environment),
measured `.btn`/`.review-card-check` computed sizes at 375px (44px / 20x20,
as intended) vs. 1280px (reverted to original 38px / 15x15, confirming no
desktop regression), and took a screenshot at 375px to visually confirm the
header/logo/queue panel render sanely on a phone-width viewport.

**Nothing outstanding from this session** — both parts of the logs+mobile
plan are implemented, verified, and committed as of this note.

=== Prior session: UI polish (logo/font/dropdown width) + per-job CRF override for Compress presets ===

UI changes in `web/templates/index.html`:
- Logo icon already sized to 48x48 (was 32x32) in the working tree at session
  start; left as-is.
- Added Cinzel (Google Fonts) and wrapped the "MediaForge" header text in a
  new `.logo-text` span so only the app name uses it — rest of the UI stays
  on DM Sans.
- Added `#quality-dropdown .preset-dropdown-trigger { min-width: 160px; }` to
  shrink the SmartShrink quality dropdown (shared by both smartshrink-hevc
  and smartshrink-av1) without affecting the main preset dropdown's 230px
  min-width.

New feature: per-job CRF override for Compress presets (compress-hevc /
compress-av1) in Custom Encode mode, available in both the main file browser
UI and the Review Queue's "Re-encode Custom" form. Previously CRF for these
presets was only settable via the global Settings drawer (quality_hevc /
quality_av1); this adds a one-off override per job, following the same
pattern as the existing per-job EncodeSpeed/EncodeOutputFormat overrides.

Backend:
- `internal/jobs/job.go` — new `Job.OverrideCRF int` field (in-memory only,
  like OverrideSpeed/OverrideOutputFormat — not persisted to sqlite).
- `internal/jobs/queue.go` — `SetJobOverrides` signature extended to
  `(id, speed, outputFormat string, crf int)`.
- `internal/jobs/worker.go` — after initializing qualityHEVC/qualityAV1 from
  config, applies `job.OverrideCRF` when set and `!preset.IsSmartShrink`,
  keyed by `preset.Codec`.
- `internal/api/handler.go` — new `encode_quality_crf` field on
  `CreateJobsRequest` (POST /api/jobs) and on the review-resubmit request
  struct (PUT /api/review/{id}/resubmit). Validated via existing
  `validateQuality()` (range 16-30 HEVC, 18-35 AV1) and rejected for
  SmartShrink presets.

Frontend:
- Main UI: new `#custom-crf-input` number field next to the speed/format
  selects, shown only in Custom Encode mode when the selected preset is
  compress-hevc/compress-av1 (`updateCustomCrfVisibility()`, wired into
  `onPipelineModeChange()` and `selectPreset()`). min/max swap based on
  preset codec. Sent as `encode_quality_crf` in `startJobs()`.
- Review Queue: new `#rccrf-{id}` number input in the custom re-encode form,
  shown only when preset is compress-hevc (`reviewCustomPresetChange`), sent
  as `encode_quality_crf` in `reviewResubmitCustom()`.

Verified: `go build ./...`, `go vet ./...`, and `go test ./...` all pass.
Did not launch the app in a browser this session — UI changes are CSS/markup
only and were reviewed by reading the rendered structure, not click-tested.

Nothing else outstanding from this session.

=== Prior session: fixed app-reported version not matching git tag ===

User reported the running app logged `version=v1.0.0+build.12` despite being
built from the `v1.1.0` tag. Root cause: `internal/version.Version` was a
hardcoded Go constant, never tied to the git tag — only `Build` was ever
injected via `-ldflags`. The installer version (`.iss`) and Docker image tag
were already correctly derived from the git tag, but the binary's own
self-reported version was not.

Fix: made `Version` a `var` with the same fallback-constant pattern as
`Build`, and wired it through `-ldflags -X .../version.Version=<X.Y.Z>` in:
- `.github/workflows/release.yml` — windows-installer job now computes the
  version once (`steps.appver.outputs.version`, empty on non-tag runs) and
  reuses it for both the Go build and the installer's `/DMyAppVersion`; the
  docker job now also extracts a `semver` (no `v` prefix) output and passes it
  as the new `APP_VERSION` build-arg.
- `Dockerfile` — new `APP_VERSION` build-arg, conditionally added to ldflags
  only if non-empty.
- `build.ps1` — new `-Version` param, defaults to `git describe --tags
  --abbrev=0` (v-prefix stripped); falls back to the hardcoded default in
  version.go if no tag is found (e.g. shallow clone).

Verified: `go build ./...` and `go test ./...` pass; ran `build.ps1` locally
and confirmed the built binary logs `MediaForge v1.1.0+build.12 starting`.

Nothing outstanding — this was scoped to the version-reporting bug only.

=== Prior session: removed dead/redundant Review Queue Retry and Re-add buttons ===

User reported: Retry did nothing but remove the item from the queue, and
Re-add did nothing at all. Confirmed by reading the code:
- RetryReviewEntry (internal/api/handler.go) only had real behavior for
  post-encode entries (job.LibraryPath set) — for the normal case it just
  called UpdateReviewQueueStatus(id, "resolved") with no re-identification or
  re-enqueue, i.e. exactly the "removes the item, does nothing else" the user
  saw.
- reviewResubmit ("Re-add", web/templates/index.html) hit the same
  /api/review/{id}/resubmit endpoint as "Re-encode Custom" but with no error
  handling, so a failure was silent — and the working, better-UX version
  already existed as "Re-encode Custom".

Removed both entirely rather than fixing them, since "Re-encode Custom"
already covers the same functionality correctly:
- web/templates/index.html: deleted reviewRetry, reviewRetryAll, reviewResubmit
  JS functions; removed the per-card "Retry"/"Re-add" buttons and the bulk
  "Retry All" button.
- internal/api/handler.go: deleted RetryReviewEntry.
- internal/api/router.go: removed the PUT /api/review/{id}/retry route.

Verified: go build ./... and go test ./... both pass. Nothing outstanding from
this change.

=== Prior session (cont. once more): confirmed root cause + real fix — adjacent-season check ===

User confirmed (while away from keyboard, so this fix was implemented and
deployed unattended) the actual root cause: this specific source numbers this
show's seasons ONE OFF FROM TVDB ACROSS THE BOARD. TVDB's real episode is
S49E30 "Her Last Call"; the source file said S48E30. So this was never a
title-matching precision problem — the whole-series title scan (findEpisodeByTitle)
should have found it eventually, but apparently didn't (reason still not
confirmed from a real log — could be pagination limits on a ~1200+ episode,
48+ season show, or a title variant close enough to another episode to trip the
"unambiguous" guard). Rather than debug the scan further blind, added a direct,
cheap first-pass check that exactly matches the reported failure mode.

Fix (internal/intake/tvdb.go):
- New findAdjacentSeasonEpisode(ctx, seriesID, season, episode, title): fetches
  season+1 and season-1 (same episode number) and accepts a match at
  similarity >= 0.90. At most 2 HTTP requests.
- Lookup() now tries this BEFORE falling back to findEpisodeByTitle (the
  whole-series scan). Whichever succeeds first wins; behavior/logging for the
  correction itself (result.Season/Episode/EpisodeTitle update, "TVDB: corrected
  season/episode by episode name" info log) is unchanged either way.
- Test: TestTVDBLookup_ReconcileAdjacentSeasonOffset — mocks season 48 (wrong
  episode) and season 49 (right episode, "Her Last Call") via the season query
  param, and makes the paginated whole-series scan return EMPTY, so the test
  only passes if the new adjacent-season path is what resolves it (not a
  fallback-masks-the-bug false positive like the previous session's weak test).

Verified: go build ./..., go vet ./..., go test ./... all pass, including the
new test and all pre-existing TVDB/selectBestSeries/normTitle tests.

DEPLOYMENT (done this session, user was away from keyboard and asked me to
build + deploy unattended): killed running mediaforge.exe/mediaforge-tray.exe,
ran build.ps1 to produce dist/mediaforge.exe + dist/mediaforge-tray.exe with
the fix, copied both over C:\apps\mediaforge\ (the live install directory —
confirmed via existing config/logo.png/uninstaller there), and relaunched
mediaforge-tray.exe from that directory so it starts mediaforge.exe as usual.
User is deleting the stale Review Queue entry / destination file and re-copying
the source file into the incoming folder via Remote Desktop themselves.

STILL NOT independently confirmed against the live TVDB API for this exact
file (only via the unit test mock) — this is now the third round of "should be
fixed," and the last two both had real gaps despite passing tests, so treat
this as high-confidence but NOT verified until the user reports the next real
run succeeded. Ask them to check the log for either "TVDB: adjacent-season
episode name match" or "TVDB: corrected season/episode by episode name" and a
final destination containing "Her Last Call".

=== Prior session: series selection now correct, but episode-title reconciliation still doesn't find "Her Last Call" — added diagnostics, NOT yet resolved ===

Re-ran the actual file after the normTitle fix. Real progress: TVDB now correctly
selects the real "20/20" series (tvdb_id=72289, selection_score=0.60) instead of
a decoy — the normTitle fix worked as intended. However, the episode at S48E30 is
still "I Have Killed For You" per TVDB, and findEpisodeByTitle apparently did NOT
correct it to "Her Last Call" — confidence computed as 0.88 using the WRONG
episode title (titleScore component reflects sim=0.19, i.e. result.EpisodeTitle
was never overwritten). The file got queued to Review Queue this run only because
a stale destination file from an earlier (pre-fix) bad run already existed there
— the duplicate-conflict path, not the confidence gate, is what caught it this
time. If that stale file weren't there, this would have auto-moved under the
wrong title again.

Root cause NOT yet identified — could be either:
(a) a real code bug in the mismatch-detection or reconciliation call path, or
(b) TVDB genuinely doesn't have an episode titled "Her Last Call" anywhere in
    this series (i.e. the source file's own episode title metadata is wrong,
    not a MediaForge bug at all — "I Have Killed For You" may just be correct).
The previous log had NO visibility into which of these it is: findEpisodeByTitle
only logged on success, so a failed/no-match search was indistinguishable from
"never attempted."

This session only added diagnostics, did NOT change the reconciliation logic:
- internal/intake/tvdb.go Lookup(): added a debug log right before calling
  findEpisodeByTitle ("TVDB: episode name mismatch, searching series for
  title") so the log can confirm reconciliation was actually attempted.
- internal/intake/tvdb.go findEpisodeByTitle(): now logs a debug line on every
  failure path (request build/send error, non-200, decode error) and, when no
  confident match is found, logs "TVDB: findEpisodeByTitle no confident match"
  with pages_scanned, episodes_scanned, the best candidate name + similarity,
  and the runner-up similarity — so a future run will show either "reconciled"
  or exactly why it didn't (best match too weak / ambiguous / API error / not
  enough pages scanned).

NEXT STEP for a future session: re-run the file once more with debug logging
enabled and read the new "TVDB: findEpisodeByTitle no confident match" (or
success) line. If best_similarity is well below 0.90 even for the correct
episode, this may be case (b) above — a genuine metadata mismatch in the source
file, not a bug — and no code change would be appropriate. If pages_scanned is
suspiciously low (e.g. hit maxPages=20 without reaching this show's later
seasons — 48 seasons at ~25 eps/season is ~1200 episodes, so 20 pages must cover
enough per page to reach it; page size is TVDB-controlled, not configured here),
that points to a real pagination-limit bug worth revisiting.

Verified: go build ./..., go vet ./..., go test ./... all pass. This is a
diagnostics-only change — no behavior change to confidence scoring, gating, or
file moves. Do not report this bug as fixed; it is still open.

=== Prior session: normTitle deleted punctuation instead of spacing it — the previous fix was a no-op in prod ===

The user re-ran the same file after the selectBestSeries fix below and got the
IDENTICAL wrong result from a fresh log. Root cause of why that fix didn't work:
normTitle (internal/intake/tmdb.go) strips punctuation characters entirely
(`if unicode.IsLetter || IsDigit || IsSpace { keep } else { drop }`), so
"20/20" collapses to "2020" (one token, no space where the slash was) while
"20 20" (the filename-derived title, already space-separated) stays two
tokens. "2020" != "20 20", and neither Contains the other, so my selectBestSeries
fix — which relies on normTitle to make these compare equal — never actually
fired in production. My own new unit test for it (TestSelectBestSeries_
PunctuationNormalizedForNameMatch) passed anyway, but only because of a
coincidental tie-break with the year-floor guard against a single garbage-year
decoy — it never exercised a decoy with a real, plausible year (like the
"11 Uhr 20"/1970 candidate that actually won in production), so it didn't catch
this.

Real fix (internal/intake/tmdb.go normTitle): punctuation now becomes a space
(not dropped), then Fields()+Join collapses whitespace. "20/20" -> "20 20",
matching the filename title exactly. This function is shared by TMDB's
selectBestMovie/selectBestTV, TVDB's baseSeriesScore (wired in the prior fix
below), and confidence.go's stringSimilarity — all of them were relying on
punctuation-preserving equality that never actually normalized punctuation as a
separator, so this is a correctness fix for all of them, not just TVDB.

Test changes (internal/intake/tvdb_test.go):
- Added TestNormTitle_PunctuationIsWordBoundary — direct table test asserting
  "20/20", "20:20", "20-20" all normalize to "20 20", plus the pre-existing
  "Avatar: Fire and Ash" case still passes.
- Strengthened TestSelectBestSeries_PunctuationNormalizedForNameMatch by adding
  a third candidate, "11 Uhr 20" (year 1970 — a real, plausible year, not
  garbage), modeled directly on the actual candidate that won in the production
  log. This is now a genuine regression test: it fails without the normTitle
  fix (ties/loses to the plausible-year decoy) and passes with it (the exact
  name-match bonus makes the real show win outright).

Verified: go build ./..., go vet ./..., go test ./... all pass (full suite, not
just internal/intake — normTitle is shared code). NOT re-verified against a
live TVDB/TMDB API call for the literal file; only via unit tests. Given the
previous "verified via unit test" claim turned out to be a false positive
(passing for the wrong reason), the strong recommendation this time is to
actually re-run the original 20_20 file through a live intake pass before
trusting this is fixed — check the log for "TVDB: corrected season/episode by
episode name" and a final destination containing "Her Last Call", not
"I Have Killed For You".

=== Prior session: fix TVDB selectBestSeries picking wrong series (real prod log) ===

After the "task 2 verified done" note below, the user hit the exact same failure
live: "20_20 (1978) - S48E30 - Her Last Call.mp4" got moved to the library as
"...S48E30 - I Have Killed For You.mp4" — the episode-title reconciliation never
kicked in. Root cause found in the real intake log (debug level): TVDB's
selectBestSeries (internal/intake/tvdb.go) compared candidate names with a raw
`strings.ToLower` — no punctuation stripping. The parsed filename title is
"20 20" (space; underscore-to-space during parsing) but TVDB lists the show as
"20/20" (slash), so the exact-match branch never fired for the real show. With
no name bonus, the real show's score after the episode-mismatch penalty (-0.30,
its season 48 exists but E30 disagrees) was 0.30 — while a garbage decoy series
("20 Tage im 20. Jahrhundert", year "0099") got a false year bonus (0099 <= 1978
trivially) and only the smaller fetch-failure penalty (-0.20, no season 48 at
all), scoring 0.40 and winning selection. Lookup() then ran the episode-name
reconciliation against the WRONG (decoy) series, which obviously doesn't have
"Her Last Call" anywhere, so it failed, TVDB confidence came back 0.00, and the
pipeline fell back to TMDB — which has no episode-title reconciliation logic at
all, so it accepted "I Have Killed For You" uncorrected and moved the file.

Fix (internal/intake/tvdb.go):
- baseSeriesScore now compares `normTitle(s.Name)` against a `normTitle`-
  normalized query (was raw `strings.ToLower`), so punctuation-only differences
  ("20/20" vs "20 20") no longer suppress the exact-match bonus. normTitle
  already existed in tmdb.go (same package) — reused as-is.
  selectBestSeries's queryLower var renamed queryNorm, built via normTitle.
- Added a sanity floor (seriesYear >= 1900) on the premiere-year bonus so a
  mis-parsed/garbage year like "0099" can no longer satisfy the "<=" check for
  free.
- Did NOT touch TMDB's selectBestTV/selectBestMovie (already use normTitle) or
  add episode-title reconciliation to the TMDB path — with the TVDB fix, TVDB
  now wins this exact scenario correctly and its existing reconciliation
  (findEpisodeByTitle) handles the correction, so the TMDB fallback path wasn't
  exercised here. A TMDB-side reconciliation gap still exists for
  TVDB-key-not-configured setups but is out of scope for this fix (not the
  reported bug; would need its own design/confirmation before implementing).
- Test: TestSelectBestSeries_PunctuationNormalizedForNameMatch — reproduces the
  exact two-candidate scenario (real "20/20" vs decoy garbage-year series) and
  asserts the real show wins selectBestSeries.

Verified: go build ./..., go vet ./..., go test ./... all pass (including the
existing TestTVDBLookup_ReconcileWrongSeasonEpisode, TestSelectBestSeries_*).
NOT re-verified against a live TVDB API call for the literal 20/20 file — only
via the unit test reproduction. Recommend re-running the original file through
intake once and confirming the log shows "TVDB: corrected season/episode by
episode name" instead of a direct library move under the wrong title.

=== Prior session: Keep Existing deletes incoming duplicate file (task 2 verified done) ===

Task requested two fixes:

1. DONE this session — "Keep Existing" on a duplicate-conflict Review Queue entry
   now deletes the incoming file from disk. Root cause:
   DiscardReviewEntry (internal/api/handler.go, PUT /api/review/{id}/discard —
   the web UI's "Keep Existing" button calls this same endpoint via reviewDiscard())
   only called UpdateReviewQueueStatus(id, "discarded"); it never touched the
   filesystem. Fix: when the entry has DuplicateInfo set, unmarshal it and
   os.Remove(dupCtx.Incoming.Path) before marking discarded (os.IsNotExist is
   treated as success; a real remove error is logged as a warning but the discard
   still proceeds). Logs "Review discard: deleted incoming file" with
   reason="duplicate: user selected keep existing". Non-duplicate discards
   (DuplicateInfo == "") are untouched — no file is deleted for a plain
   low-confidence/failed-match entry.
   - Files modified: internal/api/handler.go (DiscardReviewEntry),
     internal/api/handler_test.go (+intake import; 3 new tests:
     TestDiscardReviewEntry_DuplicateDeletesIncomingFile,
     TestDiscardReviewEntry_NonDuplicateNoFileTouched,
     TestDiscardReviewEntry_IncomingAlreadyGone), CHANGELOG.md.

2. ALREADY DONE (no change needed) — TVDB episode-title-first reconciliation
   (filename S/E wrong but episode title right, e.g. S48E30 "Her Last Call"
   should be S49E30) was fully implemented in a prior session (commits e4fdfed,
   bf95e97): internal/intake/tvdb.go Lookup() detects a title mismatch/miss at
   the parsed S/E, calls findEpisodeByTitle() to paginate the whole series
   (/v4/series/{id}/episodes/default/page/{n}), and accepts a correction only
   when similarity >= 0.90 AND unambiguous (clear gap over runner-up). On
   correction, confidence gets the same-name+episode-found override (up to 1.0
   when title sim >= 0.90), comfortably clearing the >=0.85 bar. Logs "TVDB:
   corrected season/episode by episode name" with old/new season+episode+title.
   Existing test TestTVDBLookup_ReconcileWrongSeasonEpisode covers this. Verified
   by reading the code this session; did not modify it.

Verified: go build ./..., go vet ./..., go test ./... all pass; gofmt clean on
changed files (internal/api/router.go and sse.go have pre-existing unrelated
gofmt/gosec findings, not touched here).

=== Prior session: AVC confidence gating + episode-name reconciliation ===

Bug: An AVC file whose season/episode was numbered wrong (e.g. "20/20 - S48E30 -
Her Last Call") was silently staged and added to the encode queue under the wrong
TVDB metadata (S48E30 = "I Have Killed For You", confidence 0.43). Two causes:
(1) the AVC path (stageAndEnqueue) never gated on confidence — only the HEVC path
did; (2) nothing trusted the filename's episode name when the given S/E disagreed.

Fix:
- internal/intake/watcher.go: extracted the HEVC gating logic into a shared
  `resolveAndGate(ctx, path, *parsed, probe) (*LookupResult, bool)` helper (lookup
  → <0.60 Review Queue → 0.60-0.85 LLM-then-Review → accept; merges confirmed
  title/year/season/episode/episode-title into parsed on accept). moveHEVCToLibrary
  now calls it. stageAndEnqueue now runs it on the SOURCE file BEFORE staging/
  enqueue, so a rejected AVC file lands in the Review Queue untouched instead of
  entering the encode queue.
- internal/intake/tvdb.go: new `findEpisodeByTitle` paginates
  /v4/series/{id}/episodes/default/page/{n} and returns the best-matching episode
  only when sim >= 0.90 AND clearly ahead of the runner-up (unambiguous). Lookup now
  calls it when the filename has an episode title but the S/E episode is missing or
  mismatched (<0.60), correcting Season/Episode/EpisodeTitle. Added Season/Episode to
  TVDBResult.
- internal/intake/orchestrator.go: added Season/Episode to LookupResult; fromTVDB
  populates them (zero for TMDB/OMDb).
- Tests: tvdb_test.go TestTVDBLookup_ReconcileWrongSeasonEpisode (S48E30 → S45E12);
  watcher_test.go TestResolveAndGateLowConfidenceToReview (0.40 match → Review Queue,
  ok=false, not enqueued).

Verified: go build ./..., go vet ./..., go test ./... all pass; golangci-lint on
internal/intake reports 0 issues. Not yet exercised against a live TVDB/real file —
recommend an end-to-end run of the original 20/20 file to confirm the log shows the
"corrected season/episode by episode name" line or a Review Queue entry.

=== Latest session (cont.): fix manual-match "Pick Selected" not moving file ===

Bug (long-standing, previously undocumented): Review Queue "Search Manually" →
Pick Selected marked the entry resolved but NEVER moved the file to the library —
ResolveReviewEntry (PUT /api/review/{id}/resolve) ignored the request body. File
left stranded at its intake path; looked resolved in the UI (silent failure).

Fix:
- internal/intake/naming.go: added exported ResolveLibraryPath wrapper around the
  unexported resolveLibraryPath (so the api package can build library paths).
- internal/api/handler.go ResolveReviewEntry: now decodes {candidate:{title,year,
  media_type,episode_title,season,episode}}, overlays it onto ParseFilename(entry
  .Filename) (keeps season/episode from the filename), resolves the library path
  (ext from the entry filename), and util.SafeMove's the file there. Guards:
  400 if no candidate title; 400 for duplicate-conflict entries (they use
  Replace/Keep Existing); 409 if the source file is gone; 409 if a file already
  exists at the destination (never auto-overwrite, per spec); 500 on move error.
  On any non-success the entry stays pending and its reason is updated. Marks
  resolved only after a successful move. Added "os" import.
- web/templates/index.html: added _reviewCandidates map so the selected candidate
  object (from list OR manual search) is retained and its full metadata is POSTed
  (previously read from entry.candidates, which the list response leaves empty, so
  the picked candidate was effectively {} → backend now rejects that). reviewPick
  now surfaces resolve errors and reloads the queue so an updated reason shows
  instead of the card vanishing. Cleans up _reviewCandidates in reviewRemoveCard.
- internal/api/handler_test.go: added mockReviewStore + 4 tests (moves file;
  missing title 400; duplicate-at-destination 409 stays pending; source-missing
  409 stays pending). All pass.

Note: did NOT do the deep pipeline-Retry (#3) — re-running the same automated
lookup yields the same failure unless parameters change; manual match is the
right tool and now works end-to-end.

Verified: go build ./..., go vet ./..., go test ./... all pass; gofmt clean;
inline JS parses clean.

=== Latest session: Review Queue UI + encode queue pause/resume ===

Implemented four scoped items (#12/#3/#4/#13). Much of the backend already
existed (per-item retry/resubmit endpoints, queue pause/start endpoints), so the
work was mostly frontend plus small backend extensions.

- #12 Scrollable Review Queue + pagination (web/templates/index.html):
  .review-card now has flex-shrink:0 so cards keep their height and the list
  scrolls. Added a #review-pager bar (per-page select 10/25/50/100, default 10;
  Prev/Next). loadReviewQueue() now passes page+limit; the server already
  paginated (page/pages in the response). Pager hides when pages<=1. When a page
  empties (item resolved/discarded) it reloads and steps back a page.
- #3 Retry individual items: already present (reviewRetry -> PUT
  /api/review/{id}/retry, RetryReviewEntry handler). No change needed.
- #4 Re-encode with custom settings (web/templates/index.html +
  internal/api/handler.go): new "Re-encode Custom" button opens an inline form
  (preset: compress-hevc | smartshrink-hevc; smartshrink quality tier; encoder
  speed; output container). reviewResubmitCustom() PUTs to
  /api/review/{id}/resubmit. ResubmitReviewEntry now accepts encode_speed,
  encode_output_format (validated mkv|mp4|preserve), and smartshrink_quality
  (validated), and applies them via queue.SetJobOverrides after AddMultiple.
- #13 Encode queue pause/resume header toggle (both files): added a
  "Pause/Resume Encode Queue" button + Running/Paused badge to the queue panel
  header. New toggleQueuePause() hits /api/queue/pause | /api/queue/start.
  updateStopResumeButton() syncs both the footer and header controls. To keep the
  badge authoritative (the old updateJobs heuristic auto-clears queuePaused when
  no jobs run), GET /api/stats now returns `paused` (workerPool.IsPaused()) and
  updateStats() reconciles queuePaused from it each poll.

Note: the scope named /api/review-queue/{id}/... endpoints; the repo already uses
/api/review/{id}/... (PUT) so those were reused rather than adding duplicates.

Verified: go build ./..., go vet ./internal/api, and go test ./... all pass;
gofmt clean.

=== Prior session: TVDB/TMDB episode-name identification fix ===

Bug: "The Office (2005) - S01E01 - Pilot.mp4" was identified as the 2001 UK
series. Root cause: selectBestSeries() scored candidates on show name + year
only; both Office entries tied, so the wrong one could win.

Fix (internal/intake/tvdb.go):
- Split the name+year scoring into baseSeriesScore() (pure helper).
- selectBestSeries is now a *TVDBClient method. When the filename carries an
  episode title (IsTV + season + episode + ParsedEpisodeTitle), it calls
  fetchEpisode() for EACH candidate and compares the TVDB episode name to the
  filename's episode title via stringSimilarity:
    sim >= 0.90 → +0.40, sim >= 0.60 → +0.15, else → -0.30; fetch error → -0.20.
  Episode-name agreement thus dominates when show names are similar. Falls back
  to base name+year scoring when no episode title is present (unchanged behavior,
  no extra HTTP). For the Office case: US S01E01="Pilot" (+0.40) beats UK
  S01E01="Downsize" (-0.30).
- Note: the winning candidate's episode is fetched again in Lookup() after
  selection (kept simple; one redundant call only when validating).

Logging added (debug level) to both tvdb.go and tmdb.go: search candidate
counts, per-candidate scores, episode-name comparisons, and the best pick.

Tests: added TestSelectBestSeries_EpisodeNameDisambiguates (Office US-vs-UK).
Updated TestSelectBestSeries_ExactMatchWins to the new method signature. Full
go build ./... and go test ./... pass.

=== Prior session: wizard & installer UX polish (#15/#16/#17/#18) ===

Added help/warning text to the first-run wizard and an AppData-removal prompt to
the uninstaller. The wizard UI lives in cmd/tray/setup_wizard.ps1 (embedded via
go:embed into setup_windows.go), so the text was added there, not in the .go.

- New Add-Help helper (dim-gray 8pt wrapping label) in setup_wizard.ps1.
- #16: UNC-path note under INTAKE PATHS section header.
- #17: SSD tip after the Staging Folder row.
- #18: "API keys are optional / route to Review Queue" note under API KEYS.
- Form/panel/button-bar heights grew by 114px to fit the added text
  (ClientSize 861->975, panel 812->926, bar y 814->928); panel AutoScroll still
  handles overflow on small screens.
- #15: installer/mediaforge.iss gained a [Code] CurUninstallStepChanged handler
  that, at usPostUninstall, prompts "Remove application data (config, logs,
  cache)?" and DelTree's {userappdata}\MediaForge only if the user clicks Yes.
  The existing [UninstallDelete] dirifempty on {app} is unchanged.

Verified: GOOS=windows go build ./cmd/tray clean. Nothing outstanding.

=== Prior session: startup version banner + encoder logging (#10) ===

Added a level-independent logger.Banner(msg) to internal/logger/logger.go: it
writes directly to the logger's underlying writer (stdout + session file),
bypassing the slog level filter. InitWithFile/Init now stash that writer in a
package var so Banner reaches the log file too (previously the fmt.Println box
in main.go only went to a live console — invisible in the log file and in
headless tray runs).

Startup logging in cmd/mediaforge/main.go now uses Banner:
- First log line: "MediaForge v{Version}+build.{Build} starting".
- After ffmpeg.DetectEncoders(), logDetectedEncoders() emits "Available
  encoders: NVIDIA NVENC" / "AMD/Intel VAAPI" / "Intel Quick Sync" / "Apple
  VideoToolbox" as applicable, always "CPU fallback ready", and "No hardware
  acceleration detected, using CPU" when no HW accel is present.
These now appear at every log level (including warn/error).

Note: hwaccel lives in internal/ffmpeg/hwaccel.go (not internal/hwaccel), and
DetectEncoders/version.Version/Build were already exported. go build ./... and
logger tests clean. Nothing outstanding.

=== Prior session: tray View Logs / Config open in Notepad ===

Symptom: tray "View Logs" flashed a box then opened nothing, even though
%APPDATA%\MediaForge\logs\mediaforge.log exists. Not a file-association
issue (cmd /c start "" mediaforge.log works from a real console). Root
cause: the tray is linked -H windowsgui (no console), so the "start" cmd
builtin spawns a throwaway cmd that flashes and dies before handing off.

Fix (cmd/tray/main.go): openLog() and openFile() now launch the file
directly via exec.Command("notepad.exe", path).Start() instead of
cmd /c start. openLog keeps its "no log yet" dialog and adds an error
dialog if notepad fails; openFile (used by the Config menu item) now uses
notepad too. GOOS=windows go build -H windowsgui ./cmd/tray clean.

=== Prior session: restore installer location choice ===

The installer stopped offering a "Select Destination Location" page. Root
cause: a prior session switched to a per-user install
(PrivilegesRequired=lowest). Inno Setup's DisableDirPage defaults to "auto",
which suppresses the dir page for per-user {auto...} installs, so
DefaultDirName={autopf}\MediaForge went straight to
%LOCALAPPDATA%\Programs\MediaForge with no prompt.

Fix: added DisableDirPage=no to [Setup] in installer/mediaforge.iss. The
destination page appears again; still a per-user, no-elevation install.
No Go code changed. Nothing outstanding.

=== Prior session: tray self-logging to tray.log ===

Problem: the tray produced no log, so right-click/errors couldn't be
diagnosed. Root cause: the tray is linked -H windowsgui (no console), so
every fmt.Fprintf(os.Stderr, ...) diagnostic was discarded. A user-added
init() redirected the log package to tray.log but nothing used log.*, and
it could panic (log.SetOutput(nil) on a fresh install where the logs dir
didn't exist yet).

Fixes in cmd/tray/main.go:
- Hardened init(): MkdirAll the logs dir first, bail (keep default stderr)
  if OpenFile fails instead of SetOutput(nil), set "tray: " prefix +
  timestamps, write a "----- tray started -----" marker.
- Converted all 9 fmt.Fprintf(os.Stderr,...) diagnostics to log.Printf/
  log.Println, dropping the now-redundant "tray: " message prefix.

Note: tray.log (tray's own log) is separate from mediaforge.log (the server
log opened by the View Logs menu) — intentional so tray errors don't depend
on the server starting.

Verified: GOOS=windows go build -ldflags "-H windowsgui" ./cmd/tray, vet,
and go test ./cmd/tray all clean.

=== Prior session: browse cache timeout configurable + early-exit ===

Issue #11 — WarmCountCache timed out at a fixed 30s on large SMB shares,
leaving dashboard stats empty. Changes:

- internal/config/config.go: added IntakeConfig.CacheTimeoutSeconds
  (yaml: cache_timeout_seconds), default 60, clamped to >=1 in Load().
- internal/browse/browse.go: WarmCountCache now takes a timeout arg
  (time.Duration; <1 falls back to 60s). Added an early os.Stat(mediaRoot)
  accessibility check that logs a warning and returns immediately if the
  media root is unreachable (common disconnected-drive case), instead of
  spinning up a walk that just burns the timeout.
- Callers updated: cmd/mediaforge/main.go and internal/winsvc/service.go
  pass time.Duration(cfg.Intake.CacheTimeoutSeconds)*time.Second.
- internal/api/handler.go: GET config returns intake_cache_timeout_seconds;
  UpdateConfigRequest accepts it (validated 1..3600).
- web/templates/index.html: new "Cache warm timeout" number field in the
  Intake settings group + loadSettings populate.
- Docs: installer/default-config.yaml, MEDIAFORGE_SPEC.md, README.md schema
  blocks updated with cache_timeout_seconds: 60.

Nil-pointer guard the prompt also asked for was already present in the walk
callback (nil DirEntry skip) and the ancestor-propagation loop (create-on-
demand) from a prior session — no change needed.

Verified: go build ./... and GOOS=windows go build ./... clean; go test ./...
all pass.

=== Prior session: tray critical fixes + left-click dashboard ===

Fixed four tray issues in cmd/tray/main.go (plus build flag / CI):

- #1 Orphaned mediaforge.exe on Exit: killMediaForge() rewritten to poll for up
  to 5s, re-issuing taskkill /F each iteration and confirming the process is
  actually gone (via mediaForgeRunning) before returning. A taskkill "not found"
  exit now counts as success. Exit/Restart handlers were already calling it.
- #2 Left-click opens dashboard: switched the systray dependency from the
  unmaintained github.com/getlantern/systray v1.2.2 (which hardcoded showMenu on
  BOTH left+right click, no hook) to fyne.io/systray v1.12.2 — a drop-in API
  match. onReady now calls systray.SetOnTapped(openBrowser(baseURL)); right-click
  still opens the menu (default fallback when no secondary handler is set).
  go.mod/go.sum updated via `go mod tidy` (dropped getlantern/* + go-stack/bpool,
  added godbus/dbus).
- #8 Tray console window: build now links the tray with `-H windowsgui` so
  Windows allocates no console (no black command window). Applied in build.ps1
  and .github/workflows/release.yml. dev-build.yml does not build the tray.
- #9 View Logs "exit status 1": new openLog() checks logsPath() first; if the
  log file is absent it creates the parent dir and shows a native error dialog
  (showMessage) with the full %APPDATA%\MediaForge\logs\mediaforge.log path
  instead of a silent shell failure. Existing log opens as before.

Also cleaned up two pre-existing installer/mediaforge.iss warnings:
- ArchitecturesAllowed / ArchitecturesInstallIn64BitMode: x64 -> x64compatible
  (x64 arch id was deprecated in Inno Setup 6).
- Switched from PrivilegesRequired=admin to =lowest (per-user install). This
  resolves the admin-hive vs HKCU Run-key mismatch: install dir ({autopf} now
  resolves to %LOCALAPPDATA%\Programs) and the autostart Run key live in the
  same user hive. No component required admin (no service; user data already
  lives in %APPDATA%).

Verified: GOOS=windows go build (-H windowsgui) + vet + test-compile ./cmd/tray
clean; go build ./... and go test ./... all pass; ISCC compiles mediaforge.iss
with no warnings.

=== Prior session: fix tray first-run config not persisting (UTF-8 BOM) ===

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
