# MediaForge — Automated Media Ingest System
**Tagline:** Ingest, Transcode, Organize  
**Version:** Design Spec v2.0  
**Status:** v1.0 In Development  
**Author:** krichardson  

---

## What MediaForge Is

MediaForge is a self-hosted media ingest engine — a single application and single Docker container that handles the complete journey of a raw video file from arrival to library-ready:

1. **Ingest** — watch a folder, detect new files, verify they are fully written
2. **Identify** — codec detection via ffprobe, metadata lookup via TVDB/TMDB/OMDb, LLM-assisted verification for ambiguous matches
3. **Transcode** — AVC (H.264) files are queued and encoded to HEVC (H.265) using hardware acceleration (NVIDIA, AMD, Intel, or CPU fallback)
4. **Organize** — rename and move to library with media-server-compatible folder and file naming

MediaForge is not a media server. It is designed to feed Plex, Jellyfin, Emby, or any other media server that watches a library folder.

---

## Heritage & Attribution

MediaForge's transcode engine is derived from [shrinkray](https://github.com/gwlsn/shrinkray), originally created by **@gwlsn**, with additional community contributions from **@jesposito**, **@akaBilih**, and others. The encode core, hardware acceleration logic, web UI foundation, and Docker packaging all originate from that project. MediaForge extends this foundation with a full ingest pipeline, metadata identification, and library organization system.

---

## Core Principle: No Silent Failures

**No operation should ever fail silently.**

If MediaForge cannot encode, move, rename, or identify a file for any reason, it must:
1. Record the reason in the activity log (specific error, not "unknown error")
2. Place the file in the Review Queue with the failure reason attached
3. Never leave a file stranded in the incoming directory with no indication of what happened
4. Never delete or overwrite source files on failure

The Review Queue is the primary mechanism for all edge cases requiring user intervention — failed identification, low-confidence matches, encode failures, move errors, and permission problems all surface there with enough context for the user to act.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      MediaForge                         │
│                                                         │
│  ┌─────────────┐    ┌──────────────┐    ┌───────────┐  │
│  │   Ingest    │───▶│   Identify   │───▶│  Route &  │  │
│  │   Module    │    │   Module     │    │   Move    │  │
│  │             │    │              │    │           │  │
│  │ • Watch     │    │ • ffprobe    │    │ HEVC ───▶ │  │
│  │   folder    │    │ • TVDB/TMDB  │    │  Library  │  │
│  │ • Stability │    │ • OMDb       │    │           │  │
│  │   check     │    │ • LLM verify │    │ AVC ────▶ │  │
│  └─────────────┘    └──────────────┘    │  Encode   │  │
│                                         │  Queue    │  │
│  ┌──────────────────────────────────┐   └───────────┘  │
│  │       Transcode Engine           │         │        │
│  │  SmartShrink VMAF / Fixed Target │◀────────┘        │
│  │  NVIDIA / AMD / Intel / CPU      │                  │
│  └──────────────────────────────────┘                  │
│            │                                           │
│            ▼                                           │
│    Post-encode move → Library                          │
│                                                        │
│  ┌──────────────────────────────────┐                  │
│  │         Review Queue             │                  │
│  │  All failures + low-confidence   │                  │
│  │  matches surface here for user   │                  │
│  │  action. Nothing is silently     │                  │
│  │  abandoned.                      │                  │
│  └──────────────────────────────────┘                  │
└─────────────────────────────────────────────────────────┘
```

---

## Pipeline — Step by Step

```
Incoming Folder (watched)
        │
        ▼
  [1] File Stability Check
      Wait until file size stable across N passes
      Prevents acting on files still being written
        │
        ▼
  [2] Codec Detection via ffprobe
      Extract codec_name from first video stream
      Result: "hevc" | "h264" | "unknown"
        │
        ├─── unknown / ffprobe error ──► Review Queue (reason: "codec detection failed")
        │
        ▼
  [3] Media Type Detection
      TV Show: SxxExx pattern present (S01E04, s2e12, S01E01E02)
      Movie:   no SxxExx pattern
        │
        ▼
  [4] Metadata Lookup
      Movies:   TMDB primary → OMDb fallback
      TV Shows: TVDB primary → TMDB fallback → OMDb last resort
      All lookups exhausted with no result ──► Review Queue (reason: "no metadata match found")
        │
        ▼
  [5] LLM Verification (if configured and confidence below threshold)
      LLM error or unavailable ──► Review Queue (reason: "LLM verification failed: <error>")
        │
        ├─── confidence < review_threshold ──► Review Queue (reason: "low confidence match")
        │
        ▼
  [6] Rename & Route

      HEVC:
        Movie  ──► {movies_library}/{Title} ({Year})/{Title} ({Year}).{ext}
        TV     ──► {tv_library}/{Show} ({Year})/Season {XX}/
                       {Show} - S{XX}E{XX} - {Episode Title}.{ext}
        Move error ──► Review Queue (reason: "move failed: <error>")

      AVC:
        Rename → move to staging → enqueue for encode
        Any step fails ──► Review Queue (reason: specific failure)
        On encode failure ──► Review Queue (reason: "encode failed: <error>")
        On encode complete ──► move output to library
        Post-encode move error ──► Review Queue (reason: "post-encode move failed: <error>")
