# Shrinkray API reference

Shrinkray exposes a REST API for managing video transcoding jobs. All endpoints return JSON.

**Base URL**: `http://localhost:8080/api`

## Quick reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/browse` | List files and directories |
| GET | `/presets` | List available presets |
| GET | `/encoders` | List detected hardware encoders |
| GET | `/jobs` | List all jobs with stats |
| POST | `/jobs` | Create transcoding jobs |
| GET | `/jobs/stream` | SSE stream for real-time updates |
| POST | `/jobs/clear` | Clear completed/failed jobs |
| GET | `/jobs/{id}` | Get single job details |
| DELETE | `/jobs/{id}` | Cancel a job |
| POST | `/jobs/{id}/retry` | Retry a failed job |
| POST | `/queue/pause` | Pause all processing |
| POST | `/queue/resume` | Resume processing |
| GET | `/config` | Get current configuration |
| PUT | `/config` | Update configuration |
| GET | `/stats` | Get queue statistics |
| POST | `/stats/reset-session` | Reset session statistics |
| POST | `/cache/clear` | Clear file metadata cache |
| POST | `/pushover/test` | Test Pushover notifications |

## Detailed documentation

- [Jobs API](jobs.md) - Job management, SSE events, queue control
- [Browse API](browse.md) - File browsing and media discovery
- [Config API](config.md) - Configuration management
- [Presets and encoders](presets.md) - Available presets and hardware detection
