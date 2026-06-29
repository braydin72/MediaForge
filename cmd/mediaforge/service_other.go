//go:build !windows

package main

// handleServiceCLI is a no-op on non-Windows platforms.
func handleServiceCLI(install, uninstall, service bool, configPath string, port int) bool {
	return false
}
