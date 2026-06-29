//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/braydin72/mediaforge/internal/winsvc"
)

// handleServiceCLI acts on the --install / --uninstall / --service flags.
// Returns true when one of these flags was active (so main should not continue
// with the normal server startup).
func handleServiceCLI(install, uninstall, service bool, configPath string, port int) bool {
	switch {
	case install:
		exePath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: could not determine executable path: %v\n", err)
			os.Exit(1)
		}
		if err := winsvc.InstallService(
			winsvc.ServiceName,
			winsvc.ServiceDisplayName,
			winsvc.ServiceDescription,
			exePath,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: install service: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Service %q installed successfully.\n", winsvc.ServiceName)
		return true

	case uninstall:
		if err := winsvc.UninstallService(winsvc.ServiceName); err != nil {
			fmt.Fprintf(os.Stderr, "error: uninstall service: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Service %q uninstalled successfully.\n", winsvc.ServiceName)
		return true

	case service:
		if err := winsvc.Run(configPath, port); err != nil {
			fmt.Fprintf(os.Stderr, "error: run as service: %v\n", err)
			os.Exit(1)
		}
		return true

	default:
		return false
	}
}
