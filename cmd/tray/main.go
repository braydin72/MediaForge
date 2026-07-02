//go:build windows

// Package main is the MediaForge system tray companion. It ensures the config
// exists (running the first-run setup if not), launches the mediaforge.exe
// server as a hidden background process, and then hosts the tray icon/menu.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/getlantern/systray"
)

// hideConsole is the Windows CreateProcess flag (CREATE_NO_WINDOW) that starts
// mediaforge.exe without allocating a console window.
const hideConsole = 0x08000000

func main() {
	// 1-3. Ensure a config exists; run first-run setup if it doesn't.
	if !configExists() {
		setupConfig()
	}

	// 4-5. Launch the server hidden, then give it a moment to bind its port.
	launchMediaForge()
	time.Sleep(2 * time.Second)

	// 6-7. Host the tray until the user chooses Exit.
	systray.Run(onReady, onExit)
}

// onReady sets the icon/tooltip and hands off to buildTrayMenu.
func onReady() {
	if icon, err := loadIcon(); err == nil {
		systray.SetIcon(icon)
	}
	systray.SetTooltip("MediaForge")
	buildTrayMenu()
}

// onExit is invoked when systray shuts down. Nothing to clean up yet.
func onExit() {}

// configExists reports whether the MediaForge config file is present.
func configExists() bool {
	return fileExists(configPath())
}

// configPath returns the resolved path to mediaforge.yaml
// (%APPDATA%\MediaForge\mediaforge.yaml on Windows).
func configPath() string {
	return config.ResolveConfigPath("")
}

// launchMediaForge starts mediaforge.exe as a hidden, windowless background
// process. It looks for the binary alongside this executable first, falling
// back to the working directory / PATH.
func launchMediaForge() {
	cmd := exec.Command(mediaForgePath())
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: hideConsole}
	// Fire and forget; the tray polls the server API for status once it's up.
	_ = cmd.Start()
}

// mediaForgePath resolves the mediaforge.exe location, preferring the directory
// of the running tray executable so the installed layout works regardless of
// the current working directory.
func mediaForgePath() string {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "mediaforge.exe")
		if fileExists(candidate) {
			return candidate
		}
	}
	return "mediaforge.exe"
}

// buildTrayMenu constructs the tray menu items and their click handlers.
// TODO: not yet implemented — stub so the tray shows an icon with no menu.
func buildTrayMenu() {}

// setupConfig runs the first-run configuration flow (modal wizard) and blocks
// until the user has produced a valid config file.
// TODO: not yet implemented.
func setupConfig() {}

// fileExists reports whether path exists and is not a directory error.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
