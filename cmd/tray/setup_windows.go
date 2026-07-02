//go:build windows

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/braydin72/mediaforge/internal/config"
)

// setupWizardScript is the native Windows Forms wizard, run via PowerShell so
// the tray stays pure Go (no CGO / GUI toolkit dependency).
//
//go:embed setup_wizard.ps1
var setupWizardScript string

// wizardValues mirrors the JSON exchanged with setup_wizard.ps1. All fields are
// strings (as entered) except EmailEnabled; numeric fields are parsed in Go.
type wizardValues struct {
	WatchFolder     string `json:"watch_folder"`
	StagingFolder   string `json:"staging_folder"`
	Movies          string `json:"movies"`
	TVShows         string `json:"tv_shows"`
	TMDB            string `json:"tmdb"`
	TVDB            string `json:"tvdb"`
	OMDb            string `json:"omdb"`
	LLMBackend      string `json:"llm_backend"`
	OllamaHost      string `json:"ollama_host"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url"`
	EmailEnabled    bool   `json:"email_enabled"`
	SMTPHost        string `json:"smtp_host"`
	SMTPPort        string `json:"smtp_port"`
	EmailFrom       string `json:"email_from"`
	EmailTo         string `json:"email_to"`
	FFmpeg          string `json:"ffmpeg"`
	FFprobe         string `json:"ffprobe"`
	EncoderSpeed    string `json:"encoder_speed"`
	OutputFormat    string `json:"output_format"`
	QualityPreset   string `json:"quality_preset"`
	TargetReduction string `json:"target_reduction"`
}

// setupConfig runs the first-run configuration wizard and blocks until the user
// has produced a valid config file. Cancelling exits the process (nothing to
// run without a config). On a validation error the wizard is re-shown with the
// user's entries preserved.
func setupConfig() {
	defaults := defaultWizardValues()
	for {
		vals, ok := runSetupWizard(defaults)
		if !ok {
			// User cancelled or closed the window.
			os.Exit(0)
		}
		if err := applyAndSaveConfig(vals); err != nil {
			showMessage("MediaForge Setup — Error", err.Error())
			defaults = vals // preserve entries for the retry
			continue
		}
		return
	}
}

// defaultWizardValues seeds the wizard with sensible per-user defaults.
// Network library defaults are intentionally local (Documents): a reliable
// "detect the network share" default isn't possible, so we let the user browse
// to a UNC path instead.
func defaultWizardValues() wizardValues {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = `C:\Users\Default`
	}
	docs := filepath.Join(home, "Documents")
	return wizardValues{
		WatchFolder:     filepath.Join(home, "MediaForge", "Incoming"),
		StagingFolder:   filepath.Join(home, "MediaForge", "Transcode"),
		Movies:          filepath.Join(docs, "Movies"),
		TVShows:         filepath.Join(docs, "TV Shows"),
		LLMBackend:      "disabled",
		OllamaHost:      "http://localhost:11434",
		Model:           "mistral",
		BaseURL:         "http://localhost:8080",
		SMTPHost:        "smtp.gmail.com",
		SMTPPort:        "587",
		FFmpeg:          "ffmpeg",
		FFprobe:         "ffprobe",
		EncoderSpeed:    "medium",
		OutputFormat:    "preserve",
		QualityPreset:   "good",
		TargetReduction: "40",
	}
}

// runSetupWizard writes the embedded PowerShell script and the defaults to a
// temp dir, runs the modal WinForms dialog, and returns the collected values.
// The bool is false when the user cancelled/closed the window.
func runSetupWizard(defaults wizardValues) (wizardValues, bool) {
	tmp, err := os.MkdirTemp("", "mf-setup-")
	if err != nil {
		return wizardValues{}, false
	}
	defer os.RemoveAll(tmp)

	scriptPath := filepath.Join(tmp, "setup_wizard.ps1")
	defPath := filepath.Join(tmp, "defaults.json")
	outPath := filepath.Join(tmp, "result.json")

	defData, err := json.Marshal(defaults)
	if err != nil {
		return wizardValues{}, false
	}
	if err := os.WriteFile(scriptPath, []byte(setupWizardScript), 0644); err != nil {
		return wizardValues{}, false
	}
	if err := os.WriteFile(defPath, defData, 0644); err != nil {
		return wizardValues{}, false
	}

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", scriptPath, "-DefaultsPath", defPath, "-OutPath", outPath)
	if err := cmd.Run(); err != nil {
		// Non-zero exit means Cancel/close.
		return wizardValues{}, false
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		return wizardValues{}, false
	}
	// Windows PowerShell 5.1's `Set-Content -Encoding UTF8` prepends a UTF-8 BOM,
	// which encoding/json rejects ("invalid character 'ï'"). Strip it before decode.
	out = bytes.TrimPrefix(out, []byte{0xEF, 0xBB, 0xBF})
	var v wizardValues
	if err := json.Unmarshal(out, &v); err != nil {
		return wizardValues{}, false
	}
	return v, true
}

