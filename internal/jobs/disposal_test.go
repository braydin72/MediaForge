package jobs_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/jobs"
)

// TestIsManagedLocal builds paths relative to a real staging dir via
// filepath.Join so the test is valid on both Windows and Linux (CI runs on
// Linux, where a Windows-style "C:\..." string is just an opaque filename,
// not a path with meaningful separators).
func TestIsManagedLocal(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "staging")
	cfg := &config.Config{Intake: config.IntakeConfig{StagingFolder: staging}}

	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(staging, "video.mkv"), true},
		{filepath.Join(staging, "sub", "video.mkv"), true},
		{filepath.Join(filepath.Dir(staging), "elsewhere", "video.mkv"), false},
		{staging + "-other" + string(filepath.Separator) + "video.mkv", false}, // prefix collision, not a real subpath
	}
	for _, c := range cases {
		if got := jobs.IsManagedLocal(c.path, cfg); got != c.want {
			t.Errorf("IsManagedLocal(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsManagedLocal_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive path comparison only applies on Windows")
	}
	cfg := &config.Config{Intake: config.IntakeConfig{StagingFolder: `C:\staging`}}

	if !jobs.IsManagedLocal(`C:\STAGING\video.mkv`, cfg) {
		t.Error("expected case-insensitive match on Windows")
	}
	if jobs.IsManagedLocal(`M:\Movies\video.mkv`, cfg) {
		t.Error("expected a different drive to not be managed-local")
	}
}

func TestDisposeSource(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	watch := filepath.Join(dir, "incoming")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Intake: config.IntakeConfig{StagingFolder: staging, WatchFolder: watch}}

	t.Run("delete", func(t *testing.T) {
		src := filepath.Join(staging, "delete-me.mkv")
		os.WriteFile(src, []byte("x"), 0o644)
		job := &jobs.Job{InputPath: src}

		if err := jobs.DisposeSource(job, cfg, true); err != nil {
			t.Fatalf("DisposeSource: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("expected source file to be deleted")
		}
	})

	t.Run("move back to watch folder", func(t *testing.T) {
		src := filepath.Join(staging, "move-me.mkv")
		os.WriteFile(src, []byte("x"), 0o644)
		job := &jobs.Job{InputPath: src}

		if err := jobs.DisposeSource(job, cfg, false); err != nil {
			t.Fatalf("DisposeSource: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("expected source file to be gone from staging")
		}
		dst := filepath.Join(watch, "move-me.mkv")
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("expected file moved to watch folder: %v", err)
		}
	})

	t.Run("network copy always deleted, deleteFile ignored", func(t *testing.T) {
		src := filepath.Join(staging, "net-copy.mkv")
		os.WriteFile(src, []byte("x"), 0o644)
		job := &jobs.Job{InputPath: src, IsNetworkCopy: true, OriginalNetworkPath: `M:\Movies\net-copy.mkv`}

		// deleteFile=false, but network copy always deletes the local copy.
		if err := jobs.DisposeSource(job, cfg, false); err != nil {
			t.Fatalf("DisposeSource: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("expected local network-copy staging file to be deleted regardless of deleteFile")
		}
	})

	t.Run("missing source is a no-op", func(t *testing.T) {
		job := &jobs.Job{InputPath: filepath.Join(staging, "does-not-exist.mkv")}
		if err := jobs.DisposeSource(job, cfg, true); err != nil {
			t.Errorf("expected no error for missing source, got %v", err)
		}
	})
}