```

---

## Lookup Logic

### Movie Lookup
1. Parse filename — extract title and year (strip codec tokens, resolution, scene tags)
2. **TMDB** — `GET /search/movie?query={title}&year={year}`
   - Single confident result → use it
   - Multiple results → pass top candidates to LLM
   - Zero results → retry without year
3. **OMDb fallback** — `GET /?t={title}&y={year}&type=movie`
4. **Runtime cross-check** — ffprobe duration vs API runtime (±5 min = pass, contributes to confidence)

### TV Show Lookup
1. Parse filename — extract show name, season, episode from SxxExx pattern
2. **TVDB (primary)** — `GET /search?query={show_name}&type=series` (API v4)
   - Episode detail: `GET /series/{id}/episodes/default?season={s}&episodeNumber={e}`
   - Auth: token-based (POST `/login` with API key → bearer token)
3. **TMDB fallback** — `GET /search/tv?query={show_name}` + episode detail
4. **OMDb last resort** — `GET /?t={show_name}&Season={s}&Episode={e}&type=series`

### Confidence Scoring

| Score | Action |
|---|---|
| ≥ 0.85 | Auto-proceed |
| 0.60 – 0.84 | LLM verification pass |
| < 0.60 | Review Queue |

---

## LLM Verification

### Backends (user-configurable)
| Backend | Config needed |
|---|---|
| Anthropic (Claude) | API key, model selector |
| OpenAI | API key, model selector |
| Ollama (local) | Host URL, model name |

LLM is optional — without it, files below the confidence threshold go directly to the Review Queue.

---

## Review Queue

**Every failure and every low-confidence result surfaces here. Nothing is silently abandoned.**

### Reasons a file lands in the Review Queue
- Codec detection failed (ffprobe error or unrecognized codec)
- No metadata match found after all lookup sources exhausted
- Low confidence match (below review_threshold)
- LLM verification failed or unavailable
- File move failed (permissions, network share unavailable, disk full)
- Encode failed
- Post-encode move failed
- Duplicate detected at destination (always routed to Review Queue — user decides)

### Each Review Queue entry shows
- Original filename + full path
- ffprobe info (codec, duration, resolution, container)
- Reason for queue entry (specific, human-readable)
- Top lookup candidates with poster thumbnails (if any were found)
- LLM best guess + reasoning (if LLM ran)
- Timestamp of failure
- For duplicates: side-by-side comparison of incoming vs existing (codec, resolution, bitrate, file size)

### User actions per entry
- Select a candidate from lookup results
- Manual title search (triggers fresh lookup with user-supplied title/year)
- Retry automatically (re-runs pipeline from the failed step)
- Skip / discard (moves file to a configured discard folder, logs action)
- **Replace** (duplicate entries only) — overwrites existing with incoming
- **Keep Existing** (duplicate entries only) — discards incoming, removes queue entry

### Notification
- Review Queue badge/count visible in web UI nav at all times
- Optional: notify via configured notification channel when new items arrive

---

## Transcode Engine

### Container Format
- Global setting: preferred output container (`mkv`, `mp4`, `preserve`)
- `preserve` — output matches source container
- Default: `preserve`
- All transcoded outputs are remuxed to the selected format regardless of source

### SmartShrink Mode (quality-driven)
User selects a quality preset: **Excellent / Good / Acceptable**

Encoding loop:
1. Encode at CRF associated with selected quality level
2. Measure VMAF and output file size
3. If output is larger than source → increase CRF and retry
4. Continue until either:
   - Output size ≤ 60% of source (40% reduction target met), or
   - VMAF would fall below the minimum threshold for the selected preset
5. Use the best result found within those bounds
6. If no acceptable result is achievable → **Review Queue** (reason: "no viable encode found within quality constraints"), source file untouched

### Fixed Reduction Mode (size-driven)
User specifies a target size reduction percentage (e.g. 40% = output should be ~60% of original).

MediaForge adjusts CRF until that target is reached regardless of VMAF score.

If target is unreachable without extreme quality loss → Review Queue with explanation.

### Encoding Speed
User-configurable encoder speed preset:

| Setting | Notes |
|---|---|
| Slowest | Maximum compression, lowest throughput |
| Slower | |
| Slow | |
| Medium | Default — matches current Shrinkray behavior |
| Fast | |

### GPU Support
GPU acceleration is optional. All three hardware paths remain supported:
- NVIDIA (nvenc) — `--runtime=nvidia` + `NVIDIA_VISIBLE_DEVICES`
- AMD / Intel (vaapi) — `--device=/dev/dri`
- CPU — no special config, always available as fallback

Configured in Settings UI and reflected in docker-compose/Unraid template.

---

## Path & Network Share Support

All folder paths support local and network-mounted locations. MediaForge has no SMB/CIFS awareness — it works with whatever path the OS presents.

### Platform notes
**Windows:** UNC paths (`\\server\share\Movies`) work natively. Process must run as a user account with network credentials — Task Scheduler as your own user satisfies this.

**Docker:** Mount the remote share at the host first (Unraid: Unassigned Devices plugin), then bind-mount into the container. Container sees it as a plain local path.

**Linux native:** Mount via `/etc/fstab` or `autofs` with `cifs`, point MediaForge at the mount point.

### Cross-device move handling
`os.Rename()` fails with `EXDEV` across filesystems. All move operations handle this:

```
attempt os.Rename(src, dst_tmp)      ← temp name at destination
  if EXDEV:
    io.Copy(src → dst_tmp)
    if copy succeeds: os.Rename(dst_tmp, dst_final)   ← atomic rename at destination
    if copy fails: os.Remove(dst_tmp), leave src, → Review Queue
  if any step fails: → Review Queue with specific error
