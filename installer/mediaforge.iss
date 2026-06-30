; MediaForge Windows Installer
; Requires Inno Setup 6.x — https://jrsoftware.org/isinfo.php

#define MyAppName "MediaForge"
#define MyAppVersion "1.0.0"
#define MyAppPublisher "braydin72"
#define MyAppURL "https://github.com/braydin72/mediaforge"
#define MyAppExeName "mediaforge.exe"
#define MyTrayExeName "mediaforge-tray.exe"
#define MyServiceName "MediaForge"

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

[Tasks]
Name: "startupservice"; Description: "Start MediaForge automatically at Windows startup"; GroupDescription: "Startup options:"; Flags: checkedonce
Name: "traystartup"; Description: "Launch MediaForge tray icon at login"; GroupDescription: "Startup options:"; Flags: checkedonce

[Dirs]
Name: "{commonappdata}\MediaForge"; Permissions: users-modify
Name: "{commonappdata}\MediaForge\config"; Permissions: users-modify
Name: "{commonappdata}\MediaForge\config\logs"; Permissions: users-modify
Name: "{commonappdata}\MediaForge\incoming"; Permissions: users-modify
Name: "{commonappdata}\MediaForge\staging"; Permissions: users-modify

[Files]
Source: "..\mediaforge.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\mediaforge-tray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\web\templates\logo.png"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\MediaForge"; Filename: "{app}\{#MyTrayExeName}"
Name: "{group}\Open MediaForge Web UI"; Filename: "http://127.0.0.1:8080"
Name: "{group}\Uninstall MediaForge"; Filename: "{uninstallexe}"
Name: "{userstartup}\MediaForge"; Filename: "{app}\{#MyTrayExeName}"; Tasks: traystartup

[Run]
; Install and start the Windows service
Filename: "{app}\{#MyAppExeName}"; Parameters: "--install"; Flags: runhidden waituntilterminated; StatusMsg: "Installing MediaForge service..."
Filename: "sc.exe"; Parameters: "start ""{#MyServiceName}"""; Flags: runhidden waituntilterminated; Tasks: startupservice; StatusMsg: "Starting MediaForge service..."
; Launch tray app after install if requested
Filename: "{app}\{#MyTrayExeName}"; Description: "Launch MediaForge tray icon now"; Flags: postinstall nowait skipifsilent

[UninstallRun]
; Stop and remove the service before files are deleted
Filename: "sc.exe"; Parameters: "stop ""{#MyServiceName}"""; Flags: runhidden waituntilterminated; RunOnceId: "StopService"
Filename: "{app}\{#MyAppExeName}"; Parameters: "--uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "UninstallService"

[Code]
function InitializeSetup(): Boolean;
begin
  Result := True;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
  begin
    // Give the service a moment to fully register before any start attempt
    Sleep(1000);
  end;
end;
