package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
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
	"github.com/braydin72/mediaforge/internal/setup"
	"github.com/braydin72/mediaforge/internal/store"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: ./config/mediaforge.yaml)")
	port := flag.Int("port", 8080, "Port to listen on")
	mediaPath := flag.String("media", "", "Override media path from config")
	flag.Parse()

	// Determine config path
	cfgPath := *configPath
	if cfgPath == "" {
		// Check environment variable
		if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
			cfgPath = envPath
		} else {
			// Default to ./config/mediaforge.yaml
			cfgPath = "config/mediaforge.yaml"
		}
	}

	// Record whether config existed before Load (Load creates the file when absent).
	_, statErr := os.Stat(cfgPath)
	cfgFileExisted := statErr == nil

	// Load config
	cfg, err := config.Load(cfgPath)
	if err != nil {
		// Initialize logger with default level for this warning
		logger.Init("info")
		logger.Warn("Could not load config", "path", cfgPath, "error", err)
		cfg = config.DefaultConfig()
	}

	firstRun := setup.IsFirstRun(cfgFileExisted, cfg)

	// Initialize logger with configured level
	logger.Init(cfg.LogLevel)

	// Override with environment variables
	if envMedia := os.Getenv("MEDIA_PATH"); envMedia != "" {
		cfg.MediaPath = envMedia
	}
	if *mediaPath != "" {
		cfg.MediaPath = *mediaPath
	}

	// Override temp path with environment variable
	if envTemp := os.Getenv("TEMP_PATH"); envTemp != "" {
		cfg.TempPath = envTemp
	}

	// Auto-detect /temp mount if temp_path is still not configured
	if cfg.TempPath == "" {
		if info, err := os.Stat("/temp"); err == nil && info.IsDir() {
			cfg.TempPath = "/temp"
		}
	}

	// Validate media path exists (skip on first run: path is a default placeholder).
	if !firstRun {
		if _, err := os.Stat(cfg.MediaPath); os.IsNotExist(err) { //nolint:gosec // path comes from config file, not user input
			logger.Error("Media path does not exist", "path", cfg.MediaPath)
			os.Exit(1)
		}
	}

	// Determine config directory for data storage
	configDir := filepath.Dir(cfgPath)
	if configDir == "." {
		configDir = "config"
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil { //nolint:gosec // path derived from config file path
		logger.Warn("Could not create config directory", "error", err)
	}

	// Initialize SQLite store (handles migration from JSON if needed)
	jobStore, err := store.InitStore(configDir)
	if err != nil {
		logger.Error("Failed to initialize job store", "error", err)
		os.Exit(1)
	}
	defer jobStore.Close()

	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║                        MEDIAFORGE                         ║")
	fmt.Println("║             Ingest, Transcode, Organize                   ║")
	versionLine := fmt.Sprintf("v%s", mediaforge.Version)
	padding := 59 - len(versionLine)
	fmt.Printf("║%*s%s%*s║\n", padding/2, "", versionLine, (padding+1)/2, "")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Media path:   %s\n", cfg.MediaPath)
	fmt.Printf("  Config:       %s\n", cfgPath)
	fmt.Printf("  Database:     %s\n", jobStore.Path())
	if cfg.TempPath != "" {
		fmt.Printf("  Temp path:    %s\n", cfg.TempPath)
	} else {
		fmt.Printf("  Temp path:    %s (default)\n", os.TempDir())
	}
	fmt.Printf("  Workers:      %d\n", cfg.Workers)
	fmt.Printf("  Original:     %s\n", cfg.OriginalHandling)
	fmt.Printf("  FFmpeg:       %s\n", cfg.FFmpegPath)
	fmt.Printf("  FFprobe:      %s\n", cfg.FFprobePath)
	fmt.Println()

	// Detect available hardware encoders
	ffmpeg.DetectEncoders(cfg.FFmpegPath)

	// Detect VMAF availability (must be BEFORE preset init for SmartShrink presets)
	// Logging deferred until after splash screen
	vmaf.DetectVMAF(cfg.FFmpegPath)

	// Validate max concurrent analyses setting (clamped by jobs package)
	if cfg.MaxConcurrentAnalyses < jobs.MinConcurrentAnalyses {
		cfg.MaxConcurrentAnalyses = jobs.MinConcurrentAnalyses
	}
	if cfg.MaxConcurrentAnalyses > jobs.MaxConcurrentAnalyses {
		cfg.MaxConcurrentAnalyses = jobs.MaxConcurrentAnalyses
	}

	// Initialize presets (depends on encoder AND VMAF detection)
	ffmpeg.InitPresets()

	// Display detected encoders
	fmt.Println("  Encoders:")
	best := ffmpeg.GetBestEncoder()
	for _, enc := range ffmpeg.ListAvailableEncoders() {
		if enc.Available {
			marker := "  "
			if enc.Accel == best.Accel {
				marker = "* "
			}
			fmt.Printf("    %s%s (%s)\n", marker, enc.Name, enc.Encoder)
		}
	}
	fmt.Println()

	// Initialize components
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)

	queue, err := jobs.NewQueueWithStore(jobStore)
	if err != nil {
		logger.Error("Failed to initialize job queue", "error", err)
		jobStore.Close()
		os.Exit(1) //nolint:gocritic // store closed explicitly above
	}
	queue.SetAllowSameCodec(cfg.AllowSameCodec)

	workerPool := jobs.NewWorkerPool(queue, cfg, browser.InvalidateCache)

	// Create API handler
	handler := api.NewHandler(browser, queue, workerPool, cfg, cfgPath)
	handler.SetStore(jobStore)       // Enable session/lifetime stats
	handler.SetReviewStore(jobStore) // Enable Review Queue API
	router := api.NewRouter(handler, mediaforge.WebFS)

	// Wrap router with first-run wizard if needed.
	var wizardHandler *setup.WizardHandler
	var serverHandler http.Handler = router
	if firstRun {
		logger.Info("First-run detected: serving setup wizard until configuration is complete")
		wizardHandler = setup.NewWizardHandler(router, cfgPath, cfg)
		serverHandler = wizardHandler
	}

	// Start worker pool
	workerPool.Start()

	// Protect intakeWatcher so the wizard-complete goroutine and shutdown goroutine
	// can access it safely.
	var (
		intakeWatcher   *intake.Watcher
		intakeWatcherMu sync.Mutex
	)

	startIntake := func() {
		if cfg.Intake.Enabled {
			intakeWatcherMu.Lock()
			intakeWatcher = intake.NewWatcher(&cfg.Intake, cfg.FFprobePath, jobStore)
			intakeWatcher.OnReviewQueueAdd = func(filename, reason string) {
				handler.DispatchNotification(&notify.Event{
					Type:     notify.EventReviewQueueItem,
					Filename: filename,
					Reason:   reason,
				})
			}
			intakeWatcher.EncodeQueue = queue
			intakeWatcher.EncodePresetID = cfg.DefaultPreset
			intakeWatcher.SmartShrinkQuality = "good"
			intakeWatcher.OutputFormat = cfg.OutputFormat
			intakeWatcherMu.Unlock()
			go intakeWatcher.Start(context.Background())
		} else {
			logger.Info("Intake pipeline disabled (enable in Settings to activate)")
		}
	}

	if firstRun {
		go func() {
			<-wizardHandler.Done()
			logger.Info("Setup wizard complete: starting intake watcher")
			startIntake()
		}()
	} else {
		startIntake()
	}

	fmt.Printf("  Starting server on port %d\n", *port)
	fmt.Println()
	fmt.Println("  Press Ctrl+C to stop")
	fmt.Println()

	// Print logging separator and consolidated startup log
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Printf("  Logging started (level: %s)\n", cfg.LogLevel)
	fmt.Println("─────────────────────────────────────────────────────────────")
	logger.Info("MediaForge started", "version", mediaforge.Version, "encoder", best.Name, "workers", cfg.Workers, "port", *port)
	go browser.WarmCountCache(context.Background())
	if vmaf.IsAvailable() {
		logger.Info("VMAF support detected", "models", vmaf.GetModels())
		logger.Info("VMAF scoring configured", "max_score_workers", vmaf.MaxScoreWorkers, "gomaxprocs", runtime.GOMAXPROCS(0))
	} else {
		logger.Info("VMAF not available - SmartShrink presets will be hidden")
	}

	// Set up graceful shutdown
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           serverHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n  Shutting down...")
		logger.Info("Shutdown signal received")
		intakeWatcherMu.Lock()
		w := intakeWatcher
		intakeWatcherMu.Unlock()
		if w != nil {
			w.Stop()
		}
		workerPool.Stop()
		handler.Dispatcher().Stop()
		server.Close()
	}()

	// Start server
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("Server error", "error", err)
		workerPool.Stop()
		os.Exit(1)
	}

	logger.Info("Server stopped")
	fmt.Println("  Goodbye!")
}