```

Writing to a temp name and renaming on completion ensures media servers (Plex, Jellyfin, Emby) never index a partial file.

### Media server compatibility note
Most Docker deployments will have MediaForge and the media server on the same host. The temp-name → rename approach means the media server only ever sees complete, correctly-named files.

---

## Windows Path Handling

All path manipulation throughout the codebase uses platform-aware path libraries (`filepath` package in Go). String-based path manipulation has been eliminated. MediaForge is cross-platform and handles Windows separators (`\`) correctly throughout — including breadcrumb generation, browse API responses (normalized to forward slashes for the JS frontend), and all move/rename operations.

---

## Configuration (`mediaforge.yaml`)

```yaml
intake:
  enabled: false
  watch_folder: ""
  staging_folder: ""
  library:
    movies: ""
    tv_shows: ""
  stability_check:
    interval_seconds: 10
    passes_required: 6
  confidence_threshold: 0.85
  review_threshold: 0.60
  naming:
    movie_folder:   "{title} ({year})"
    movie_file:     "{title} ({year})"
    show_folder:    "{show} ({year})"
    episode_file:   "{show} - S{season:02d}E{episode:02d} - {episode_title}"

apis:
  tmdb_key:  ""
  tvdb_key:  ""
  omdb_key:  ""

llm:
  backend:      ""                        # "anthropic" | "openai" | "ollama" | "" (disabled)
  api_key:      ""
  model:        ""
  ollama_host:  "http://localhost:11434"

