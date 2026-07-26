CURRENT STATE NOTE

=== Latest session (cont. yet again): third live bug — ResolveReviewEntry ("Pick Selected") had its own dead-end duplicate check ===

Direct continuation, found while live-testing v1.7.0 right after release. User
manually identified a multi-part episode (S10E06) via Search Manually + Pick
Selected; it hit a duplicate at the resolved destination and displayed a
plain-text "file already exists at library destination: ..." reason with only
Pick Selected/Search Manually/Discard buttons — no comparison, no way to act
on it, and no auto-resolution applied even though the bitrate difference was
obviously a real upgrade (confirmed manually: 8.06 vs 4.78 Mbps, ~69% higher).

Root cause: `ResolveReviewEntry` (internal/api/handler.go, backs the
"Pick Selected" manual-search flow used for metadata_failure and
unresolved_multipart categories) had its OWN separate, older
duplicate-at-destination check — written before this session's auto-resolution
work — that just wrote a plain reason string via
`UpdateReviewEntryReason` and returned 409, completely bypassing both
`internal/upgrade`'s auto-resolution rules AND the DuplicateInfo/category
mechanism the UI needs to render the comparison panel + Replace/Keep
Existing buttons. This was a THIRD duplicate-check call site (distinct from
the intake watcher's `checkDuplicate` and the post-encode path in
`jobs/worker.go`, both already wired to auto-resolution earlier this
session) that got missed.

Fix (internal/api/handler.go):
- On a duplicate-at-destination, probes both files (new
  `probeDuplicateFileInfo` helper) and runs the same `upgrade.Decide` check.
- `upgrade.Replace` → kicks off the same async `startReplaceMove` used by
  the Review Queue's Replace button (progress bar, resolves the entry on
  completion).
- `upgrade.Keep` → removes the incoming file, marks the entry "discarded".
- Ambiguous → new `SQLiteStore.ConvertReviewEntryToDuplicate(id, reason,
  duplicateInfoJSON)` (internal/store/sqlite.go) converts the entry in
  place: sets `DuplicateInfo` + `category = "duplicate"` so a subsequent
  reload of the Review Queue renders the existing duplicate-comparison UI
  (`web/templates/index.html`'s category-based rendering already handles
  this — no frontend changes needed, confirmed by inspection).
- New `ReviewQueueStore` interface method + mock implementation
  (internal/api/handler_test.go).
- Updated `TestResolveReviewEntry_DuplicateAtDestination` to also assert the
  ambiguous case now converts to `category="duplicate"` with populated
  `DuplicateInfo`, on top of the pre-existing 409/pending/source-untouched
  assertions (still ambiguous in the test since the placeholder test files
  have no real probeable video data).

Verified live end-to-end immediately after redeploying (build 210): re-ran
Pick Selected on the same S10E06 entry — log showed "Review resolve:
duplicate auto-resolved, replacing existing library file" followed by
"Review: replaced existing file with incoming", confirming the full chain
(manual resolve → auto-resolution check → async move → completion) works.

Minor, NOT fixed this session (cosmetic only, no data-safety impact):
after that successful resolve, the Review Queue card didn't disappear from
the live page until the user manually refreshed, even though the
`/resolve` response was a success status that should have triggered
`reviewPick()`'s existing `reviewRemoveCard(id)` call (web/templates/
index.html:6212-6214) immediately. Root cause not identified — user
confirmed a manual refresh correctly showed the queue empty (server-side
state was correct throughout), so this is a live-UI-refresh gap only, not
a backend defect. Flagged here for a future session if it recurs/annoys.

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass. Redeployed via `update-mediaforge.ps1` (build 210) before the
live verification above.

=== Prior session (cont. once more): the REAL bug — restart + auto-resolve silently overrides pending manual Review Queue decisions ===

Direct continuation. The `reviewMoveMu` serialization fix (below) turned out
NOT to be the actual root cause of anything observed live — a real 24-file
concurrent intake batch succeeded with zero errors right after that fix,
which undercuts "concurrent moves to this share fail" as an explanation.
The `reviewMoveMu` fix is still kept (it's a reasonable safety net restoring
old one-at-a-time behavior), but the REAL bug is different and more serious:

