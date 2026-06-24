# Browse API

Discover and browse video files in your media library.

## Browse directory

```
GET /api/browse?path=/media/Movies
```

List contents of a directory with video metadata.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `path` | Config `media_path` | Directory to browse |

**Response:**

```json
{
  "path": "/media/Movies",
  "parent": "/media",
  "entries": [
    {
      "name": "Action",
      "path": "/media/Movies/Action",
      "is_dir": true,
      "size": 0,
      "mod_time": "2024-01-15T10:00:00Z",
      "file_count": 12,
      "total_size": 42949672960
    },
    {
      "name": "Movie.mkv",
      "path": "/media/Movies/Movie.mkv",
      "is_dir": false,
      "size": 4294967296,
      "mod_time": "2024-01-15T10:00:00Z",
      "video_info": {
        "path": "/media/Movies/Movie.mkv",
        "size": 4294967296,
        "duration": 7200000000000,
        "format": "matroska,webm",
        "video_codec": "h264",
        "audio_codec": "aac",
        "width": 1920,
        "height": 1080,
        "bitrate": 5000000,
        "frame_rate": 23.976,
        "is_hevc": false,
        "is_av1": false,
        "profile": "High",
        "pix_fmt": "yuv420p",
        "bit_depth": 8,
        "color_transfer": "bt709",
        "color_primaries": "bt709",
        "color_space": "bt709",
        "is_hdr": false
      }
    }
  ],
  "video_count": 5,
  "total_size": 21474836480
}
```

### Entry fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Filename or directory name |
| `path` | string | Absolute path |
| `is_dir` | bool | True for directories |
| `size` | int64 | File size in bytes |
| `mod_time` | string | Last modified timestamp (RFC 3339) |
| `file_count` | int | Directories only: number of video files |
| `total_size` | int64 | Directories only: total size of video files |
| `video_info` | object | Video files only: probe metadata (see below) |

### Video info fields

| Field | Type | Description |
|-------|------|-------------|
| `path` | string | Absolute file path |
| `size` | int64 | File size in bytes |
| `duration` | int64 | Duration in nanoseconds (`time.Duration`) |
| `format` | string | Container format |
| `video_codec` | string | Codec name (h264, hevc, av1, etc.) |
| `audio_codec` | string | Audio codec name |
| `width` | int | Video width in pixels |
| `height` | int | Video height in pixels |
| `bitrate` | int64 | Video bitrate in bits/second |
| `frame_rate` | float64 | Frames per second |
| `is_hevc` | bool | True if already HEVC encoded |
| `is_av1` | bool | True if already AV1 encoded |
| `profile` | string | Codec profile (High, Main, Main 10) |
| `pix_fmt` | string | Pixel format (yuv420p, yuv420p10le) |
| `bit_depth` | int | Color bit depth (8, 10, 12) |
| `color_transfer` | string | Transfer characteristics (bt709, smpte2084) |
| `color_primaries` | string | Color primaries (bt709, bt2020) |
| `color_space` | string | Color space (bt709, bt2020nc) |
| `is_hdr` | bool | True if HDR content (HDR10, HLG) |

**Errors:**
- `500` - Directory not found or inaccessible

## Clear cache

```
POST /api/cache/clear
```

Clear the file metadata cache. Useful after adding new files or if metadata appears stale.

**Response:**

```json
{
  "status": "cache cleared"
}
```

## Notes

- Metadata is cached to speed up browsing large directories
- Cache validation uses inode + file size to detect replaced files
- Entries are sorted: directories first, then alphabetically by name
- Hidden files and directories (starting with `.`) are excluded
- Non-video files are included in entries but without `video_info`
