; MediaForge Windows Installer
; Requires Inno Setup 6.x — https://jrsoftware.org/isinfo.php
#define MyAppName "MediaForge"
; MyAppVersion may be overridden by the build via ISCC /DMyAppVersion=x.y.z
; (see .github/workflows/release.yml). The fallback below lets the script
; compile standalone.
#ifndef MyAppVersion
#define MyAppVersion "1.0.0-alpha4"
#endif
#define MyAppPublisher "braydin72"
#define MyAppURL "https://github.com/braydin72/mediaforge"
#define MyAppExeName "mediaforge.exe"
#define MyTrayExeName "mediaforge-tray.exe"
[Setup]
AppId={{8F3A2B1C-7D4E-4A9B-9C2D-1E5F6A7B8C9D}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
AppUpdatesURL={#MyAppURL}/releases
DefaultDirName={autopf}\MediaForge
; Force the "Select Destination Location" page to appear. Without this,
; DisableDirPage defaults to "auto", which suppresses the page for per-user
; {auto...} installs (PrivilegesRequired=lowest). "no" restores the choice
; while keeping the per-user, no-elevation model.
DisableDirPage=no
DefaultGroupName=MediaForge
DisableProgramGroupPage=yes
; Per-user install (no elevation). This keeps the install location and the HKCU
; autostart entry in the same user hive, so the "MediaForge" Run key always
; belongs to the user who installed — avoiding the admin-hive mismatch that a
; PrivilegesRequired=admin install could produce when a standard user installs
; with separate admin credentials. With lowest privileges {autopf} resolves to
; the per-user Programs folder (%LOCALAPPDATA%\Programs).
PrivilegesRequired=lowest
OutputDir=installer-output
OutputBaseFilename=MediaForge-Setup-{#MyAppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExeName}
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"
; No [Dirs] section: the installer does NOT pre-create %APPDATA%\MediaForge or
; its logs subfolder. The tray/setup app and mediaforge.exe (EnsureWindowsDirs)
; create those on first run, so user data lives entirely outside Program Files.
[Files]
Source: "..\mediaforge.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\mediaforge-tray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\web\templates\logo.png"; DestDir: "{app}"; Flags: ignoreversion
[Icons]
Name: "{group}\MediaForge"; Filename: "{app}\{#MyTrayExeName}"
Name: "{group}\Open MediaForge Web UI"; Filename: "http://127.0.0.1:8080"
Name: "{group}\Uninstall MediaForge"; Filename: "{uninstallexe}"
[Registry]
; Auto-launch the tray at login (user-level). The tray is responsible for
; starting mediaforge.exe, so this is the single autostart entry.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "MediaForge"; ValueData: """{app}\{#MyTrayExeName}"""; Flags: uninsdeletevalue
[Run]
; Launch the tray after install. The tray runs first-run setup if needed and
; then launches mediaforge.exe (hidden). We do NOT open a browser here — the
; tray handles that after configuration.
Filename: "{app}\{#MyTrayExeName}"; Parameters: "--first-run"; Flags: nowait skipifsilent; StatusMsg: "Starting MediaForge..."
[UninstallRun]
; Stop the tray and server so their files can be removed cleanly.
Filename: "taskkill.exe"; Parameters: "/F /IM {#MyTrayExeName}"; Flags: runhidden; RunOnceId: "KillTray"
Filename: "taskkill.exe"; Parameters: "/F /IM {#MyAppExeName}"; Flags: runhidden; RunOnceId: "KillServer"
[UninstallDelete]
; Remove the install dir if empty after installed files are deleted. User data
; in %APPDATA%\MediaForge (config, logs, database) is preserved unless the user
; opts to remove it during uninstall (see [Code] below).
Type: dirifempty; Name: "{app}"
[Code]
// On uninstall, offer to remove the user's application data (config, logs,
// cache, database) from %APPDATA%\MediaForge. Defaults to keeping it so an
// accidental uninstall/reinstall doesn't wipe the user's configuration.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  AppData: string;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    AppData := ExpandConstant('{userappdata}\MediaForge');
    if DirExists(AppData) then
    begin
      if MsgBox('Remove application data (config, logs, cache)?' + #13#10 + #13#10 +
                AppData, mbConfirmation, MB_YESNO) = IDYES then
      begin
        DelTree(AppData, True, True, True);
      end;
    end;
  end;
end;