`Watcher.known` (internal/intake/watcher.go) — the in-memory set tracking
"already seen this file this process lifetime" — is never persisted. Every
`update-mediaforge.ps1` redeploy force-restarts the process, wiping `known`
back to empty. The next folder scan then treats every file still sitting in
`Incoming` as brand new, INCLUDING files that already have a pending Review
Queue duplicate entry a human hasn't acted on yet. Before this session's
auto-resolution feature, reprocessing was harmless (worst case: re-detect
the same duplicate, no-op due to AddToReviewQueue's existing pending-dedup).
With auto-resolution now live, reprocessing can SILENTLY auto-replace or
auto-keep the file on its own — racing ahead of, and orphaning, the original
pending Review Queue entry (which now points at a source file that's already
gone). Confirmed live: the redeploy after the `reviewMoveMu` fix caused
exactly this — a 24-file intake batch auto-resolved every one of those files
independently, and clicking Replace afterward on the original (now-stale)
Review Queue entries failed with "The system cannot find the file
specified" (the source path had already been consumed).

Fix: new `SQLiteStore.HasPendingReviewEntry(originalPath) (bool, error)`
(internal/store/sqlite.go) — same query `AddToReviewQueue` already used to
dedup ("WHERE original_path = ? AND status = 'pending'"), exposed one level
up. Wired into `runPipeline` (internal/intake/watcher.go), which both the
watcher's own scan path (`process`) and the manual API path (`ProcessFile`)
funnel through — if a pending entry already exists for this exact path, skip
entirely (no ffprobe, no codec routing, no auto-resolve, nothing) and leave
both the file and the existing pending entry untouched. This closes the gap
for every intake code path, not just auto-resolution specifically.

New test: `internal/store/review_queue_test.go`'s `TestHasPendingReviewEntry`
(before/with-pending-entry/after-resolution cases). Did NOT add a
`runPipeline`-level regression test — existing watcher tests deliberately
avoid calling `runPipeline`/`process` directly since it shells out to a real
`ffprobe` binary (existing tests call `moveHEVCToLibrary` directly instead,
bypassing that call); adding one reliably would need a proper ffprobe
mock/stub that doesn't exist in this codebase yet.

Cleanup performed this session (via the live running app's own API, not a
direct DB edit): confirmed all 24 of the orphaned Review Queue entries
correctly had their file already present at the correct library destination
(spot-checked 3, all correct), then bulk-discarded all 24 via
`PUT /api/review/bulk/discard` — safe, since `discardReviewEntryFile`
no-ops via `os.IsNotExist` when the incoming path is already gone. Review
Queue confirmed empty (0 entries) afterward.

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass. Redeployed via `update-mediaforge.ps1` after this fix (see git
log / build number if reading this out of order). NOT yet re-verified live
against an actual restart-during-pending-duplicate scenario (would need to:
drop a duplicate, let it land in Review Queue, restart the app before
touching it, confirm the entry is still pending and unresolved rather than
silently auto-consumed) — recommend testing this specific scenario if it
comes up again, since it's exactly what broke this session.

=== Prior session (cont.): live bug found immediately after deploy — bulk Replace concurrency vs. network share ===

Direct continuation of the session below. Deployed via `update-mediaforge.ps1`
and watched the live log (Monitor tool). User ran a real bulk Replace against
~11 duplicate Stargate SG-1 S07 entries whose destination is a network share
(`\\TOWER\Media\...`). Every single one failed within the same millisecond
with a Windows sharing-violation error ("The process cannot access the file
because it is being used by another process") on the very first rename step
(local incoming file -> `<dest>.mediaforge.tmp` on the network share).

Confirmed no data loss before investigating further: all source files still
present in `Incoming`, and no review entries were marked resolved (the code
only calls `UpdateReviewQueueStatus(..., "resolved")` after a successful
move) — this is a "nothing happened" failure, not a corruption/loss one.

Root cause: `BulkReplaceReviewEntries`/`startReplaceMove` (added this
session) spawns one goroutine PER entry with no coordination between them.
The prior (pre-this-session) bulk replace was a synchronous for-loop, one
move at a time. Firing 11 concurrent renames at the exact same instant
against the same remote SMB share tripped a sharing violation on every one
of them — something about simultaneous CreateFile/MoveFile calls to the same
network destination directory that a sequential loop never exercised.

Fix: new `Handler.reviewMoveMu sync.Mutex` (internal/api/handler.go),
locked for the duration of the actual `SafeMoveWithProgress` call inside
`startReplaceMove`'s goroutine. Moves are still asynchronous from the HTTP
response/progress-bar's perspective (the request returns immediately, and
progress events still stream over SSE), but the real file I/O across all
Replace calls (single or bulk) is now serialized to one at a time again,
matching the old safe behavior.

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass. NOT yet re-verified live against the same real batch — recommend
the user redeploy (`update-mediaforge.ps1`) and re-run the same bulk Replace
against those Stargate SG-1 S07 entries (still sitting pending in the Review
Queue, untouched) to confirm they now succeed one-at-a-time instead of all
failing simultaneously.

=== Prior session: intelligent duplicate auto-resolution + async Replace progress bar ===

User asked for two Review Queue improvements: (1) obvious upgrades (higher
resolution/bitrate) should auto-replace instead of always sitting in the
Review Queue, and (2) a progress indicator while a file is being moved to
the library. Both implemented; this deliberately changes the CLAUDE.md
standing rule "never auto-overwrite duplicates" — updated that rule in
CLAUDE.md to describe the new opt-out (`intake.duplicate_resolution:
"manual"`) auto-resolution behavior rather than leave it contradicting the
code.

Auto-resolution:
- New package `internal/upgrade` (`upgrade.Decide`) holds the shared
  decision logic — resolution mismatch wins outright; else HEVC/AV1 beats
  AVC/older at equal resolution; else ≥`duplicate_bitrate_upgrade_threshold`
  (default 0.25 = 25%) higher bitrate at equal resolution/codec wins; else
  ambiguous → Review Queue. It has no dependency on `internal/intake` or
  `internal/jobs` specifically so both packages (which cannot import each
  other — `intake` imports `jobs` to enqueue encodes) can share one
  implementation instead of duplicating the rule.
- New config: `intake.duplicate_resolution` (`"manual"`|`"auto"`, default
  `"auto"`) and `intake.duplicate_bitrate_upgrade_threshold` (default
  `0.25`), in `internal/config/config.go`.
- `internal/intake/duplicate.go`'s `checkDuplicate` gained two params
  (resolution mode, bitrate threshold) and now calls `upgrade.Decide`
  before falling back to `dupSendReview`; new `dupAutoReplace`/`dupAutoKeep`
  decisions. `internal/intake/watcher.go`'s `moveHEVCToLibrary` handles all
  three outcomes (review/auto-keep removes incoming file/auto-replace moves
  it, all logged).
- Applied to the SECOND duplicate-check call site too (per CLAUDE.md's
  warning that watcher.go/worker.go's two intake paths — HEVC direct and
  AVC→encode-queue — must be kept consistent): `internal/jobs/worker.go`'s
  post-encode library-move duplicate check now runs the same
  `upgrade.Decide` before falling back to a Review Queue entry.
- MEDIAFORGE_SPEC.md's "Intelligent Duplicate Resolution" future-enhancement
  section marked implemented (VMAF tiebreaker still explicitly deferred —
  ambiguous same-resolution/same-codec-tier/close-bitrate cases still go to
  Review Queue, they just don't get a VMAF-based decision yet).
- New tests: `internal/upgrade/upgrade_test.go` (pure decision-logic cases).

Async Replace + progress bar:
- `internal/util/file.go`: new `SafeMoveWithProgress(src, dst,
  onProgress)` — same rename-then-copy-fallback semantics as `SafeMove`,
  but the cross-device copy fallback reports byte progress via a counting
  `io.Reader` wrapper (throttled to one callback per 250ms plus a final
  call, so large files don't flood callbacks). The same-device
  `os.Rename` fast path reports a single 100% callback since a rename has
  no measurable copy phase.
- `internal/jobs/job.go`/`queue.go`: `Job` gained a `Kind` field (empty =
  normal transcode job, `"move"` = ephemeral file-move progress event);
  new `Queue.BroadcastMoveEvent(eventType, job)` broadcasts over the
  existing `/api/jobs/stream` SSE channel WITHOUT touching the transcode
  jobs map/persistence/Stats() — move jobs never appear in the Encode
  Queue tab or job counts, they're purely progress-bar carriers for the
  Review Queue UI.
- `internal/api/handler.go`: `ReplaceReviewEntry` and
  `BulkReplaceReviewEntries` are now async — they validate the duplicate
  entry synchronously (so bad requests still 400 immediately) then spawn
  a goroutine (`startReplaceMove`) that calls `SafeMoveWithProgress` and
  broadcasts `move_started`/`move_progress`/`move_complete`/`move_failed`
  events, updating the review entry to `"resolved"` only once the move
  actually completes. Both handlers now respond `202 Accepted` with
  `job_id`(s) instead of blocking until the move finishes.
- `web/templates/index.html`: review cards gained a hidden
  `#rmove-<entryId>` placeholder; `reviewReplace`/`reviewReplaceSelected`
  register the returned `job_id` → entry id in `_reviewMoveJobs`, and a
  new SSE branch routes `move_*` events to `reviewUpdateMoveProgress`,
  which renders the same `.progress-bar`/`.progress-fill` markup used by
  the Encode Queue tab, hides the action buttons while moving, and removes
  the card on `move_complete` (or shows the error and restores the
  buttons on `move_failed`).
- Existing `TestBulkReplaceReviewEntries` (internal/api/handler_test.go)
  updated for the new 202/async response shape — added a `waitFor` polling
  helper and a mutex on the test's mock review store (now touched from a
  background goroutine).

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite,
including new `internal/upgrade` tests and updated/new `internal/util`,
`internal/api` tests) all pass. Node `--check` run against the extracted
inline `<script>` from `web/templates/index.html` to catch JS syntax
errors (no real browser available in this environment) — did NOT
click-test the actual progress bar rendering live in a browser; recommend
the user redeploy and manually confirm: (1) dropping a higher-resolution
duplicate auto-replaces without a Review Queue entry appearing at all
(check the log for "auto-resolved: incoming resolution..."), (2) clicking
Replace on a genuine duplicate entry shows a live progress bar and the
card disappears when the move finishes, (3) a large cross-device move
actually shows intermediate percentages rather than jumping straight to
100%.

Nothing else outstanding from this session.

=== Prior session: Review Queue category + bulk actions (closes roadmap item A) ===

Implemented the "Review Queue: context-aware actions + bulk actions beyond
Discard" item from COMPREHENSIVE_STATUS_AND_ROADMAP.md (planned 2026-07-25,
this session implemented it). Full plan, decisions, and rationale are in
the session's plan file; summary:

- New `ReviewEntryCategory` (duplicate, metadata_failure, encode_failure,
  unresolved_multipart, system_failure) added to `ReviewEntry`
  (internal/store/sqlite.go), schema bumped to v11 with a migration for
  existing installs (pre-upgrade rows get category="", treated as the
  metadata_failure-equivalent fallback everywhere it's read). Set
  explicitly at all ~20 send-to-review call sites across
  internal/intake/watcher.go and internal/jobs/worker.go (via
  internal/jobs/queue.go's SendToReviewQueue/SendDuplicateToReviewQueue,
  which gained a trailing category string param).
- internal/api/handler.go: ResolveReviewEntry/ResubmitReviewEntry now
  reject entries whose category doesn't support the action — this is what
  fixes the real dead-button bug (Pick Selected rendered, and did nothing,
  on encode-failure entries like "SmartShrink: no viable encode").
- New bulk endpoints PUT /api/review/bulk/{discard,replace,resubmit},
  each taking {"ids":[...]} (resubmit also takes the shared encode
  overrides). Per-item error collection — one bad/stale id never aborts
  the whole batch. Refactored shared per-entry logic out of the existing
  single-item handlers (discardReviewEntryFile, replaceReviewEntryFile,
  enqueueResubmit, resolveResubmitPreset, validateResubmitOverrides) so
  single and bulk share one implementation. New
  BulkUpdateReviewQueueStatus store method replaces N sequential updates.
- web/templates/index.html: reviewCardHTML's actionsHTML now switches on
  category instead of the old duplicate/non-duplicate binary check; added
  a category badge per card. Bulk action bar shows Discard always,
  Replace only when the whole selection is duplicates, Re-encode only
  when the whole selection is encode failures (mixed selections hide the
  category-specific buttons). reviewDiscardSelected() now calls the bulk
  endpoint instead of looping single-item fetches; new
  reviewReplaceSelected() and a bulk re-encode settings panel
  (reviewBulkResubmitSelected) round out the three actions.

Verified: go build/vet/test all pass (full suite, including new store/
jobs/intake/api tests for category assignment, bulk endpoints, and
category-gating rejections). Also verified LIVE: built the binary,
seeded a real SQLite review_queue with one entry per category (via a
temporary cmd/seedtest/main.go, deleted after use — not committed),
launched the server, and confirmed via the API + a Node harness running
the actual page JS that every category renders its correct button set
and badge. Exercised all three bulk endpoints against the running
server: bulk resubmit correctly reverted entries to pending with an
explanatory reason when ffprobe failed on placeholder test files
(confirms the no-silent-failures fallback works), bulk replace moved a
real file over an existing one and removed the incoming duplicate, bulk
discard removed two more. A mixed-category bulk replace call correctly
reported both ids as failed rather than silently no-op'ing.

Known, deliberate limitation (documented in code, not fixed this
session): reviewGetSelected()/reviewSelectAll() only consider the
currently rendered page — no cross-page bulk selection. UI shows
"(this page only)" on the select-all label when there's more than one
page. Fixing this is separate, larger scope (would need either a
server-side "select all matching filter" id list, or persisting
selection across loadReviewQueue() page reloads).

Nothing else outstanding from this session — all 5 planned phases
(schema/store, write-path threading, single-item gating fix, bulk
backend, bulk frontend) completed and committed separately, each with
passing tests before moving to the next phase.

=== Prior session (cont.): two more reconciliation bugs found live, after v1.5.0 shipped ===

Direct continuation of the same session. User built/deployed/tested v1.5.0
live (combined multi-part episode detection) and ran a real Stargate SG-1
S01 batch through it. Reported two things, traced from real log excerpts:

1. "S01E19 and S01E20 both are trying to go to the same file but have 2
   different episode titles" — filename "Politics" (source S01E20) and
   filename "There But For the Grace of God" (source S01E19) were both
   landing at the same destination path. Root cause: episode-title
   reconciliation (findAdjacentSeasonEpisode -> findEpisodeByTitle,
   existing logic) correctly detected the mismatch for "Politics" (TVDB's
   actual S01E20 is "There But For the Grace of God", similarity 0.10) and
   correctly searched the whole series for "Politics" — but rejected the
   real match ("Politics (1)" at S01E21, similarity only 0.80 against the
   bare filename title) as "not confident enough" (needs >=0.90), then
   SILENTLY fell back to keeping the original wrong episode
   ("There But For the Grace of God") as if it were a normal match. Worse:
   this scored confidence 0.87 (name+year+"episode exists" components
   alone are enough; the 15%-weighted title-mismatch penalty wasn't enough
   to drag it below the 0.85 auto-accept threshold) — meaning this would
   have SILENTLY overwritten the library with the wrong title if an
   unrelated duplicate-file collision hadn't happened to catch it first.
2. User clarified when asked: TVDB's "(1)" suffix here doesn't mean a
   same-titled two-parter — "Politics" and its story-continuation episode
   have genuinely DIFFERENT titles, TVDB just tacks "(1)" onto the first
   half of a two-part STORYLINE regardless of whether the second half
   shares the name. (This matters for the fix design: stripping the
   suffix generically, without assuming a matching "(2)" title exists
   elsewhere, is the correct approach — which is what was already
   implemented, so no further change was needed for this clarification.)

Two fixes, both in internal/intake/tvdb.go:
1. New TVDBResult.TitleMismatchUnresolved bool, set in the mismatch branch
   when neither findAdjacentSeasonEpisode nor findEpisodeByTitle finds a
   confident match (mirrors the MultiPartUnconfirmed pattern from earlier
   this session). Propagated through orchestrator.go's LookupResult (same
   pattern as Episode2/MultiPartUnconfirmed) and fromTVDB. watcher.go's
   resolveAndGate checks it immediately after the MultiPartUnconfirmed
   check, BEFORE the Confidence threshold checks, and routes to Review
   Queue with a reason naming both the filename's title and TVDB's
   (wrong) title at that number — so a result already proven mismatched
   can never sail through on a high raw confidence score again.
