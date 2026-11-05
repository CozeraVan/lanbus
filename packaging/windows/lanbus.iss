; Inno Setup script for lanbus (optional installer build).
; Compile with: iscc /DMyAppVersion=1.9.0 packaging\windows\lanbus.iss
#define MyAppName "lanbus"
#ifndef MyAppVersion
  #define MyAppVersion "1.9.0"
#endif
#define MyAppPublisher "lanbus contributors"
#define MyAppURL "https://github.com/CozeraVan/lanbus"
#define MyAppExeName "lanbus-gui.exe"
#define PayloadDir "..\..\dist\lanbus-" + MyAppVersion + "-windows-amd64"

[Setup]
AppId={{D8E6D7B4-3B5A-4D3A-9B7C-6E5F1A2B3C4D}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
DefaultDirName={autopf}\lanbus
DefaultGroupName=lanbus
OutputDir=..\..\dist
OutputBaseFilename=lanbus-{#MyAppVersion}-setup
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern
PrivilegesRequired=lowest
SetupIconFile=..\..\go\cmd\lanbus-gui\icon.ico
UninstallDisplayIcon={app}\bin\{#MyAppExeName}

[Files]
Source: "{#PayloadDir}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion

[Icons]
Name: "{group}\lanbus GUI"; Filename: "{app}\bin\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{group}\lanbus Hub (console)"; Filename: "{app}\start-hub.cmd"; WorkingDir: "{app}"
Name: "{userdesktop}\lanbus"; Filename: "{app}\bin\{#MyAppExeName}"; WorkingDir: "{app}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"

[Run]
Filename: "{app}\bin\{#MyAppExeName}"; Description: "Launch lanbus GUI"; Flags: nowait postinstall skipifsilent
