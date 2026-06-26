package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

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
// It tries os.Rename first; on cross-device error it copies then renames so the
// destination is never visible in a partial state. Destination directory is
// created if it doesn't exist.
func SafeMove(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	tmpDst := dst + ".mediaforge.tmp"

	if err := os.Rename(src, tmpDst); err != nil {
		if !isCrossDeviceError(err) {
			return fmt.Errorf("rename: %w", err)
		}
		// Cross-device: copy then rename.
		if copyErr := CopyFile(src, tmpDst); copyErr != nil {
			os.Remove(tmpDst)
			return fmt.Errorf("cross-device copy: %w", copyErr)
		}
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return fmt.Errorf("final rename: %w", err)
	}
	return nil
}

// isCrossDeviceError reports whether err is an EXDEV / cross-device-link error.
func isCrossDeviceError(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}
	s := linkErr.Err.Error()
	return strings.Contains(s, "cross-device") ||
		strings.Contains(s, "different disk drive") ||
		strings.Contains(s, "cannot move")
}
