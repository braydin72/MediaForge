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

	// Resolve the library destination path from naming templates and parsed metadata.
	parsed := ParseFilename(filename)
	outFmt := w.OutputFormat
	if outFmt == "" || outFmt == "preserve" {
		outFmt = "mkv"
	}
	libraryPath := resolveLibraryPath(w.cfg, parsed, "."+outFmt)
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

// waitForStability polls the file size every StabilityCheck.IntervalSeconds seconds
// until StabilityCheck.PassesRequired consecutive reads return the same non-zero size.
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