poster_cache:
  enabled: true
  path: /config/poster_cache

transcode:
  output_format: "preserve"              # "mkv" | "mp4" | "preserve"
  working_dir: ""
  mode: "smartshrink"                    # "smartshrink" | "fixed_reduction"
  quality_preset: "good"                 # "excellent" | "good" | "acceptable"
  target_reduction_pct: 40
  encoder_speed: "medium"                # "slowest"|"slower"|"slow"|"medium"|"fast"
  gpu: "cpu"                             # "nvidia" | "amd" | "intel" | "cpu"

notifications:
  base_url: ""
  events:
    encode_complete: false
    encode_failed: true
    review_queue_item: true
    daily_summary: false
    weekly_summary: false
  email:
    enabled: false
    smtp_host: ""
    smtp_port: 587
    smtp_tls: true
    username: ""
    password: ""
    from: ""
    to: ""
    mode: "per_file"                     # "per_file" | "batched"
    interval_minutes: 60
```

---

## Docker Volume Mapping

```yaml
volumes:
  - /mnt/user/incoming:/incoming            # watch folder
  - /mnt/cache/staging:/staging             # AVC staging + transcode working dir (fast)
  - /mnt/user/media:/media                  # library root (local or host-mounted share)
  - /mnt/user/appdata/mediaforge:/config    # config + poster cache
