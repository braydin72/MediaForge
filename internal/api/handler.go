package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mediaforge "github.com/braydin72/mediaforge"
	"github.com/braydin72/mediaforge/internal/browse"
	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/ffmpeg/vmaf"
	"github.com/braydin72/mediaforge/internal/intake"
	"github.com/braydin72/mediaforge/internal/jobs"
	"github.com/braydin72/mediaforge/internal/logger"
	"github.com/braydin72/mediaforge/internal/notify"
	"github.com/braydin72/mediaforge/internal/pushover"
	"github.com/braydin72/mediaforge/internal/store"
	"github.com/braydin72/mediaforge/internal/upgrade"
	"github.com/braydin72/mediaforge/internal/util"
	"github.com/google/uuid"
)

// StatsStore defines the interface for stats-related store operations.
type StatsStore interface {
	ResetSession() error
}

// ReviewQueueStore defines the interface for Review Queue read/write operations.
type ReviewQueueStore interface {
	AddToReviewQueue(e *store.ReviewEntry) error
	GetReviewQueue() ([]store.ReviewEntry, error)
	GetReviewEntry(id string) (*store.ReviewEntry, error)
	GetReviewQueueCount() (int, error)
	UpdateReviewQueueStatus(id, status string) error
	UpdateReviewEntryReason(id, reason string) error
	BulkUpdateReviewQueueStatus(ids []string, status string) error
	ConvertReviewEntryToDuplicate(id, reason, duplicateInfoJSON string) error
}

// isMetadataPickCategory reports whether a review entry's category supports
// Pick Selected / Search Manually (candidate-driven metadata resolution).
// An empty category means the entry predates the Category field and is
// treated as the legacy fallback, which historically supported these actions.
func isMetadataPickCategory(category string) bool {
	switch store.ReviewEntryCategory(category) {
	case store.ReviewCategoryMetadataFailure, store.ReviewCategoryUnresolvedMultipart, "":
		return true
	default:
		return false
	}
}

// isEncodeFailureCategory reports whether a review entry's category supports
// Re-encode Custom. An empty category is the legacy fallback (see above).
func isEncodeFailureCategory(category string) bool {
	switch store.ReviewEntryCategory(category) {
	case store.ReviewCategoryEncodeFailure, "":
		return true
	default:
		return false
	}
}

// Handler provides HTTP API handlers
type Handler struct {
	browser     *browse.Browser
	queue       *jobs.Queue
	workerPool  *jobs.WorkerPool
	cfg         *config.Config
	cfgPath     string
	pushover    *pushover.Client
	dispatcher  *notify.Dispatcher
	notifyMu    sync.Mutex       // Protects notification sending to prevent duplicates
	store       StatsStore       // For stats operations (may be nil)
	reviewStore ReviewQueueStore // For Review Queue operations (may be nil)
	watcher     *intake.Watcher  // For full_pipeline mode (may be nil)

	// reviewMoveMu serializes Review Queue Replace file moves (startReplaceMove).
	// Single Replace and BulkReplaceReviewEntries both spawn one goroutine per
	// entry; without this, a bulk replace fires every move concurrently, and
	// hammering the same network-share destination with simultaneous renames at
	// the same instant has been observed to trip a Windows sharing-violation
	// error on every single one (confirmed live against a real \\TOWER\Media
	// SMB share — the prior synchronous one-at-a-time loop never hit this).
	// The moves still run asynchronously from the HTTP response/progress-bar
	// perspective; only the actual file I/O is serialized to one at a time.
	reviewMoveMu sync.Mutex
}

// NewHandler creates a new API handler
func NewHandler(browser *browse.Browser, queue *jobs.Queue, workerPool *jobs.WorkerPool, cfg *config.Config, cfgPath string) *Handler {
	d := notify.NewDispatcher(&cfg.Notifications)
	smtpClient := notify.NewSMTPClient(&cfg.Notifications.Email)
	d.AddChannel(smtpClient, cfg.Notifications.Email.IntervalMinutes)

	h := &Handler{
		browser:    browser,
		queue:      queue,
		workerPool: workerPool,
		cfg:        cfg,
		cfgPath:    cfgPath,
		pushover:   pushover.NewClient(cfg.PushoverUserKey, cfg.PushoverAppToken),
		dispatcher: d,
	}

	// Wired once here so encode-complete/failed notifications fire exactly
	// once per event regardless of how many SSE clients (browser tabs) are
	// connected to /api/jobs/stream — previously each connected client
	// independently dispatched the same notification (see sse.go history).
	queue.OnTerminalEvent = h.dispatchEncodeEvent

	return h
}

// Dispatcher returns the notification dispatcher so callers (e.g. main.go) can
// call Stop() on shutdown.
func (h *Handler) Dispatcher() *notify.Dispatcher {
	return h.dispatcher
}

// SetStore sets the stats store for session/lifetime stats operations.
func (h *Handler) SetStore(store StatsStore) {
	h.store = store
}

// SetReviewStore sets the Review Queue store.
func (h *Handler) SetReviewStore(s ReviewQueueStore) {
	h.reviewStore = s
}

// SetIntakeWatcher sets the intake watcher used for full_pipeline job creation.
func (h *Handler) SetIntakeWatcher(w *intake.Watcher) {
	h.watcher = w
}

// response helpers

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// Validation helpers for config updates

// validateQuality validates a quality/CRF value for the given codec.
// Returns an error message if invalid, empty string if valid.
func validateQuality(value int, codec string) string {
	// 0 = auto mode (use encoder-specific default)
	if value == 0 {
		return ""
	}
	var min, max int
	switch codec {
	case "hevc":
		min, max = 16, 30
	case "av1":
		min, max = 18, 35
	default:
		return fmt.Sprintf("unknown codec: %s", codec)
	}
	if value < min || value > max {
		return fmt.Sprintf("quality_%s must be between %d and %d (or 0 for auto)", codec, min, max)
	}
	return ""
}

// validateScheduleHour validates a schedule hour value (0-23).
// Returns an error message if invalid, empty string if valid.
func validateScheduleHour(value int, field string) string {
	if value < 0 || value > 23 {
		return fmt.Sprintf("%s must be between 0 and 23", field)
	}
	return ""
}

// Browse handles GET /api/browse?path=...
func (h *Handler) Browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = h.cfg.MediaPath
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.browser.Browse(ctx, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Normalize to forward slashes so the JS breadcrumb builder works on Windows.
	result.Path = filepath.ToSlash(result.Path)
	result.Parent = filepath.ToSlash(result.Parent)
	for _, e := range result.Entries {
		e.Path = filepath.ToSlash(e.Path)
	}

	writeJSON(w, http.StatusOK, result)
}

// Presets handles GET /api/presets
func (h *Handler) Presets(w http.ResponseWriter, r *http.Request) {
	presets := ffmpeg.ListPresets()
	writeJSON(w, http.StatusOK, presets)
}

// logsDir returns the directory containing the app's rotated log files,
// derived the same way cmd/mediaforge/main.go derives it for the logger.
func (h *Handler) logsDir() string {
	dir := filepath.Dir(h.cfgPath)
	if dir == "." || h.cfgPath == "" {
		dir = "config"
	}
	return filepath.Join(dir, "logs")
}

// logFileNames maps the "file" query param to its on-disk log file name.
var logFileNames = map[string]string{
	"current": "mediaforge.log",
	"1":       "mediaforge.1.log",
	"2":       "mediaforge.2.log",
}

// GetLogs handles GET /api/logs?file=current|1|2&lines=200
func (h *Handler) GetLogs(w http.ResponseWriter, r *http.Request) {
	fileParam := r.URL.Query().Get("file")
	if fileParam == "" {
		fileParam = "current"
	}
	fileName, ok := logFileNames[fileParam]
	if !ok {
		writeError(w, http.StatusBadRequest, "file must be 'current', '1', or '2'")
		return
	}

	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines < 1 || lines > 1000 {
		lines = 200
	}

	dir := h.logsDir()

	available := map[string]bool{}
	for key, name := range logFileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			available[key] = true
		} else {
			available[key] = false
		}
	}

	if !available[fileParam] {
		writeError(w, http.StatusNotFound, "log file not found")
		return
	}

	content, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	allLines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(allLines) == 1 && allLines[0] == "" {
		allLines = allLines[:0]
	}
	total := len(allLines)
	tail := allLines
	if total > lines {
		tail = allLines[total-lines:]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"file":       fileParam,
		"lines":      tail,
		"totalLines": total,
		"available":  available,
	})
}

// Encoders handles GET /api/encoders
func (h *Handler) Encoders(w http.ResponseWriter, r *http.Request) {
	encoders := ffmpeg.ListAvailableEncoders()
	best := ffmpeg.GetBestEncoder()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"encoders":       encoders,
		"best":           best,
		"vmaf_available": vmaf.IsAvailable(),
		"vmaf_models":    vmaf.GetModels(),
	})
}

// CreateJobsRequest is the request body for creating jobs.
// PipelineMode controls how the file is processed:
//   - "" or "encode_only": direct to encode queue (default, preserves legacy behavior)
//   - "encode_only_custom": encode queue with per-job speed and output container overrides
//   - "full_pipeline": routed through the intake identification pipeline
type CreateJobsRequest struct {
	Paths              []string `json:"paths"`
	PresetID           string   `json:"preset_id"`
	SmartShrinkQuality string   `json:"smartshrink_quality,omitempty"`
	PipelineMode       string   `json:"pipeline_mode,omitempty"`        // "full_pipeline" | "encode_only" | "encode_only_custom"
	EncodeSpeed        string   `json:"encode_speed,omitempty"`         // encode_only_custom: override encoder speed preset
	EncodeOutputFormat string   `json:"encode_output_format,omitempty"` // encode_only_custom: override output container
	EncodeQualityCRF   int      `json:"encode_quality_crf,omitempty"`   // encode_only_custom: override CRF for Compress presets
	AddOnly            bool     `json:"add_only,omitempty"`             // encode_only/encode_only_custom: "Add to Queue" (append, no unpause) vs "Start Encode" (priority + auto pause/resume)
}

