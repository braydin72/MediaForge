# MediaForge first-run setup wizard (native Windows Forms).
# Invoked by cmd/tray (Windows only). Reads default field values from
# -DefaultsPath (JSON) and, on "Save & Launch", writes the collected values to
# -OutPath (JSON) and exits 0. On Cancel / close it exits 1 and writes nothing.
param(
    [Parameter(Mandatory = $true)][string]$DefaultsPath,
    [Parameter(Mandatory = $true)][string]$OutPath
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$d = Get-Content -Raw -Path $DefaultsPath | ConvertFrom-Json

# --- Helpers ---------------------------------------------------------------

function Get-UNCForDrive([string]$drive) {
    # $drive like "M:"
    $out = cmd /c "net use $drive" 2>$null
    foreach ($line in $out) {
        if ($line -match 'Remote name\s+(\\\\\S+)') { return $matches[1] }
    }
    return $null
}

function Browse-Folder {
    $dlg = New-Object System.Windows.Forms.FolderBrowserDialog
    if ($dlg.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { return $null }
    $p = $dlg.SelectedPath
    if ($p.StartsWith('\\')) { return $p }
    if ($p.Length -lt 2 -or $p[1] -ne ':') { return $p }
    $drive = $p.Substring(0, 2)
    $isNet = $false
    try {
        $isNet = ((Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$drive'").DriveType -eq 4)
    } catch { $isNet = $false }
    if (-not $isNet) { return $p }
    $res = [System.Windows.Forms.MessageBox]::Show(
        "This is a mapped drive. UNC paths (\\server\share) are recommended for network shares.`n`nConvert to UNC?`n`nYes = Convert    No = Keep as-is    Cancel = pick again",
        "Mapped Drive Detected",
        [System.Windows.Forms.MessageBoxButtons]::YesNoCancel,
        [System.Windows.Forms.MessageBoxIcon]::Warning)
    if ($res -eq [System.Windows.Forms.DialogResult]::Cancel) { return $null }
    if ($res -eq [System.Windows.Forms.DialogResult]::No) { return $p }
    $unc = Get-UNCForDrive $drive
    if ($unc) { return ($unc + $p.Substring(2)) }
    [System.Windows.Forms.MessageBox]::Show(
        "Could not determine the UNC path for $drive. You may proceed with the mapped drive at your own risk.",
        "Conversion Failed", 'OK', 'Warning') | Out-Null
    return $p
}

# --- Form scaffolding ------------------------------------------------------

$form = New-Object System.Windows.Forms.Form
$form.Text = "MediaForge - First Run Setup"
$form.ClientSize = New-Object System.Drawing.Size(690, 861)
$form.StartPosition = 'CenterScreen'
$form.FormBorderStyle = 'FixedDialog'
$form.MaximizeBox = $false
$form.MinimizeBox = $false

$panel = New-Object System.Windows.Forms.Panel
$panel.Location = New-Object System.Drawing.Point(0, 0)
$panel.Size = New-Object System.Drawing.Size(690, 812)
$panel.Anchor = 'Top,Bottom,Left,Right'
$panel.AutoScroll = $true
$form.Controls.Add($panel)

$script:y = 12

function Add-Section([string]$text) {
    $l = New-Object System.Windows.Forms.Label
    $l.Text = $text
    $l.Location = New-Object System.Drawing.Point(12, $script:y)
    $l.AutoSize = $true
    $l.Font = New-Object System.Drawing.Font("Segoe UI", 10, [System.Drawing.FontStyle]::Bold)
    $panel.Controls.Add($l)
    $script:y += 26
}

function Add-Field([string]$text) {
    $l = New-Object System.Windows.Forms.Label
    $l.Text = $text
    $l.Location = New-Object System.Drawing.Point(12, $script:y)
    $l.AutoSize = $true
    $panel.Controls.Add($l)
    $script:y += 20
}

function Add-Entry([string]$default, [string]$placeholder) {
    $t = New-Object System.Windows.Forms.TextBox
    $t.Location = New-Object System.Drawing.Point(12, $script:y)
    $t.Size = New-Object System.Drawing.Size(620, 23)
    $t.Text = [string]$default
    if ($placeholder) { $t.Add_GotFocus({}) ; $t.ForeColor = [System.Drawing.Color]::Black }
    $panel.Controls.Add($t)
    $script:y += 30
    return $t
}

function Add-PathRow([string]$labelText, [string]$default) {
    Add-Field $labelText
    $t = New-Object System.Windows.Forms.TextBox
    $t.Location = New-Object System.Drawing.Point(12, $script:y)
    $t.Size = New-Object System.Drawing.Size(530, 23)
    $t.Text = [string]$default
    $panel.Controls.Add($t)
    $b = New-Object System.Windows.Forms.Button
    $b.Text = "Browse..."
    $b.Location = New-Object System.Drawing.Point(550, ($script:y - 1))
    $b.Size = New-Object System.Drawing.Size(82, 25)
    $b.Add_Click({ $sel = Browse-Folder; if ($sel) { $t.Text = $sel } }.GetNewClosure())
    $panel.Controls.Add($b)
    $script:y += 32
    return $t
}

function Add-Combo([string[]]$items, [string]$default) {
    $c = New-Object System.Windows.Forms.ComboBox
    $c.DropDownStyle = 'DropDownList'
    $c.Location = New-Object System.Drawing.Point(12, $script:y)
    $c.Size = New-Object System.Drawing.Size(220, 23)
    foreach ($i in $items) { [void]$c.Items.Add($i) }
    $idx = $c.Items.IndexOf([string]$default)
    if ($idx -ge 0) { $c.SelectedIndex = $idx } elseif ($c.Items.Count -gt 0) { $c.SelectedIndex = 0 }
    $panel.Controls.Add($c)
    $script:y += 30
    return $c
}

# --- Fields ----------------------------------------------------------------

Add-Section "INTAKE PATHS"
$eWatch  = Add-PathRow "Watch Folder (required)"     $d.watch_folder
$eStage  = Add-PathRow "Staging Folder (required)"   $d.staging_folder
$eMovies = Add-PathRow "Movies Library (required)"   $d.movies
$eTV     = Add-PathRow "TV Shows Library (required)" $d.tv_shows

Add-Section "API KEYS (optional)"
Add-Field "TMDB API Key";  $eTmdb = Add-Entry $d.tmdb "(optional)"
Add-Field "TVDB API Key";  $eTvdb = Add-Entry $d.tvdb "(optional)"
Add-Field "OMDb API Key";  $eOmdb = Add-Entry $d.omdb "(optional)"

Add-Section "LLM (optional)"
Add-Field "LLM Backend"; $cLLM = Add-Combo @('disabled', 'ollama', 'anthropic', 'openai') $d.llm_backend
Add-Field "Ollama Host"; $eOllama = Add-Entry $d.ollama_host ""
Add-Field "Model Name";  $eModel = Add-Entry $d.model ""

Add-Section "NOTIFICATIONS (optional)"
Add-Field "Base URL"; $eBase = Add-Entry $d.base_url ""
$chkEmail = New-Object System.Windows.Forms.CheckBox
$chkEmail.Text = "Enable Email Notifications"
$chkEmail.Location = New-Object System.Drawing.Point(12, $script:y)
$chkEmail.AutoSize = $true
$chkEmail.Checked = [bool]$d.email_enabled
$panel.Controls.Add($chkEmail)
$script:y += 28
Add-Field "SMTP Host"; $eSmtpH = Add-Entry $d.smtp_host ""
Add-Field "SMTP Port"; $eSmtpP = Add-Entry $d.smtp_port ""
Add-Field "Email From"; $eFrom = Add-Entry $d.email_from ""
Add-Field "Email To"; $eTo = Add-Entry $d.email_to ""
$syncEmail = {
    $en = $chkEmail.Checked
    $eSmtpH.Enabled = $en; $eSmtpP.Enabled = $en; $eFrom.Enabled = $en; $eTo.Enabled = $en
}.GetNewClosure()
$chkEmail.Add_CheckedChanged($syncEmail)
& $syncEmail

Add-Section "ADVANCED (optional)"
Add-Field "FFmpeg Path"; $eFF = Add-Entry $d.ffmpeg ""
Add-Field "FFprobe Path"; $eFP = Add-Entry $d.ffprobe ""
Add-Field "Encoder Speed"; $cSpeed = Add-Combo @('slowest', 'slower', 'slow', 'medium', 'fast') $d.encoder_speed
Add-Field "Output Format"; $cFmt = Add-Combo @('preserve', 'mp4', 'mkv') $d.output_format
Add-Field "Quality Preset"; $cQual = Add-Combo @('excellent', 'good', 'acceptable') $d.quality_preset
Add-Field "Target Reduction %"; $eRed = Add-Entry $d.target_reduction ""

# --- Button bar ------------------------------------------------------------

$bar = New-Object System.Windows.Forms.Panel
$bar.Location = New-Object System.Drawing.Point(0, 814)
$bar.Size = New-Object System.Drawing.Size(690, 47)
$bar.Anchor = 'Bottom,Left,Right'
$form.Controls.Add($bar)

$btnCancel = New-Object System.Windows.Forms.Button
$btnCancel.Text = "Cancel"
$btnCancel.Size = New-Object System.Drawing.Size(100, 30)
$btnCancel.Location = New-Object System.Drawing.Point(430, 9)
$bar.Controls.Add($btnCancel)

$btnSave = New-Object System.Windows.Forms.Button
$btnSave.Text = "Save & Launch"
$btnSave.Size = New-Object System.Drawing.Size(140, 30)
$btnSave.Location = New-Object System.Drawing.Point(538, 9)
$bar.Controls.Add($btnSave)

$script:saved = $false

$btnCancel.Add_Click({ $form.Close() })

$btnSave.Add_Click({
    $obj = [ordered]@{
        watch_folder     = $eWatch.Text.Trim()
        staging_folder   = $eStage.Text.Trim()
        movies           = $eMovies.Text.Trim()
        tv_shows         = $eTV.Text.Trim()
        tmdb             = $eTmdb.Text.Trim()
        tvdb             = $eTvdb.Text.Trim()
        omdb             = $eOmdb.Text.Trim()
        llm_backend      = [string]$cLLM.SelectedItem
        ollama_host      = $eOllama.Text.Trim()
        model            = $eModel.Text.Trim()
        base_url         = $eBase.Text.Trim()
        email_enabled    = [bool]$chkEmail.Checked
        smtp_host        = $eSmtpH.Text.Trim()
        smtp_port        = $eSmtpP.Text.Trim()
        email_from       = $eFrom.Text.Trim()
        email_to         = $eTo.Text.Trim()
        ffmpeg           = $eFF.Text.Trim()
        ffprobe          = $eFP.Text.Trim()
        encoder_speed    = [string]$cSpeed.SelectedItem
        output_format    = [string]$cFmt.SelectedItem
        quality_preset   = [string]$cQual.SelectedItem
        target_reduction = $eRed.Text.Trim()
    }
    if (-not $obj.watch_folder -or -not $obj.staging_folder -or -not $obj.movies -or -not $obj.tv_shows) {
        [System.Windows.Forms.MessageBox]::Show(
            "Watch Folder, Staging Folder, Movies Library, and TV Shows Library are all required.",
            "Missing Required Fields", 'OK', 'Error') | Out-Null
        return
    }
    ($obj | ConvertTo-Json) | Set-Content -Path $OutPath -Encoding UTF8
    $script:saved = $true
    $form.Close()
})

[void]$form.ShowDialog()
if (-not $script:saved) { exit 1 }
exit 0
