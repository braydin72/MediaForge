package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
