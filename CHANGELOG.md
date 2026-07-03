# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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