// sendSystemFailureToReview writes a Review Queue entry for an out-of-band
// failure that has nothing to do with identification/encoding (e.g. a
// Manual Add file-copy error), so it's never silently dropped. No-op if no
// review store is configured.
func (h *Handler) sendSystemFailureToReview(path, reason string) {
	if h.reviewStore == nil {
		return
	}
	entry := store.ReviewEntry{
		ID:           uuid.New().String(),
		OriginalPath: path,
		Filename:     filepath.Base(path),
		Reason:       reason,
		Category:     string(store.ReviewCategorySystemFailure),
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.reviewStore.AddToReviewQueue(&entry); err != nil {
		logger.Error("Failed to write system-failure review entry", "file", entry.Filename, "error", err)
	}
}

// CreateJobs handles POST /api/jobs
// Responds immediately and processes files in background to avoid UI freeze
func (h *Handler) CreateJobs(w http.ResponseWriter, r *http.Request) {
	var req CreateJobsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "no paths provided")
		return
	}

	// Validate pipeline_mode.
	switch req.PipelineMode {
	case "", "encode_only", "encode_only_custom", "full_pipeline":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "pipeline_mode must be 'encode_only', 'encode_only_custom', or 'full_pipeline'")
		return
	}

	// full_pipeline requires library paths; intake watcher being enabled is not required
	// (the watcher is always available for manual runs even when folder watching is off).
	if req.PipelineMode == "full_pipeline" && h.cfg.Intake.Library.Movies == "" && h.cfg.Intake.Library.TVShows == "" {
		writeError(w, http.StatusBadRequest, "Library paths not configured — set Movies and TV Shows library paths in Settings before using Full Pipeline mode.")
		return
	}

	// encode_only_custom: validate output format override if provided.
	if req.PipelineMode == "encode_only_custom" && req.EncodeOutputFormat != "" {
		switch req.EncodeOutputFormat {
		case "mkv", "mp4", "preserve":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "encode_output_format must be 'mkv', 'mp4', or 'preserve'")
			return
		}
	}

	// full_pipeline does not use a preset; encode_only modes require one.
	var smartShrinkQuality string
	if req.PipelineMode != "full_pipeline" {
		preset := ffmpeg.GetPreset(req.PresetID)
		if preset == nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown preset: %s", req.PresetID))
			return
		}

		smartShrinkQuality = req.SmartShrinkQuality
		if smartShrinkQuality != "" && !jobs.IsValidSmartShrinkQuality(smartShrinkQuality) {
			writeError(w, http.StatusBadRequest, "smartshrink_quality must be 'acceptable', 'good', or 'excellent'")
			return
		}

		if req.EncodeQualityCRF != 0 {
			if preset.IsSmartShrink {
				writeError(w, http.StatusBadRequest, "encode_quality_crf is not applicable to SmartShrink presets")
				return
			}
			if msg := validateQuality(req.EncodeQualityCRF, string(preset.Codec)); msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
		}
	}

	// Respond immediately - jobs will be added in background and appear via SSE
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "processing",
		"message": fmt.Sprintf("Processing %d paths in background...", len(req.Paths)),
	})

	if req.PipelineMode == "full_pipeline" {
		go func() {
			// resolveCtx bounds only the directory-scan + initial ffprobe step.
			resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			probes, err := h.browser.GetVideoFilesWithProgress(resolveCtx, req.Paths, nil)
			resolveCancel()
			if err != nil {
				logger.Error("full_pipeline: error resolving video files", "error", err)
				return
			}
			// Copy each file into the watch folder and let the normal folder
			// watcher pick it up on its next scan cycle. This intentionally
			// does NOT call watcher.ProcessFile directly — Manual Add's job is
			// only to get the file into the pipeline's front door, not to run
			// the pipeline itself.
			for _, p := range probes {
				dst := filepath.Join(h.cfg.Intake.WatchFolder, filepath.Base(p.Path))
				if err := util.CopyFile(p.Path, dst); err != nil {
					logger.Error("full_pipeline: failed to copy file to watch folder", "path", p.Path, "error", err)
					h.sendSystemFailureToReview(p.Path, fmt.Sprintf("Manual Add: failed to copy file to watch folder: %v", err))
				}
			}
		}()
		return
	}

	// encode_only / encode_only_custom: probe files and add to encode queue.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Progress callback broadcasts SSE events (throttled to max 10/sec).
		var lastBroadcastNano int64
		onProgress := func(probed, total int) {
			now := time.Now()
			last := time.Unix(0, atomic.LoadInt64(&lastBroadcastNano))
			if probed > 0 && probed < total && now.Sub(last) < 100*time.Millisecond {
				return
			}
			atomic.StoreInt64(&lastBroadcastNano, now.UnixNano())
			h.queue.BroadcastProgress(probed, total)
		}

		probes, err := h.browser.GetVideoFilesWithProgress(ctx, req.Paths, onProgress)
		if err != nil {
			logger.Error("Error getting video files", "error", err)
			return
		}
		if len(probes) == 0 {
			return
		}

		// Files not already under the managed staging folder (i.e. anything
		// browsed to directly, which may live on a network/library share) are
		// copied into staging first, so a later "delete on cancel" disposal
		// decision only ever removes MediaForge's own local copy — never the
		// original. See jobs.DisposeSource.
		networkOriginal := make(map[string]string) // staging path -> original path
		for _, p := range probes {
			if jobs.IsManagedLocal(p.Path, h.cfg) {
				continue
			}
			dst := filepath.Join(h.cfg.Intake.StagingFolder, filepath.Base(p.Path))
			if err := util.CopyFile(p.Path, dst); err != nil {
				logger.Error("encode_only: failed to stage local copy", "path", p.Path, "error", err)
				h.sendSystemFailureToReview(p.Path, fmt.Sprintf("Manual Add: failed to copy file to staging folder: %v", err))
				continue
			}
			networkOriginal[dst] = p.Path
			p.Path = dst
		}

		var addedJobs []*jobs.Job
		if req.AddOnly {
			addedJobs, _ = h.queue.AddMultiple(probes, req.PresetID, smartShrinkQuality)
		} else {
			addedJobs, _ = h.queue.AddMultiplePriority(probes, req.PresetID, smartShrinkQuality)
		}

		for _, job := range addedJobs {
			if orig, ok := networkOriginal[job.InputPath]; ok {
				h.queue.SetJobNetworkCopy(job.ID, orig)
			}
			// Apply per-job overrides for encode_only_custom.
			if req.PipelineMode == "encode_only_custom" && (req.EncodeSpeed != "" || req.EncodeOutputFormat != "" || req.EncodeQualityCRF != 0) {
				h.queue.SetJobOverrides(job.ID, req.EncodeSpeed, req.EncodeOutputFormat, req.EncodeQualityCRF)
			}
		}

		if !req.AddOnly {
			ids := make([]string, len(addedJobs))
			for i, job := range addedJobs {
				ids[i] = job.ID
			}
			h.workerPool.BeginPriorityBatch(ids)
		}
	}()
}

// ListJobs handles GET /api/jobs
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	allJobs := h.queue.GetAll()
	stats := h.queue.Stats()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":  allJobs,
		"stats": stats,
	})
}

// GetJob handles GET /api/jobs/:id
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path - expects /api/jobs/{id}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// CancelJob handles DELETE /api/jobs/:id
func (h *Handler) CancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// If job is running, cancel it via worker pool
	if job.Status == jobs.StatusRunning {
		h.workerPool.CancelJob(id)
	}

	// Cancel in queue
	if err := h.queue.CancelJob(id); err != nil {
		// Might already be cancelled/completed
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	cancelled := h.queue.Get(id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "cancelled", "job": cancelled})
}

// SkipCurrentJob handles POST /api/jobs/:id/skip
// Stops the in-flight encode (like Cancel) but records the job as Skipped
// rather than Cancelled, and does not pause the queue — the next pending job
// starts immediately. Returns the job so the caller can drive a file-disposal
// prompt for the interrupted source file.
func (h *Handler) SkipCurrentJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != jobs.StatusRunning {
		writeError(w, http.StatusConflict, "job is not running")
		return
	}

	h.workerPool.CancelJob(id)
	if err := h.queue.MarkSkippedByUser(id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "skipped", "job": h.queue.Get(id)})
}

// RequeueJob handles POST /api/jobs/:id/requeue
// Resets a Skipped or Cancelled job back to Pending at the front of the
// queue. Body: {"force_encode": bool} — true forces past a same-codec skip
// (used when ReQueue-ing a Skipped item), false leaves that check as-is
// (used when ReQueue-ing a Cancelled item).
func (h *Handler) RequeueJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	var req struct {
		ForceEncode bool `json:"force_encode"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	job, err := h.queue.RequeueTerminal(id, req.ForceEncode)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "requeued", "job": job})
}

// DisposeJobSource handles POST /api/jobs/:id/dispose
// Applies a file-disposal decision to a Cancelled/Skipped job's source file:
// body {"delete": true} deletes it, {"delete": false} moves it back to the
// intake watch folder. Network-copy jobs always have their local staging
// copy deleted regardless of the request body (see jobs.DisposeSource).
// Must be called after the job has stopped running (Cancel/Skip/Stop All).
func (h *Handler) DisposeJobSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status == jobs.StatusRunning {
		writeError(w, http.StatusConflict, "job is still running")
		return
	}

	var req struct {
		Delete bool `json:"delete"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if err := jobs.DisposeSource(job, h.cfg, req.Delete); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("dispose file: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disposed"})
}

