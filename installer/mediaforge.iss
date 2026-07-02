; MediaForge Windows Installer
; Requires Inno Setup 6.x — https://jrsoftware.org/isinfo.php
#define MyAppName "MediaForge"
#define MyAppVersion "1.0.0"
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
DefaultGroupName=MediaForge
DisableProgramGroupPage=yes
PrivilegesRequired=admin
OutputDir=installer-output
OutputBaseFilename=MediaForge-Setup-{#MyAppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExeName}
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
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
; in %APPDATA%\MediaForge (config, logs, database) is intentionally preserved.
Type: dirifempty; Name: "{app}"
