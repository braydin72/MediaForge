---
name: release
description: Cut a new MediaForge release locally — versions the CHANGELOG, merges develop into main, tags, and syncs branches back. Use when the user asks to "release", "cut a release", "tag a new version", or "ship this".
user-invocable: true
---

# /release — MediaForge Release Procedure

This is a **local git procedure**, not a CI workflow — nothing in
`.github/workflows/release.yml` changes. That workflow only reacts to a
pushed `vX.Y.Z` tag (builds the Windows installer, creates the GitHub
Release); this skill is what produces that tag correctly in the first place.

## Before starting

1. `git status` on `develop` — working tree must be clean. If there are
   uncommitted changes, stop and ask the user whether to commit them first
   (this skill does not commit unrelated work).
2. `git log --oneline -10` and diff `CHANGELOG.md`'s `[Unreleased]` section
   against recent commits — confirm every commit since the last tag is
   actually described under `[Unreleased]`. If a fix landed without a
   changelog entry (check `git log <last-tag>..HEAD --oneline`), add it now.
   This step exists because a changelog gap has happened before — the
   version heading was skipped entirely in a prior release.
3. Determine the next version number: read the latest tag (`git tag
   --sort=-v:refname | head -1`) and bump per semver based on what's in
   `[Unreleased]` — patch for fixes only, minor if anything under a
   `### Added`/`### Changed` heading represents new user-facing behavior.
   Default to patch bump; ask the user only if genuinely ambiguous.

## Steps, in order

1. **Version the changelog on `develop`** — rename `## [Unreleased]` to
   `## [X.Y.Z] - <today's date>` and add a fresh empty `## [Unreleased]`
   section above it. This must happen *before* the merge/tag, not after —
   the tagged commit's CHANGELOG.md should already describe what's in it.
   Also update `templates/mediaforge.xml`: set `<Changes>` to
   `vX.Y.Z — see CHANGELOG.md on GitHub for full release notes.` and
   `<Date>` to today's date (YYYY-MM-DD) — this is the Unraid Community
   Applications template and it does not update itself. Commit both files
   together on `develop` (`docs(changelog): prepare vX.Y.Z release`).
2. **Push `develop`**: `git push origin develop`.
3. **Merge into `main`**: `git checkout main`, `git merge --no-ff develop -m
   "Merge develop into main for vX.Y.Z"`.
4. **Verify before tagging** — `go build ./...` and `go test ./...` locally
   on `main`. Then push `main` and check CI (`gh run list --branch main
   --limit 3`, or `gh run watch <id> --exit-status`) for **both** Lint and
   Test workflows before tagging. Do not tag on a red or in-progress run —
   tagging first and fixing lint after means force-pushing a published tag,
   which is avoidable by just waiting here.
5. **Tag `main`**: `git tag -a vX.Y.Z -m "vX.Y.Z\n\n<bullet summary pulled
   from the changelog entry just written>"`, then `git push origin vX.Y.Z`.
   This push is what triggers `release.yml` (GitHub Release +
   Windows installer build) — treat it as the point of no easy return.
6. **Sync `develop`**: `git checkout develop`, `git merge main -m "Merge
   main into develop to sync vX.Y.Z"` (usually fast-forwards since `develop`
   already had everything `main` just got), `git push origin develop`.
7. **Confirm final state**: `git rev-parse main develop` and `git rev-parse
   vX.Y.Z` should all match. Report the tag, and link/mention the
   `release.yml` run if relevant.

## If lint/tests fail after tagging anyway

Ask the user before force-moving a published tag (`git tag -d`, retag,
`git push --force`) — this rewrites a public ref. Confirm the fix is
correct and CI is green on the new commit first, per the steps above,
*then* move the tag with explicit confirmation. Never force-push `main` or
`develop` themselves to fix this — only the tag ref moves.
