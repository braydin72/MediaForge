package intake

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// renameFn is the rename implementation used by SafeMove. Overridable in tests.
var renameFn = os.Rename

// SafeMove moves src to dst atomically when possible. On a cross-device error
// (EXDEV) it falls back to copy→rename→remove. The source is never removed
// unless the full operation succeeds; on any failure dst+".tmp" is cleaned up.
func SafeMove(src, dst string) error {
	err := renameFn(src, dst)
	if err == nil {
		return nil
	}
	if !isEXDEV(err) {
		return err
	}

	tmp := dst + ".tmp"

	if copyErr := copyFile(src, tmp); copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}

	if renameErr := renameFn(tmp, dst); renameErr != nil {
		_ = os.Remove(tmp)
		return renameErr
	}

	return os.Remove(src)
}

func isEXDEV(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return false
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(filepath.Clean(src))
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(filepath.Clean(dst))
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	return dstFile.Close()
}
