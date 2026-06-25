package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	shrinkray "github.com/gwlsn/shrinkray"
	"github.com/gwlsn/shrinkray/internal/browse"
	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/ffmpeg/vmaf"
	"github.com/gwlsn/shrinkray/internal/intake"
	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/logger"
	"github.com/gwlsn/shrinkray/internal/pushover"
	"github.com/gwlsn/shrinkray/internal/store"
)

// StatsStore defines the interface for stats-related store operations.
type StatsStore interface {
	ResetSession() error
}

// ReviewQueueStore defines the interface for Review Queue read/write operations.
type ReviewQueueStore interface {
	GetReviewQueue() ([]store.ReviewEntry, error)
	GetReviewEntry(id string) (*store.ReviewEntry, error)
	GetReviewQueueCount() (int, error)
	UpdateReviewQueueStatus(id, status string) error
}

// Handler provides HTTP API handlers
type Handler struct {
	browser      *browse.Browser
	queue        *jobs.Queue
	workerPool   *jobs.WorkerPool
	cfg          *config.Config
	cfgPath      string
	pushover     *pushover.Client
	notifyMu     sync.Mutex    // Protects notification sending to prevent duplicates
	store        StatsStore    // For stats operations (may be nil)
	reviewStore  ReviewQueueStore // For Review Queue operations (may be nil)
}

// NewHandler creates a new API handler
func NewHandler(browser *browse.Browser, queue *jobs.Queue, workerPool *jobs.WorkerPool, cfg *config.Config, cfgPath string) *Handler {
	return &Handler{
		browser:    browser,
		queue:      queue,
		workerPool: workerPool,
		cfg:        cfg,
		cfgPath:    cfgPath,
		pushover:   pushover.NewClient(cfg.PushoverUserKey, cfg.PushoverAppToken),
	}
}

// SetStore sets the stats store for session/lifetime stats operations.
func (h *Handler) SetStore(store StatsStore) {
	h.store = store
}

// SetReviewStore sets the Review Queue store.
func (h *Handler) SetReviewStore(s ReviewQueueStore) {
	h.reviewStore = s
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

	writeJSON(w, http.StatusOK, result)
}

// Presets handles GET /api/presets
func (h *Handler) Presets(w http.ResponseWriter, r *http.Request) {
	presets := ffmpeg.ListPresets()
	writeJSON(w, http.StatusOK, presets)
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

// CreateJobsRequest is the request body for creating jobs
type CreateJobsRequest struct {
	Paths              []string `json:"paths"`
	PresetID           string   `json:"preset_id"`
	SmartShrinkQuality string   `json:"smartshrink_quality,omitempty"`
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

	preset := ffmpeg.GetPreset(req.PresetID)
	if preset == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown preset: %s", req.PresetID))
		return
	}

	// Validate SmartShrink quality if provided
	smartShrinkQuality := req.SmartShrinkQuality
	if smartShrinkQuality != "" && !jobs.IsValidSmartShrinkQuality(smartShrinkQuality) {
		writeError(w, http.StatusBadRequest, "smartshrink_quality must be 'acceptable', 'good', or 'excellent'")
		return
	}

	// Respond immediately - jobs will be added in background and appear via SSE
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "processing",
		"message": fmt.Sprintf("Processing %d paths in background...", len(req.Paths)),
	})

	// Auto-unpause when adding new jobs (prevents accidental blocking)
	h.workerPool.Unpause()

	// Process in background goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Progress callback broadcasts SSE events (throttled to max 10/sec).
		// Uses atomic time to avoid race between concurrent probe goroutines.
		var lastBroadcastNano int64
		onProgress := func(probed, total int) {
			now := time.Now()
			last := time.Unix(0, atomic.LoadInt64(&lastBroadcastNano))
			// Throttle broadcasts, but always send first (0/N) and last (N/N)
			if probed > 0 && probed < total && now.Sub(last) < 100*time.Millisecond {
				return
			}
			atomic.StoreInt64(&lastBroadcastNano, now.UnixNano())
			h.queue.BroadcastProgress(probed, total)
		}

		// Get all video files with progress reporting
		probes, err := h.browser.GetVideoFilesWithProgress(ctx, req.Paths, onProgress)
		if err != nil {
			logger.Error("Error getting video files", "error", err)
			return
		}

		if len(probes) == 0 {
			return
		}

		// Add jobs to queue - SSE will notify frontend of new jobs
		_, _ = h.queue.AddMultiple(probes, req.PresetID, smartShrinkQuality)
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

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
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