// ClearQueue handles POST /api/jobs/clear
// Optional query param: ?status=pending|complete|failed|skipped|cancelled
// If status is provided, only jobs matching that status are cleared.
// Running jobs are never cleared.
func (h *Handler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	status := jobs.Status(r.URL.Query().Get("status"))
	count := h.queue.Clear(status)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cleared": count,
		"message": fmt.Sprintf("Cleared %d jobs", count),
	})
}

// PausePipeline handles POST /api/queue/pause
// Prevents new jobs from starting. Jobs already running are left to finish
// normally, not cancelled — use StopPipeline for a hard cancel.
// Called by the tray app menu and the web UI Stop button.
func (h *Handler) PausePipeline(w http.ResponseWriter, r *http.Request) {
	count := h.workerPool.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused":      true,
		"in_progress": count,
	})
}

// StartPipeline handles POST /api/queue/start
// Allows workers to pick up jobs again after a pause/stop.
// Called by the tray app menu and the web UI Start button.
func (h *Handler) StartPipeline(w http.ResponseWriter, r *http.Request) {
	h.workerPool.Unpause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused": false,
		"status": "pipeline started",
	})
}

// StopPipeline handles POST /api/queue/stop
// Cancels any currently-running job(s) and pauses the queue so nothing new
// starts automatically. Pending jobs are left intact. Called by the web UI's
// Stop icon. Returns the cancelled job(s) so the caller can drive a
// file-disposal prompt per stopped file.
func (h *Handler) StopPipeline(w http.ResponseWriter, r *http.Request) {
	stopped := h.workerPool.StopAll()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "pipeline stopped",
		"stopped": stopped,
	})
}

// GetConfig handles GET /api/config
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	// Get per-codec encoder defaults: HEVC and AV1 may use different hardware
	// (e.g., NVENC for HEVC but software for AV1 on older GPUs)
	bestHEVC := ffmpeg.GetBestEncoderForCodec(ffmpeg.CodecHEVC)
	bestAV1 := ffmpeg.GetBestEncoderForCodec(ffmpeg.CodecAV1)
	defaultHEVC, _ := ffmpeg.GetEncoderDefaults(bestHEVC.Accel)
	_, defaultAV1 := ffmpeg.GetEncoderDefaults(bestAV1.Accel)
	// Fall back to software defaults for bitrate-based encoders (VideoToolbox)
	if defaultHEVC == 0 {
		defaultHEVC = 22
	}
	if defaultAV1 == 0 {
		defaultAV1 = 25
	}

	// Return a sanitized config (no sensitive paths exposed)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":                     mediaforge.Version,
		"media_path":                  h.cfg.MediaPath,
		"original_handling":           h.cfg.OriginalHandling,
		"use_completed_dir":           h.cfg.UseCompletedDir,
		"workers":                     h.cfg.Workers,
		"has_temp_path":               h.cfg.TempPath != "",
		"pushover_user_key":           h.cfg.PushoverUserKey,
		"pushover_app_token":          h.cfg.PushoverAppToken,
		"pushover_configured":         h.pushover.IsConfigured(),
		"notify_on_complete":          h.cfg.NotifyOnComplete,
		"quality_hevc":                h.cfg.QualityHEVC,
		"quality_av1":                 h.cfg.QualityAV1,
		"default_quality_hevc":        defaultHEVC,
		"default_quality_av1":         defaultAV1,
		"hevc_encoder":                string(bestHEVC.Accel),
		"av1_encoder":                 string(bestAV1.Accel),
		"schedule_enabled":            h.cfg.ScheduleEnabled,
		"schedule_start_hour":         h.cfg.ScheduleStartHour,
		"schedule_end_hour":           h.cfg.ScheduleEndHour,
		"output_format":               h.cfg.OutputFormat,
		"tonemap_hdr":                 h.cfg.TonemapHDR,
		"tonemap_algorithm":           h.cfg.TonemapAlgorithm,
		"max_concurrent_analyses":     h.cfg.MaxConcurrentAnalyses,
		"log_level":                   h.cfg.LogLevel,
		"allow_same_codec":            h.cfg.AllowSameCodec,
		"skip_smartshrink_for_av1":    h.cfg.SkipSmartShrinkForAV1,
		"smartshrink_adaptive_target": h.cfg.SmartShrinkAdaptiveTarget,
		"vmaf_sample_count":           h.cfg.VMafSampleCount,
		// Intake pipeline
		"intake_enabled":               h.cfg.Intake.Enabled,
		"intake_watch_folder":          h.cfg.Intake.WatchFolder,
		"intake_staging_folder":        h.cfg.Intake.StagingFolder,
		"intake_library_movies":        h.cfg.Intake.Library.Movies,
		"intake_library_tv_shows":      h.cfg.Intake.Library.TVShows,
		"intake_stability_interval":    h.cfg.Intake.StabilityCheck.IntervalSeconds,
		"intake_stability_passes":      h.cfg.Intake.StabilityCheck.PassesRequired,
		"intake_confidence_threshold":  h.cfg.Intake.ConfidenceThreshold,
		"intake_review_threshold":      h.cfg.Intake.ReviewThreshold,
		"intake_cache_timeout_seconds": h.cfg.Intake.CacheTimeoutSeconds,
		"intake_naming_movie_folder":   h.cfg.Intake.Naming.MovieFolder,
		"intake_naming_movie_file":     h.cfg.Intake.Naming.MovieFile,
		"intake_naming_show_folder":    h.cfg.Intake.Naming.ShowFolder,
		"intake_naming_episode_file":   h.cfg.Intake.Naming.EpisodeFile,
		"intake_enable_naming_lookup":  h.cfg.Intake.EnableNamingLookup,
		// Metadata API keys
		"apis_tmdb_key": h.cfg.APIs.TMDBKey,
		"apis_tvdb_key": h.cfg.APIs.TVDBKey,
		"apis_omdb_key": h.cfg.APIs.OMDbKey,
		// LLM verification
		"llm_backend":     h.cfg.LLM.Backend,
		"llm_model":       h.cfg.LLM.Model,
		"llm_ollama_host": h.cfg.LLM.OllamaHost,
		// Poster cache
		"poster_cache_enabled": h.cfg.PosterCache.Enabled,
		"poster_cache_path":    h.cfg.PosterCache.Path,
		// Notifications
		"notify_base_url":                 h.cfg.Notifications.BaseURL,
		"notify_events_encode_complete":   h.cfg.Notifications.Events.EncodeComplete,
		"notify_events_encode_failed":     h.cfg.Notifications.Events.EncodeFailed,
		"notify_events_review_queue_item": h.cfg.Notifications.Events.ReviewQueueItem,
		"notify_events_daily_summary":     h.cfg.Notifications.Events.DailySummary,
		"notify_events_weekly_summary":    h.cfg.Notifications.Events.WeeklySummary,
		"notify_email_enabled":            h.cfg.Notifications.Email.Enabled,
		"notify_email_smtp_host":          h.cfg.Notifications.Email.SMTPHost,
		"notify_email_smtp_port":          h.cfg.Notifications.Email.SMTPPort,
		"notify_email_smtp_tls":           h.cfg.Notifications.Email.SMTPTLS,
		"notify_email_username":           h.cfg.Notifications.Email.Username,
		"notify_email_from":               h.cfg.Notifications.Email.From,
		"notify_email_to":                 h.cfg.Notifications.Email.To,
		"notify_email_mode":               h.cfg.Notifications.Email.Mode,
		"notify_email_interval_minutes":   h.cfg.Notifications.Email.IntervalMinutes,
	})
}

