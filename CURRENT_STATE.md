CURRENT STATE NOTE

This file holds a short summary of the most recent session only — what
changed and what's still incomplete. Older session-by-session history (~50
entries, 2500+ lines) was trimmed on 2026-07-27 since it was fully superseded
by real sources of truth: `git log` for what changed and when, `CHANGELOG.md`
for released fixes, `CURRENT_KNOW_ISSUES.md` for tracked open issues, and
`MEDIAFORGE_SPEC.md`/`COMPREHENSIVE_STATUS_AND_ROADMAP.md` for design intent
and forward-looking plans. Nothing was lost — it's all still in git history
on this file's prior commits if a past session's blow-by-blow is ever needed.

=== Latest session: Control & Workflow redesign (queue/intake controls, file disposal, Manual Add split, naming-lookup toggle) ===

Implemented the full control/workflow redesign from `CURRENT_KNOW_ISSUES.md`'s
"Pause & Stop Rework" roadmap item, per an explicit session spec. Summary in
`CHANGELOG.md` under [Unreleased] → Added; key points not fully captured there:

- User explicitly chose (via AskUserQuestion): for Manual Add Encode
  Only/Custom Encode, files browsed from outside the staging folder are now
  copied into staging before encoding, specifically so the "network copy"
  disposal rule can safely silently-delete the local copy without ever
  risking the original file.
- `internal/jobs/worker.go` `StopAll()` was previously a documented stub
  (TODO comment, unused by any client) — now has real cancel+pause
  semantics. `WorkerPool` gained a small priority-batch tracking mechanism
  (`BeginPriorityBatch`, `watchPriorityBatch` — subscribes to queue events
  rather than touching `processJob`'s many return paths) for Manual Add
  "Start Encode"'s auto-pause/resume behavior.
- `internal/config/config.go`: new `Intake.EnableNamingLookup` bool field
  needed special handling in `Load()` — a plain YAML unmarshal can't
  distinguish "key absent" from "explicitly false" for a bool, and this
  field must default to `true` for existing installs (predating the field)
  to avoid silently disabling identification. Handled via a raw-map presence
  check in `Load()`.
- `internal/intake/watcher.go`'s `resolveAndGate` already had a "no
  Orchestrator configured" bypass path (returns `nil, true`, skipping
  lookup); the new `EnableNamingLookup` toggle reuses that exact same
  bypass, just gated on the config flag too. A new
  `resolveLibraryPathPassthrough` (in `naming.go`) mirrors
  `resolveLibraryPath`'s folder classification but keeps the original
  filename instead of rendering a naming-template name.
- Two pre-existing intake tests (`watcher_test.go`) constructed
  `config.IntakeConfig{}` literals directly, bypassing `Load()`'s defaults —
  had to add `EnableNamingLookup: true` to those literals or they broke
  (lookup now short-circuits on the zero-value `false`).
- Verified end-to-end against a real running server (not just unit tests):
  intake pause/resume, queue pause/start/stop, system stop/start, a real
  Manual Add "Encode Only" job that got staged as a network copy and then
  had its local copy silently deleted via `/dispose` regardless of
  `delete:false`, ReQueue on a Skipped job, and a full "Full Pipeline"
  Manual Add run (copy → watcher pickup → stage → encode attempt → Review
  Queue entry on "no viable encode" for the tiny synthetic test clip used).

All backend changes covered by `go build ./...`, `go vet ./...`, and
`go test ./...` (all packages pass), plus new tests in
`internal/jobs/disposal_test.go` and additions to `internal/jobs/queue_test.go`.

Nothing incomplete from this session — all 11 spec sections implemented.
Not done: no browser/UI click-through of the new frontend controls (only
verified via direct API calls + a JS-syntax parse check on
`web/templates/index.html`) — a future session should click through the
transport bar, disposal modal, and Manual Add buttons in an actual browser
before the next release.

=== Previous session: lower-bitrate re-encodes no longer auto-discarded on duplicate ===

User was about to re-transcode some previously-encoded files with better
settings; expected some to come out at a lower bitrate than what's already in
the library (better shrink ratio) despite similar/better perceptual quality
(scored separately with a personal, gitignored vmaf-compare.ps1 script — not
part of the app). Under the old `internal/upgrade.Decide` logic, a lower
incoming bitrate at equal resolution/codec tier past `bitrateThreshold`
auto-returned `Keep`, silently discarding the incoming (better) file with no
review.

Fix (internal/upgrade/upgrade.go): the bitrate-comparison branch no longer
auto-`Keep`s when existing bitrate is meaningfully higher than incoming at
equal resolution/codec — it now returns `Review` instead, routing the
duplicate to the Review Queue for a manual look. The auto-`Replace` path
(incoming bitrate meaningfully higher) is unchanged. `TestDecide_Bitrate`
(internal/upgrade/upgrade_test.go) updated with a case asserting this.

All three call sites (internal/intake/duplicate.go, internal/jobs/worker.go,
internal/api/handler.go) already handle `Review` generically via the
existing Review Queue duplicate-comparison UI, so no other code changes were
needed.

`go build ./...` and `go test ./...` both pass. Pushed to develop.

Same session: full documentation audit and cleanup pass across all tracked
.md files (see git log for the individual commits) — fixed the stale
duplicate-resolution rule description in MEDIAFORGE_SPEC.md, corrected/
expanded docs/api/README.md's endpoint table, added the missing packages
(internal/intake, internal/upgrade, internal/notify, internal/setup,
internal/winsvc, internal/version, internal/util) to
docs/architecture/packages.md and docs/architecture/README.md, fixed
docs/architecture/job-lifecycle.md's pause-behavior description (was
documenting the old requeue-on-pause behavior, not the current
finish-in-flight-jobs behavior), pruned CURRENT_KNOW_ISSUES.md down to the
two genuinely open items (staging directory cleanup; a pause/stop UX rework
that's still in the design/thinking stage, not yet scoped), and refreshed
COMPREHENSIVE_STATUS_AND_ROADMAP.md's stale header date. This file
(CURRENT_STATE.md) was trimmed as the last step of that pass.

Nothing incomplete/follow-up from this session.
