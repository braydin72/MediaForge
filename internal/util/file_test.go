package util

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// exdevErr builds an *os.LinkError wrapping EXDEV, matching what os.Rename
// returns when src and dst are on different filesystems.
func exdevErr(src, dst string) error {
	return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
}

func TestSafeMove(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, dir string)
	}{
		{
			name: "same-device success via os.Rename",
			run: func(t *testing.T, dir string) {
				src := filepath.Join(dir, "src.txt")
				dst := filepath.Join(dir, "dst.txt")
				if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
					t.Fatal(err)
				}

				if err := SafeMove(src, dst); err != nil {
					t.Fatalf("SafeMove: %v", err)
				}

				if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
					t.Error("src should not exist after successful move")
				}
				got, err := os.ReadFile(dst)
				if err != nil {
					t.Fatalf("reading dst: %v", err)
				}
				if string(got) != "hello" {
					t.Errorf("dst content = %q, want %q", got, "hello")
				}
			},
		},
		{
			name: "EXDEV fallback success",
			run: func(t *testing.T, dir string) {
				src := filepath.Join(dir, "src.txt")
				dst := filepath.Join(dir, "dst.txt")
				if err := os.WriteFile(src, []byte("world"), 0o644); err != nil {
					t.Fatal(err)
				}

				// Force first rename to return EXDEV; subsequent calls (tmp→dst) use real os.Rename.
				calls := 0
				orig := renameFn
				t.Cleanup(func() { renameFn = orig })
				renameFn = func(oldpath, newpath string) error {
					calls++
					if calls == 1 {
						return exdevErr(oldpath, newpath)
					}
					return os.Rename(oldpath, newpath)
				}

				if err := SafeMove(src, dst); err != nil {
					t.Fatalf("SafeMove: %v", err)
				}

				if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
					t.Error("src should not exist after successful move")
				}
				if _, err := os.Stat(dst + ".mediaforge.tmp"); !errors.Is(err, os.ErrNotExist) {
					t.Error("tmp file should be cleaned up after success")
				}
				got, err := os.ReadFile(dst)
				if err != nil {
					t.Fatalf("reading dst: %v", err)
				}
				if string(got) != "world" {
					t.Errorf("dst content = %q, want %q", got, "world")
				}
			},
		},
		{
			name: "EXDEV copy failure cleans up temp and leaves src intact",
			run: func(t *testing.T, dir string) {
				src := filepath.Join(dir, "src.txt")
				dst := filepath.Join(dir, "dst.txt")
				if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
					t.Fatal(err)
				}

				// Place a directory at tmpDst so CopyFile's os.Create fails.
				tmpDst := dst + ".mediaforge.tmp"
				if err := os.Mkdir(tmpDst, 0o755); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(tmpDst)

				orig := renameFn
				t.Cleanup(func() { renameFn = orig })
				renameFn = func(oldpath, newpath string) error {
					return exdevErr(oldpath, newpath)
				}

				err := SafeMove(src, dst)
				if err == nil {
					t.Fatal("expected error, got nil")
				}

				got, readErr := os.ReadFile(src)
				if readErr != nil {
					t.Fatalf("src missing after failed move: %v", readErr)
				}
				if string(got) != "data" {
					t.Errorf("src content changed: %q", got)
				}
			},
		},
		{
			name: "failed final rename cleans up temp and leaves src intact",
			run: func(t *testing.T, dir string) {
				src := filepath.Join(dir, "src.txt")
				dst := filepath.Join(dir, "dst.txt")
				if err := os.WriteFile(src, []byte("data2"), 0o644); err != nil {
					t.Fatal(err)
				}

				badRenameErr := errors.New("rename failed")
				calls := 0
				orig := renameFn
				t.Cleanup(func() { renameFn = orig })
				renameFn = func(oldpath, newpath string) error {
					calls++
					if calls == 1 {
						// Trigger EXDEV fallback path.
						return exdevErr(oldpath, newpath)
					}
					// Second call is renameFn(tmp, dst) inside SafeMove — fail it.
					return badRenameErr
				}

				err := SafeMove(src, dst)
				if !errors.Is(err, badRenameErr) {
					t.Fatalf("expected badRenameErr, got %v", err)
				}

				got, readErr := os.ReadFile(src)
				if readErr != nil {
					t.Fatalf("src missing after failed move: %v", readErr)
				}
				if string(got) != "data2" {
					t.Errorf("src content changed: %q", got)
				}

				if _, statErr := os.Stat(dst + ".mediaforge.tmp"); !errors.Is(statErr, os.ErrNotExist) {
					t.Error("tmp file should have been removed on final rename failure")
				}
			},
		},
		{
			name: "creates destination directory",
			run: func(t *testing.T, dir string) {
				src := filepath.Join(dir, "src.txt")
				dst := filepath.Join(dir, "a", "b", "c", "dst.txt")
				if err := os.WriteFile(src, []byte("mkdir"), 0o644); err != nil {
					t.Fatal(err)
				}

				if err := SafeMove(src, dst); err != nil {
					t.Fatalf("SafeMove: %v", err)
				}

				got, err := os.ReadFile(dst)
				if err != nil {
					t.Fatalf("reading dst: %v", err)
				}
				if string(got) != "mkdir" {
					t.Errorf("dst content = %q, want %q", got, "mkdir")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.run(t, dir)
		})
	}
}
