# MediaForge

**Ingest · Transcode · Organize**

MediaForge is a self-hosted media ingest engine. Drop a file in the watch folder and MediaForge handles the rest — it detects the codec, identifies the title via TVDB/TMDB/OMDb, renames it to match your media server's conventions, and routes it straight to your library. H.264 files are automatically queued for hardware-accelerated HEVC encoding first. Nothing lands in your library until it's identified, named correctly, and fully processed.

MediaForge is not a media server. It feeds Plex, Jellyfin, Emby, or any other media server that watches a library folder.

---

## What it does

- **Watches** a folder for incoming media files
- **Detects** codec via ffprobe (not filename guessing)
- **Identifies** movies via TMDB with OMDb fallback; TV shows via TVDB with TMDB and OMDb fallback
- **Verifies** matches using an optional LLM (Anthropic, OpenAI, or local Ollama)
- **Renames** to Plex/Jellyfin/Emby-compatible naming conventions with configurable templates
- **Routes** HEVC files straight to your library; H.264 files to the encode queue first
- **Encodes** H.264 → HEVC using hardware acceleration (NVIDIA, AMD, Intel) or CPU fallback
- **Moves** finished files to your library, handling cross-device moves cleanly
- **Never fails silently** — anything that can't be processed automatically lands in the Review Queue with a specific reason

---

## Quick start (Unraid)

Search **MediaForge** in Community Applications and install, or use the template manually:

1. Set your paths:
   - `/config` → appdata location (e.g. `/mnt/user/appdata/mediaforge`)
   - `/incoming` → watch folder for new media
   - `/staging` → fast storage for encode working files (SSD/cache pool recommended)
   - `/media/Movies` → movie library output
   - `/media/TV Shows` → TV show library output
2. Add your API keys in Settings (TMDB required, TVDB required for TV, OMDb optional)
3. Configure GPU passthrough if using hardware encoding (see GPU Setup below)
4. Open the web UI at `http://[IP]:8080`

---

## Quick start (Docker Compose)

```yaml
services:
  mediaforge:
    image: ghcr.io/braydin72/mediaforge:dev
    container_name: mediaforge
    runtime: nvidia                          # remove if not using NVIDIA
    environment:
      - PUID=99
      - PGID=100
      - NVIDIA_VISIBLE_DEVICES=all           # remove if not using NVIDIA
      - NVIDIA_DRIVER_CAPABILITIES=all       # remove if not using NVIDIA
    ports:
      - 8080:8080
    volumes:
      - /path/to/appdata/mediaforge:/config
      - /path/to/incoming:/incoming
      - /path/to/cache/staging:/staging
      - /path/to/media/Movies:/media/Movies
      - /path/to/media/TV Shows:/media/TV Shows
    restart: unless-stopped
```

---

## GPU setup

Hardware encoding is optional. MediaForge falls back to CPU encoding if no GPU is configured.

**NVIDIA**
Install the Nvidia-Driver plugin on Unraid (reboot after), then add to Extra Parameters:
```
--runtime=nvidia
```
Set environment variables:
```
NVIDIA_VISIBLE_DEVICES=all
NVIDIA_DRIVER_CAPABILITIES=all
```
For multi-GPU systems, set `NVIDIA_VISIBLE_DEVICES` to a specific GPU UUID from the Nvidia-Driver plugin page.

**Intel / AMD**
Add to Extra Parameters:
```
--device=/dev/dri
```
Set `PGID` to the group that owns `/dev/dri/renderD128` on your host.

MediaForge automatically detects and uses the best available hardware encoder. If hardware encoding fails mid-job, it falls back to the next available encoder automatically.

---

## Configuration

Configuration is stored in `/config/mediaforge.yaml`. Most settings are available in the web UI.

```yaml
mediaforge:
  intake:
    enabled: true
    watch_folder: /incoming
    staging_folder: /staging
    library:
      movies: /media/Movies
      tv_shows: /media/TV Shows

  apis:
    tmdb_key: ""       # required — tmdb.org (free)
    tvdb_key: ""       # required for TV — thetvdb.com (free account)
    omdb_key: ""       # optional fallback — omdbapi.com (free tier)

  llm:
    backend: ""        # "anthropic" | "openai" | "ollama" | "" (disabled)
    api_key: ""
    model: ""
    ollama_host: "http://localhost:11434"

  transcode:
    output_container: "mkv"         # "mkv" | "mp4" | "preserve"
    mode: "smartshrink"             # "smartshrink" | "fixed_reduction"
    quality_preset: "good"          # "excellent" | "good" | "acceptable"
    encoder_speed: "medium"         # "slowest" | "slower" | "slow" | "medium" | "fast"
    gpu: "nvidia"                   # "nvidia" | "amd" | "intel" | "cpu"
```