// PauseQueue handles POST /api/queue/pause
// Stops all running jobs and prevents new jobs from starting
func (h *Handler) PauseQueue(w http.ResponseWriter, r *http.Request) {
	count := h.workerPool.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused":   true,
		"requeued": count,
	})
}

// ResumeQueue handles POST /api/queue/resume
// Allows workers to pick up jobs again
func (h *Handler) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	h.workerPool.Unpause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused": false,
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
		"version":                 shrinkray.Version,
		"media_path":              h.cfg.MediaPath,
		"original_handling":       h.cfg.OriginalHandling,
		"use_completed_dir":       h.cfg.UseCompletedDir,
		"workers":                 h.cfg.Workers,
		"has_temp_path":           h.cfg.TempPath != "",
		"pushover_user_key":       h.cfg.PushoverUserKey,
		"pushover_app_token":      h.cfg.PushoverAppToken,
		"pushover_configured":     h.pushover.IsConfigured(),
		"notify_on_complete":      h.cfg.NotifyOnComplete,
		"quality_hevc":            h.cfg.QualityHEVC,
		"quality_av1":             h.cfg.QualityAV1,
		"default_quality_hevc":    defaultHEVC,
		"default_quality_av1":     defaultAV1,
		"hevc_encoder":            string(bestHEVC.Accel),
		"av1_encoder":             string(bestAV1.Accel),
		"schedule_enabled":        h.cfg.ScheduleEnabled,
		"schedule_start_hour":     h.cfg.ScheduleStartHour,
		"schedule_end_hour":       h.cfg.ScheduleEndHour,
		"output_format":           h.cfg.OutputFormat,
		"tonemap_hdr":             h.cfg.TonemapHDR,
		"tonemap_algorithm":       h.cfg.TonemapAlgorithm,
		"max_concurrent_analyses": h.cfg.MaxConcurrentAnalyses,
		"log_level":               h.cfg.LogLevel,
		"allow_same_codec":        h.cfg.AllowSameCodec,
		// Intake pipeline
		"intake_enabled":             h.cfg.Intake.Enabled,
		"intake_watch_folder":        h.cfg.Intake.WatchFolder,
		"intake_staging_folder":      h.cfg.Intake.StagingFolder,
		"intake_library_movies":      h.cfg.Intake.Library.Movies,
		"intake_library_tv_shows":    h.cfg.Intake.Library.TVShows,
		"intake_stability_interval":  h.cfg.Intake.StabilityCheck.IntervalSeconds,
		"intake_stability_passes":    h.cfg.Intake.StabilityCheck.PassesRequired,
		"intake_confidence_threshold": h.cfg.Intake.ConfidenceThreshold,
		"intake_review_threshold":    h.cfg.Intake.ReviewThreshold,
		"intake_naming_movie_folder": h.cfg.Intake.Naming.MovieFolder,
		"intake_naming_movie_file":   h.cfg.Intake.Naming.MovieFile,
		"intake_naming_show_folder":  h.cfg.Intake.Naming.ShowFolder,
		"intake_naming_episode_file": h.cfg.Intake.Naming.EpisodeFile,
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
	})
}