2. New bestTitleSimilarity(a, b) + stripTrailingPartSuffix (regex
   `\s*\(\d+\)\s*$`): compares two titles both raw AND with either side's
   trailing " (N)" TVDB disambiguator stripped, returning the max
   similarity. Used in findEpisodeByTitle's per-candidate scoring and
   findAdjacentSeasonEpisode's >=0.90 check (both previously used bare
   stringSimilarity). "Politics" vs "Politics (1)" now scores 1.0 after
   stripping instead of 0.80, so it clears the 0.90 confident-match bar
   and resolves correctly to the real episode (S01E21) instead of leaving
   the file stuck on the wrong number.

Test changes (internal/intake/tvdb_test.go, watcher_test.go): the original
test written for item 1 (TestTVDBLookup_UnresolvedTitleMismatchFlagged /
TestResolveAndGate_UnresolvedTitleMismatchRoutesToReview) used the real
"Politics (1)" mock data and asserted TitleMismatchUnresolved=true — this
started FAILING once fix 2 was added, because the fix means "Politics" now
correctly resolves instead of staying unresolved. This is the CORRECT
outcome (bug fixed), not a regression — renamed/repurposed those two tests
into positive-path regression tests (TestTVDBLookup_ReconcileTitleWithPartSuffix /
TestResolveAndGate_ReconcilesTitleWithPartSuffix, asserting the correct
S01E21 resolution), and added a new TestTVDBLookup_GenuinelyUnresolvedTitleMismatchFlagged
with a totally-unrelated filename title (no plausible match at all) to keep
coverage of the TitleMismatchUnresolved flag itself.

Verified: go build ./..., go vet ./..., go test ./... (full suite,
including all new/renamed tests) all pass.

Committed on develop (single commit covering both fixes + test changes).
CHANGELOG.md/CURRENT_STATE.md updated as a same-session follow-up commit
(missed doing this in the same commit as the code change — noting so a
future session doesn't wonder why the split). NOT yet redeployed/re-tested
live against the actual remaining S01E19/S01E20-area files in the user's
real batch — recommend rebuilding via update-mediaforge.ps1 and confirming
in the log that "Politics" now resolves straight to
"TVDB: corrected season/episode by episode name ... new_episode=21
matched_title=\"Politics (1)\"" instead of the old silent-wrong-fallback
behavior, and that any remaining episodes in the S01E19-22 area file
correctly instead of continuing to collide.

=== Prior session (cont.): combined multi-part episode detection + naming ===

Direct continuation of the same session, resuming the scoping paused
earlier when the user wanted to verify the TVDB title-reconciliation fix
live first (that verification and the pause/tray fixes above are now
released as v1.4.1/v1.4.2). This closes out open-bugs.md item #2
(multi-episode naming) plus the harder "single-numbered file that TVDB
considers two episodes" case surfaced by the live Stargate SG-1 test.

Design, confirmed with the user via AskUserQuestion before implementing:
- Destination filename/title keeps the SOURCE's own episode title (e.g.
  "Children of the Gods_ Parts 1 & 2", sanitized) rather than combining or
  replacing it with TVDB's per-episode titles — the source file was written
  for exactly this combined content.
- If the title signal fires (filename title says "Parts 1 & 2" etc.) but
  duration can't confirm it actually contains both episodes, route to
  Review Queue rather than guess (no-silent-failures principle).

Implementation:
- internal/intake/tvdb.go: new reMultiPartTitle regex + isMultiPartTitle
  helper (catches "Parts 1 & 2", "Part 1 and 2", "1 & 2", etc., case-
  insensitive). tvdbEpisode gained Runtime int (TVDB's per-episode minutes,
  json "runtime"). TVDBResult gained Episode2 int and MultiPartUnconfirmed
  bool. Lookup's signature gained a probeDuration time.Duration param
  (probe.Duration from the intake pipeline; 0 skips the check, used by the
  manual-search API path which has no real file to probe). New detection
  block after the existing episode-name-reconciliation logic: when the
  resolved episode's title matches the multi-part pattern, fetches
  episode+1 via the existing fetchEpisode helper and compares
  probeDuration against (episode.Runtime + nextEpisode.Runtime)*1min with
  15% tolerance (new multiPartRuntimeTolerancePct const) — confirmed sets
  Episode2, unconfirmed sets MultiPartUnconfirmed and logs why (no runtime
  data vs. duration mismatch, distinguishable in the log).
- internal/intake/orchestrator.go: LookupResult gained Episode2/
  MultiPartUnconfirmed (TV-only, zero-value for movie/TMDB/OMDb results).
  LookupTV gained the same probeDuration param, passed through to
  TVDB.Lookup; fromTVDB converter carries the two new fields.