```

---

## Naming Templates

| Token | Description |
|---|---|
| `{title}` | Movie title (confirmed via TMDB/OMDb) |
| `{show}` | TV show name (confirmed via TVDB/TMDB) |
| `{year}` | Release year |
| `{season}` | Season number |
| `{episode}` | Episode number |
| `{episode_title}` | Episode name from TVDB/TMDB |

Format spec: `{season:02d}` zero-pads to 2 digits. Default templates match Plex and Jellyfin naming conventions.

---

## Edge Cases

- **No year in filename** → lookup without year, lower confidence, more likely Review Queue
- **Multi-episode file** (S01E01E02) → detect both episode numbers, name as `S01E01E02`
- **Specials / Season 0** → map to `Season 00/`
- **Non-English titles** → TVDB and TMDB handle natively
- **Duplicate at destination** → always routed to Review Queue; user chooses Replace or Keep Existing. MediaForge never silently overwrites or auto-appends `(1)`, `(2)` suffixes.
- **Post-encode cleanup** → delete staging copy only after successful library move is confirmed

---

## Implementation Order

| Phase | Scope |
|---|---|
| ~~0~~ | ~~Repo rename~~ — **COMPLETE.** Repo initialized at github.com/BRAYDIN72/MediaForge, version reset to 1.0.0, README updated, dev-build.yml verified, Unraid template renamed to mediaforge.xml. |
| ~~0b~~ | ~~Internal rename~~ — **COMPLETE.** go.mod module path updated to github.com/braydin72/mediaforge, all internal import paths updated, go build and tests pass clean. |
| ~~1~~ | ~~Config schema~~ — **COMPLETE.** mediaforge.yaml structure implemented, Settings UI fields for all sections. |
| ~~2~~ | ~~Stats DB schema~~ — **COMPLETE.** SQLite per-file records and aggregate counters, reset logic. |
| ~~3~~ | ~~File watcher + ffprobe~~ — **COMPLETE.** Stability check, codec detect, basic route, Review Queue skeleton. |
| ~~4~~ | ~~Filename parser~~ — **COMPLETE.** Title/year/SxxExx + multi-episode extraction, scene-tag stripping, filepath package migration. |
| ~~5~~ | ~~TVDB integration~~ — **COMPLETE.** TV search + episode detail, token auth. |
| ~~6~~ | ~~TMDB integration~~ — **COMPLETE.** Movie search + TV fallback. |
| ~~7~~ | ~~OMDb integration~~ — **COMPLETE.** Last-resort fallback. |
| ~~8~~ | ~~Confidence scoring + runtime cross-check~~ — **COMPLETE.** |
| ~~9~~ | ~~LLM verification~~ — **COMPLETE.** Pluggable backend (Anthropic, OpenAI, Ollama), structured prompt, response parsing. |
| ~~10~~ | ~~Dashboard UI~~ — **COMPLETE.** Active queue with per-file progress, storage savings gauge, stats bar. |
| ~~11~~ | ~~Review Queue UI~~ — **COMPLETE.** Full entry display, poster thumbnails, retry/discard/re-add/replace/keep-existing actions, duplicate side-by-side comparison. |
| ~~12~~ | ~~Manual add UI~~ — **COMPLETE.** File/folder picker, pipeline mode selector, per-job encode settings override. |
| ~~13~~ | ~~Transcode engine updates~~ — **COMPLETE.** SmartShrink retry loop, Fixed Reduction mode, speed preset, container setting. |
| ~~14~~ | ~~Encode queue handoff~~ — **COMPLETE.** AVC files and manual-add files wired into transcode engine. |
| ~~15~~ | ~~Post-encode move~~ — **COMPLETE.** Hook into encode-complete, move to library, temp-name strategy. |
| ~~16~~ | ~~Cross-device move~~ — **COMPLETE.** EXDEV handling, all move operations use filepath package. |
| ~~17~~ | ~~Notifications~~ — **COMPLETE.** Existing Shrinkray channels retained, SMTP email added (Gmail + self-hosted), per-event toggles, batched digest, test button. |
| ~~18~~ | ~~Windows first-run wizard~~ — **COMPLETE.** Detects missing config on first launch, guided setup. |
| ~~19~~ | ~~Windows path separator fix~~ — **COMPLETE.** All path handling audited and migrated to filepath throughout. |

---

## Known Working (v1.0 Testing)

Confirmed working end-to-end during v1.0 development and testing:

- **File watcher** — detects new files in watch folder, waits for stability across configured passes before processing
- **Codec detection** — ffprobe correctly identifies HEVC, AVC, and unknown codecs; unknown routes to Review Queue
- **Filename parsing** — title/year extraction, SxxExx TV detection, multi-episode (S01E01E02), scene-tag stripping
- **TVDB lookup** — token auth, series search, episode detail fetch; falls through to TMDB/OMDb on failure
- **TMDB lookup** — movie and TV search, poster URL construction, runtime cross-check
- **OMDb fallback** — movie and TV last-resort lookup
- **Confidence scoring** — auto-proceed / LLM-verify / Review Queue routing based on thresholds
- **LLM verification** — Anthropic, OpenAI, and Ollama backends; structured prompt with candidate list
- **HEVC routing** — confirmed files moved to correct library path with Plex/Jellyfin-compatible naming
- **AVC staging** — AVC files renamed, staged, and enqueued for encode; post-encode library move confirmed
- **Review Queue** — all failure modes produce entries with correct reason strings; entries persist across restarts
- **Review Queue UI** — candidate display, manual search, retry, discard, re-add all functional
- **Duplicate detection** — duplicate at destination always routes to Review Queue (never silent overwrite)
- **Duplicate UI** — Replace / Keep Existing actions shown; side-by-side codec/resolution/bitrate/size comparison displayed when duplicate_info present
- **SmartShrink** — VMAF-driven CRF iteration, loops until size target met or quality floor hit
- **Fixed Reduction** — CRF adjusted to hit user-specified size reduction percentage
- **Hardware encode** — NVIDIA NVENC, AMD/Intel VAAPI, CPU fallback all confirmed functional
- **Cross-device move** — EXDEV caught and retried with copy+rename; temp-name strategy prevents partial-file indexing
- **SMTP notifications** — Gmail App Password and self-hosted SMTP confirmed; per-event toggles and batched digest working
- **Windows native** — path separators handled correctly throughout; breadcrumb API normalizes to forward slashes for the frontend
- **Settings UI** — all config fields round-trip correctly; live save to mediaforge.yaml confirmed
- **First-run wizard** — triggers on missing config, guides through folder setup and API key entry; auto-populates standard paths when running inside Docker container
- **Recursive subfolder watching** — watch folder scan uses `filepath.WalkDir`; video files in subdirectories at any depth are picked up and processed automatically
- **Extended codec routing** — mpeg2video, mpeg4, vc1, vp9, and av1 all handled; mpeg2/mpeg4/vc1 route to encode queue, vp9/av1 pass through as HEVC-equivalent
- **DVD subtitle passthrough** — subtitle streams passed through to MKV output; dropped with a warning log entry when output container is MP4
- **Session log rotation** — structured log written to `config/logs/mediaforge.log`; rotates with 2 backups so log history survives restarts
- **TVDB confidence scoring** — rewrote to 4-component weighted formula (series name, premiere year, episode record, network); eliminates false high-confidence matches
- **Windows service** — `--install`, `--uninstall`, and `--service` flags; service runs under the invoking user account so network share credentials are inherited
- **Post-encode move retry** — transient move failures after encode are retried without re-running codec detection or re-encoding
- **Ollama timeout** — LLM request timeout raised to 120 s to accommodate Ollama cold-start model load
- **Build number injection** — `internal/version` package; `version.Build` overridden at compile time via `-ldflags`; banner and startup log show `v1.0.0+build.<N>`

---

## In Progress / Next Sessions

Work that has started or is queued for the next development sessions:

- **Browse path fix** — Windows drive letters (`C:\`) and UNC paths (`\\server\share`) not resolving correctly in the file picker; breadcrumb generation needs special-casing for both forms
- **Nil pointer panic on network share walk** — directory walker dereferences a nil pointer when a network share becomes unavailable mid-walk; needs a nil guard and Review Queue entry
- **TMDB movie confidence scoring** — year removed as a search-API filter; scoring now uses title (60%), year ±0/±1 (30%/15%), runtime ±5 min (10%); override rules: exact title + exact year → 0.95, title match + year off >1 → 0.70 *(landed in build 8cf6a69)*
- **SmartShrink quality cascade** — when Excellent quality preset produces no result within size bounds, automatically fall back to Good, then Acceptable, before sending to Review Queue
- **CRF search range expansion** — lower CRF floor from 28 to 16 to give SmartShrink more headroom on high-motion content
- **VMAF sample count configurable** — expose VMAF sample count as a config field (default 5); higher values increase accuracy at the cost of analysis time
- **Windows tray app** — system-tray icon using `systray`; actions: pause/resume queue, open browser, show notification count badge
- **Inno Setup installer** — Windows `.exe` installer that registers the service, creates start-menu shortcuts, and sets default config paths

---

## Future Enhancements (Post v1.0)

### Intelligent Duplicate Resolution

Currently all duplicates are routed to the Review Queue for manual decision. A future release should add automatic resolution logic before falling back to manual review:

**Auto-resolution rules (in order):**

1. **Resolution mismatch — keep higher**
   - Incoming higher resolution than existing → auto-replace (log action, no queue entry)
   - Existing higher resolution than incoming → auto-keep (discard incoming, log action, no queue entry)

2. **Same resolution, same codec — VMAF comparison**
   - Run VMAF on both files against a common reference (or compare encode bitrate as proxy)
   - Higher VMAF score wins → auto-replace or auto-keep accordingly
   - If VMAF scores within threshold (e.g. < 1.0 point difference) → send to Review Queue (too close to call automatically)

3. **Same resolution, different codec**
   - Prefer HEVC over AVC at same resolution (lossless upgrade)
   - Otherwise → Review Queue

4. **Ambiguous / inconclusive** → Review Queue with full context

**Review Queue entry for unresolved duplicates** (already implemented):
- Side-by-side panel: codec, resolution, bitrate, file size for incoming vs existing
- VMAF scores if comparison was run (future)
- **Replace** button — overwrites existing with incoming
- **Keep Existing** button — discards incoming, removes entry

**Config additions (future):**
```yaml
intake:
  duplicate_resolution: "manual"   # "manual" | "auto" | "prefer_higher_res"
  duplicate_vmaf_threshold: 1.0    # auto-keep if VMAF difference below this