// UpdateConfigRequest is the request body for updating config
type UpdateConfigRequest struct {
	OriginalHandling      *string `json:"original_handling,omitempty"`
	UseCompletedDir       *bool   `json:"use_completed_dir,omitempty"`
	Workers               *int    `json:"workers,omitempty"`
	PushoverUserKey       *string `json:"pushover_user_key,omitempty"`
	PushoverAppToken      *string `json:"pushover_app_token,omitempty"`
	NotifyOnComplete      *bool   `json:"notify_on_complete,omitempty"`
	QualityHEVC           *int    `json:"quality_hevc,omitempty"`
	QualityAV1            *int    `json:"quality_av1,omitempty"`
	ScheduleEnabled       *bool   `json:"schedule_enabled,omitempty"`
	ScheduleStartHour     *int    `json:"schedule_start_hour,omitempty"`
	ScheduleEndHour       *int    `json:"schedule_end_hour,omitempty"`
	OutputFormat          *string `json:"output_format,omitempty"`
	TonemapHDR            *bool   `json:"tonemap_hdr,omitempty"`
	TonemapAlgorithm      *string `json:"tonemap_algorithm,omitempty"`
	MaxConcurrentAnalyses *int    `json:"max_concurrent_analyses,omitempty"`
	LogLevel              *string `json:"log_level,omitempty"`
	AllowSameCodec        *bool   `json:"allow_same_codec,omitempty"`
	// Intake pipeline
	IntakeEnabled            *bool    `json:"intake_enabled,omitempty"`
	IntakeWatchFolder        *string  `json:"intake_watch_folder,omitempty"`
	IntakeStagingFolder      *string  `json:"intake_staging_folder,omitempty"`
	IntakeLibraryMovies      *string  `json:"intake_library_movies,omitempty"`
	IntakeLibraryTVShows     *string  `json:"intake_library_tv_shows,omitempty"`
	IntakeStabilityInterval  *int     `json:"intake_stability_interval,omitempty"`
	IntakeStabilityPasses    *int     `json:"intake_stability_passes,omitempty"`
	IntakeConfidenceThreshold *float64 `json:"intake_confidence_threshold,omitempty"`
	IntakeReviewThreshold    *float64 `json:"intake_review_threshold,omitempty"`
	IntakeNamingMovieFolder  *string  `json:"intake_naming_movie_folder,omitempty"`
	IntakeNamingMovieFile    *string  `json:"intake_naming_movie_file,omitempty"`
	IntakeNamingShowFolder   *string  `json:"intake_naming_show_folder,omitempty"`
	IntakeNamingEpisodeFile  *string  `json:"intake_naming_episode_file,omitempty"`
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
}

// UpdateConfig handles PUT /api/config
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
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
	writeJSON(w, http.StatusOK, stats)
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
	for _, e := range all {
		if e.Status == statusFilter {
			filtered = append(filtered, e)
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
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// RetryReviewEntry handles PUT /api/review/{id}/retry
func (h *Handler) RetryReviewEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "entry ID required")
		return
	}
	if h.reviewStore == nil {
		writeError(w, http.StatusServiceUnavailable, "review store not configured")
		return
	}
	// Mark resolved; full pipeline re-run wired in a future phase.
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "retried"})
}

// DiscardReviewEntry handles PUT /api/review/{id}/discard
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
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "discarded"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
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
		PresetID     string `json:"preset_id"`
		OriginalPath string `json:"original_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OriginalPath == "" {
		writeError(w, http.StatusBadRequest, "original_path required")
		return
	}
	if req.PresetID == "" {
		req.PresetID = "compress-hevc"
	}

	preset := ffmpeg.GetPreset(req.PresetID)
	if preset == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown preset: %s", req.PresetID))
		return
	}

	// Mark resolved and enqueue the file.
	if err := h.reviewStore.UpdateReviewQueueStatus(id, "resolved"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.workerPool.Unpause()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		probes, err := h.browser.GetVideoFilesWithProgress(ctx, []string{req.OriginalPath}, nil)
		if err != nil || len(probes) == 0 {
			logger.Warn("Review resubmit: probe failed", "path", req.OriginalPath, "error", err)
			return
		}
		_, _ = h.queue.AddMultiple(probes, req.PresetID, "")
	}()

	writeJSON(w, http.StatusOK, map[string]string{"status": "resubmitted"})
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

	parsed := &intake.ParsedFilename{
		Title:   q,
		Year:    year,
		Season:  season,
		Episode: episode,
	}
	if mediaType == "tv" || (mediaType == "" && (season > 0 || episode > 0)) {
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
		result, lookupErr = orch.LookupTV(r.Context(), parsed, 0.0)
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
