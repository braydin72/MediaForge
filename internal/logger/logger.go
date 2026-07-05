package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Log is the global logger instance
var Log *slog.Logger

// output is the underlying writer the logger writes to (stdout, or stdout+file).
// Banner writes here directly so it bypasses the level filter.
var output io.Writer = os.Stdout

// level is the dynamic log level, changeable at runtime via SetLevel.
// Uses slog.LevelVar which is backed by atomic.Int64 — safe for concurrent use.
var level slog.LevelVar

// Init initializes the global logger writing only to stdout.
func Init(levelStr string) {
	SetLevel(levelStr)
	output = os.Stdout
	Log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: &level,
	}))
}

// InitWithFile initializes the global logger writing to both stdout and a session
// log file under logDir/logs/mediaforge.log. Up to 2 previous session files are
// kept as .1.log and .2.log — the oldest is deleted at each startup.
// Errors creating the log file are non-fatal; logging falls back to stdout only.
func InitWithFile(levelStr, logDir string) {
	SetLevel(levelStr)

	logsDir := filepath.Join(logDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		Init(levelStr)
		return
	}

	current := filepath.Join(logsDir, "mediaforge.log")
	backup1 := filepath.Join(logsDir, "mediaforge.1.log")
	backup2 := filepath.Join(logsDir, "mediaforge.2.log")

	// Rotate: drop oldest, shift remaining.
	_ = os.Remove(backup2)
	_ = os.Rename(backup1, backup2)
	_ = os.Rename(current, backup1)

	f, err := os.OpenFile(current, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		Init(levelStr)
		fmt.Fprintf(os.Stderr, "logger: could not open log file %s: %v\n", current, err)
		return
	}

	w := io.MultiWriter(os.Stdout, f)
	output = w
	Log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: &level,
	}))
}

// Banner writes a message directly to the log output (stdout + file), bypassing
// the level filter so it is always recorded regardless of the configured log
// level. Use for startup banners and the encoder summary.
func Banner(msg string) {
	fmt.Fprintln(output, msg)
}

// SetLevel changes the log level at runtime. Valid values: debug, info, warn, error.
// Invalid values fall back to info.
func SetLevel(levelStr string) {
	var lvl slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	level.Set(lvl)
}

// Debug logs a debug message
func Debug(msg string, args ...any) {
	if Log != nil {
		Log.Debug(msg, args...)
	}
}

// Info logs an info message
func Info(msg string, args ...any) {
	if Log != nil {
		Log.Info(msg, args...)
	}
}

// Warn logs a warning message
func Warn(msg string, args ...any) {
	if Log != nil {
		Log.Warn(msg, args...)
	}
}

// Error logs an error message
func Error(msg string, args ...any) {
	if Log != nil {
		Log.Error(msg, args...)
	}
}
