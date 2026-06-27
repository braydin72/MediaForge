package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type IntakeLibraryConfig struct {
	Movies  string `yaml:"movies"`
	TVShows string `yaml:"tv_shows"`
}

type IntakeStabilityConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
	PassesRequired  int `yaml:"passes_required"`
}

type IntakeNamingConfig struct {
	MovieFolder string `yaml:"movie_folder"`
	MovieFile   string `yaml:"movie_file"`
	ShowFolder  string `yaml:"show_folder"`
	EpisodeFile string `yaml:"episode_file"`
}

type IntakeConfig struct {
	Enabled             bool                  `yaml:"enabled"`
	WatchFolder         string                `yaml:"watch_folder"`
	StagingFolder       string                `yaml:"staging_folder"`
	Library             IntakeLibraryConfig   `yaml:"library"`
	StabilityCheck      IntakeStabilityConfig `yaml:"stability_check"`
	ConfidenceThreshold float64               `yaml:"confidence_threshold"`
	ReviewThreshold     float64               `yaml:"review_threshold"`
	Naming              IntakeNamingConfig    `yaml:"naming"`
}

type APIsConfig struct {
	TMDBKey string `yaml:"tmdb_key"`
	TVDBKey string `yaml:"tvdb_key"`
	OMDbKey string `yaml:"omdb_key"`
}

type LLMConfig struct {
	Backend    string `yaml:"backend"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	OllamaHost string `yaml:"ollama_host"`
}

type PosterCacheConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type NotificationEventsConfig struct {
	EncodeComplete  bool `yaml:"encode_complete"`
	EncodeFailed    bool `yaml:"encode_failed"`
	ReviewQueueItem bool `yaml:"review_queue_item"`
	DailySummary    bool `yaml:"daily_summary"`
	WeeklySummary   bool `yaml:"weekly_summary"`
}

type EmailNotificationConfig struct {
	Enabled         bool   `yaml:"enabled"`
	SMTPHost        string `yaml:"smtp_host"`
	SMTPPort        int    `yaml:"smtp_port"`
	SMTPTLS         bool   `yaml:"smtp_tls"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	From            string `yaml:"from"`
	To              string `yaml:"to"`
	Mode            string `yaml:"mode"`             // "per_file" | "batched"
	IntervalMinutes int    `yaml:"interval_minutes"` // used when mode == "batched"
}

type NotificationsConfig struct {
	BaseURL string                   `yaml:"base_url"`
	Events  NotificationEventsConfig `yaml:"events"`
	Email   EmailNotificationConfig  `yaml:"email"`
}