// UpdateConfigRequest is the request body for updating config
type UpdateConfigRequest struct {
	MediaPath                 *string `json:"media_path,omitempty"`
	OriginalHandling          *string `json:"original_handling,omitempty"`
	UseCompletedDir           *bool   `json:"use_completed_dir,omitempty"`
	Workers                   *int    `json:"workers,omitempty"`
	PushoverUserKey           *string `json:"pushover_user_key,omitempty"`
	PushoverAppToken          *string `json:"pushover_app_token,omitempty"`
	NotifyOnComplete          *bool   `json:"notify_on_complete,omitempty"`
	QualityHEVC               *int    `json:"quality_hevc,omitempty"`
	QualityAV1                *int    `json:"quality_av1,omitempty"`
	ScheduleEnabled           *bool   `json:"schedule_enabled,omitempty"`
	ScheduleStartHour         *int    `json:"schedule_start_hour,omitempty"`
	ScheduleEndHour           *int    `json:"schedule_end_hour,omitempty"`
	OutputFormat              *string `json:"output_format,omitempty"`
	TonemapHDR                *bool   `json:"tonemap_hdr,omitempty"`
	TonemapAlgorithm          *string `json:"tonemap_algorithm,omitempty"`
	MaxConcurrentAnalyses     *int    `json:"max_concurrent_analyses,omitempty"`
	LogLevel                  *string `json:"log_level,omitempty"`
	AllowSameCodec            *bool   `json:"allow_same_codec,omitempty"`
	SkipSmartShrinkForAV1     *bool   `json:"skip_smartshrink_for_av1,omitempty"`
	SmartShrinkAdaptiveTarget *bool   `json:"smartshrink_adaptive_target,omitempty"`
	VMafSampleCount           *int    `json:"vmaf_sample_count,omitempty"`
	// Intake pipeline
	IntakeEnabled             *bool    `json:"intake_enabled,omitempty"`
	IntakeWatchFolder         *string  `json:"intake_watch_folder,omitempty"`
	IntakeStagingFolder       *string  `json:"intake_staging_folder,omitempty"`
	IntakeLibraryMovies       *string  `json:"intake_library_movies,omitempty"`
	IntakeLibraryTVShows      *string  `json:"intake_library_tv_shows,omitempty"`
	IntakeStabilityInterval   *int     `json:"intake_stability_interval,omitempty"`
	IntakeStabilityPasses     *int     `json:"intake_stability_passes,omitempty"`
	IntakeConfidenceThreshold *float64 `json:"intake_confidence_threshold,omitempty"`
	IntakeReviewThreshold     *float64 `json:"intake_review_threshold,omitempty"`
	IntakeCacheTimeoutSeconds *int     `json:"intake_cache_timeout_seconds,omitempty"`
	IntakeNamingMovieFolder   *string  `json:"intake_naming_movie_folder,omitempty"`
	IntakeNamingMovieFile     *string  `json:"intake_naming_movie_file,omitempty"`
	IntakeNamingShowFolder    *string  `json:"intake_naming_show_folder,omitempty"`
	IntakeNamingEpisodeFile   *string  `json:"intake_naming_episode_file,omitempty"`
	IntakeEnableNamingLookup  *bool    `json:"intake_enable_naming_lookup,omitempty"`
	// Metadata API keys
	APIsTMDBKey *string `json:"apis_tmdb_key,omitempty"`
	APIsTVDBKey *string `json:"apis_tvdb_key,omitempty"`
	APIsOMDbKey *string `json:"apis_omdb_key,omitempty"`
	// LLM verification
	LLMBackend    *string `json:"llm_backend,omitempty"`
	LLMAPIKey     *string `json:"llm_api_key,omitempty"`
	LLMModel      *string `json:"llm_model,omitempty"`
	LLMOllamaHost *string `json:"llm_ollama_host,omitempty"`
	// Poster cache
	PosterCacheEnabled *bool   `json:"poster_cache_enabled,omitempty"`
	PosterCachePath    *string `json:"poster_cache_path,omitempty"`
	// Notifications
	NotifyBaseURL               *string `json:"notify_base_url,omitempty"`
	NotifyEventsEncodeComplete  *bool   `json:"notify_events_encode_complete,omitempty"`
	NotifyEventsEncodeFailed    *bool   `json:"notify_events_encode_failed,omitempty"`
	NotifyEventsReviewQueueItem *bool   `json:"notify_events_review_queue_item,omitempty"`
	NotifyEventsDailySummary    *bool   `json:"notify_events_daily_summary,omitempty"`
	NotifyEventsWeeklySummary   *bool   `json:"notify_events_weekly_summary,omitempty"`
	NotifyEmailEnabled          *bool   `json:"notify_email_enabled,omitempty"`
	NotifyEmailSMTPHost         *string `json:"notify_email_smtp_host,omitempty"`
	NotifyEmailSMTPPort         *int    `json:"notify_email_smtp_port,omitempty"`
	NotifyEmailSMTPTLS          *bool   `json:"notify_email_smtp_tls,omitempty"`
	NotifyEmailUsername         *string `json:"notify_email_username,omitempty"`
	NotifyEmailPassword         *string `json:"notify_email_password,omitempty"`
	NotifyEmailFrom             *string `json:"notify_email_from,omitempty"`
	NotifyEmailTo               *string `json:"notify_email_to,omitempty"`
	NotifyEmailMode             *string `json:"notify_email_mode,omitempty"`
	NotifyEmailIntervalMinutes  *int    `json:"notify_email_interval_minutes,omitempty"`
}

