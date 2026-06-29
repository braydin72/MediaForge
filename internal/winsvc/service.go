//go:build windows

package winsvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	mediaforge "github.com/braydin72/mediaforge"
	"github.com/braydin72/mediaforge/internal/api"
	"github.com/braydin72/mediaforge/internal/browse"
	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/ffmpeg"
	"github.com/braydin72/mediaforge/internal/ffmpeg/vmaf"
	"github.com/braydin72/mediaforge/internal/intake"
	"github.com/braydin72/mediaforge/internal/jobs"
	"github.com/braydin72/mediaforge/internal/logger"
	"github.com/braydin72/mediaforge/internal/notify"
	"github.com/braydin72/mediaforge/internal/store"
	"golang.org/x/sys/windows/svc"
)

const (
	ServiceName        = "MediaForge"
	ServiceDisplayName = "MediaForge - Media Ingest Engine"
	ServiceDescription = "Automated media ingest, transcode, and library organization"
)

// WindowsService implements svc.Handler so MediaForge can run as a Windows service.
type WindowsService struct {
	ConfigPath string
	Port       int
}

// Execute is called by the Windows Service Control Manager. It runs the full
// startup sequence, then blocks handling SCM commands until Stop or Shutdown.
func (s *WindowsService) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	cfgPath := s.ConfigPath
	if cfgPath == "" {
		cfgPath = "config/mediaforge.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Init("info")
		logger.Warn("Service: could not load config, using defaults", "error", err)
		cfg = config.DefaultConfig()
	}
	logger.Init(cfg.LogLevel)
	logger.Info("MediaForge service starting", "version", mediaforge.Version, "config", cfgPath)

	configDir := filepath.Dir(cfgPath)
	if configDir == "." {
		configDir = "config"
	}
	if err := os.MkdirAll(configDir, 0755); err != nil { //nolint:gosec
		logger.Warn("Service: could not create config dir", "error", err)
	}

	jobStore, err := store.InitStore(configDir)
	if err != nil {
		logger.Error("Service: failed to init store", "error", err)
		return false, 1
	}
	defer jobStore.Close()

	ffmpeg.DetectEncoders(cfg.FFmpegPath)
	vmaf.DetectVMAF(cfg.FFmpegPath)
	if cfg.MaxConcurrentAnalyses < jobs.MinConcurrentAnalyses {
		cfg.MaxConcurrentAnalyses = jobs.MinConcurrentAnalyses
	}
	if cfg.MaxConcurrentAnalyses > jobs.MaxConcurrentAnalyses {
		cfg.MaxConcurrentAnalyses = jobs.MaxConcurrentAnalyses
	}
	ffmpeg.InitPresets()

	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)

	queue, err := jobs.NewQueueWithStore(jobStore)
	if err != nil {
		logger.Error("Service: failed to init job queue", "error", err)
		return false, 1
	}
	queue.SetAllowSameCodec(cfg.AllowSameCodec)

	workerPool := jobs.NewWorkerPool(queue, cfg, browser.InvalidateCache)
	handler := api.NewHandler(browser, queue, workerPool, cfg, cfgPath)
	handler.SetStore(jobStore)
	handler.SetReviewStore(jobStore)
	router := api.NewRouter(handler, mediaforge.WebFS)

	workerPool.Start()

	var (
		intakeWatcher   *intake.Watcher
		intakeWatcherMu sync.Mutex
	)

	startIntake := func() {
		intakeWatcherMu.Lock()
		defer intakeWatcherMu.Unlock()

		w := intake.NewWatcher(&cfg.Intake, cfg.FFprobePath, jobStore)
		w.OnReviewQueueAdd = func(filename, reason string) {
			handler.DispatchNotification(&notify.Event{
				Type:     notify.EventReviewQueueItem,
				Filename: filename,
				Reason:   reason,
			})
		}
		w.EncodeQueue = queue
		w.EncodePresetID = cfg.DefaultPreset
		quality := cfg.DefaultQuality
		if quality == "" {
			quality = "good"
		}
		w.SmartShrinkQuality = quality
		w.OutputFormat = cfg.OutputFormat

		if cfg.APIs.TVDBKey != "" {
			tvdb := intake.NewTVDBClient(cfg.APIs.TVDBKey, nil)
			tmdb := (*intake.TMDBClient)(nil)
			omdb := (*intake.OMDbClient)(nil)
			if cfg.APIs.TMDBKey != "" {
				tmdb = intake.NewTMDBClient(cfg.APIs.TMDBKey, nil)
			}
			if cfg.APIs.OMDbKey != "" {
				omdb = intake.NewOMDbClient(cfg.APIs.OMDbKey, nil)
			}
			w.Orchestrator = intake.NewOrchestrator(tvdb, tmdb, omdb)
		} else if cfg.APIs.TMDBKey != "" || cfg.APIs.OMDbKey != "" {
			var tmdb *intake.TMDBClient
			var omdb *intake.OMDbClient
			if cfg.APIs.TMDBKey != "" {
				tmdb = intake.NewTMDBClient(cfg.APIs.TMDBKey, nil)
			}
			if cfg.APIs.OMDbKey != "" {
				omdb = intake.NewOMDbClient(cfg.APIs.OMDbKey, nil)
			}
			w.Orchestrator = intake.NewOrchestrator(nil, tmdb, omdb)
		}

		if cfg.LLM.Backend != "" {
			w.LLMClient = intake.NewLLMClient(cfg.LLM, nil)
		}

		handler.SetIntakeWatcher(w)
		intakeWatcher = w
	}

	startIntake()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go browser.WarmCountCache(ctx)

	if intakeWatcher != nil && cfg.Intake.Enabled {
		go intakeWatcher.Start(ctx)
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Service: server error", "error", err)
		}
	}()

	logger.Info("MediaForge service running", "port", s.Port)
	status <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue,
	}

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus

		case svc.Stop, svc.Shutdown:
			logger.Info("Service: stop requested")
			status <- svc.Status{State: svc.StopPending}

			intakeWatcherMu.Lock()
			w := intakeWatcher
			intakeWatcherMu.Unlock()
			if w != nil {
				w.Stop()
			}

			// Wait up to 30 seconds for running encode jobs to finish.
			stopped := make(chan struct{})
			go func() {
				workerPool.Stop()
				close(stopped)
			}()
			select {
			case <-stopped:
				logger.Info("Service: all encode jobs finished")
			case <-time.After(30 * time.Second):
				logger.Warn("Service: encode jobs did not finish within 30s; forcing shutdown")
			}

			handler.Dispatcher().Stop()
			_ = server.Close()
			cancel()
			return false, 0

		case svc.Pause:
			intakeWatcherMu.Lock()
			w := intakeWatcher
			intakeWatcherMu.Unlock()
			if w != nil {
				w.Pause()
			}
			logger.Info("Service: paused")
			status <- svc.Status{
				State:   svc.Paused,
				Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue,
			}

		case svc.Continue:
			intakeWatcherMu.Lock()
			w := intakeWatcher
			intakeWatcherMu.Unlock()
			if w != nil {
				w.Resume()
			}
			logger.Info("Service: resumed")
			status <- svc.Status{
				State:   svc.Running,
				Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue,
			}

		default:
			logger.Warn("Service: unexpected SCM command", "cmd", c.Cmd)
		}
	}
	return false, 0
}

// Run starts MediaForge under Windows Service Control Manager control.
func Run(configPath string, port int) error {
	return svc.Run(ServiceName, &WindowsService{ConfigPath: configPath, Port: port})
}
