//go:build windows

package main

import "testing"

func TestDriveOf(t *testing.T) {
	if d, ok := driveOf(`M:\Movies`); !ok || d != "M" {
		t.Fatalf("driveOf(M:\\Movies) = %q,%v", d, ok)
	}
	if _, ok := driveOf(`\\server\share`); ok {
		t.Fatal("UNC should not report a drive letter")
	}
	if _, ok := driveOf(`relative\path`); ok {
		t.Fatal("relative path should not report a drive letter")
	}
}

func TestLocalPathClassification(t *testing.T) {
	// C: is virtually always a local fixed drive on the test host.
	if !isLocalPath(`C:\Users\test\Movies`) {
		t.Fatal("C:\\ path should be local")
	}
	if isLocalPath(`\\server\share\Movies`) {
		t.Fatal("UNC path should not be local")
	}
	if isLocalPath("") {
		t.Fatal("empty path should not be local")
	}
}

func TestConvertLocalDriveFails(t *testing.T) {
	// C: has no network mapping → convert must error.
	if unc, err := convertMappedDriveToUNC("C"); err == nil {
		t.Fatalf("expected error for local drive C:, got UNC %q", unc)
	}
}

func TestValidateLibraryPath(t *testing.T) {
	if err := validateLibraryPath("Movies", `\\server\share\Movies`); err != nil {
		t.Fatalf("UNC path should validate: %v", err)
	}
	if err := validateLibraryPath("Movies", `C:\Users\test\Movies`); err != nil {
		t.Fatalf("local path should validate: %v", err)
	}
}