// UpdateConfig handles PUT /api/config
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Handle media browser root path.
	// Accepts local paths (D:\Media), UNC paths (\\server\share), and relative
	// paths (./media or /media for Docker). No existence check at save time —
	// the path may be a network share not yet accessible.
	if req.MediaPath != nil {
		p := strings.TrimSpace(*req.MediaPath)
		if p == "" {
			writeError(w, http.StatusBadRequest, "media_path cannot be empty")
			return
		}
		h.cfg.MediaPath = p
		h.browser.SetRoot(p)
	}

	// Only allow updating certain fields
	if req.OriginalHandling != nil {
		if *req.OriginalHandling != "replace" && *req.OriginalHandling != "keep" {
			writeError(w, http.StatusBadRequest, "original_handling must be 'replace' or 'keep'")
			return
		}
		h.cfg.OriginalHandling = *req.OriginalHandling
	}

	if req.UseCompletedDir != nil {
		h.cfg.UseCompletedDir = *req.UseCompletedDir
	}

	if req.Workers != nil && *req.Workers > 0 {
		workers := jobs.ClampWorkerCount(*req.Workers)
		// Dynamically resize the worker pool
		h.workerPool.Resize(workers)
	}

	// Handle Pushover settings
	if req.PushoverUserKey != nil {
		h.cfg.PushoverUserKey = *req.PushoverUserKey
		h.pushover.UserKey = *req.PushoverUserKey
	}
	if req.PushoverAppToken != nil {
		h.cfg.PushoverAppToken = *req.PushoverAppToken
		h.pushover.AppToken = *req.PushoverAppToken
	}
	if req.NotifyOnComplete != nil {
		h.cfg.NotifyOnComplete = *req.NotifyOnComplete
	}

	// Handle quality settings
	if req.QualityHEVC != nil {
		if errMsg := validateQuality(*req.QualityHEVC, "hevc"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.QualityHEVC = *req.QualityHEVC
	}
	if req.QualityAV1 != nil {
		if errMsg := validateQuality(*req.QualityAV1, "av1"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.QualityAV1 = *req.QualityAV1
	}

	// Handle schedule settings
	if req.ScheduleEnabled != nil {
		h.cfg.ScheduleEnabled = *req.ScheduleEnabled
	}
	if req.ScheduleStartHour != nil {
		if errMsg := validateScheduleHour(*req.ScheduleStartHour, "schedule_start_hour"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.ScheduleStartHour = *req.ScheduleStartHour
	}
	if req.ScheduleEndHour != nil {
		if errMsg := validateScheduleHour(*req.ScheduleEndHour, "schedule_end_hour"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.ScheduleEndHour = *req.ScheduleEndHour
	}

	// Handle output format
	if req.OutputFormat != nil {
		if *req.OutputFormat != "mkv" && *req.OutputFormat != "mp4" {
			writeError(w, http.StatusBadRequest, "output_format must be 'mkv' or 'mp4'")
			return
		}
		h.cfg.OutputFormat = *req.OutputFormat
	}

	// Handle HDR tonemapping settings
	if req.TonemapHDR != nil {
		h.cfg.TonemapHDR = *req.TonemapHDR
	}
	if req.TonemapAlgorithm != nil {
		if !config.IsValidTonemapAlgorithm(*req.TonemapAlgorithm) {
			writeError(w, http.StatusBadRequest, "tonemap_algorithm must be one of: hable, bt2390, reinhard, mobius, clip, linear, gamma")
			return
		}
		h.cfg.TonemapAlgorithm = *req.TonemapAlgorithm
	}

	// Handle max concurrent analyses (SmartShrink VMAF)
	if req.MaxConcurrentAnalyses != nil {
		val := *req.MaxConcurrentAnalyses
		if !jobs.IsValidAnalysisCount(val) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("max_concurrent_analyses must be between %d and %d", jobs.MinConcurrentAnalyses, jobs.MaxConcurrentAnalyses))
			return
		}
		h.cfg.MaxConcurrentAnalyses = val
		// Update the worker pool's analysis limit
		h.workerPool.SetAnalysisLimit(val)
	}

	// Handle allow same codec (re-encode HEVC→HEVC or AV1→AV1)
	if req.AllowSameCodec != nil {
		h.cfg.AllowSameCodec = *req.AllowSameCodec
		h.queue.SetAllowSameCodec(*req.AllowSameCodec)
	}
	if req.SkipSmartShrinkForAV1 != nil {
		h.cfg.SkipSmartShrinkForAV1 = *req.SkipSmartShrinkForAV1
	}
	if req.SmartShrinkAdaptiveTarget != nil {
		h.cfg.SmartShrinkAdaptiveTarget = *req.SmartShrinkAdaptiveTarget
	}
	if req.VMafSampleCount != nil {
		val := *req.VMafSampleCount
		if val < 3 {
			val = 3
		}
		if val > 6 {
			val = 6
		}
		h.cfg.VMafSampleCount = val
	}

	// Handle log level
	if req.LogLevel != nil {
		val := strings.ToLower(*req.LogLevel)
		if val != "debug" && val != "info" && val != "warn" && val != "error" {
			writeError(w, http.StatusBadRequest, "log_level must be 'debug', 'info', 'warn', or 'error'")
			return
		}
		h.cfg.LogLevel = val
		logger.SetLevel(val)
	}

	// Handle intake settings
	if req.IntakeEnabled != nil {
		h.cfg.Intake.Enabled = *req.IntakeEnabled
	}
	if req.IntakeWatchFolder != nil {
		h.cfg.Intake.WatchFolder = *req.IntakeWatchFolder
	}
	if req.IntakeStagingFolder != nil {
		h.cfg.Intake.StagingFolder = *req.IntakeStagingFolder
	}
	if req.IntakeLibraryMovies != nil {
		h.cfg.Intake.Library.Movies = *req.IntakeLibraryMovies
	}
	if req.IntakeLibraryTVShows != nil {
		h.cfg.Intake.Library.TVShows = *req.IntakeLibraryTVShows
	}
	if req.IntakeStabilityInterval != nil {
		val := *req.IntakeStabilityInterval
		if val < 1 {
			val = 1
		}
		h.cfg.Intake.StabilityCheck.IntervalSeconds = val
	}
	if req.IntakeStabilityPasses != nil {
		val := *req.IntakeStabilityPasses
		if val < 1 {
			val = 1
		}
		h.cfg.Intake.StabilityCheck.PassesRequired = val
	}
	if req.IntakeConfidenceThreshold != nil {
		val := *req.IntakeConfidenceThreshold
		if val < 0 || val > 1 {
			writeError(w, http.StatusBadRequest, "intake_confidence_threshold must be between 0 and 1")
			return
		}
		h.cfg.Intake.ConfidenceThreshold = val
	}
	if req.IntakeReviewThreshold != nil {
		val := *req.IntakeReviewThreshold
		if val < 0 || val > 1 {
			writeError(w, http.StatusBadRequest, "intake_review_threshold must be between 0 and 1")
			return
		}
		h.cfg.Intake.ReviewThreshold = val
	}
	if req.IntakeEnableNamingLookup != nil {
		h.cfg.Intake.EnableNamingLookup = *req.IntakeEnableNamingLookup
	}
	if req.IntakeCacheTimeoutSeconds != nil {
		val := *req.IntakeCacheTimeoutSeconds
		if val < 1 || val > 3600 {
			writeError(w, http.StatusBadRequest, "intake_cache_timeout_seconds must be between 1 and 3600")
			return
		}
		h.cfg.Intake.CacheTimeoutSeconds = val
	}
	if req.IntakeNamingMovieFolder != nil {
		h.cfg.Intake.Naming.MovieFolder = *req.IntakeNamingMovieFolder
	}
	if req.IntakeNamingMovieFile != nil {
		h.cfg.Intake.Naming.MovieFile = *req.IntakeNamingMovieFile
	}
	if req.IntakeNamingShowFolder != nil {
		h.cfg.Intake.Naming.ShowFolder = *req.IntakeNamingShowFolder
	}
	if req.IntakeNamingEpisodeFile != nil {
		h.cfg.Intake.Naming.EpisodeFile = *req.IntakeNamingEpisodeFile
	}

	// Handle API keys
	if req.APIsTMDBKey != nil {
		h.cfg.APIs.TMDBKey = *req.APIsTMDBKey
	}
	if req.APIsTVDBKey != nil {
		h.cfg.APIs.TVDBKey = *req.APIsTVDBKey
	}
	if req.APIsOMDbKey != nil {
		h.cfg.APIs.OMDbKey = *req.APIsOMDbKey
	}

	// Handle LLM settings
	if req.LLMBackend != nil {
		val := *req.LLMBackend
		if val != "" && val != "anthropic" && val != "openai" && val != "ollama" {
			writeError(w, http.StatusBadRequest, "llm_backend must be 'anthropic', 'openai', 'ollama', or empty")
			return
		}
		h.cfg.LLM.Backend = val
	}
	if req.LLMAPIKey != nil {
		h.cfg.LLM.APIKey = *req.LLMAPIKey
	}
	if req.LLMModel != nil {
		h.cfg.LLM.Model = *req.LLMModel
	}
	if req.LLMOllamaHost != nil {
		h.cfg.LLM.OllamaHost = *req.LLMOllamaHost
	}

	// Handle poster cache settings
	if req.PosterCacheEnabled != nil {
		h.cfg.PosterCache.Enabled = *req.PosterCacheEnabled
	}
	if req.PosterCachePath != nil {
		h.cfg.PosterCache.Path = *req.PosterCachePath
	}

	// Handle notification settings
	if req.NotifyBaseURL != nil {
		h.cfg.Notifications.BaseURL = *req.NotifyBaseURL
	}
	if req.NotifyEventsEncodeComplete != nil {
		h.cfg.Notifications.Events.EncodeComplete = *req.NotifyEventsEncodeComplete
	}
	if req.NotifyEventsEncodeFailed != nil {
		h.cfg.Notifications.Events.EncodeFailed = *req.NotifyEventsEncodeFailed
	}
	if req.NotifyEventsReviewQueueItem != nil {
		h.cfg.Notifications.Events.ReviewQueueItem = *req.NotifyEventsReviewQueueItem
	}
	if req.NotifyEventsDailySummary != nil {
		h.cfg.Notifications.Events.DailySummary = *req.NotifyEventsDailySummary
	}
	if req.NotifyEventsWeeklySummary != nil {
		h.cfg.Notifications.Events.WeeklySummary = *req.NotifyEventsWeeklySummary
	}
	if req.NotifyEmailEnabled != nil {
		h.cfg.Notifications.Email.Enabled = *req.NotifyEmailEnabled
	}
	if req.NotifyEmailSMTPHost != nil {
		h.cfg.Notifications.Email.SMTPHost = *req.NotifyEmailSMTPHost
	}
	if req.NotifyEmailSMTPPort != nil {
		port := *req.NotifyEmailSMTPPort
		if port < 1 || port > 65535 {
			writeError(w, http.StatusBadRequest, "notify_email_smtp_port must be between 1 and 65535")
			return
		}
		h.cfg.Notifications.Email.SMTPPort = port
	}
	if req.NotifyEmailSMTPTLS != nil {
		h.cfg.Notifications.Email.SMTPTLS = *req.NotifyEmailSMTPTLS
	}
	if req.NotifyEmailUsername != nil {
		h.cfg.Notifications.Email.Username = *req.NotifyEmailUsername
	}
	if req.NotifyEmailPassword != nil {
		h.cfg.Notifications.Email.Password = *req.NotifyEmailPassword
	}
	if req.NotifyEmailFrom != nil {
		h.cfg.Notifications.Email.From = *req.NotifyEmailFrom
	}
	if req.NotifyEmailTo != nil {
		h.cfg.Notifications.Email.To = *req.NotifyEmailTo
	}
	if req.NotifyEmailMode != nil {
		val := *req.NotifyEmailMode
		if val != "per_file" && val != "batched" {
			writeError(w, http.StatusBadRequest, "notify_email_mode must be 'per_file' or 'batched'")
			return
		}
		h.cfg.Notifications.Email.Mode = val
	}
	if req.NotifyEmailIntervalMinutes != nil {
		val := *req.NotifyEmailIntervalMinutes
		if val < 1 {
			val = 1
		}
		h.cfg.Notifications.Email.IntervalMinutes = val
	}

	// Persist config to disk
	if h.cfgPath != "" {
		if err := h.cfg.Save(h.cfgPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// Stats handles GET /api/stats
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats := h.queue.Stats()
	// Include the encode queue's paused state so the UI badge/toggle (Issue #13)
	// reflects the authoritative backend state rather than inferring from jobs.
	writeJSON(w, http.StatusOK, struct {
		jobs.Stats
		Paused bool `json:"paused"`
	}{Stats: stats, Paused: h.workerPool.IsPaused()})
}

// ResetSession handles POST /api/stats/reset-session
func (h *Handler) ResetSession(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusInternalServerError, "stats store not configured")
		return
	}

	if err := h.store.ResetSession(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "session reset"})
}

// ClearCache handles POST /api/cache/clear
func (h *Handler) ClearCache(w http.ResponseWriter, r *http.Request) {
	h.browser.ClearCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cache cleared"})
}

// TestPushover handles POST /api/pushover/test
func (h *Handler) TestPushover(w http.ResponseWriter, r *http.Request) {
	if !h.pushover.IsConfigured() {
		writeError(w, http.StatusBadRequest, "Pushover credentials not configured")
		return
	}

	if err := h.pushover.Test(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "Test notification sent"})
}

// --- Review Queue handlers ---

// reviewEntryResponse is the API shape for a single Review Queue entry.
type reviewEntryResponse struct {
	store.ReviewEntry
	Candidates []interface{} `json:"candidates"`
	LLMGuess   interface{}   `json:"llm_guess"`
}

func toReviewResponse(e *store.ReviewEntry) reviewEntryResponse {
	return reviewEntryResponse{
		ReviewEntry: *e,
		Candidates:  []interface{}{},
		LLMGuess:    nil,
	}
}

// ListReviewQueue handles GET /api/review
func (h *Handler) ListReviewQueue(w http.ResponseWriter, r *http.Request) {
	if h.reviewStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"entries": []interface{}{}, "total": 0})
		return
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "pending"
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}

	all, err := h.reviewStore.GetReviewQueue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Filter by status.
	filtered := all[:0]
	for i := range all {
		e := &all[i]
		if e.Status == statusFilter {
			filtered = append(filtered, *e)
		}
	}
	total := len(filtered)

	// Paginate.
	start := (page - 1) * limit
	end := start + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	page_count := (total + limit - 1) / limit
	if page_count == 0 {
		page_count = 1
	}

	entries := make([]reviewEntryResponse, 0, end-start)
	for i := range filtered[start:end] {
		entries = append(entries, toReviewResponse(&filtered[start+i]))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   total,
		"page":    page,
		"pages":   page_count,
	})
}

