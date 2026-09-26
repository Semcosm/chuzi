#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef PayloadDir
  #define PayloadDir "."
#endif
#ifndef OutputDir
  #define OutputDir "."
#endif

#define MyAppName "Chuzi"
#define MyAppPublisher "Semcosm"
#define MyAppExeName "Chuzi.Native.Windows.exe"
#define MyAppId "{{8E0C5D6C-4B1E-4D42-9D7C-7A7F2A5DF1A2}}"

[Setup]
AppId={#MyAppId}
AppName={#MyAppName}
AppVersion={#AppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\Chuzi
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutputDir}
OutputBaseFilename=ChuziSetup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExeName}
SetupLogging=yes
CloseApplications=yes
RestartApplications=no

[Files]
Source: "{#PayloadDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Dirs]
; Core is launched by the signed-in user, while the installer runs elevated.
; Create the shared data root with write access before the first launch.
Name: "{commonappdata}\chuzi"; Permissions: users-modify
Name: "{commonappdata}\chuzi\data"; Permissions: users-modify

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional shortcuts:"

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "Launch {#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{app}\CorePayload\chuzi-launcher.exe"; Parameters: "-root ""{commonappdata}\chuzi\data"" -source-root ""{app}\CorePayload"" -manifest ""{app}\CorePayload\release-manifest.json"" -command core-stop"; Flags: runhidden waituntilterminated skipifdoesntexist; RunOnceId: StopChuziCore

[UninstallDelete]
Type: filesandordirs; Name: "{app}"
Type: filesandordirs; Name: "{commonappdata}\chuzi"; Check: ShouldDeleteUserData

[Code]
var
  KeepUserDataValue: Boolean;
  KeepUserDataPrompted: Boolean;

function KeepUserData(): Boolean;
begin
  if not KeepUserDataPrompted then
  begin
    KeepUserDataValue := MsgBox(
      '是否保留 Chuzi 用户数据？选择“否”将删除 %ProgramData%\chuzi。',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON1) = IDYES;
    KeepUserDataPrompted := True;
  end;
  Result := KeepUserDataValue;
end;

function ShouldDeleteUserData(): Boolean;
begin
  Result := not KeepUserData();
end;

function InitializeUninstall(): Boolean;
begin
  KeepUserData();
  Result := True;
end;