// applyAndSaveConfig validates the wizard input, builds a full Config from the
// package defaults, creates any local directories, and writes mediaforge.yaml.
func applyAndSaveConfig(v wizardValues) error {
	paths := []struct{ name, val string }{
		{"Watch Folder", v.WatchFolder},
		{"Staging Folder", v.StagingFolder},
		{"Movies Library", v.Movies},
		{"TV Shows Library", v.TVShows},
	}
	for _, p := range paths {
		if strings.TrimSpace(p.val) == "" {
			return fmt.Errorf("%s is required and cannot be empty.", p.name)
		}
		if err := validateLibraryPath(p.name, p.val); err != nil {
			return err
		}
	}

	cfg := config.DefaultConfig()

	cfg.Intake.Enabled = true
	cfg.Intake.WatchFolder = v.WatchFolder
	cfg.Intake.StagingFolder = v.StagingFolder
	cfg.Intake.Library.Movies = v.Movies
	cfg.Intake.Library.TVShows = v.TVShows

	cfg.APIs.TMDBKey = v.TMDB
	cfg.APIs.TVDBKey = v.TVDB
	cfg.APIs.OMDbKey = v.OMDb

	cfg.LLM.Backend = firstNonEmpty(v.LLMBackend, "disabled")
	cfg.LLM.Model = v.Model
	cfg.LLM.OllamaHost = firstNonEmpty(v.OllamaHost, "http://localhost:11434")

	cfg.PosterCache.Enabled = true
	cfg.PosterCache.Path = filepath.Join(v.StagingFolder, "poster_cache")

	cfg.Notifications.BaseURL = firstNonEmpty(v.BaseURL, "http://localhost:8080")
	cfg.Notifications.Email.Enabled = v.EmailEnabled
	cfg.Notifications.Email.SMTPHost = firstNonEmpty(v.SMTPHost, "smtp.gmail.com")
	if port, err := strconv.Atoi(strings.TrimSpace(v.SMTPPort)); err == nil && port > 0 {
		cfg.Notifications.Email.SMTPPort = port
	}
	cfg.Notifications.Email.Username = v.EmailFrom
	cfg.Notifications.Email.From = v.EmailFrom
	cfg.Notifications.Email.To = v.EmailTo

	cfg.MediaPath = filepath.Dir(v.Movies)
	cfg.TempPath = filepath.Join(v.StagingFolder, "tmp")
	cfg.AllowSameCodec = true
	cfg.FFmpegPath = firstNonEmpty(v.FFmpeg, "ffmpeg")
	cfg.FFprobePath = firstNonEmpty(v.FFprobe, "ffprobe")
	cfg.EncoderSpeed = firstNonEmpty(v.EncoderSpeed, "medium")
	cfg.OutputFormat = firstNonEmpty(v.OutputFormat, "preserve")
	cfg.DefaultQuality = firstNonEmpty(v.QualityPreset, "good")
	if r, err := strconv.Atoi(strings.TrimSpace(v.TargetReduction)); err == nil && r > 0 {
		cfg.TargetReductionPct = r
	}

	// Create local directories now; network (UNC) paths are validated at runtime.
	for _, dir := range []string{v.WatchFolder, v.StagingFolder, v.Movies, v.TVShows, cfg.TempPath, cfg.PosterCache.Path} {
		if isLocalPath(dir) {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("could not create local folder %s: %v", dir, err)
			}
		}
	}

	config.EnsureWindowsDirs()
	if err := cfg.Save(configPath()); err != nil {
		return fmt.Errorf("could not write config: %v", err)
	}
	return nil
}

// validateLibraryPath rejects network locations expressed as a mapped drive
// letter (M:\...), requiring UNC (\\server\share) instead. UNC and local paths
// are accepted.
func validateLibraryPath(name, p string) error {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, `\\`) {
		return nil // UNC — validated at runtime.
	}
	if drive, ok := driveOf(p); ok && isNetworkDrive(drive) {
		return fmt.Errorf(
			"%s uses mapped drive %s:. Please use UNC path format (\\\\server\\share) for network locations.",
			name, drive)
	}
	return nil
}

// isLocalPath reports whether p is a local filesystem path we can create
// (i.e. not empty, not UNC, and not a mapped network drive).
func isLocalPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, `\\`) {
		return false
	}
	if drive, ok := driveOf(p); ok {
		return !isNetworkDrive(drive)
	}
	return true
}

// driveOf returns the single drive letter (upper-case, no colon) for a path
// like "M:\Movies", and whether the path had a drive-letter prefix.
func driveOf(p string) (string, bool) {
	if len(p) >= 2 && p[1] == ':' {
		return strings.ToUpper(p[0:1]), true
	}
	return "", false
}

// isNetworkDrive reports whether the given drive letter maps to a network share.
func isNetworkDrive(driveLetter string) bool {
	_, err := convertMappedDriveToUNC(driveLetter)
	return err == nil
}

// convertMappedDriveToUNC resolves a mapped drive letter (e.g. "M" or "M:") to
// its UNC target (\\server\share) by parsing `net use`. It returns an error for
// local drives or when no mapping can be found.
func convertMappedDriveToUNC(driveLetter string) (string, error) {
	d := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(driveLetter), ":"))
	if len(d) != 1 {
		return "", fmt.Errorf("invalid drive letter %q", driveLetter)
	}
	out, err := exec.Command("net", "use", d+":").CombinedOutput()
	if err != nil {
		// `net use X:` errors for drives with no network mapping.
		return "", fmt.Errorf("no network mapping for %s: %v", d, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "remote name") {
			for _, f := range strings.Fields(line) {
				if strings.HasPrefix(f, `\\`) {
					return f, nil
				}
			}
		}
	}
	return "", fmt.Errorf("could not determine UNC path for %s:", d)
}

// showMessage displays a blocking native error dialog.
func showMessage(title, msg string) {
	script := fmt.Sprintf(
		`Add-Type -AssemblyName System.Windows.Forms; `+
			`[System.Windows.Forms.MessageBox]::Show(%s,%s,'OK','Error') | Out-Null`,
		psQuote(msg), psQuote(title))
	_ = exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", script).Run()
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