// GetReviewQueueCount handles GET /api/review/count
func (h *Handler) GetReviewQueueCount(w http.ResponseWriter, r *http.Request) {
	if h.reviewStore == nil {
		writeJSON(w, http.StatusOK, map[string]int{"count": 0})
		return
	}
	count, err := h.reviewStore.GetReviewQueueCount()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

// ResolveReviewEntry handles PUT /api/review/{id}/resolve
// Body: {"candidate": {title, year, media_type, episode_title, season, episode}}
// The picked candidate is used to build the library destination path; the file is
// then moved there. Only on a successful move is the entry marked resolved — a
// failed move (or a duplicate at the destination) leaves the entry pending with an
// updated reason so nothing is silently lost.
func (h *Handler) ResolveReviewEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "entry ID required")
		return
	}
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}

	var req struct {
		Candidate struct {
			Title        string `json:"title"`
			Year         int    `json:"year"`
			MediaType    string `json:"media_type"`
			EpisodeTitle string `json:"episode_title"`
			Season       int    `json:"season"`
			Episode      int    `json:"episode"`
		} `json:"candidate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Candidate.Title == "" {
		writeError(w, http.StatusBadRequest, "candidate title required")
		return
	}

	entry, err := h.reviewStore.GetReviewEntry(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "review entry not found")
		return
	}
	if entry.DuplicateInfo != "" {
		writeError(w, http.StatusBadRequest, "duplicate entries use Replace or Keep Existing, not Pick Selected")
		return
	}
	if !isMetadataPickCategory(entry.Category) {
		writeError(w, http.StatusBadRequest, "this entry's category does not support Pick Selected")
		return
	}

	// The source file must still be where it was queued.
	if _, statErr := os.Stat(entry.OriginalPath); statErr != nil {
		newReason := "source file no longer present: " + entry.OriginalPath
		_ = h.reviewStore.UpdateReviewEntryReason(id, newReason)
		writeError(w, http.StatusConflict, newReason)
		return
	}

	// Start from the filename parse (for season/episode) and overlay the confirmed
	// metadata from the picked candidate.
	parsed := intake.ParseFilename(entry.Filename)
	parsed.Title = req.Candidate.Title
	if req.Candidate.Year > 0 {
		parsed.Year = req.Candidate.Year
	}
	if req.Candidate.EpisodeTitle != "" {
		parsed.EpisodeTitle = req.Candidate.EpisodeTitle
	} else if parsed.EpisodeTitle == "" {
		// Fall back to the title parsed out of the filename text itself when the
		// picked candidate didn't supply one (ParseFilename only ever populates
		// ParsedEpisodeTitle, not the EpisodeTitle field naming templates read).
		parsed.EpisodeTitle = parsed.ParsedEpisodeTitle
	}
	if req.Candidate.MediaType != "" {
		parsed.MediaType = req.Candidate.MediaType
		parsed.IsTV = req.Candidate.MediaType == "tv"
	}
	if req.Candidate.Season > 0 {
		parsed.Season = req.Candidate.Season
	}
	if req.Candidate.Episode > 0 {
		parsed.Episode = req.Candidate.Episode
	}

	ext := filepath.Ext(entry.Filename)
	libraryPath := intake.ResolveLibraryPath(&h.cfg.Intake, &parsed, ext)
	if libraryPath == "" {
		newReason := fmt.Sprintf("could not build library path for %q (missing title/season?)", req.Candidate.Title)
		_ = h.reviewStore.UpdateReviewEntryReason(id, newReason)
		writeError(w, http.StatusBadRequest, newReason)
		return
	}

	// Never silently overwrite an existing file at the destination — unless
	// it's an unambiguous upgrade per the same auto-resolution rules the
	// intake pipeline uses (internal/upgrade). Ambiguous cases are converted
	// in place into a duplicate-conflict entry (DuplicateInfo + category)
	// instead of a dead-end plain-text reason, so the UI shows the
	// incoming/existing comparison and Replace/Keep Existing actions.
	if _, statErr := os.Stat(libraryPath); statErr == nil {
		prober := ffmpeg.NewProber(h.cfg.FFprobePath)
		probeCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		incoming := probeDuplicateFileInfo(probeCtx, prober, entry.OriginalPath)
		existing := probeDuplicateFileInfo(probeCtx, prober, libraryPath)
		cancel()

		decision, decisionReason := upgrade.Review, ""
		if h.cfg.Intake.DuplicateResolution != "manual" {
			decision, decisionReason = upgrade.Decide(dupFileInfoToUpgrade(incoming), dupFileInfoToUpgrade(existing), h.cfg.Intake.DuplicateBitrateUpgradeThreshold)
		}

		switch decision {
		case upgrade.Replace:
			logger.Info("Review resolve: duplicate auto-resolved, replacing existing library file",
				"entry_id", id, "dst", libraryPath, "reason", decisionReason)
			jobID := h.startReplaceMove(id, entry.OriginalPath, libraryPath)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "replacing", "job_id": jobID, "reason": decisionReason})
			return
		case upgrade.Keep:
			logger.Info("Review resolve: duplicate auto-resolved, keeping existing library file",
				"entry_id", id, "dst", libraryPath, "reason", decisionReason)
			if err := os.Remove(entry.OriginalPath); err != nil && !os.IsNotExist(err) {
				logger.Warn("Review resolve: failed to remove incoming file after auto-keep", "entry_id", id, "error", err)
			}
			if err := h.reviewStore.UpdateReviewQueueStatus(id, "discarded"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "kept_existing", "reason": decisionReason})
			return
		default:
			newReason := "duplicate: file already exists at destination " + libraryPath
			dupCtx := intake.DuplicateContext{Incoming: incoming, Existing: existing}
			dupJSON, _ := json.Marshal(dupCtx)
			if err := h.reviewStore.ConvertReviewEntryToDuplicate(id, newReason, string(dupJSON)); err != nil {
				logger.Warn("Review resolve: failed to convert entry to duplicate", "entry_id", id, "error", err)
			}
			writeError(w, http.StatusConflict, newReason)
			return
		}
	}

	if err := util.SafeMove(entry.OriginalPath, libraryPath); err != nil {
		newReason := fmt.Sprintf("library move failed: %v", err)
		logger.Warn("Review resolve: move failed",
			"entry_id", id, "src", entry.OriginalPath, "dst", libraryPath, "error", err)
		_ = h.reviewStore.UpdateReviewEntryReason(id, newReason)
		writeError(w, http.StatusInternalServerError, newReason)
		return
	}

	logger.Info("Review resolve: moved to library", "entry_id", id, "dst", libraryPath)
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved", "library_path": libraryPath})
}

// DiscardReviewEntry handles PUT /api/review/{id}/discard
// For duplicate-conflict entries this is the "Keep Existing" action: the incoming
// file (which lost out to the existing library file) is deleted from disk so it
// does not linger in the intake/staging directory or reappear on restart.
func (h *Handler) DiscardReviewEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "entry ID required")
		return
	}
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}

	entry, err := h.reviewStore.GetReviewEntry(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.discardReviewEntryFile(entry)

	if err := h.reviewStore.UpdateReviewQueueStatus(id, "discarded"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

// discardReviewEntryFile deletes the incoming file for a duplicate-conflict
// entry (the "Keep Existing" side effect), so it does not linger in the
// intake/staging directory or reappear on restart. No-op for every other
// category. Shared by DiscardReviewEntry and BulkDiscardReviewEntries.
func (h *Handler) discardReviewEntryFile(entry *store.ReviewEntry) {
	if entry == nil || entry.DuplicateInfo == "" {
		return
	}
	var dupCtx intake.DuplicateContext
	if err := json.Unmarshal([]byte(entry.DuplicateInfo), &dupCtx); err != nil || dupCtx.Incoming.Path == "" {
		return
	}
	if err := os.Remove(dupCtx.Incoming.Path); err != nil && !os.IsNotExist(err) {
		logger.Warn("Review discard: failed to delete incoming duplicate file",
			"path", dupCtx.Incoming.Path, "error", err)
	} else {
		logger.Info("Review discard: deleted incoming file",
			"path", dupCtx.Incoming.Path, "reason", "duplicate: user selected keep existing")
	}
}

// BulkDiscardReviewEntries handles PUT /api/review/bulk/discard
// Body: {"ids": ["id1", "id2", ...]}
func (h *Handler) BulkDiscardReviewEntries(w http.ResponseWriter, r *http.Request) {
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids required")
		return
	}

	var succeeded []string
	var failed []string
	for _, id := range req.IDs {
		entry, err := h.reviewStore.GetReviewEntry(id)
		if err != nil || entry == nil {
			failed = append(failed, id)
			continue
		}
		h.discardReviewEntryFile(entry)
		succeeded = append(succeeded, id)
	}

	if err := h.reviewStore.BulkUpdateReviewQueueStatus(succeeded, "discarded"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"discarded": len(succeeded), "failed": failed})
}

// ReplaceReviewEntry handles PUT /api/review/{id}/replace
// Kicks off an async move of the incoming file over the destination path and
// returns immediately with a move-job ID; progress and completion are
// reported over the /api/jobs/stream SSE channel as Kind:"move" events. Only
// valid for duplicate conflict entries.
func (h *Handler) ReplaceReviewEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "entry ID required")
		return
	}
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}

	entry, err := h.reviewStore.GetReviewEntry(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "review entry not found")
		return
	}

	incoming, existing, err := parseDuplicatePaths(entry)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	jobID := h.startReplaceMove(id, incoming, existing)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "replacing", "job_id": jobID})
}

// probeDuplicateFileInfo builds an intake.DuplicateFileInfo for one side of a
// duplicate comparison. Best-effort — a probe failure leaves codec/resolution
// zero-valued rather than erroring, matching intake's own duplicate probing.
func probeDuplicateFileInfo(ctx context.Context, prober *ffmpeg.Prober, path string) intake.DuplicateFileInfo {
	info := intake.DuplicateFileInfo{Path: path}
	if stat, err := os.Stat(path); err == nil {
		info.FileSizeB = stat.Size()
	}
	if probe, err := prober.Probe(ctx, path); err == nil {
		info.Codec = probe.VideoCodec
		info.Width = probe.Width
		info.Height = probe.Height
		info.BitrateBps = probe.Bitrate
	}
	return info
}

// dupFileInfoToUpgrade adapts an intake.DuplicateFileInfo to upgrade.FileInfo
// for upgrade.Decide.
func dupFileInfoToUpgrade(f intake.DuplicateFileInfo) upgrade.FileInfo {
	return upgrade.FileInfo{Codec: f.Codec, Width: f.Width, Height: f.Height, BitrateBps: f.BitrateBps}
}

// parseDuplicatePaths extracts the incoming/existing file paths from a
// duplicate-conflict review entry's DuplicateInfo JSON. Shared by
// ReplaceReviewEntry and BulkReplaceReviewEntries.
func parseDuplicatePaths(entry *store.ReviewEntry) (incoming, existing string, err error) {
	if entry.DuplicateInfo == "" {
		return "", "", fmt.Errorf("entry is not a duplicate conflict")
	}
	var dupCtx intake.DuplicateContext
	if err := json.Unmarshal([]byte(entry.DuplicateInfo), &dupCtx); err != nil {
		return "", "", fmt.Errorf("failed to parse duplicate info")
	}
	if dupCtx.Incoming.Path == "" || dupCtx.Existing.Path == "" {
		return "", "", fmt.Errorf("duplicate info is incomplete")
	}
	return dupCtx.Incoming.Path, dupCtx.Existing.Path, nil
}

// startReplaceMove moves incoming over existing in a background goroutine,
// broadcasting Kind:"move" progress events over the jobs SSE stream
// (move_started/move_progress/move_complete/move_failed) and updating the
// review entry's status to "resolved" once the move succeeds. Returns the
// move job ID immediately; the caller does not wait for completion.
func (h *Handler) startReplaceMove(entryID, incoming, existing string) string {
	jobID := "move-" + uuid.NewString()
	job := &jobs.Job{ID: jobID, Kind: "move", InputPath: incoming, OutputPath: existing, Status: jobs.StatusRunning, StartedAt: time.Now()}
	h.queue.BroadcastMoveEvent("move_started", job.Copy())

	go func() {
		// Serialize the actual move against any other concurrent Review Queue
		// replace move — see reviewMoveMu's doc comment on the Handler struct.
		h.reviewMoveMu.Lock()
		defer h.reviewMoveMu.Unlock()

		err := util.SafeMoveWithProgress(incoming, existing, func(copied, total int64) {
			p := job.Copy()
			if total > 0 {
				p.Progress = float64(copied) / float64(total) * 100
			}
			h.queue.BroadcastMoveEvent("move_progress", p)
		})
		if err != nil {
			logger.Warn("Review: replace move failed", "entry_id", entryID, "dest", existing, "error", err)
			f := job.Copy()
			f.Status = jobs.StatusFailed
			f.Error = err.Error()
			h.queue.BroadcastMoveEvent("move_failed", f)
			return
		}

		logger.Info("Review: replaced existing file with incoming", "dest", existing)
		if err := h.reviewStore.UpdateReviewQueueStatus(entryID, "resolved"); err != nil {
			logger.Error("Review: replace move succeeded but failed to update review status", "entry_id", entryID, "error", err)
		}
		c := job.Copy()
		c.Status = jobs.StatusComplete
		c.Progress = 100
		c.CompletedAt = time.Now()
		h.queue.BroadcastMoveEvent("move_complete", c)
	}()

	return jobID
}

// BulkReplaceReviewEntries handles PUT /api/review/bulk/replace
// Body: {"ids": ["id1", "id2", ...]}
// Each valid duplicate-conflict entry starts its own async move job
// immediately; validation failures (not a duplicate entry, missing info) are
// reported as failed without starting a move.
func (h *Handler) BulkReplaceReviewEntries(w http.ResponseWriter, r *http.Request) {
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids required")
		return
	}

	type replacing struct {
		ID    string `json:"id"`
		JobID string `json:"job_id"`
	}
	var started []replacing
	var failed []string
	for _, id := range req.IDs {
		entry, err := h.reviewStore.GetReviewEntry(id)
		if err != nil || entry == nil {
			failed = append(failed, id)
			continue
		}
		incoming, existing, err := parseDuplicatePaths(entry)
		if err != nil {
			logger.Warn("Bulk replace: skipping entry", "entry_id", id, "error", err)
			failed = append(failed, id)
			continue
		}
		jobID := h.startReplaceMove(id, incoming, existing)
		started = append(started, replacing{ID: id, JobID: jobID})
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{"replacing": started, "failed": failed})
}

// ResubmitReviewEntry handles PUT /api/review/{id}/resubmit
// Body: {"preset_id":"compress-hevc","original_path":"/incoming/file.mkv"}
func (h *Handler) ResubmitReviewEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "entry ID required")
		return
	}
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}

	var req struct {
		PresetID           string `json:"preset_id"`
		OriginalPath       string `json:"original_path"`
		EncodeSpeed        string `json:"encode_speed"`
		EncodeOutputFormat string `json:"encode_output_format"`
		SmartShrinkQuality string `json:"smartshrink_quality"`
		EncodeQualityCRF   int    `json:"encode_quality_crf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OriginalPath == "" {
		writeError(w, http.StatusBadRequest, "original_path required")
		return
	}

	preset, presetID, errMsg := resolveResubmitPreset(req.PresetID)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	if errMsg := validateResubmitOverrides(preset, req.EncodeOutputFormat, req.SmartShrinkQuality, req.EncodeQualityCRF); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	// Fetch the entry so we can rebuild its intended library destination below —
	// AddMultiple never sets Job.LibraryPath itself, so without this the worker's
	// post-encode move step (worker.go processJob, gated on job.LibraryPath != "")
	// silently never fires and the file is left in the staging/transcode folder.
	entry, err := h.reviewStore.GetReviewEntry(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "review entry not found")
		return
	}
	if !isEncodeFailureCategory(entry.Category) {
		writeError(w, http.StatusBadRequest, "this entry's category does not support Re-encode Custom")
		return
	}

	// Mark resolved and enqueue the file.
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.enqueueResubmit(id, entry, req.OriginalPath, presetID, req.EncodeSpeed, req.EncodeOutputFormat, req.SmartShrinkQuality, req.EncodeQualityCRF)

	writeJSON(w, http.StatusOK, map[string]string{"status": "resubmitted"})
}

