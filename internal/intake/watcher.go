package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/jobs"
	"github.com/braydin72/mediaforge/internal/logger"
	"github.com/braydin72/mediaforge/internal/store"
	"github.com/braydin72/mediaforge/internal/util"
)

// defaultScanInterval is how often the watch folder is polled in production.
const defaultScanInterval = 30 * time.Second

// probeTimeout is the maximum time allowed for a single ffprobe call.
const probeTimeout = 2 * time.Minute

// Watcher polls a watch folder for new video files and drives the intake pipeline.
//
// Lifecycle:
//  1. Scan watch folder every ScanInterval for new video files.
//  2. For each new file, run a stability check (repeated os.Stat until size is
//     stable for StabilityCheck.PassesRequired consecutive reads).
//  3. Run ffprobe to detect the video codec.
//  4. Route: HEVC → log "ready for library move", H264 → log "ready for staging",
//     unknown → Review Queue with specific reason.
// ReviewQueueNotifyFn is called when a file is added to the Review Queue.
// filename is the base name of the file; reason is the human-readable failure reason.
type ReviewQueueNotifyFn func(filename, reason string)

type Watcher struct {
	cfg          config.IntakeConfig
	prober       *ffmpeg.Prober
	st           *store.SQLiteStore
	ScanInterval time.Duration // overridable for tests; defaults to defaultScanInterval

	// OnReviewQueueAdd is called each time a file is added to the Review Queue.
	// May be nil.
	OnReviewQueueAdd ReviewQueueNotifyFn

	// EncodeQueue is the job queue used to schedule H264/AVC files for encoding.
	// If nil, AVC files are detected but not queued (logged only).
	EncodeQueue *jobs.Queue

	// EncodePresetID is the preset used when adding AVC files to the encode queue.
	// Defaults to "compress-hevc" when empty.
	EncodePresetID string

	// SmartShrinkQuality is the quality tier for SmartShrink presets ("good" by default).
	SmartShrinkQuality string

	// OutputFormat is the container format for encoded output ("mkv" or "mp4").
	// Used to determine the file extension when resolving the library destination path.
	// Defaults to "mkv" when empty.
	OutputFormat string

	// Orchestrator is the tiered metadata lookup chain (TVDB → TMDB → OMDb).
	// When nil, the lookup step is skipped and the file is moved using parsed filename
	// metadata only.
	Orchestrator *Orchestrator

	// LLMClient is the optional AI verification backend. When nil (or when its
	// configured backend is disabled), files with confidence between review_threshold
	// and confidence_threshold go directly to the Review Queue.
	LLMClient *LLMClient

	// known tracks files we have seen and are currently processing or have processed
	// in this session. Files removed from the watch folder by later pipeline phases
	// will simply not be present on the next scan.
	known map[string]struct{}
	mu    sync.Mutex

	stopCh chan struct{}
}

// NewWatcher creates a Watcher. Call Start in a goroutine to begin polling.
func NewWatcher(cfg *config.IntakeConfig, ffprobePath string, st *store.SQLiteStore) *Watcher {
	return &Watcher{
		cfg:          *cfg,
		prober:       ffmpeg.NewProber(ffprobePath),
		st:           st,
		ScanInterval: defaultScanInterval,
		known:        make(map[string]struct{}),
		stopCh:       make(chan struct{}),
	}
}