```

### Other Post-v1.0 Candidates

- Automatic library scanning (index files already in library on startup)
- Per-show override for naming templates
- Subtitle file handling (move `.srt`/`.ass` alongside the video)
- Multi-file episode detection (single file containing multiple episodes)
- Webhook outbound notifications
- Mobile-friendly UI polish
- Stats circular gauge with 30-day trend line (speedometer-style radial gauge on the dashboard)
- Configurable VMAF sample count per-job

---

## Non-Goals for v1

- No scraping — all metadata via official APIs only
- No automatic library scanning — intake only handles files as they arrive
- No multi-file episode detection — single file per episode only
- No media server integration API — MediaForge writes files, media server picks them up by watching the folder

---

## Web UI

The web UI is the primary interface for MediaForge. It should be functional, informative, and require minimal clicks for common tasks.

### Dashboard (main view)

**Active Queue**
- List of all files currently in the pipeline, one row per file
- Columns: filename, detected type (Movie/TV/Unknown), current stage (Detecting / Identifying / Encoding / Moving), progress bar for encode stage, ETA
- Encode progress shows percentage + current fps + estimated time remaining
- Clicking a row expands inline detail (ffprobe info, matched metadata, encode settings in use)

**Storage Savings Gauge**
- Circular/radial gauge (speedometer style) showing lifetime storage saved
- Center value: total GB/TB saved
- Arc fill: percentage of processed files that resulted in size reduction
- Below gauge: two secondary stats
  - Lifetime savings (total, with reset button)
  - Since last reset (date of last reset shown)
- Small trend line or bar chart showing savings per day/week (last 30 days)

**Stats Bar**
- Files processed (lifetime / since reset)
- Average size reduction percentage
- Total files in Review Queue (clickable, jumps to Review Queue tab)
- Current encode queue depth

### Queue Management

**Manual Add button** (prominent, always visible)
- Accepts individual files or entire folders (recursive)
- On add, user chooses pipeline mode:
  - **Full pipeline** — identify, rename, route, encode if AVC
  - **Encode only** — skip identification, go straight to encode queue with current default settings
  - **Encode only with custom settings** — encode queue with per-job override of preset, speed, reduction target, output container
- Folder add shows a preview of files found before confirming

**Queue controls**
- Pause / Resume all
- Cancel individual job (with confirmation)
- Reorder queue (drag or priority bump)
- Clear completed jobs from view

### Review Queue tab

Separate tab, always shows count badge when items are present.

Each entry shows:
- Original filename + full source path
- Reason for queue entry (specific human-readable message)
- ffprobe info (codec, duration, resolution, container format)
- Top metadata candidates with poster thumbnails (if lookup ran)
- LLM best guess + reasoning (if LLM ran)
- Timestamp
- For duplicate entries: side-by-side comparison panel (incoming vs existing)

Per-entry actions:
- **Pick a candidate** from lookup results
- **Manual search** — user types a title/year, triggers fresh lookup
- **Re-add with different settings** — opens the same modal as Manual Add, pre-populated with this file
- **Retry** — re-runs from the failed step with current settings
- **Discard** — moves file to configured discard folder, logs action, removes from queue
- **Replace** (duplicate entries) — overwrites existing with incoming file
- **Keep Existing** (duplicate entries) — discards incoming, removes entry

Bulk actions: retry all, discard all selected.

### Configuration

Settings accessible via a slide-out drawer or dedicated Settings page.

Settings sections:
- **Intake** — watch folder, staging folder, library paths, stability check intervals
- **Identification** — API keys (TMDB, TVDB, OMDb), confidence thresholds, LLM backend config
- **Naming** — template strings for movie/episode file and folder naming, live preview of template output
- **Transcode** — encode mode (SmartShrink / Fixed Reduction), quality preset, speed preset, output container, GPU selection, working directory
- **Notifications** — see Notifications section
- **Stats** — reset lifetime stats button, reset since-last-reset button

---

## Multi-Episode File Naming

Plex, Jellyfin, and Emby all use the following convention for a single file containing multiple consecutive episodes:

```
{Show} - S{XX}E{XX}-E{XX} - {Episode Title 1} + {Episode Title 2}.{ext}
```

Examples:
```
Breaking Bad - S01E01-E02 - Pilot + Cat's in the Bag.mkv
The Office - S03E01-E02 - Gay Witch Hunt + The Convention.mkv
```

Detection: filename contains `SxxExxExx` or `SxxExx-Exx` pattern.
Naming: always use the hyphen-E form (`E01-E02`) — this is the form all three major media servers recognize.
Episode titles: concatenate with ` + ` separator.
If second episode title lookup fails: use first episode title only, append `+ Episode {N}` as fallback.

---

## Notifications

### Retained from Shrinkray
All existing Shrinkray notification integrations are preserved as-is.

### Added: Email (SMTP)
Full SMTP email notification support:

**Gmail (default / recommended for most users)**
```yaml
notifications:
  email:
    enabled: false
    smtp_host: smtp.gmail.com
    smtp_port: 587
    smtp_tls: true
    username: ""          # Gmail address
    password: ""          # Gmail App Password (not account password)
    from: ""
    to: ""