- internal/intake/watcher.go: resolveAndGate now computes probeDuration
  from probe.Duration (nil-safe — probe can be nil in some call paths,
  guarded explicitly; this was actually a LATENT nil-deref bug this change
  would have introduced for the TV path, caught by an existing test
  passing probe=nil, fixed with a nil check before the change was
  committed). New early-exit: if result.MultiPartUnconfirmed, routes to
  Review Queue with a reason mentioning "multi-part episode" before the
  normal confidence-threshold gating runs. Metadata-merge block: when
  result.Episode2 > 0, merges it into parsed.Episode2 AND sets
  parsed.EpisodeTitle = parsed.ParsedEpisodeTitle (the source's own title)
  instead of the normal `parsed.EpisodeTitle = result.EpisodeTitle`
  (TVDB's title) — this is the one place in the file where TVDB's title is
  deliberately NOT trusted, per the user's explicit design decision above.
- internal/intake/naming.go: applyNamingTemplate's {episode:02d} token now
  renders "01-E02" (not just "01") when parsed.Episode2 > 0, producing
  "S01E01-E02" from the existing "S{season:02d}E{episode:02d}" template
  shape. This also fixes the naming side of the OTHER, simpler case noted
  in open-bugs.md item #2: a filename that already encodes a range like
  S01E01E02 was correctly parsed into Episode/Episode2 by parse.go
  already, but the naming template never read Episode2 — now it does,
  for both sources of Episode2 (filename-parsed and TVDB-confirmed).
- internal/api/handler.go: the manual-search LookupTV call site
  (SearchReviewEntry) updated for the new probeDuration param, passing 0
  (no real file to probe from a manual title search).

New tests:
- internal/intake/tvdb_test.go: TestTVDBLookup_CombinedMultiPartConfirmedByDuration
  (92-minute probe matches two 46-minute TVDB runtimes -> Episode2=2) and
  TestTVDBLookup_CombinedMultiPartUnconfirmed (probeDuration=0 ->
  MultiPartUnconfirmed=true, Episode2=0). Both reproduce the real Stargate
  SG-1 S01E01 case (tvdb_id 72449, "Children of the Gods (1)"/"(2)").
- internal/intake/watcher_test.go: two new shared fixtures
  (combinedEpisodeMockTVDB, combinedEpisodeParsed) plus
  TestResolveAndGate_MultiPartUnconfirmedRoutesToReview (nil probe -> ok=false,
  1 review queue entry, reason mentions "multi-part") and
  TestResolveAndGate_MultiPartConfirmedKeepsSourceTitle (92min probe ->
  ok=true, parsed.Episode2=2, parsed.EpisodeTitle stays the source's own
  title, not TVDB's).
- internal/intake/naming_test.go: TestApplyNamingTemplateEpisode2Range
  (Episode2=2 -> "S01E01-E02" in the rendered filename) and
  TestApplyNamingTemplateSingleEpisodeUnaffected (regression guard: a
  normal single-episode file's rendered name is unchanged by this feature).

Verified: go build ./..., go vet ./..., go test ./... (full suite,
including all 6 new tests) all pass.

NOT yet committed/released as of writing this note, and NOT yet verified
against a live intake run — recommend the user redeploy
(update-mediaforge.ps1) and re-drop the actual Stargate SG-1 combined pilot
file (or any similarly-combined episode) to confirm in the log: "TVDB:
confirmed combined multi-part episode" appears with the right
episode/episode2 numbers, and the file lands in the library as
"...S01E01-E02 - <source's own title>.mp4" instead of just "S01E01 -
Children of the Gods (1).mp4" as it did before this feature existed.

=== Prior session (cont.): tray menu now exposes intake pause separately from queue pause ===

Direct continuation of the Pause fix below, same session. User clarified the
exact intended model for pipeline controls after the Pause fix landed:
- A tray control to stop intake, period — only stops files moving from the
  Incoming folder into the pipeline. Does not touch the encode queue.
- A separate "Pause Queue" control, period — only stops new jobs from
  starting. Files can still be added to the queue while paused, and it does
  not stop whatever job is currently encoding (this half was already fixed
  below).
- Per-item Cancel in the web UI queue list, period — already exists,
  untouched.

Investigated and found the backend for intake pause already existed and was
fully wired (`POST /api/intake/pause` / `/api/intake/resume` /
`GET /api/intake/status` in internal/api/router.go + handler.go, calling
`Watcher.Pause()`/`Resume()` in internal/intake/watcher.go — `scan()`
early-returns while paused, leaving in-flight processing untouched) but was
never exposed in the tray or web UI. The tray's existing "Pipeline" checkbox,
"Pause Queue" checkbox, and "Start Queue"/"Stop Queue" buttons all called
`/api/queue/*` (WorkerPool) exclusively and cross-toggled each other
(clicking "Pipeline" also flipped "Pause Queue" and vice versa) — there was
no way to stop intake without also touching the queue, or vice versa.

Fix (cmd/tray/main.go, buildTrayMenu + a new shared postAPI helper): replaced
the old single conflated "Pipeline" checkbox and the separate "Start
Queue"/"Stop Queue" buttons with two independent checkboxes with no
cross-toggling: "Stop Pipeline" (calls /api/intake/pause /resume) and "Pause
Queue" (calls /api/queue/pause /start, unchanged target from before).
callQueueAPI/callIntakeAPI now both wrap a shared postAPI(path) helper
instead of duplicating the POST-and-log logic. "Stop Queue" (which called the
still-unimplemented /api/queue/stop no-op stub) was removed rather than kept
as a non-functional menu item.

Verified: go build ./..., go vet ./..., go test ./... all pass (cmd/tray is
Windows-only, build tag verified on this Windows machine). NOT yet manually
clicked through in a live tray session this round — recommend the user
redeploy and confirm: unchecking "Stop Pipeline" alone leaves an in-progress
encode running and the queue still accepting new jobs, and checking "Pause
Queue" alone still lets new files land in Incoming and get added to the
queue while no new job starts.

Web UI note: not changed this round — the web UI's "Pause Queue" controls
already only call /api/queue/pause (matches the desired model as of the fix
below); there is currently no intake-pause control surfaced in the web UI
either, only via this tray menu and the raw API. Not implemented since the
user's ask was specifically about the tray icon; mention if they want parity
in the web UI too.

=== Prior session (cont.): fixed Pause killing the running encode instead of just blocking new jobs ===

User reported (this was already flagged as open item #1 in the auto-memory
open-bugs note from the prior session): pausing the encode queue (tray
"Pause Queue", or either pause control in the web UI — the header toggle
and the footer "Stop Queue" button both call the same endpoint) stopped the
currently-running encode instead of just preventing new encodes from
starting. This is the same underlying mechanism that caused the SmartShrink
finalization crash fixed in the prior session/release (v1.4.1) — that fix
only stopped the crash side-effect of pause cancelling a job; it didn't
change the fact that pause still cancelled the job at all.

Root cause, confirmed by reading the code: `WorkerPool.Pause()`
(internal/jobs/worker.go) explicitly collected every running job, requeued
it via `queue.Requeue`, then called `CancelCurrentJob` — cancelling the
job's context, which kills the in-flight ffmpeg process. The "let the
current job finish, just block new ones" half of pause semantics already
existed structurally (the worker's run() loop rechecks `pool.IsPaused()`
before calling `queue.GetNext()` for a new job) — it just never got a
chance to matter because the running job was killed first, every time.

Fix (internal/jobs/worker.go, Pause() only): now just sets the paused flag
and counts currently-running jobs for reporting — no longer touches
`Worker.currentJob`, calls `CancelCurrentJob`, or touches the queue at all.
The now-unused-here `runningJob` struct/`sort` import are still used
elsewhere (SetWorkerCount's resize-down path, which legitimately does need
to cancel jobs when shrinking worker count) so nothing else changed there.

Also fixed two things that would otherwise have been left inconsistent by
this change:
- internal/api/handler.go PausePipeline: response field renamed
  `requeued` -> `in_progress` (nothing is requeued anymore), doc comment
  updated.
- web/templates/index.html: the footer "Stop Queue" button's confirmation
  modal previously said "This will stop all running jobs. They will return
  to the queue but must restart from the beginning." — true before this
  fix (Pause() used to hard-cancel), false after. Both the header pause
  toggle and the footer "Stop Queue" button call the same
  `/api/queue/pause` endpoint and always have — updated the footer modal's
  text to describe the actual soft-pause behavior instead of building out
  a separate hard-cancel code path. `WorkerPool.StopAll()` /
  `POST /api/queue/stop` remain an unimplemented no-op stub (pre-existing,
  explicitly TODO'd in the code) — a true hard-cancel action doesn't exist
  anywhere in the app right now. Not implemented this session since it
  wasn't asked for; flagged here in case the user wants a real "stop
  everything now, including the running job" control later.
- docs/api/jobs.md: Pause queue section updated to match (response field,
  description).

New test: internal/jobs/worker_test.go
TestWorkerPoolPause_DoesNotCancelRunningJob — constructs a WorkerPool with
a fake Worker holding a running Job directly (no real ffmpeg/queue needed,
since the fixed Pause() no longer touches the queue), calls Pause(), and
asserts: reported count is correct, pool.IsPaused() is true, the worker's
currentJob is untouched, and the job's Status is still "running" (not
requeued to "pending").

Verified: go build ./..., go vet ./..., go test ./... (full suite,
including the new test) all pass.

Committed on develop. Released together with the follow-up tray-menu fix
above via /release — confirm exact version/tag in git log if this note is
read out of order relative to the release commit.

NOT yet re-verified against a live pause-mid-encode run in this
environment (no real ffmpeg encode was in flight during this session) —
recommend the user redeploy (update-mediaforge.ps1) and confirm: start a
real transcode, click Pause (either control), and confirm in the log that
the running job's ffmpeg process is NOT killed and the job completes
normally, while no new job starts until Resume is clicked.

=== Prior session: fixed SmartShrink retry-loop finalization crash on a failed retry encode ===

User pasted a real log from pausing the queue mid-job:
`FFmpeg failed` (exit status 1, stderr trailing off mid mov_text subtitle
stream) -> `SmartShrink retry encode failed` -> `Job failed - finalization
error: failed to copy temp to final location: ... The system cannot find
the file specified`.

Root cause (internal/jobs/worker.go, SmartShrink size-retry loop,
~lines 925-969): every retry attempt reused the SAME tempPath as the prior
(already-successful) attempt, explicitly `os.Remove(tempPath)`-ing it right
before calling `attemptTranscode` again. If that retry's ffmpeg process then
failed for any reason — including simply being killed because
`WorkerPool.Pause()` cancelled the job's context mid-encode — the loop broke
out and fell through to `result = bestResult`, which still pointed at the
PREVIOUS successful result's metadata, but its backing file on disk had
already been deleted and never recreated. Finalization then tried to copy a
tempPath that no longer existed. This is a general bug (any retry failure
for any reason loses the previous best output), not exclusively a
pause-related one, though pause is what the user hit it via.

Fix:
- Each retry now encodes to a separate `tempPath + ".retry"` path instead of
  overwriting tempPath directly. Only promoted (old tempPath removed,
  `.retry` renamed onto tempPath) when the retry succeeds AND beats the
  current best size — so a failed or worse retry can never destroy the
  previously-best output that finalization will use.
- Added an explicit `jobCtx.Err() == context.Canceled` check right after the
  retry loop exits, mirroring the existing cancellation-handling block
  around the initial (pre-retry) encode attempt earlier in the same
  function. If the job was cancelled mid-retry (e.g. by Pause, which already
  calls `queue.Requeue` before cancelling), it's now treated the same way as
  that earlier check (log + `queue.CancelJob` or "interrupted by shutdown",
  then return) instead of falling through into finalize/Review-Queue logic
  and racing with whoever picks the requeued job up next.

Verified: go build ./..., go vet ./..., go test ./... (full suite) all
pass. NOT re-verified against a live pause-mid-SmartShrink-retry run in this
environment (no real ffmpeg/media here) — recommend the user reproduce the
original repro (pause the queue while a SmartShrink job is mid-retry) and
confirm the job now either completes cleanly with the retry's output or is
cleanly requeued/cancelled, with no more "finalization error: cannot find
the file specified" in the log.

Explicitly NOT addressed this session (separate, larger scope, per the
user's own memory note from 2026-07-24): the fact that Pause() cancels the
CURRENTLY RUNNING job at all, rather than letting it finish and only
blocking new jobs from starting. The user has already flagged this as a
distinct pause-semantics change they want to describe further before it's
scoped — this session's fix only stops the crash-on-cancel side effect, it
does not change what Pause() cancels.

=== Prior session: fixed TVDB episode-title reconciliation always silently failing (malformed pagination URL) ===

User reported a real production case: "Stargate SG-1 (1997) - S01E02 - The
Enemy Within.mp4" (source combines TVDB's S01E01+E02 into one file, throwing
every subsequent episode number in the season off by one) got moved to the
library as "S01E02 - Children of the Gods (2).mp4" — the wrong episode
number was kept and the correct filename-derived title ("The Enemy Within")
was overwritten, backwards from the intended behavior (CLAUDE.md/prior
sessions: on a title/number mismatch, trust the filename's episode title and
correct the number, not the other way around).

Root cause, found by reading internal/intake/tvdb.go: that reconciliation
logic (findAdjacentSeasonEpisode then findEpisodeByTitle, added in an
earlier session — see the "ReconcileWrongSeasonEpisode"/
"ReconcileAdjacentSeasonOffset" tests further down this file) already exists
and is structurally correct, but findEpisodeByTitle's whole-series page scan
built its URL as `/v4/series/{id}/episodes/default/page/{n}` (page as a path
segment) — not a valid TVDB v4 route. Every call returned HTTP 400 (confirmed
in the user's real log: "TVDB: findEpisodeByTitle non-200 response ...
status=400"), so the fallback silently returned ok=false every time it ran,
and the reconciliation branch fell through with the mismatch un-corrected.
The other working call, fetchEpisode, was already using the correct shape
(`/v4/series/{id}/episodes/default?season=N`), which is what made the
inconsistency easy to spot by comparison.

Fix (internal/intake/tvdb.go, findEpisodeByTitle only): send `page` as a
query parameter instead of a path segment, matching fetchEpisode's request
shape. Also fixed two stale doc comments referencing the old URL shape.

Two existing tests' mock servers were asserting against the old (bug-
matching) URL shape and had to be updated: TestTVDBLookup_
ReconcileWrongSeasonEpisode and TestTVDBLookup_ReconcileAdjacentSeasonOffset
(internal/intake/tvdb_test.go) both routed their mock's page-scan response
based on `strings.Contains(r.URL.Path, "/page/")` — switched both to
`r.URL.Query().Get("page") != ""`. Without this the first test actually
failed after the fix (asserted differently before only because the buggy
mock happened to serve the season-fetch body for every request, including
page-scan ones).

Verified: go build ./... and go test ./... (full suite, internal/intake
specifically) all pass, including the now-corrected
ReconcileWrongSeasonEpisode test. Files modified: internal/intake/tvdb.go,
internal/intake/tvdb_test.go, CHANGELOG.md. NOT yet re-verified against a
live rerun of the actual Stargate SG-1 file — recommend rebuilding/
redeploying and either re-running intake on a similarly-mis-numbered file or
re-triggering the existing S01E02 entry (if it's still sitting in the
library with the wrong name) to confirm the log now shows "TVDB: corrected
season/episode by episode name" with new_episode=3 (or whatever TVDB's real
numbering is) instead of the earlier silent-failure path.

=== Latest session (cont. once more): adaptive-target validated over ~45 real jobs, promoted to default, merged into develop ===

Direct continuation of the two sessions below. After the post-encode
verification removal (previous entry), continued watching the user's live
queue in real time (Monitor tool tailing the log) through ~45 more real
SmartShrink jobs across 6 different shows (Teenage Mutant Ninja Turtles
2012, Star Wars Rebels, Earth 2 1994, Lost in Space 1965, S.W.A.T. 1975,
Smallville 2001), covering both the "good" (85) and "excellent" (94) tiers
and both branches of the adaptive logic (ceiling below tier, ceiling above
tier). Zero false failures. Also incidentally confirmed the legitimate
"no viable encode" safety net (pre-existing, unrelated code) correctly
identified genuinely unshrinkable content (all of Earth 2 1994, one old
Lost in Space episode) without adaptive-target overriding it.

Separately noticed (not fixed, out of scope): a pre-existing bug where
"no viable encode" jobs get automatically re-queued and reprocessed from
scratch instead of staying resolved in Review Queue — watched the exact
same Earth 2 episode ("Flower Child") get reprocessed 3 times with
byte-identical results, including once across a ~7 hour unattended
overnight gap. This burns real GPU time on guaranteed-to-fail files. Not
investigated further this session (separate from adaptive-target) —
flagged for a future session if the user wants it fixed.

User decision after reviewing the validation results: promote
`smartshrink_adaptive_target` to the default (was `false`, opt-in), move
its Settings toggle out of the collapsed "Advanced" group into the
always-visible "Transcoding" group, and merge the branch into `develop`.
Implemented:
- `internal/config/config.go`: `DefaultConfig()` now sets
  `SmartShrinkAdaptiveTarget: true`; updated the field's doc comment
  (was stale — still described the removed post-encode verification pass
  and the old `false` default).
- `web/templates/index.html`: moved the "Adaptive VMAF Target" toggle
  from the `advanced-settings` group to the `Transcoding` group (right
  after "Allow same-codec re-encoding"), dropped the "(Experimental)"
  suffix from its label, and updated its description (removed the stale
  post-encode-verification sentence, added "On by default."). Left
  "VMAF Sample Count" in Advanced — it's a tuning knob, not a mode switch.
- `MEDIAFORGE_SPEC.md` and `CHANGELOG.md` updated to reflect the new
  default and the setting's new location; the CHANGELOG "Added" section
  no longer says "not merged."

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass after the default/UI changes.

Merged `experimental/adaptive-vmaf-target` into `develop` (see git log for
the merge commit). The branch is NOT deleted in case further follow-up
work is wanted; `develop` is now the source of truth for this feature.

Per explicit user instruction: after this merge, kept the Monitor tool
watching the user's live log (same `%APPDATA%\Mediaforge\logs\mediaforge.log`
tail) to continue observing the running queue, since the user said they
have limited coding headway remaining this session/week and wants to
mostly watch rather than keep making code changes. No further code changes
should be made unless the user asks or a new real problem shows up in the
log.

=== Prior session (cont.): removed post-encode VMAF verification after live testing found a structural bug; adaptive-target threshold logic validated and kept ===

Direct continuation of the adaptive-VMAF-target session below, same branch
(`experimental/adaptive-vmaf-target`, off `develop`, still NOT merged). User
deployed the feature to their live MediaForge install (via
`update-mediaforge.ps1`) and ran a real batch of ~20 SmartShrink jobs
(Teenage Mutant Ninja Turtles 2012 S02, Star Wars Rebels S01) with
`smartshrink_adaptive_target: true`, while this session watched the live
log in real time (via the Monitor tool tailing
`%APPDATA%\Mediaforge\logs\mediaforge.log`).

Ceiling probe + effective-threshold search + retry loop (the CORE adaptive-
target logic, described below) worked correctly across every single job:
correctly lowered the target when a source's ceiling fell below the tier
threshold, correctly left the raw tier threshold untouched when the ceiling
already cleared it, and the retry loop correctly pushed toward smaller
files using whichever target applied. This part is validated against real
production content and kept as-is.

The post-encode verification step (also described below) did NOT hold up:
- First false failure (TMNT S02E17, initial deploy): traced to
  `ExtractSamples`'s stream-copy mode snapping to mismatched keyframes
  between the source and the independently-encoded output — fixed by
  adding `vmaf.ExtractSamplesAccurate` (lossless re-encode instead of
  stream copy, for frame-accurate seeking).
- Second false failure (Star Wars Rebels S01E04, after deploying the above
  fix): per-sample scores degraded the further into the file the sample
  was (sample 0 fine, samples 1-3 progressively worse) — traced to
  `verifyPostEncodeVMAF` applying the SOURCE's probed duration to compute
  seek fractions for BOTH the source AND the output extraction; fixed by
  probing the output file's own real duration via `w.prober.Probe` and
  applying the same fractional positions to it independently.
- Third false failure (Star Wars Rebels S01E09, after deploying THAT fix
  too) — at this point disabled `smartshrink_adaptive_target` live via the
  running app's `PUT /api/config` (not a code change, just flipped the
  config flag through the API to stop disrupting the user's live queue)
  and investigated offline rather than guessing a fourth blind fix.
  Reproduced the exact failure locally: re-ran the real transcode command
  from the log against the real source file, confirmed via direct ffprobe
  that source and output have IDENTICAL frame rate/frame count/start_time
  (ruling out the duration theory entirely for this case), then extracted
  the same 20-second sample pair my code would extract and ran the actual
  VMAF scoring command with a per-frame CSV log
  (`libvmaf=...:log_fmt=csv:log_path=...`). This revealed the real root
  cause: score was correct (~80) for the first ~2 seconds (frame 0-46 of
  the 20s/24fps clip) then collapsed to near-zero for the rest of the
  clip. Spot-checking individual frames via `-ss` seeks at 1s/3s/5s/10s
  confirmed the source and output ARE correctly content-aligned throughout
  — so this isn't a timestamp/duration alignment problem at all. The real
  issue: `libvmaf` pairs frames by sequence index, not timestamp. Two
  independently re-extracted/re-encoded clips can end up with a differing
  frame count somewhere early on (a single dropped/duplicated frame during
  the lossless re-encode is enough — an ffmpeg "non monotonically
  increasing dts" warning was present in the reproduction), and once that
  happens every frame after that point compares mismatched content for the
  rest of the clip. This is a structural problem with "independently
  extract and compare two clips" as a verification method, not something
  fixable by adjusting seek-time math — the third fix attempt in a row
  would have been another guess, so it was not attempted.

Decision (confirmed with user): keep the adaptive-target threshold logic
(validated, real production benefit), remove the post-encode verification
step entirely rather than attempt a fourth fix against live production
traffic. Removed `Worker.verifyPostEncodeVMAF`
(`internal/jobs/worker.go`), `vmaf.ExtractSamplesAccurate`
(`internal/ffmpeg/vmaf/sample.go`) and its test, and the
`postEncodeVerifyTolerance` constant. The call site in `processJob` that
invoked verification (between the SmartShrink retry loop and the
"output larger than input" check) was deleted outright, not gated behind a
flag — there's no remaining code path that calls the removed functions.
`smartshrink_adaptive_target` re-enabled via the API after the removal was
deployed (build confirmed working, but NOT yet re-validated against a full
live batch the way the threshold logic was above — recommend the user run
another batch to confirm the simplified version behaves identically to the
validated runs, now just without the verification step that was causing
false failures).

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass after the removal.

Files modified this continuation: `internal/jobs/worker.go`,
`internal/ffmpeg/vmaf/sample.go`, `internal/ffmpeg/vmaf/sample_test.go`,
`CHANGELOG.md`.

=== Prior session: experimental adaptive VMAF target for SmartShrink (branch `experimental/adaptive-vmaf-target`, off `develop`, NOT merged) ===

User reported real production logs showing SmartShrink jobs landing in
Review Queue with "no viable encode found ... best attempt was 117-200% of
original size". Root cause: SmartShrink's phase-1 CRF search
(`interpolatedSearchCRF`, `internal/ffmpeg/vmaf/search.go`) always chases a
fixed absolute VMAF threshold per quality tier (85/90/94). When the source
content's real achievable quality ceiling sits below that threshold
(common on already-efficiently-compressed sources or grainy/noisy content),
the search has no way to know that upfront — it narrows toward near-lossless
CRFs chasing an unreachable score, and only discovers the ceiling reactively
once every CRF has failed. The existing best-effort fallback
(`applyBestEffortFallback`) is a capped, coarse ±2-step/2.0-VMAF-tolerance
probe that often isn't aggressive enough to actually beat the source size.

Implemented an opt-in "adaptive VMAF target" mode entirely behind a new
config flag `smartshrink_adaptive_target` (default `false` — existing
behavior is completely unchanged unless explicitly enabled):
- New ceiling probe: before the tier search runs, one extra sample-scoring
  pass at the best-quality end of the CRF/bitrate range (`qRange.Min` /
  `qRange.MaxMod`) establishes the content's real achievable VMAF ceiling.
  New `AdaptiveBinarySearch` (`internal/ffmpeg/vmaf/search.go`) does this,
  then computes `effectiveThreshold = min(tier_threshold, ceiling - 2.0)`
  (`computeEffectiveThreshold`, unit-tested) and runs the existing
  `interpolatedSearchCRF`/`interpolatedSearchBitrate` against that instead
  of the raw tier threshold — so the search converges directly on the
  highest CRF close to the content's own ceiling instead of wastefully
  probing toward an unreachable number.
- `internal/jobs/worker.go`'s SmartShrink retry loop now also uses this
  effective threshold (when adaptive mode is on) instead of the raw tier
  threshold, so it keeps pushing toward genuinely achievable smaller sizes.
- New post-encode VMAF verification step (adaptive mode only, gated on
  `preset.IsSmartShrink && cfg.SmartShrinkAdaptiveTarget`): after the real
  full-file encode completes, re-extracts samples at the SAME deterministic
  positions (`vmaf.SamplePositions(duration, job.InputPath, sampleCount)` —
  no need to persist temp files across phases, positions are reproducible
  from just those three inputs) from both `job.InputPath` and the real
  output file, re-scores VMAF, and compares against
  `effectiveThreshold - 1.0` tolerance. On failure, routes to Review Queue
  with a new, distinct reason ("post-encode VMAF verification failed:
  measured X, expected >= Y") instead of silently shipping an unverified
  file — consistent with the project's "no silent failures" principle.
  New `Worker.verifyPostEncodeVMAF` helper.
- Also bumped VMAF sample count from a hardcoded 3 to a new configurable
  `vmaf_sample_count` (default 4, range 3-6) — applies regardless of
  adaptive mode, reduces sensitivity to any single atypical sample clip.
  `SamplePositions` (`internal/ffmpeg/vmaf/sample.go`) generalized from
  hardcoded `{0.25, 0.50, 0.75}` anchors to evenly-spaced anchors for any
  count, with jitter range scaled down proportionally so windows never
  overlap.
- `runSmartShrinkAnalysis` (`internal/jobs/worker.go`) refactored from an
  unwieldy 7-value return tuple into a `smartShrinkAnalysisResult` struct
  (motivated directly by adding 2 more fields: `CeilingVMAF`,
  `EffectiveThreshold`).
- Config: `smartshrink_adaptive_target` (bool, default false) and
  `vmaf_sample_count` (int, default 4, clamped 3-6 in `Load()`) added to
  `internal/config/config.go`, wired through `GET`/`PUT /api/config`
  (`internal/api/handler.go`) and a new Settings-drawer toggle + number
  input (`web/templates/index.html`, next to the existing "Skip SmartShrink
  for AV1 Sources" toggle). Both fields added to `MEDIAFORGE_SPEC.md`'s
  `transcode:` config sample.

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite,
including new `TestComputeEffectiveThreshold` and updated/new
`SamplePositions` tests covering count=3/4/5/6 spacing and non-overlap)
all pass.

NOT yet verified live — this needs real ffmpeg/VMAF and real media files
which aren't available in this environment. Recommend the user build this
branch, enable `smartshrink_adaptive_target: true`, and re-run a real
SmartShrink job against a known hard-to-compress/low-ceiling source (e.g.
the ER (1994) S01E12 file referenced further down this file that previously
produced a 117-200% Review Queue rejection under the old fixed-threshold
behavior) and confirm: (1) the log shows the new ceiling-probe score and
effective threshold ("Adaptive VMAF ceiling probe complete" /
"SmartShrink analysis complete" with `ceiling_vmaf`/`effective_threshold`),
(2) the job either completes with a genuinely smaller output or lands in
Review Queue with the new distinct post-encode-verification reason rather
than the old "no viable encode ... 117-200%" message, (3) toggling the flag
back off reproduces today's exact existing behavior unchanged. This branch
(`experimental/adaptive-vmaf-target`) is intentionally NOT merged into
`develop`/`main` — stays experimental pending that real-world validation.

=== Prior session: released v1.3.0, synced docs ===

Ran the `/release` skill: versioned CHANGELOG.md's `[Unreleased]` section as
`[1.3.0] - 2026-07-17` (minor bump — the AV1-skip option below is new
user-facing behavior), merged `develop` into `main`, confirmed Lint+Test
green on `main`, tagged `v1.3.0`, pushed (triggered `release.yml` —
GitHub Release + Windows installer built successfully, assets:
MediaForge-Setup-1.3.0.exe, mediaforge.exe, mediaforge-tray.exe), synced
`develop` back to `main` (fast-forward). `main`/`develop`/`v1.3.0` all
confirmed at the same commit.

Also added the missing `skip_smartshrink_for_av1` field to
MEDIAFORGE_SPEC.md's `transcode:` config sample (`## Configuration
(mediaforge.yaml)` section) — it was implemented in the prior session but
never added to the spec's config reference.

Nothing outstanding from this session.

=== Prior session: SmartShrink log now shows output size/CRF on rejection; added AV1-source skip option ===

User's log showed a job that failed SmartShrink ("SmartShrink: no viable
encode") with no output file size anywhere in the log, making it hard to
tell what CRF/size it landed on without digging through DEBUG lines. Root
cause: `internal/jobs/worker.go` line ~968, the `logger.Warn("SmartShrink:
no viable encode", "job_id", job.ID)` call omitted `result.OutputSize`,
`inputSize`, and the CRF used — even though those values were already
computed for the Review Queue `reason` string two lines later. Fixed by
adding `crf`, `output_size`, `input_size` fields to both the log line and
the reason string, so log and Review Queue now show the same info.

Follow-up: the specific job that triggered this was an AV1 source file. AV1
already compresses ~20-30% better than HEVC/AVC at comparable quality, so
SmartShrink re-encoding an AV1 source to HEVC will frequently fail to beat
the original size and land in Review Queue anyway, burning VMAF-analysis
time first. User asked for this to be skippable, but as an *option* (not
hardcoded) — they may sometimes want AV1 sources force-re-encoded to
HEVC/AVC for a device/platform that lacks an AV1 decoder.

Added `skip_smartshrink_for_av1` config field (default `false`, so existing
behavior is unchanged unless explicitly enabled). When true,
`runSmartShrinkAnalysis` (internal/jobs/worker.go) short-circuits with
`shouldSkip=true` for any job where `job.VideoCodec` is `av1`
(case-insensitive), using the same `SkipJob` path as the existing HDR/
too-short skip checks — so it shows up in the job list tagged "skipped"
with an explanatory reason, not silently dropped and not sent to Review Queue.
Wired end-to-end: config struct + default (internal/config/config.go),
GET /api/config response field + PUT /api/config request field + apply
logic (internal/api/handler.go), and a new Settings-drawer toggle "Skip
SmartShrink for AV1 Sources" under Transcode settings, next to Tonemap HDR
(web/templates/index.html) — both the setting-item block and the
loadSettings() checkbox population were added.

Verified: `go build ./...`, `go vet ./...`, `go test ./...` (full suite)
all pass. NOT yet verified live in the browser (Settings drawer toggle
render/save round-trip, or an actual AV1-source job hitting the skip path)
— recommend testing both before relying on this in production.

=== Prior session: fixed false "duplicate" on Review Queue re-encode that replaces a library file in place ===

User reported: re-encoding an existing library file (AVC "ER (1994) - S05E07
- Hazed and Confused.mp4" -> HEVC) via Review Queue "Re-encode Custom"
produced a bogus "duplicate: file already exists at destination" Review
Queue entry, showing Incoming and Existing as byte-for-byte identical
(same codec/resolution/bitrate/size) — which shouldn't happen since the
whole point of that resubmit was to replace the file at that exact path.

Root cause, confirmed by reading the code (not a guess): `ResubmitReviewEntry`
(internal/api/handler.go) sets `job.InputPath` = `entry.OriginalPath`, which
for this workflow IS the in-library file being replaced. `processJob`
(internal/jobs/worker.go) then calls `ffmpeg.FinalizeTranscode(job.InputPath,
...)` with `replace: true` (OriginalHandling == "replace") — which deletes
`inputPath` and writes the new encode to `filepath.Dir(inputPath) +
name+outExt`, i.e. the SAME path, since the input was already the
correctly-named library file. So `finalPath` and the freshly recomputed
`job.LibraryPath` (via ResolveLibraryPath) resolved to the identical path —
the "replace" had already happened in-place inside FinalizeTranscode before
the post-encode duplicate check even ran. That check (`os.Stat(job.LibraryPath)`)
then found the file that was JUST written and misreported it as a
pre-existing collision with something else, and `buildPostEncodeDuplicateJSON`
probed the same path for both "incoming" and "existing", hence identical
details in the UI.

Fix (internal/jobs/worker.go, processJob only): new `samePath(a, b string)
bool` helper (filepath.Clean + case-insensitive compare on Windows, exact on
other platforms). Before the duplicate-exists check, if `samePath(finalPath,
job.LibraryPath)`, skip both the duplicate check and the `SafeMove` — the
file is already correctly in place — and go straight to the
`OnLibraryMoveComplete` callback/logging. The existing duplicate-detection
behavior for genuinely distinct incoming vs. existing files (the normal
intake-pipeline collision case) is unchanged; only the identical-path case is
newly short-circuited.

Test: new `TestSamePath` (internal/jobs/worker_test.go) — table test covering
identical paths, same path differing only in case (Windows-only expectation),
genuinely different files, and an uncleaned-but-equivalent path (`.` segment).

Verified: go build ./..., go vet ./..., go test ./... all pass (full suite,
including the new test). NOT yet verified against a live resubmit of the
real ER S05E07 file — recommend re-running the same "Re-encode Custom"
AVC->HEVC resubmit on that file and confirming in the log: "Post-encode
library move complete (in-place replace)" appears instead of "duplicate at
destination, queuing for review", the job shows as completed (not sent to
Review Queue), and the file at the destination is now genuinely HEVC
(confirm via mediainfo/ffprobe, not just the app's own report).

=== Prior session (cont. once more): fixed triplicate Pushover/email notifications ===

User reported (separate, non-performance annoyance, same live-testing
session): identical "encode failed" / "added to Review Queue" notifications
arriving 3x per event. Asked a clarifying question first (AskUserQuestion) —
confirmed it's real Pushover/email notifications tripling, and the count
matched the number of open browser tabs to the web UI.

Root cause found by reading internal/api/sse.go: JobStream (the SSE handler
for GET /api/jobs/stream) runs once PER CONNECTED CLIENT — each open browser
tab gets its own subscriber channel via queue.Subscribe(). The per-connection
read loop called dispatchEncodeEvent(ctx, event) directly for every
complete/failed job event with no dedup guard, so N open tabs => N identical
real notification sends for the same event. (Separately confirmed
checkAndSendNotification, the OTHER notification path in the same loop — the
"queue empty, N complete/M failed" Pushover summary — already has a correct
dedup guard via h.notifyMu + flipping cfg.NotifyOnComplete off after firing,
so it was NOT part of this bug; only dispatchEncodeEvent was unguarded.)
Also confirmed the "added to Review Queue" notification path
(watcher.go OnReviewQueueAdd -> handler.DispatchNotification) is dispatched
once, centrally, from the intake watcher — NOT per-SSE-connection — so it
was not expected to triple the same way; not independently re-verified live
this session.

Fix:
- internal/jobs/queue.go: Queue gained an exported OnTerminalEvent
  func(JobEvent) field, invoked exactly once inside broadcast() regardless
  of how many subscriber channels exist (called before the subscriber
  fan-out loop, not gated by subsMu).
- internal/api/handler.go NewHandler: wires queue.OnTerminalEvent =
  h.dispatchEncodeEvent once, at handler construction time.
- internal/api/sse.go: removed the per-connection
  h.dispatchEncodeEvent(r.Context(), event) call from JobStream's loop
  (checkAndSendNotification call is UNCHANGED, still there, since it already
  had its own correct dedup). dispatchEncodeEvent's signature simplified to
  no longer take a ctx param (it's no longer tied to any single HTTP
  request's lifetime, since it now fires from queue.broadcast() which can be
  called from arbitrary worker goroutines) — uses context.Background()
  internally instead, matching the existing pattern in
  handler.DispatchNotification.
- internal/jobs/queue_test.go: new TestQueueOnTerminalEventFiresOnce —
  subscribes 3 channels (simulating 3 open browser tabs), adds a job, and
  asserts OnTerminalEvent fires exactly once while confirming all 3
  subscriber channels still independently receive the event (fan-out to the
  UI itself is unaffected, only the external-notification dispatch was
  de-duplicated).

Verified: go build ./... and go test ./... (full suite, including the new
test) all pass.

NOT yet verified against a live run with multiple real browser tabs open —
recommend opening 2-3 tabs to the web UI, triggering an encode-failed or
Review Queue event, and confirming exactly 1 Pushover/email notification
arrives instead of one per open tab.

=== Prior session (cont.): best-achievable-quality fallback for content below the VMAF tier floor ===

Direct continuation of the jitter fix below, same live-testing session. User
re-ran the ER (1994) S01 batch after the jitter fix (confirmed working: jobs
8-11 converged in a single pass, zero retries, balanced per-sample scores).
Job -12 (S01E12) correctly went to Review Queue with real per-sample scores
[69.7, 80.6, 84.9] — genuinely below the tier's threshold (85, "acceptable"
tier) at every CRF tested, not a sampling artifact. User asked why the tool
couldn't just accept the smaller file at CRF 18 (score 80.9, essentially
identical to CRF 16's 80.895) instead of failing outright, since quality
wasn't actually dropping between those two CRFs.

Traced the real root cause (deeper than "no viable encode"):
interpolatedSearchCRF (internal/ffmpeg/vmaf/search.go)'s failure-handling
branch only ever narrows toward qRange.Min (larger files) once it decides a
score is below threshold — it never explores upward (smaller files) to check
whether a higher CRF would score nearly the same. So the best-effort CRF the
search lands on is structurally biased toward near-lossless/big files even
when a much smaller file would cost nothing. Then the worker's size-retry
loop (internal/jobs/worker.go ~866-937) compared every retry step against
the ABSOLUTE tier threshold, which best-effort content (by definition
already below that threshold) can never pass — so it always failed
immediately on the first check.

User explicitly chose (via AskUserQuestion, confirmed): auto-complete the
job with the best-achievable-quality fallback rather than route to Review
Queue — an explicit, confirmed exception to CLAUDE.md's "every failure path
routes to Review Queue" rule, scoped specifically to this one case (content
has a hard, unfixable quality ceiling — no human decision changes that).

Implemented:
- internal/ffmpeg/vmaf/vmaf.go: AnalysisResult gained BestEffort bool.
- internal/ffmpeg/vmaf/analyze.go: sets AnalysisResult.BestEffort from the
  search result.
- internal/ffmpeg/vmaf/search.go: new applyBestEffortFallback(s, best,
  maxCRF) — probes up to 3 CRF steps (+2 each, bestEffortFallbackMaxProbes/
  bestEffortFallbackStep) above a best-effort result, accepting each step
  via the new pure helper bestEffortFallbackAccepts(bestScore,
  candidateScore) (accepts if the drop is <= bestEffortFallbackTolerance =
  2.0 VMAF points). All 4 of interpolatedSearchCRF's existing BestEffort:
  true return sites now route through this before returning. SearchResult
  gained BestEffortBase int (the original best-effort CRF, for logging).
- internal/jobs/worker.go: runSmartShrinkAnalysis's return signature gained
  a bestEffort bool (6th return value, before error) — one call site
  updated (processJob). New hoisted vars smartShrinkBestEffort/
  smartShrinkVMafCeiling (declared before the `if preset.IsSmartShrink`
  block so they survive to the later retry-loop block, which is a separate
  scope). The size-retry loop (~873-950) now branches: when
  smartShrinkBestEffort, steps are accepted via
  smartShrinkVMafCeiling-sampleScore > smartShrinkFallbackTolerance (new
  const, 2.0, mirrors the vmaf package one — duplicated intentionally since
  it's unexported in vmaf and this is a small, clearly-commented constant,
  not worth exporting cross-package plumbing for) instead of the absolute
  threshold comparison used for normal (non-best-effort) jobs. Added
  bestCRF tracking alongside bestResult for accurate fallback-acceptance
  logging. Two new INFO logs: one when entering fallback mode (ceiling_vmaf,
  tier_threshold), one when a fallback result is accepted (crf,
  vmaf_ceiling, output_size vs input_size) — distinguishable from normal
  "SmartShrink retry"/success logging. The existing "still >= source size"
  safety-net check (routes to Review Queue) is UNCHANGED and applies
  regardless of bestEffort — a fallback result must still be smaller than
  the source or it still fails to Review Queue same as before.
- internal/ffmpeg/vmaf/search_test.go: new TestBestEffortFallbackAccepts
  table test covering the tolerance boundary (exactly at 2.0pt drop accepts,
  2.1pt rejects, etc.) — this is the only new automated coverage; the
  probe-loop itself (applyBestEffortFallback) isn't unit tested since it
  calls sampleScorer.scoreCRF which shells out to real ffmpeg, consistent
  with this file's existing pattern of only testing pure helper logic and
  deferring encode-dependent paths to real/Docker integration tests.

Verified: go build ./... and go test ./... (full suite) both pass.

NOT yet verified against a live run — recommend re-running S01E12 (or any
file that previously hit "no viable encode") and confirming in the log:
(1) "SmartShrink: quality tier unreachable for this content, falling back to
closest-achievable-quality size optimization" appears instead of going
straight to the old absolute-threshold retry failure, (2) "SmartShrink:
accepted best-achievable-quality fallback" appears with a smaller
output_size than the original best-effort CRF's full-file size, (3) the job
completes normally (Job complete log line, file lands in library) instead of
creating a Review Queue entry, and (4) a genuinely non-shrinkable file (if
one exists) still correctly fails to Review Queue via the unchanged
"still >= source size" safety net.

=== Prior session (cont.): SmartShrink VMAF sample positions jittered to break fixed-timestamp bias ===

User reported SmartShrink jobs (real prod logs, ER (1994) S01 batch,
mediaforge.log 2026-07-15) producing huge oversized first-try outputs,
requiring 6-8 full-file re-encodes to converge (CRF climbing 16→18→20→...→28
in +2 steps, ~3-4 min per full-file retry). Investigated in stages:

1. Confirmed via real logs that phase-1 VMAF search (interpolatedSearchCRF,
   internal/ffmpeg/vmaf/search.go) was landing on best-effort CRF 16
   (near-lossless) because no CRF cleared the VMAF 85 threshold — this then
   fed the size-retry loop (internal/jobs/worker.go ~866-915) a starting
   point far below what the size target needed, forcing many +2 full-file
   re-encode steps.
2. Discussed (but did NOT implement) two alternative fixes: (a) preset-tier
   fixed starting CRF (18/24/30) — user correctly identified this fails for
   the Excellent tier since near-lossless CRF on already-compressed AVC
   source almost always oversizes regardless of tier; (b) a sample-driven
   combined VMAF+size search reusing existing sample infrastructure instead
   of full-file retries — still a reasonable follow-up but not implemented
   this session, deferred pending re-measurement after item 3 below.
3. User asked specifically whether the sampler was hitting intro/credits.
   Checked internal/ffmpeg/vmaf/score.go:187, which already logs per-sample
   scores (`msg="VMAF score" scores=[...]`) separately from the averaged
   line — pulled the real per-sample breakdown from the log and found the
   MIDDLE sample (50% position, not the ends) scoring 15-30 VMAF points
   above the other two on nearly every episode of the same show (e.g.
   [75.8, 96.3, 74.9], [68.3, 97.2, 96.2]). This is consistent with a
   recurring low-motion structural element (recap/bumper/act-break card,
   common in syndicated 90s TV) landing at a fixed relative timestamp every
   episode, inflating the averaged score and damping the search's
   sensitivity to the CRF actually needed for the real content — the likely
   root cause of the CRF-16 best-effort landing in the first place.

Fix implemented (internal/ffmpeg/vmaf/sample.go): SamplePositions now takes
a seedKey string param (the input file path) and jitters each of the 3
anchor positions (0.25/0.50/0.75) by up to ±8% (new sampleJitterRange
const), seeded deterministically via fnv.New64a hash of seedKey feeding
math/rand.NewSource — same file always gets the same positions (debuggable/
reproducible), but different files/episodes no longer systematically hit
the same relative timestamp. Anchor spacing (0.25 apart) keeps ±8% windows
from overlapping.

Call sites updated to pass a seed key: internal/ffmpeg/vmaf/analyze.go
(inputPath), internal/jobs/worker.go quickSampleVMAF and
runFixedReductionAnalysis (both job.InputPath).

Tests (internal/ffmpeg/vmaf/sample_test.go): existing TestSamplePositions/
TestSamplePositionsEdgeCases updated to check length/bounds instead of
exact 0.25/0.50/0.75 equality (no longer holds with jitter). Added
TestSamplePositionsJitterBounds (positions stay within ±sampleJitterRange
of their anchor across multiple seed keys) and
TestSamplePositionsDeterministic (same seedKey → same positions; different
seedKey → different positions).

Verified: go build ./... and go test ./... (full suite) both pass.

NOT yet verified against a live SmartShrink run — recommend re-running the
same ER (1994) S01 batch (or similar) and checking two things in the log:
(1) per-sample VMAF scores (`msg="VMAF score" scores=[...]`) no longer show
one consistent outlier-high index across episodes, and (2) phase-1's
selected_crf lands meaningfully above 16 on the first pass, reducing or
eliminating the size-retry loop's iteration count. If the retry loop still
fires often after this fix, the sample-driven combined VMAF+size search
discussed in item 2(b) above is the next thing to implement — re-measure
before deciding whether it's still needed at the scope discussed.

The VMAF-85-threshold-may-be-uncalibrated-for-this-content-class question
(raised during the same discussion — phase-1 topped out around VMAF 82-83
even at CRF 16 for some episodes, suggesting a natural ceiling for older/
SD-sourced content) and the vmaf_sample_count config change (3→5,
configurable) were both explicitly deferred as separate follow-up items,
not addressed this session.

=== Prior session: LLM verification pass had no logging ===

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