type Config struct {
	// Intake is the ingest pipeline configuration
	Intake IntakeConfig `yaml:"intake"`

	// APIs holds metadata lookup service keys
	APIs APIsConfig `yaml:"apis"`

	// LLM is the optional AI verification backend config
	LLM LLMConfig `yaml:"llm"`

	// PosterCache controls artwork thumbnail caching
	PosterCache PosterCacheConfig `yaml:"poster_cache"`

	// Notifications holds all notification channel and event configuration
	Notifications NotificationsConfig `yaml:"notifications"`

	// MediaPath is the root directory to browse for media files
	MediaPath string `yaml:"media_path"`

	// TempPath is where temp files are written during transcoding.
	// If empty, defaults to os.TempDir().
	TempPath string `yaml:"temp_path"`

	// OriginalHandling determines what happens to original files after transcoding
	// Options: "replace" (rename original to .old), "keep" (keep original, new file replaces)
	OriginalHandling string `yaml:"original_handling"`

	// UseCompletedDir writes transcoded files into a "completed/" subdirectory
	// next to the source file instead of in the same directory.
	UseCompletedDir bool `yaml:"use_completed_dir"`

	// Workers is the number of concurrent transcode jobs (default 1)
	Workers int `yaml:"workers"`

	// FFmpegPath is the path to ffmpeg binary (default: "ffmpeg")
	FFmpegPath string `yaml:"ffmpeg_path"`

	// FFprobePath is the path to ffprobe binary (default: "ffprobe")
	FFprobePath string `yaml:"ffprobe_path"`

	// PushoverUserKey is the Pushover user key for notifications
	PushoverUserKey string `yaml:"pushover_user_key"`

	// PushoverAppToken is the Pushover application token for notifications
	PushoverAppToken string `yaml:"pushover_app_token"`

	// NotifyOnComplete triggers a Pushover notification when all jobs finish
	NotifyOnComplete bool `yaml:"notify_on_complete"`

	// QualityHEVC is the CRF value for HEVC encoding (lower = higher quality, default 26)
	QualityHEVC int `yaml:"quality_hevc"`

	// QualityAV1 is the CRF value for AV1 encoding (lower = higher quality, default 35)
	QualityAV1 int `yaml:"quality_av1"`

	// ScheduleEnabled enables time-based scheduling for transcoding
	ScheduleEnabled bool `yaml:"schedule_enabled"`

	// ScheduleStartHour is when transcoding is allowed to start (0-23, default 22 = 10 PM)
	ScheduleStartHour int `yaml:"schedule_start_hour"`

	// ScheduleEndHour is when transcoding must stop (0-23, default 6 = 6 AM)
	ScheduleEndHour int `yaml:"schedule_end_hour"`

	// LogLevel controls logging verbosity: debug, info, warn, error (default: info)
	LogLevel string `yaml:"log_level"`

	// KeepLargerFiles keeps transcoded files even if they're larger than the original
	// Useful for users who want codec consistency across their library
	KeepLargerFiles bool `yaml:"keep_larger_files"`

	// AllowSameCodec allows transcoding files that are already in the target codec
	// Useful for re-encoding at different bitrates or quality settings
	AllowSameCodec bool `yaml:"allow_same_codec"`

	// OutputFormat is the container format for transcoded files: "mkv" or "mp4"
	// MKV preserves all streams; MP4 transcodes audio to AAC and strips subtitles
	OutputFormat string `yaml:"output_format"`

	// TonemapHDR enables automatic HDR to SDR conversion (default: false)
	// When enabled, HDR content (HDR10, HLG) is tonemapped to SDR using CPU.
	// When disabled, HDR metadata is preserved for HDR-capable displays.
	TonemapHDR bool `yaml:"tonemap_hdr"`

	// TonemapAlgorithm is the tonemapping algorithm to use: "hable", "bt2390", "reinhard"
	// Default is "hable" (filmic, good for movies)
	TonemapAlgorithm string `yaml:"tonemap_algorithm"`

	// MaxConcurrentAnalyses limits how many SmartShrink VMAF analyses can run simultaneously.
	// VMAF analysis is CPU-intensive and cannot be hardware accelerated.
	// Default is 1 to avoid high CPU usage on media servers.
	// Range: 1-3
	MaxConcurrentAnalyses int `yaml:"max_concurrent_analyses"`

	// EncoderSpeed is the transcoding speed preset: "slowest", "slower", "slow", "medium", "fast"
	EncoderSpeed string `yaml:"encoder_speed"`

	// TranscodeMode selects encoding strategy: "smartshrink" (default) or "fixed_reduction"
	TranscodeMode string `yaml:"transcode_mode"`

	// TargetReductionPct is the target file-size reduction for fixed_reduction mode (1-99).
	// 40 means output should be ~60% of the original size.
	TargetReductionPct int `yaml:"target_reduction_pct"`

	// DefaultPreset is the encode preset used for AVC files from the intake pipeline.
	// Defaults to "compress-hevc".
	DefaultPreset string `yaml:"default_preset"`
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Intake: IntakeConfig{
			Enabled:       false,
			WatchFolder:   "/incoming",
			StagingFolder: "/staging",
			Library: IntakeLibraryConfig{
				Movies:  "/media/Movies",
				TVShows: "/media/TV Shows",
			},
			StabilityCheck: IntakeStabilityConfig{
				IntervalSeconds: 10,
				PassesRequired:  6,
			},
			ConfidenceThreshold: 0.85,
			ReviewThreshold:     0.60,
			Naming: IntakeNamingConfig{
				MovieFolder: "{title} ({year})",
				MovieFile:   "{title} ({year})",
				ShowFolder:  "{show} ({year})",
				EpisodeFile: "{show} - S{season:02d}E{episode:02d} - {episode_title}",
			},
		},
		LLM: LLMConfig{
			OllamaHost: "http://localhost:11434",
		},
		PosterCache: PosterCacheConfig{
			Enabled: true,
			Path:    "/config/poster_cache",
		},
		MediaPath:         "/media",
		TempPath:          "", // defaults to os.TempDir()
		OriginalHandling:  "replace",
		Workers:           1,
		FFmpegPath:        "ffmpeg",
		FFprobePath:       "ffprobe",
		QualityHEVC:       0, // 0 = use encoder-specific default
		QualityAV1:        0, // 0 = use encoder-specific default
		ScheduleEnabled:   false,
		ScheduleStartHour: 22, // 10 PM
		ScheduleEndHour:   6,  // 6 AM
		LogLevel:          "info",
		OutputFormat:      "mkv",
		TonemapHDR:            false,
		TonemapAlgorithm:      "hable",
		MaxConcurrentAnalyses: 1,
		EncoderSpeed:          "medium",
		TranscodeMode:         "smartshrink",
		TargetReductionPct:    40,
		DefaultPreset:         "compress-hevc",
		Notifications: NotificationsConfig{
			Events: NotificationEventsConfig{
				EncodeComplete:  false,
				EncodeFailed:    true,
				ReviewQueueItem: true,
				DailySummary:    false,
				WeeklySummary:   false,
			},
			Email: EmailNotificationConfig{
				SMTPPort:        587,
				SMTPTLS:         true,
				Mode:            "per_file",
				IntervalMinutes: 60,
			},
		},
	}
}

