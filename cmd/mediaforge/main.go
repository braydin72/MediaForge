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
	"github.com/braydin72/mediaforge/internal/version"
)

// logDetectedEncoders logs a human-readable summary of the hardware encoders
// found by ffmpeg.DetectEncoders. CPU (software) encoding is always available,
// so it is always reported as a fallback.
func logDetectedEncoders(detected map[ffmpeg.EncoderKey]*ffmpeg.HWEncoder) {
	// Collect which hardware accel types are available across any codec.
	seen := make(map[ffmpeg.HWAccel]bool)
	for _, enc := range detected {
		if enc.Available && enc.Accel != ffmpeg.HWAccelNone {
			seen[enc.Accel] = true
		}
	}

	if seen[ffmpeg.HWAccelNVENC] {
		logger.Banner("Available encoders: NVIDIA NVENC")
	}
	if seen[ffmpeg.HWAccelVAAPI] {
		logger.Banner("Available encoders: AMD/Intel VAAPI")
	}
	if seen[ffmpeg.HWAccelQSV] {
		logger.Banner("Available encoders: Intel Quick Sync")
	}
	if seen[ffmpeg.HWAccelVideoToolbox] {
		logger.Banner("Available encoders: Apple VideoToolbox")
	}

	if len(seen) == 0 {
		logger.Banner("No hardware acceleration detected, using CPU")
	}
	logger.Banner("Available encoders: CPU fallback ready")
}

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: ./config/mediaforge.yaml)")
	port := flag.Int("port", 8080, "Port to listen on")
	mediaPath := flag.String("media", "", "Override media path from config")
	installSvc := flag.Bool("install", false, "Install MediaForge as a Windows service (Windows only)")
	uninstallSvc := flag.Bool("uninstall", false, "Remove the MediaForge Windows service (Windows only)")
	serviceSvc := flag.Bool("service", false, "Run as a Windows service (called by the service manager, not users directly)")
	flag.Parse()

	// Handle Windows service management flags before any other startup.
	if handleServiceCLI(*installSvc, *uninstallSvc, *serviceSvc, *configPath, *port) {
		return
	}

	// Determine config path: explicit flag > CONFIG_PATH env > platform default.
	// On Windows, EnsureWindowsDirs creates %APPDATA%\MediaForge\{,logs} so that
	// the config lookup and log rotation have a home regardless of working dir.
	cfgPath := *configPath
	if cfgPath == "" {
		if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
			cfgPath = envPath
		}
	}
	config.EnsureWindowsDirs()
	cfgPath = config.ResolveConfigPath(cfgPath)

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

	// Determine config directory before logger init so the log file lands there.
	// (configDir is re-derived below after config load; this early derivation is
	// only for logger bootstrap — the canonical assignment below still applies.)
	earlyConfigDir := filepath.Dir(cfgPath)
	if earlyConfigDir == "." {
		earlyConfigDir = "config"
	}

	// Initialize logger: write to stdout and a rotating session log file.
	logger.InitWithFile(cfg.LogLevel, earlyConfigDir)

	// Startup banner: record version and build number as the first log line so
	// every session log opens with exactly what binary produced it. Uses Banner
	// so it is written at every log level (even warn/error).
	logger.Banner(fmt.Sprintf("MediaForge v%s+build.%s starting", version.Version, version.Build))

	// Platform-specific first-run handling.
	// On Windows the MediaForge Setup/tray app is responsible for creating the
	// config before mediaforge.exe is ever launched, so a missing config is a
	// fatal error — we fail fast and never show a GUI. On Linux/Docker we keep
	// the web-based first-run wizard (wired below via the firstRun flag).
	if runtime.GOOS == "windows" {
		if !cfgFileExisted {
			logger.Error("Config not found. Run Windows Setup or the MediaForge tray app first.", "path", cfgPath)
			os.Exit(1)
		}
		// Never serve the setup wizard on Windows; config is owned by Setup/tray.
		firstRun = false
	}

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
	versionLine := version.String()
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
	detected := ffmpeg.DetectEncoders(cfg.FFmpegPath)
	logDetectedEncoders(detected)

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
		quality := cfg.DefaultQuality
		if quality == "" {
			quality = "good"
		}
		intakeWatcher.SmartShrinkQuality = quality
		intakeWatcher.OutputFormat = cfg.OutputFormat

		// Build the metadata lookup chain from configured API keys.
		// Any client with an empty key is left nil; the Orchestrator skips nil sources.
		var tvdbClient *intake.TVDBClient
		var tmdbClient *intake.TMDBClient
		var omdbClient *intake.OMDbClient
		if cfg.APIs.TVDBKey != "" {
			tvdbClient = intake.NewTVDBClient(cfg.APIs.TVDBKey, nil)
		}
		if cfg.APIs.TMDBKey != "" {
			tmdbClient = intake.NewTMDBClient(cfg.APIs.TMDBKey, nil)
		}
		if cfg.APIs.OMDbKey != "" {
			omdbClient = intake.NewOMDbClient(cfg.APIs.OMDbKey, nil)
		}
		if tvdbClient != nil || tmdbClient != nil || omdbClient != nil {
			intakeWatcher.Orchestrator = intake.NewOrchestrator(tvdbClient, tmdbClient, omdbClient)
			logger.Info("Intake: metadata lookup configured",
				"tvdb", tvdbClient != nil, "tmdb", tmdbClient != nil, "omdb", omdbClient != nil)
		} else {
			logger.Info("Intake: no API keys configured — metadata lookup disabled; set tvdb_key/tmdb_key in config")
		}

		// Wire the LLM verification client if a backend is configured.
		if cfg.LLM.Backend != "" {
			intakeWatcher.LLMClient = intake.NewLLMClient(cfg.LLM, nil)
			logger.Info("Intake: LLM verification configured", "backend", cfg.LLM.Backend, "model", cfg.LLM.Model)
		}

		handler.SetIntakeWatcher(intakeWatcher)
		intakeWatcherMu.Unlock()

		// Only start the folder watcher when intake is enabled; the watcher is
		// always created so that manual Full Pipeline runs (ProcessFile) work
		// regardless of whether automatic folder watching is active.
		if cfg.Intake.Enabled {
			go intakeWatcher.Start(context.Background())
		} else {
			logger.Info("Intake pipeline disabled (enable in Settings to activate folder watching)")
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
	logger.Info("MediaForge started", "version", version.String(), "encoder", best.Name, "workers", cfg.Workers, "port", *port)
	go browser.WarmCountCache(context.Background(), time.Duration(cfg.Intake.CacheTimeoutSeconds)*time.Second)
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
