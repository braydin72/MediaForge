package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// renameFn is the rename implementation used by SafeMove. Overridable in tests.
var renameFn = os.Rename

// CopyFile copies a file from src to dst.
// Works across filesystems unlike os.Rename.
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	return dstFile.Close()
}

// SafeMove moves src to dst atomically using a write-to-temp-then-rename pattern.
// It tries os.Rename first; on cross-device error (EXDEV) it falls back to
// copy-then-rename so the destination is never visible in a partial state.
// Destination directory is created if it doesn't exist. The source file is
// never removed unless the full operation succeeds.
func SafeMove(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	tmpDst := dst + ".mediaforge.tmp"

	var usedCopy bool
	if err := renameFn(src, tmpDst); err != nil {
		if !isCrossDeviceError(err) {
			return fmt.Errorf("rename: %w", err)
		}
		if copyErr := CopyFile(src, tmpDst); copyErr != nil {
			os.Remove(tmpDst)
			return fmt.Errorf("cross-device copy: %w", copyErr)
		}
		usedCopy = true
	}

	if err := renameFn(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return fmt.Errorf("final rename: %w", err)
	}

	if usedCopy {
		return os.Remove(src)
	}
	return nil
}

// ProgressFunc reports bytes copied so far out of the known total. It may be
// called frequently during a copy; implementations should be cheap/non-blocking.
type ProgressFunc func(bytesCopied, totalBytes int64)

// progressReportInterval throttles how often progressReader invokes onProgress
// so copying doesn't call back on every small io.Copy buffer (typically 32KB).
const progressReportInterval = 250 * time.Millisecond

// progressReader wraps an io.Reader, calling onProgress periodically as bytes
// are read, plus a final call once the read completes (EOF or error).
type progressReader struct {
	r          io.Reader
	total      int64
	copied     int64
	onProgress ProgressFunc
	lastReport time.Time
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.copied += int64(n)
		now := time.Now()
		if p.onProgress != nil && (err != nil || now.Sub(p.lastReport) >= progressReportInterval) {
			p.lastReport = now
			p.onProgress(p.copied, p.total)
		}
	}
	return n, err
}

// copyFileWithProgress is CopyFile with periodic byte-progress reporting.
func copyFileWithProgress(src, dst string, onProgress ProgressFunc) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	var total int64
	if info, statErr := srcFile.Stat(); statErr == nil {
		total = info.Size()
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	reader := &progressReader{r: srcFile, total: total, onProgress: onProgress}
	if _, err := io.Copy(dstFile, reader); err != nil {
		dstFile.Close()
		return err
	}

	return dstFile.Close()
}

// SafeMoveWithProgress behaves exactly like SafeMove but reports byte-copy
// progress via onProgress when a cross-device copy is required. When the move
// completes via a same-device os.Rename (the common case), onProgress is
// called once with the file already at 100% since a rename has no measurable
// byte-copy phase. onProgress may be nil.
func SafeMoveWithProgress(src, dst string, onProgress ProgressFunc) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	tmpDst := dst + ".mediaforge.tmp"

	var usedCopy bool
	if err := renameFn(src, tmpDst); err != nil {
		if !isCrossDeviceError(err) {
			return fmt.Errorf("rename: %w", err)
		}
		if copyErr := copyFileWithProgress(src, tmpDst, onProgress); copyErr != nil {
			os.Remove(tmpDst)
			return fmt.Errorf("cross-device copy: %w", copyErr)
		}
		usedCopy = true
	} else if onProgress != nil {
		var total int64
		if info, statErr := os.Stat(tmpDst); statErr == nil {
			total = info.Size()
		}
		onProgress(total, total)
	}

	if err := renameFn(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return fmt.Errorf("final rename: %w", err)
	}

	if usedCopy {
		return os.Remove(src)
	}
	return nil
}

// isCrossDeviceError reports whether err is an EXDEV / cross-device-link error.
// On Windows, os.Rename across drives returns ERROR_NOT_SAME_DEVICE (0x11) which
// equals syscall.EXDEV (17). String fallbacks cover cases where the error is
// wrapped or surfaced via a network path without a recognized syscall code.
func isCrossDeviceError(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}
	if errors.Is(linkErr.Err, syscall.EXDEV) {
		return true
	}
	msg := linkErr.Err.Error()
	return strings.Contains(msg, "cannot move the file to a different disk drive") ||
		strings.Contains(msg, "invalid cross-device link")
}
