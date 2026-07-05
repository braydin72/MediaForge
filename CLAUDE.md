# MediaForge — Claude Code Project Instructions

This file is read automatically at the start of every Claude Code session in this repo. Follow these standing rules in addition to whatever the user asks for in their prompt.

## Before starting any work

1. Run `git log --oneline -10` to see what has actually landed in recent commits. Do not assume something is done or not done based on the user's prompt alone — verify against the real commit history first.
2. Read `CURRENT_STATE.md` at the repo root for a short summary of what was being worked on in the most recent session and what is still incomplete.
3. Read `MEDIAFORGE_SPEC.md` for the full design spec and behavior requirements.

## While working

- Keep sessions focused on exactly what the user's prompt asks for. Do not expand scope into adjacent fixes unless explicitly asked.
- If a prompt asks for something that touches Windows-only code (Windows service, system tray, etc.), use the `//go:build windows` tag and confirm the Linux/Docker build still passes before committing.
- Run `go build ./...` and `go test ./...` before every commit. If `golangci-lint` is part of CI, fix lint issues before committing rather than relying on a follow-up commit.
- Never silently change scope-relevant defaults (config values, thresholds, output formats) without noting it in the commit message.

## Before committing

When done, update CHANGELOG.md with:
- Fixed: [issue description]
- Files modified: [list]


Update `CURRENT_STATE.md` at the repo root with:
- A short note on what was changed in this session
- What (if anything) is still incomplete or needs follow-up in a future session
- Any new files created and their purpose
- Any new config fields, API endpoints, or CLI flags added

Keep this update brief — a few bullet points, not a full essay. The goal is that a fresh Claude Code session (or a human) can read it and know exactly where things stand without re-reading the whole git history.

## Commit messages

Use conventional commit style: `fix(scope): description`, `feat(scope): description`, `chore(scope): description`, `docs(scope): description`. Keep the first line under 72 characters. Add a body if the change needs more explanation than the summary allows.

## Things to never do

- Never commit built binaries (mediaforge.exe, mediaforge-tray.exe) or installer output — these are gitignored build artifacts.
- Never remove or weaken the "no silent failures" principle from MEDIAFORGE_SPEC.md — every failure path must route to the Review Queue with a specific, human-readable reason.
- Never hardcode Windows or Linux path separators — always use Go's `filepath` package.
- Never pass year as a search filter to TMDB or TVDB lookups — year is used only for confidence scoring, not as a search query parameter (this caused real bugs earlier in development).
- Never auto-overwrite or auto-append (1)/(2) suffixes on duplicate files at the library destination — all duplicates route to the Review Queue for manual decision.

## Project structure quick reference

- `cmd/mediaforge/` — main application entry point
- `cmd/tray/` — Windows system tray app (build tag: windows)
- `internal/intake/` — file watcher, codec detection, TMDB/TVDB/OMDb lookup, filename parsing
- `internal/ffmpeg/` — encoder detection, transcode presets, subtitle handling, hardware acceleration
- `internal/api/` — HTTP handlers for the web UI and REST API
- `internal/browse/` — file browser backend for the manual queue UI
- `internal/config/` — configuration schema, defaults, load/save
- `internal/setup/` — first-run wizard
- `internal/notify/` — notification dispatch (SMTP, Pushover)
- `internal/winsvc/` — Windows service wrapper (build tag: windows)
- `internal/version/` — version and build number injection
- `web/templates/` — the single-page web UI (index.html, embedded CSS/JS)
- `installer/` — Inno Setup script for the Windows installer
- `.github/workflows/` — CI: dev-build.yml (develop branch), release.yml (tagged releases), lint.yml, test.yml

## Known sensitive areas (changes here have caused regressions before)

- `internal/intake/watcher.go` — the core pipeline orchestration. Touching this affects both HEVC direct-to-library and AVC encode-queue paths simultaneously. Be careful not to fix one path while breaking the other.
- `internal/ffmpeg/hwaccel.go` — NVENC test resolution must stay at 320x320 or higher; lower resolutions fail with "Frame dimensions are less than the minimum supported value."
- `web/templates/index.html` — single large file containing all HTML/CSS/JS. Settings drawer and Review Queue tab rendering have broken silently before due to JS errors elsewhere in the file; test both after any UI change.
- Path handling anywhere a Windows mapped network drive (e.g. `M:\` via SMB) is involved — these can return nil `DirEntry`/`FileInfo` during directory walks, unlike local drives. Always nil-check.