// Start begins the polling loop. It blocks until Stop is called or ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	logger.Info("Intake watcher started", "folder", w.cfg.WatchFolder,
		"stability_interval_s", w.cfg.StabilityCheck.IntervalSeconds,
		"stability_passes", w.cfg.StabilityCheck.PassesRequired,
	)

	ticker := time.NewTicker(w.ScanInterval)
	defer ticker.Stop()

	// Scan immediately on start rather than waiting the full interval.
	w.scan(ctx)

	for {
		select {
		case <-ticker.C:
			w.scan(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop signals the watcher to exit after the current scan completes.
func (w *Watcher) Stop() {
	close(w.stopCh)
}

// scan reads the top-level contents of the watch folder and spawns a pipeline
// goroutine for each newly discovered video file.
func (w *Watcher) scan(ctx context.Context) {
	entries, err := os.ReadDir(w.cfg.WatchFolder)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn("Intake: watch folder does not exist", "folder", w.cfg.WatchFolder)
		} else {
			logger.Warn("Intake: scan error", "folder", w.cfg.WatchFolder, "error", err)
		}
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !ffmpeg.IsVideoFile(e.Name()) {
			continue
		}

		path := filepath.Join(w.cfg.WatchFolder, e.Name())

		w.mu.Lock()
		_, seen := w.known[path]
		if !seen {
			w.known[path] = struct{}{}
		}
		w.mu.Unlock()

		if seen {
			continue
		}

		go w.process(ctx, path)
	}
}

// process runs the full stability → ffprobe → route pipeline for one file.
// It never modifies the file.
func (w *Watcher) process(ctx context.Context, path string) {
	filename := filepath.Base(path)
	logger.Info("Intake: new file detected", "file", filename)

	if err := w.waitForStability(ctx, path); err != nil {
		logger.Warn("Intake: stability check failed", "file", filename, "error", err)
		// Remove from known so it will be retried on the next scan.
		w.mu.Lock()
		delete(w.known, path)
		w.mu.Unlock()
		return
	}

	w.runPipeline(ctx, path)
}

// ProcessFile runs the intake pipeline for a single file, skipping the
// stability check. Intended for manually added files via the API where
// the caller guarantees the file is already fully written.
func (w *Watcher) ProcessFile(ctx context.Context, path string) {
	w.mu.Lock()
	w.known[path] = struct{}{}
	w.mu.Unlock()
	go w.runPipeline(ctx, path)
}

// runPipeline runs ffprobe and routes the file: HEVC to library, H264 to
// staging/encode queue, anything else to the Review Queue.
func (w *Watcher) runPipeline(ctx context.Context, path string) {
	filename := filepath.Base(path)
	logger.Info("Intake: file is stable, running codec detection", "file", filename)

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	probe, err := w.prober.Probe(probeCtx, path)
	if err != nil {
		reason := fmt.Sprintf("codec detection failed: %s", err.Error())
		logger.Warn("Intake: ffprobe error", "file", filename, "error", err)
		w.sendToReviewQueue(path, reason, nil)
		return
	}

	switch classifyCodec(probe.VideoCodec) {
	case "hevc":
		logger.Info("Intake: HEVC — ready for library move",
			"file", filename, "codec", probe.VideoCodec,
			"resolution", fmt.Sprintf("%dx%d", probe.Width, probe.Height),
		)
		w.moveHEVCToLibrary(ctx, path, probe)
	case "h264":
		logger.Info("Intake: H264 — staging for encode",
			"file", filename, "codec", probe.VideoCodec,
			"resolution", fmt.Sprintf("%dx%d", probe.Width, probe.Height),
		)
		w.stageAndEnqueue(ctx, path, probe)
	default:
		var reason string
		if probe.VideoCodec == "" {
			reason = "codec detection failed: no video stream found"
		} else {
			reason = fmt.Sprintf("codec detection failed: unrecognized codec %q", probe.VideoCodec)
		}
		logger.Warn("Intake: unknown codec, queuing for review", "file", filename, "codec", probe.VideoCodec)
		w.sendToReviewQueue(path, reason, probe)
	}
}

// stageAndEnqueue moves an AVC file to the staging folder and adds it to the
// encode queue. On any failure the file is sent to the Review Queue with a
// specific reason and the source file is left untouched.
func (w *Watcher) stageAndEnqueue(ctx context.Context, path string, probe *ffmpeg.ProbeResult) {
	filename := filepath.Base(path)

	if w.EncodeQueue == nil {
		logger.Info("Intake: H264 detected but no encode queue configured — skipping", "file", filename)
		return
	}

	if w.cfg.StagingFolder == "" {
		w.sendToReviewQueue(path, "staging folder not configured", probe)
		return
	}

	stagingPath := filepath.Join(w.cfg.StagingFolder, filename)
	if err := util.SafeMove(path, stagingPath); err != nil {
		reason := fmt.Sprintf("staging move failed: %v", err)
		logger.Warn("Intake: failed to move AVC file to staging", "file", filename, "error", err)
		w.sendToReviewQueue(path, reason, probe)
		return
	}

	presetID := w.EncodePresetID
	if presetID == "" {
		presetID = "compress-hevc"
	}
	quality := w.SmartShrinkQuality
	if quality == "" {
		quality = "good"
	}

	job, err := w.EncodeQueue.Add(stagingPath, presetID, probe, quality)
	if err != nil {
		reason := fmt.Sprintf("failed to queue encode job: %v", err)
		logger.Warn("Intake: failed to enqueue AVC file", "file", filename, "error", err)
		w.sendToReviewQueue(stagingPath, reason, probe)
		return
	}

	// Parse filename then run metadata lookup to populate EpisodeTitle and confirmed
	// title/year before resolving the library destination path.
	parsed := ParseFilename(filename)
	if w.Orchestrator != nil {
		reviewThreshold := w.cfg.ReviewThreshold
		if reviewThreshold == 0 {
			reviewThreshold = 0.60
		}
		var (
			result    *LookupResult
			lookupErr error
		)
		if parsed.IsTV {
			result, lookupErr = w.Orchestrator.LookupTV(ctx, &parsed, reviewThreshold)
		} else {
			result, lookupErr = w.Orchestrator.LookupMovie(ctx, &parsed, probe.Duration, reviewThreshold)
		}
		if lookupErr != nil {
			logger.Warn("Intake: AVC metadata lookup failed — library path will be set without episode title",
				"file", filename, "error", lookupErr)
		} else {
			logger.Info("Intake: AVC metadata lookup succeeded",
				"file", filename, "title", result.Title,
				"episode_title", result.EpisodeTitle, "confidence", result.Confidence)
			parsed.Title = result.Title
			if result.Year > 0 {
				parsed.Year = result.Year
			}
			parsed.EpisodeTitle = result.EpisodeTitle
		}
	}

	// Resolve output extension: "preserve" inherits the source container; otherwise
	// use the configured format. ffmpeg.ResolveOutputFormat handles "preserve" by
	// inspecting the input file extension, matching what the worker does at encode time.
	outFmt := ffmpeg.ResolveOutputFormat(filename, w.OutputFormat)
	if outFmt == "" {
		outFmt = "mkv"
	}
	libraryPath := resolveLibraryPath(&w.cfg, &parsed, "."+outFmt)
	if libraryPath != "" {
		w.EncodeQueue.SetLibraryPath(job.ID, libraryPath)
		logger.Info("Intake: AVC file queued for encode",
			"file", filename, "job_id", job.ID,
			"staging", stagingPath, "library", libraryPath,
		)
	} else {
		logger.Info("Intake: AVC file queued for encode (library path pending identification)",
			"file", filename, "job_id", job.ID, "staging", stagingPath,
		)
	}
}

// moveHEVCToLibrary runs metadata lookup, applies confidence gating, and moves
// the file to the library. Any failure routes the file to the Review Queue with
// a specific reason; the source file is never left stranded.
func (w *Watcher) moveHEVCToLibrary(ctx context.Context, path string, probe *ffmpeg.ProbeResult) {
	filename := filepath.Base(path)
	parsed := ParseFilename(filename)

	if w.Orchestrator != nil {
		confThreshold := w.cfg.ConfidenceThreshold
		if confThreshold == 0 {
			confThreshold = 0.85
		}
		reviewThreshold := w.cfg.ReviewThreshold
		if reviewThreshold == 0 {
			reviewThreshold = 0.60
		}

		var (
			result *LookupResult
			err    error
		)
		if parsed.IsTV {
			result, err = w.Orchestrator.LookupTV(ctx, &parsed, reviewThreshold)
		} else {
			result, err = w.Orchestrator.LookupMovie(ctx, &parsed, probe.Duration, reviewThreshold)
		}
		if err != nil {
			reason := "no metadata match found: " + err.Error()
			logger.Warn("Intake: HEVC metadata lookup failed", "file", filename, "error", err)
			w.sendToReviewQueue(path, reason, probe)
			return
		}

		if result.Confidence < reviewThreshold {
			reason := fmt.Sprintf("low confidence match (%.0f%%) for %q", result.Confidence*100, result.Title)
			logger.Warn("Intake: HEVC confidence below review threshold", "file", filename, "confidence", result.Confidence)
			w.sendToReviewQueue(path, reason, probe)
			return
		}

		if result.Confidence < confThreshold {
			// Confidence is between review_threshold and confidence_threshold — try LLM.
			if w.LLMClient == nil {
				reason := fmt.Sprintf("confidence %.0f%% requires LLM verification — LLM not configured", result.Confidence*100)
				w.sendToReviewQueue(path, reason, probe)
				return
			}
			llmResult, llmErr := w.LLMClient.Verify(ctx, &parsed, []*LookupResult{result})
			if llmErr != nil {
				reason := fmt.Sprintf("LLM verification failed: %v", llmErr)
				logger.Warn("Intake: HEVC LLM verification error", "file", filename, "error", llmErr)
				w.sendToReviewQueue(path, reason, probe)
				return
			}
			if llmResult.Disabled {
				reason := fmt.Sprintf("confidence %.0f%% requires LLM verification — LLM not configured", result.Confidence*100)
				w.sendToReviewQueue(path, reason, probe)
				return
			}
			if llmResult.CandidateID == "none" || llmResult.Confidence < reviewThreshold {
				reason := fmt.Sprintf("LLM verification rejected match: %s", llmResult.Reasoning)
				logger.Warn("Intake: HEVC LLM rejected match", "file", filename)
				w.sendToReviewQueue(path, reason, probe)
				return
			}
			result.Confidence = llmResult.Confidence
		}

		// Merge confirmed metadata into parsed for path resolution.
		parsed.Title = result.Title
		if result.Year > 0 {
			parsed.Year = result.Year
		}
		parsed.EpisodeTitle = result.EpisodeTitle
	}

	ext := filepath.Ext(filename)
	libraryPath := resolveLibraryPath(&w.cfg, &parsed, ext)
	if libraryPath == "" {
		reason := "could not resolve library destination path from metadata"
		logger.Warn("Intake: HEVC library path resolution failed", "file", filename)
		w.sendToReviewQueue(path, reason, probe)
		return
	}

	logger.Info("Intake: HEVC moving to library", "file", filename, "destination", libraryPath)
	if err := util.SafeMove(path, libraryPath); err != nil {
		reason := fmt.Sprintf("library move failed: %v", err)
		logger.Warn("Intake: HEVC move error", "file", filename, "error", err)
		w.sendToReviewQueue(path, reason, probe)
		return
	}
	logger.Info("Intake: HEVC successfully moved to library", "file", filename, "destination", libraryPath)
}

// waitForStability polls the file size every StabilityCheck.IntervalSeconds seconds
// until StabilityCheck.PassesRequired consecutive reads return the same non-zero size,
// then confirms the file is fully released by attempting an exclusive open. On Windows,
// the reported file size can appear stable momentarily during an active copy, so both
// signals must pass before the file is considered ready.
func (w *Watcher) waitForStability(ctx context.Context, path string) error {
	interval := time.Duration(w.cfg.StabilityCheck.IntervalSeconds) * time.Second
	required := w.cfg.StabilityCheck.PassesRequired

	var lastSize int64 = -1
	consecutive := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stopCh:
			return fmt.Errorf("watcher stopped")
		case <-time.After(interval):
		}

		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat: %w", err)
		}

		size := info.Size()
		if size == 0 {
			// File is empty — still being created; reset and keep waiting.
			consecutive = 0
			lastSize = 0
			continue
		}

		if size == lastSize {
			consecutive++
			if consecutive >= required {
				// Size is stable — confirm the file is not held open for writing
				// by attempting an exclusive open. On Windows this fails while a
				// copy is in progress even when the reported size is stable.
				f, openErr := os.OpenFile(path, os.O_RDWR, 0)
				if openErr != nil {
					// File still locked; reset size streak and keep waiting.
					consecutive = 0
					continue
				}
				f.Close()
				return nil
			}
		} else {
			consecutive = 1
			lastSize = size
		}
	}
}