// resolveResubmitPreset resolves a preset ID, defaulting to compress-hevc
// when empty. Returns a non-empty errMsg if the preset is unknown.
func resolveResubmitPreset(presetID string) (preset *ffmpeg.Preset, resolvedID, errMsg string) {
	if presetID == "" {
		presetID = "compress-hevc"
	}
	preset = ffmpeg.GetPreset(presetID)
	if preset == nil {
		return nil, presetID, fmt.Sprintf("unknown preset: %s", presetID)
	}
	return preset, presetID, ""
}

// validateResubmitOverrides validates the optional custom-encode override
// fields (Issue #4: re-encode with custom settings). Returns a non-empty
// errMsg on the first invalid field.
func validateResubmitOverrides(preset *ffmpeg.Preset, encodeOutputFormat, smartShrinkQuality string, encodeQualityCRF int) string {
	if encodeOutputFormat != "" {
		switch encodeOutputFormat {
		case "mkv", "mp4", "preserve":
		default:
			return "encode_output_format must be 'mkv', 'mp4', or 'preserve'"
		}
	}
	if smartShrinkQuality != "" && !jobs.IsValidSmartShrinkQuality(smartShrinkQuality) {
		return "smartshrink_quality must be 'acceptable', 'good', or 'excellent'"
	}
	if encodeQualityCRF != 0 {
		if preset.IsSmartShrink {
			return "encode_quality_crf is not applicable to SmartShrink presets"
		}
		if msg := validateQuality(encodeQualityCRF, string(preset.Codec)); msg != "" {
			return msg
		}
	}
	return ""
}

// enqueueResubmit probes originalPath and enqueues a re-encode job for a
// resolved review entry, applying custom-encode overrides and rebuilding the
// entry's library destination so the worker's post-encode move step fires on
// completion. Runs asynchronously; on probe failure the entry is put back to
// pending with an explanatory reason rather than left silently resolved with
// nothing enqueued. Shared by ResubmitReviewEntry and BulkResubmitReviewEntries.
func (h *Handler) enqueueResubmit(id string, entry *store.ReviewEntry, originalPath, presetID, encodeSpeed, encodeOutputFormat, smartShrinkQuality string, encodeQualityCRF int) {
	h.workerPool.Unpause()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// Use ProbeFile (direct ffprobe call), not the media-root-scoped browser:
		// OriginalPath for encode-failure entries points at the staging/transcode
		// working directory, which normally sits outside the configured media
		// browse root and would be silently filtered out by GetVideoFilesWithProgress.
		probe, err := h.browser.ProbeFile(ctx, originalPath)
		if err != nil {
			reason := fmt.Sprintf("resubmit failed: %v (%s)", err, originalPath)
			logger.Warn("Review resubmit: probe failed", "path", originalPath, "error", err)
			// Don't leave the entry silently resolved with nothing enqueued —
			// put it back in the queue with a reason the user can act on.
			_ = h.reviewStore.UpdateReviewEntryReason(id, reason)
			_ = h.reviewStore.UpdateReviewQueueStatus(id, "pending")
			return
		}
		addedJobs, _ := h.queue.AddMultiple([]*ffmpeg.ProbeResult{probe}, presetID, smartShrinkQuality)

		// Apply per-job custom-encode overrides (Issue #4).
		if encodeSpeed != "" || encodeOutputFormat != "" || encodeQualityCRF != 0 {
			for _, job := range addedJobs {
				h.queue.SetJobOverrides(job.ID, encodeSpeed, encodeOutputFormat, encodeQualityCRF)
			}
		}

		// Rebuild the intended library destination from the entry's filename
		// (same approach as ResolveReviewEntry) and set it on the new job(s) so
		// the worker's post-encode move step actually fires on completion.
		parsed := intake.ParseFilename(entry.Filename)
		// ParseFilename only ever populates ParsedEpisodeTitle (the raw title parsed
		// out of the filename text) — naming templates read the separate EpisodeTitle
		// field (confirmed metadata), which ParseFilename never sets. Without this,
		// every TV resubmit silently drops the episode title from the rebuilt path.
		if parsed.EpisodeTitle == "" {
			parsed.EpisodeTitle = parsed.ParsedEpisodeTitle
		}
		cfgFmt := h.cfg.OutputFormat
		if encodeOutputFormat != "" {
			cfgFmt = encodeOutputFormat
		}
		ext := ffmpeg.ResolveOutputFormat(originalPath, cfgFmt)
		libraryPath := intake.ResolveLibraryPath(&h.cfg.Intake, &parsed, "."+ext)
		if libraryPath == "" {
			logger.Warn("Review resubmit: could not rebuild library path, file will not auto-move on completion",
				"entry_id", id, "filename", entry.Filename)
			return
		}
		for _, job := range addedJobs {
			h.queue.SetLibraryPath(job.ID, libraryPath)
		}
	}()
}