---

## Encode modes

**SmartShrink** (default)
Select a quality target (Excellent / Good / Acceptable). MediaForge encodes at the corresponding CRF, measures VMAF and output size, and automatically adjusts CRF upward if the output is larger than the source — continuing until it finds the best size-to-quality ratio within your quality floor. Never gives up and leaves a file untouched.

**Fixed Reduction**
Specify a target size reduction percentage (e.g. 40% = output should be ~60% of original size). MediaForge adjusts CRF until that target is reached regardless of VMAF score.

In both modes, if no acceptable result can be found, the file goes to the Review Queue with a specific reason rather than being silently abandoned.

---

## Network share support

Library paths (`/media/Movies`, `/media/TV Shows`) can point to network-mounted shares. MediaForge has no SMB/CIFS awareness — it works with whatever path the OS presents.

**Unraid:** Use the Unassigned Devices plugin to mount a remote SMB share at the host level, then bind-mount that path into the container as a volume.

**Linux:** Mount via `/etc/fstab` or `autofs` with `cifs` type, point MediaForge at the mount point.

**Windows:** UNC paths (`\\server\share\Movies`) work natively when running the Windows build as a user account with network credentials.

MediaForge handles cross-device moves (local staging → network share library) automatically using a copy-then-atomic-rename strategy, ensuring your media server never indexes a partial file.

---

## Naming conventions

Default templates match Plex, Jellyfin, and Emby out of the box:

```
Movies:   {Title} ({Year}) / {Title} ({Year}).mkv
TV Shows: {Show} ({Year}) / Season 01 / {Show} - S01E01 - {Episode Title}.mkv
Multi-ep: {Show} - S01E01-E02 - {Title 1} + {Title 2}.mkv
```

Templates are configurable in Settings. Supported tokens: `{title}`, `{show}`, `{year}`, `{season:02d}`, `{episode:02d}`, `{episode_title}`.

---

## Review Queue

Every file that can't be processed automatically lands in the Review Queue — never silently dropped, never stranded with no explanation. Each entry shows the specific reason (e.g. "No metadata match found after TVDB, TMDB, and OMDb lookups" or "Encode failed: output larger than source at all CRF values within quality constraints"), the top lookup candidates with poster thumbnails, and the LLM's best guess if one is configured.

From the Review Queue you can pick a candidate, search manually, re-add with different settings, retry automatically, or discard.

---

## Notifications

MediaForge supports push notifications (Pushover and others inherited from Shrinkray) plus email via SMTP.

**Gmail:** Use an App Password, not your account password. [Get one here](https://myaccount.google.com/apppasswords) (requires 2FA enabled).

**Self-hosted SMTP:** Configure host, port, and TLS settings in the Notifications section of Settings. Supports STARTTLS (587), TLS (465), and plain SMTP (25).

Notification events (individually toggleable): encode complete, encode failed, file landed in Review Queue, daily stats summary, weekly stats summary. Per-file or batched digest mode, configurable per channel.

---

## Stats

The dashboard shows lifetime storage saved on a circular gauge, plus files processed, average size reduction, encode success rate, and a 30-day savings trend. Two independent reset counters: lifetime and since-last-reset.

---

## Building from source

Requires Go 1.21+ and ffmpeg/ffprobe available on PATH.

```bash
git clone https://github.com/braydin72/mediaforge.git
cd mediaforge
go build -o mediaforge .
./mediaforge
```

---

## Attribution

MediaForge is built on [shrinkray](https://github.com/gwlsn/shrinkray), originally created by **@gwlsn**. The transcode engine, hardware acceleration logic, web UI foundation, SmartShrink VMAF optimization, and Docker packaging all originate from that project.

Additional contributions to the Shrinkray lineage from **@jesposito** and **@akaBilih**.

This project was developed with AI assistance. Claude intentionally left as a contributor. All generated code is manually reviewed.

---

## License

MIT — see [LICENSE](LICENSE) for details.
See [SHRINKRAY_CHANGELOG.md](SHRINKRAY_CHANGELOG.md) for pre-fork history.
