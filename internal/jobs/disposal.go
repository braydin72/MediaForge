package jobs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/util"
)

// IsManagedLocal reports whether path lives inside the configured staging
// folder — i.e. a location MediaForge itself moved the file into and fully
// owns (the normal intake AVC path), as opposed to a path browsed to
// directly for Manual Add, which may live on a network/library share.
// Comparison is case-insensitive on Windows to match samePath's convention
// (worker.go).
func IsManagedLocal(path string, cfg *config.Config) bool {
	if cfg == nil || cfg.Intake.StagingFolder == "" || path == "" {
		return false
	}
	staging := filepath.Clean(cfg.Intake.StagingFolder)
	p := filepath.Clean(path)

	if runtime.GOOS == "windows" {
		staging = strings.ToLower(staging)
		p = strings.ToLower(p)
	}

	if p == staging {
		return true
	}
	return strings.HasPrefix(p, staging+string(filepath.Separator))
}

// DisposeSource applies the user's file-disposal decision to a job's source
// file after it has stopped running (cancelled, skipped, or stopped). Only
// ever called from the API layer, once the disposal prompt's result is
// known.
//
//   - IsNetworkCopy jobs always have their local staging copy deleted,
//     regardless of deleteFile — the original network/library file was
//     never touched (see InputPath vs OriginalNetworkPath), so there is
//     nothing else to preserve.
//   - Otherwise, deleteFile=true removes the source outright.
//   - Otherwise (deleteFile=false), the source is moved back to the intake
//     watch folder, mirroring the reverse of the original watcher move.
func DisposeSource(job *Job, cfg *config.Config, deleteFile bool) error {
	if job == nil || job.InputPath == "" {
		return nil
	}
	if _, err := os.Stat(job.InputPath); os.IsNotExist(err) {
		return nil
	}

	if job.IsNetworkCopy {
		return os.Remove(job.InputPath)
	}

	if deleteFile {
		return os.Remove(job.InputPath)
	}

	dst := filepath.Join(cfg.Intake.WatchFolder, filepath.Base(job.InputPath))
	return util.SafeMove(job.InputPath, dst)
}