// BulkResubmitReviewEntries handles PUT /api/review/bulk/resubmit
// Body: {"ids": [...], "preset_id", "encode_speed", "encode_output_format", "smartshrink_quality", "encode_quality_crf"}
// The encode settings are shared across every selected entry; each entry's own
// OriginalPath (not a client-supplied path) is used for the probe.
func (h *Handler) BulkResubmitReviewEntries(w http.ResponseWriter, r *http.Request) {
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}
	var req struct {
		IDs                []string `json:"ids"`
		PresetID           string   `json:"preset_id"`
		EncodeSpeed        string   `json:"encode_speed"`
		EncodeOutputFormat string   `json:"encode_output_format"`
		SmartShrinkQuality string   `json:"smartshrink_quality"`
		EncodeQualityCRF   int      `json:"encode_quality_crf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids required")
		return
	}

	preset, presetID, errMsg := resolveResubmitPreset(req.PresetID)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	if errMsg := validateResubmitOverrides(preset, req.EncodeOutputFormat, req.SmartShrinkQuality, req.EncodeQualityCRF); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	var succeededEntries []*store.ReviewEntry
	var failed []string
	for _, id := range req.IDs {
		entry, err := h.reviewStore.GetReviewEntry(id)
		if err != nil || entry == nil || !isEncodeFailureCategory(entry.Category) {
			failed = append(failed, id)
			continue
		}
		succeededEntries = append(succeededEntries, entry)
	}

	succeededIDs := make([]string, len(succeededEntries))
	for i, e := range succeededEntries {
		succeededIDs[i] = e.ID
	}
	if err := h.reviewStore.BulkUpdateReviewQueueStatus(succeededIDs, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, entry := range succeededEntries {
		h.enqueueResubmit(entry.ID, entry, entry.OriginalPath, presetID, req.EncodeSpeed, req.EncodeOutputFormat, req.SmartShrinkQuality, req.EncodeQualityCRF)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"resubmitted": len(succeededEntries), "failed": failed})
}

// SearchReviewEntry handles GET /api/review/{id}/search
// Query params: q (title), year, type (movie|tv), season, episode
func (h *Handler) SearchReviewEntry(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter required")
		return
	}

	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	season, _ := strconv.Atoi(r.URL.Query().Get("season"))
	episode, _ := strconv.Atoi(r.URL.Query().Get("episode"))
	mediaType := r.URL.Query().Get("type")

	// q may be a raw filename-style query (e.g. "Mystery Science Theater 3000 -
	// S04E23 - Bride of the Monster") rather than a bare title — reuse the same
	// filename parser the intake pipeline uses so the search title actually
	// matches the show/movie name instead of the whole query string.
	fromQ := intake.ParseFilename(q)
	title := fromQ.Title
	if title == "" {
		title = q
	}
	if year == 0 {
		year = fromQ.Year
	}
	if season == 0 {
		season = fromQ.Season
	}
	if episode == 0 {
		episode = fromQ.Episode
	}

	parsed := &intake.ParsedFilename{
		Title:   title,
		Year:    year,
		Season:  season,
		Episode: episode,
	}
	if mediaType == "tv" || (mediaType == "" && (season > 0 || episode > 0 || fromQ.IsTV)) {
		parsed.IsTV = true
		parsed.MediaType = "tv"
	} else {
		parsed.MediaType = "movie"
	}

	tvdb := intake.NewTVDBClient(h.cfg.APIs.TVDBKey, nil)
	tmdb := intake.NewTMDBClient(h.cfg.APIs.TMDBKey, nil)
	omdb := intake.NewOMDbClient(h.cfg.APIs.OMDbKey, nil)
	orch := intake.NewOrchestrator(tvdb, tmdb, omdb)

	var result *intake.LookupResult
	var lookupErr error
	if parsed.IsTV {
		result, lookupErr = orch.LookupTV(r.Context(), parsed, 0, 0.0)
	} else {
		result, lookupErr = orch.LookupMovie(r.Context(), parsed, 0, 0.0)
	}

	candidates := []interface{}{}
	if lookupErr == nil && result != nil {
		posterURL := ""
		if result.PosterPath != "" {
			posterURL = "https://image.tmdb.org/t/p/w154" + result.PosterPath
		}
		candidates = append(candidates, map[string]interface{}{
			"source":           result.Source,
			"media_type":       result.MediaType,
			"title":            result.Title,
			"year":             result.Year,
			"runtime_minutes":  result.RuntimeMinutes,
			"season":           result.Season,
			"episode":          result.Episode,
			"episode_title":    result.EpisodeTitle,
			"episode_air_date": result.EpisodeAirDate,
			"poster_path":      result.PosterPath,
			"poster_url":       posterURL,
			"imdb_id":          result.ImdbID,
			"tmdb_id":          result.TMDBId,
			"tvdb_series_id":   result.TVDBSeriesID,
			"confidence":       result.Confidence,
		})
	}

	errMsg := ""
	if lookupErr != nil {
		errMsg = lookupErr.Error()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"candidates": candidates,
		"error":      errMsg,
	})
}

// RetryJob handles POST /api/jobs/:id/retry
func (h *Handler) RetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if job.Status != jobs.StatusFailed {
		writeError(w, http.StatusBadRequest, "can only retry failed jobs")
		return
	}

	// Re-probe the file and create a new job
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	probe, err := h.browser.ProbeFile(ctx, job.InputPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to probe file: %v", err))
		return
	}

	// Add new job with same preset and quality tier
	newJob, err := h.queue.Add(job.InputPath, job.PresetID, probe, job.SmartShrinkQuality)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create job: %v", err))
		return
	}

	// Remove the failed job
	h.queue.Remove(id)

	writeJSON(w, http.StatusOK, newJob)
}

// TestNotifications handles POST /api/notifications/test
// Sends a test message through all configured notification channels.
func (h *Handler) TestNotifications(w http.ResponseWriter, r *http.Request) {
	if h.dispatcher == nil || !h.dispatcher.IsAnyConfigured() {
		writeError(w, http.StatusBadRequest, "no notification channels configured")
		return
	}

	if err := h.dispatcher.DispatchTest(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "test notification sent"})
}

// PauseIntake handles POST /api/intake/pause
func (h *Handler) PauseIntake(w http.ResponseWriter, r *http.Request) {
	if h.watcher == nil {
		writeError(w, http.StatusServiceUnavailable, "intake watcher not configured")
		return
	}
	h.watcher.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": true})
}

// ResumeIntake handles POST /api/intake/resume
func (h *Handler) ResumeIntake(w http.ResponseWriter, r *http.Request) {
	if h.watcher == nil {
		writeError(w, http.StatusServiceUnavailable, "intake watcher not configured")
		return
	}
	h.watcher.Resume()
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": false})
}

// IntakeStatus handles GET /api/intake/status
func (h *Handler) IntakeStatus(w http.ResponseWriter, r *http.Request) {
	if h.watcher == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false, "paused": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"paused":  h.watcher.Paused(),
	})
}

// SystemStop handles POST /api/system/stop
// Combined kill switch used by the tray "Stop MediaForge" menu item: pauses
// intake (no new files picked up from the watch folder) and stops the encode
// queue (cancels any in-progress job(s), pauses the queue). Distinct from the
// web UI's per-control Pause Intake / Stop All buttons, which act on one
// subsystem at a time.
func (h *Handler) SystemStop(w http.ResponseWriter, r *http.Request) {
	if h.watcher != nil {
		h.watcher.Pause()
	}
	stopped := h.workerPool.StopAll()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "stopped",
		"stopped": stopped,
	})
}

// SystemStart handles POST /api/system/start
// Reverses SystemStop: resumes intake and unpauses the encode queue.
func (h *Handler) SystemStart(w http.ResponseWriter, r *http.Request) {
	if h.watcher != nil {
		h.watcher.Resume()
	}
	h.workerPool.Unpause()
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "started"})
}

// DispatchNotification dispatches a notification event from outside the handler
// (e.g. from the SSE handler or intake watcher).
func (h *Handler) DispatchNotification(e *notify.Event) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(context.Background(), e)
	}
}
