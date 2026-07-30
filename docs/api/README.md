# MediaForge API reference

MediaForge exposes a REST API for managing video transcoding jobs. All endpoints return JSON.

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
| POST | `/queue/start` | Start/resume the encode queue |
| POST | `/queue/pause` | Pause the encode queue (in-flight jobs finish; no new jobs picked up) |
| POST | `/queue/stop` | Stop the encode queue |
| POST | `/intake/pause` | Pause the intake watcher |
| POST | `/intake/resume` | Resume the intake watcher |
| GET | `/intake/status` | Get intake watcher status |
| GET | `/config` | Get current configuration |
| PUT | `/config` | Update configuration |
| GET | `/logs` | Get application log output |
| GET | `/stats` | Get queue statistics |
| POST | `/stats/reset-session` | Reset session statistics |
| POST | `/cache/clear` | Clear file metadata cache |
| POST | `/pushover/test` | Test Pushover notifications |
| POST | `/notifications/test` | Send a test notification on a configured channel |
| GET | `/review` | List Review Queue entries |
| GET | `/review/count` | Get Review Queue entry count |
| GET | `/review/{id}/search` | Manual title/year search for a Review Queue entry |
| PUT | `/review/{id}/resolve` | Resolve an entry by picking a lookup candidate |
| PUT | `/review/{id}/discard` | Discard a single entry |
| PUT | `/review/{id}/replace` | Replace the existing library file with the incoming one (duplicate entries) |
| PUT | `/review/{id}/resubmit` | Re-add a single entry to the pipeline, optionally with custom encode settings |
| PUT | `/review/bulk/discard` | Discard multiple selected entries |
| PUT | `/review/bulk/replace` | Replace existing library files for multiple selected duplicate entries |
| PUT | `/review/bulk/resubmit` | Re-add multiple selected entries to the pipeline |

## Detailed documentation

- [Jobs API](jobs.md) - Job management, SSE events, queue control
- [Browse API](browse.md) - File browsing and media discovery
- [Config API](config.md) - Configuration management
- [Presets and encoders](presets.md) - Available presets and hardware detection

The Review Queue, intake watcher control, and logs endpoints above don't have a
dedicated doc yet — see `internal/api/handler.go` and `internal/api/router.go`
for request/response details in the meantime.
