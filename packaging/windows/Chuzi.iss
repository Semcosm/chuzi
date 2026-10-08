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
; Stop a running Core before launching the freshly installed payload. Core is
; installed under ProgramData, so replacing the UI files alone is insufficient
; during upgrades unless this old process is stopped first.
Filename: "{app}\CorePayload\chuzi-launcher.exe"; Parameters: "-root ""{commonappdata}\chuzi\data"" -source-root ""{app}\CorePayload"" -manifest ""{app}\CorePayload\release-manifest.json"" -command core-stop"; Flags: runhidden waituntilterminated skipifdoesntexist runascurrentuser
Filename: "{app}\{#MyAppExeName}"; Description: "Launch {#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{app}\CorePayload\chuzi-launcher.exe"; Parameters: "-root ""{commonappdata}\chuzi\data"" -source-root ""{app}\CorePayload"" -manifest ""{app}\CorePayload\release-manifest.json"" -command core-stop"; Flags: runhidden waituntilterminated skipifdoesntexist; RunOnceId: StopChuziCore

[UninstallDelete]
Type: filesandordirs; Name: "{app}"
Type: filesandordirs; Name: "{commonappdata}\chuzi"; Check: ShouldDeleteUserData

[Code]
var
  UninstallContext: Boolean;
  KeepUserDataValue: Boolean;
  KeepUserDataPrompted: Boolean;

function KeepUserData(): Boolean;
begin
  if UninstallContext and not KeepUserDataPrompted then
  begin
    KeepUserDataValue := SuppressibleMsgBox(
      '是否保留 Chuzi 用户数据？选择“否”将删除 %ProgramData%\chuzi。',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON1, IDYES) = IDYES;
    KeepUserDataPrompted := True;
  end;
  Result := KeepUserDataValue;
end;

function ShouldDeleteUserData(): Boolean;
begin
  { Inno may evaluate uninstall-delete checks while building the install
    transaction. Never prompt or delete user data outside uninstall mode. }
  Result := UninstallContext and not KeepUserData();
end;

function InitializeUninstall(): Boolean;
begin
  UninstallContext := True;
  KeepUserData();
  Result := True;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ExistingLauncher: String;
  ExistingManifest: String;
  DataRoot: String;
  Parameters: String;
  ResultCode: Integer;
begin
  NeedsRestart := False;
  Result := '';
  ExistingLauncher := ExpandConstant('{app}\CorePayload\chuzi-launcher.exe');
  ExistingManifest := ExpandConstant('{app}\CorePayload\release-manifest.json');
  DataRoot := ExpandConstant('{commonappdata}\chuzi\data');
  if (not FileExists(ExistingLauncher)) or (not FileExists(ExistingManifest)) then
    Exit;
  Parameters := '-root "' + DataRoot + '" -source-root "' + ExpandConstant('{app}\CorePayload') +
    '" -manifest "' + ExistingManifest + '" -command core-stop';
  { Old launchers may not have a PID file. Let the new launcher perform the
    Windows orphan-process fallback after its files have been copied. }
  Exec(ExistingLauncher, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Launcher: String;
  Manifest: String;
  DataRoot: String;
  Parameters: String;
  ResultCode: Integer;
begin
  if CurStep <> ssPostInstall then
    Exit;
  Launcher := ExpandConstant('{app}\CorePayload\chuzi-launcher.exe');
  Manifest := ExpandConstant('{app}\CorePayload\release-manifest.json');
  DataRoot := ExpandConstant('{commonappdata}\chuzi\data');
  if (not FileExists(Launcher)) or (not FileExists(Manifest)) then
  begin
    MsgBox('安装包缺少 Core 文件，无法完成安装。请重新下载最新安装包。', mbError, MB_OK);
    Abort;
  end;
  Parameters := '-root "' + DataRoot + '" -source-root "' + ExpandConstant('{app}\CorePayload') +
    '" -manifest "' + Manifest + '" -command core-stop';
  if (not ExecAsOriginalUser(Launcher, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode)) or
     (ResultCode <> 0) then
  begin
    MsgBox('无法停止旧版 Core，安装已停止。请关闭 Chuzi 后重试。', mbError, MB_OK);
    Abort;
  end;
  Parameters := '-root "' + DataRoot + '" -source-root "' + ExpandConstant('{app}\CorePayload') +
    '" -manifest "' + Manifest + '" -command component-install -item service';
  if (not ExecAsOriginalUser(Launcher, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode)) or
     (ResultCode <> 0) then
  begin
    MsgBox('Core 文件安装失败，已停止安装。请关闭 Chuzi 后重试。', mbError, MB_OK);
    Abort;
  end;
end;