// sendToReviewQueue persists a Review Queue entry for the given file.
// probe may be nil if ffprobe itself failed.
func (w *Watcher) sendToReviewQueue(path, reason string, probe *ffmpeg.ProbeResult) {
	var ffprobeJSON string
	if probe != nil {
		if b, err := json.Marshal(probe); err == nil {
			ffprobeJSON = string(b)
		}
	}

	entry := store.ReviewEntry{
		ID:           uuid.New().String(),
		OriginalPath: path,
		Filename:     filepath.Base(path),
		Reason:       reason,
		FFProbeInfo:  ffprobeJSON,
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}

	if err := w.st.AddToReviewQueue(&entry); err != nil {
		logger.Error("Intake: failed to save review queue entry", "file", entry.Filename, "error", err)
	} else {
		logger.Info("Intake: added to review queue", "file", entry.Filename, "reason", reason)
		if w.OnReviewQueueAdd != nil {
			w.OnReviewQueueAdd(entry.Filename, reason)
		}
	}
}

// classifyCodec maps a raw ffprobe codec_name to one of "hevc", "h264", or "unknown".
func classifyCodec(codec string) string {
	switch strings.ToLower(codec) {
	case "hevc", "h265", "x265":
		return "hevc"
	case "h264", "avc", "x264":
		return "h264"
	default:
		return "unknown"
	}
}