// Load reads config from a YAML file, applying defaults for missing values
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file - create one with defaults
			if saveErr := cfg.Save(path); saveErr != nil {
				fmt.Printf("Warning: Could not create config file: %v\n", saveErr)
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Apply defaults for empty values
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}

	// Normalize tool paths so forward slashes and redundant separators work on
	// all platforms. filepath.Clean("ffmpeg") == "ffmpeg" (no-op for bare names).
	cfg.FFmpegPath = filepath.Clean(cfg.FFmpegPath)
	cfg.FFprobePath = filepath.Clean(cfg.FFprobePath)
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	// Note: QualityHEVC/QualityAV1 of 0 means "use encoder-specific default"
	// The API handler will determine the actual default based on detected encoder

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	if cfg.OutputFormat == "" {
		cfg.OutputFormat = "mkv"
	}

	if cfg.EncoderSpeed == "" {
		cfg.EncoderSpeed = "medium"
	}
	if cfg.TranscodeMode == "" {
		cfg.TranscodeMode = "smartshrink"
	}
	if cfg.TargetReductionPct == 0 {
		cfg.TargetReductionPct = 40
	}

	// Validate tonemapping algorithm (use shared validation)
	cfg.TonemapAlgorithm = ValidateTonemapAlgorithm(cfg.TonemapAlgorithm)

	// Validate max concurrent analyses (1-3)
	if cfg.MaxConcurrentAnalyses < 1 {
		cfg.MaxConcurrentAnalyses = 1
	}
	if cfg.MaxConcurrentAnalyses > 3 {
		cfg.MaxConcurrentAnalyses = 3
	}

	if cfg.DefaultPreset == "" {
		cfg.DefaultPreset = "compress-hevc"
	}

	// Intake defaults
	if cfg.Intake.StabilityCheck.IntervalSeconds < 1 {
		cfg.Intake.StabilityCheck.IntervalSeconds = 10
	}
	if cfg.Intake.StabilityCheck.PassesRequired < 1 {
		cfg.Intake.StabilityCheck.PassesRequired = 6
	}
	if cfg.Intake.ConfidenceThreshold == 0 {
		cfg.Intake.ConfidenceThreshold = 0.85
	}
	if cfg.Intake.ReviewThreshold == 0 {
		cfg.Intake.ReviewThreshold = 0.60
	}
	if cfg.Intake.Naming.MovieFolder == "" {
		cfg.Intake.Naming.MovieFolder = "{title} ({year})"
	}
	if cfg.Intake.Naming.MovieFile == "" {
		cfg.Intake.Naming.MovieFile = "{title} ({year})"
	}
	if cfg.Intake.Naming.ShowFolder == "" {
		cfg.Intake.Naming.ShowFolder = "{show} ({year})"
	}
	if cfg.Intake.Naming.EpisodeFile == "" {
		cfg.Intake.Naming.EpisodeFile = "{show} - S{season:02d}E{episode:02d} - {episode_title}"
	}
	if cfg.LLM.OllamaHost == "" {
		cfg.LLM.OllamaHost = "http://localhost:11434"
	}

	// Notification defaults
	if cfg.Notifications.Email.SMTPPort == 0 {
		cfg.Notifications.Email.SMTPPort = 587
	}
	if cfg.Notifications.Email.Mode == "" {
		cfg.Notifications.Email.Mode = "per_file"
	}
	if cfg.Notifications.Email.IntervalMinutes < 1 {
		cfg.Notifications.Email.IntervalMinutes = 60
	}

	return cfg, nil
}

// Save writes the config to a YAML file
func (c *Config) Save(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// GetTempDir returns the directory for temp files.
// If TempPath is set, returns that; otherwise defaults to os.TempDir()
// to avoid writing temp files to slow or unreliable media filesystems (#103).
func (c *Config) GetTempDir() string {
	if c.TempPath != "" {
		return c.TempPath
	}
	return os.TempDir()
}