```

Gmail requires an App Password when 2FA is enabled — document this prominently in the Settings UI with a link to the Google App Passwords page.

**Self-hosted SMTP**
Same fields, user fills in their own host/port/TLS settings. Supports:
- Standard SMTP with STARTTLS (port 587)
- SMTP over TLS (port 465)
- Plain SMTP (port 25, for internal mail servers)

### Notification events (all user-toggleable individually)

| Event | Default |
|---|---|
| Encode complete (per file) | Off |
| Encode failed (per file) | On |
| File landed in Review Queue | On |
| Daily stats summary | Off |
| Weekly stats summary | Off |

### Frequency modes (user-configurable)
- **Per-file** — notification fires immediately for each event
- **Batched digest** — events are collected and sent as a single digest on a user-configured schedule
- Both modes available, user selects per notification channel

### Notification content
Each notification includes:
- Event type
- Filename
- Reason (for failures and Review Queue entries — specific error, not generic)
- Brief stats snapshot (files processed today, storage saved today)
- Direct link to MediaForge web UI (configurable base URL in settings)

### Test button
Settings UI includes a "Send test notification" button per channel.

---

## Stats Tracking

All stats are persisted in the MediaForge database (SQLite).

### Per-file record
- Original filename, source path
- Detected codec, container, resolution, duration
- Matched title, year, TMDB/TVDB ID
- Pipeline mode used
- Original file size
- Output file size (post-encode, if applicable)
- Size reduction percentage
- Encode duration (wall time)
- Encode settings used (preset, speed, GPU/CPU)
- Outcome (success / review queue / discarded)
- Timestamp

### Aggregate stats (displayed on dashboard)
- Total files processed (lifetime / since reset)
- Total storage saved in GB/TB (lifetime / since reset)
- Average size reduction percentage
- Encode success rate
- Average encode speed (fps)
- Most common failure reason (last 30 days)

### Reset behavior
Two independent reset points:
- **Lifetime** — full reset, clears all aggregate counters (per-file records optionally retained for audit)
- **Since last reset** — rolling counter, resets independently, date of last reset shown on dashboard
