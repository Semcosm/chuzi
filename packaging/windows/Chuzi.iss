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
  CleanupReady: Boolean;

procedure ShowSetupMessage(Text: String; Typ: TMsgBoxType);
begin
  SuppressibleMsgBox(Text, Typ, MB_OK, IDOK);
end;

function ProductionPayloadRequired(): Boolean;
var
  Version: String;
begin
  Version := '{#AppVersion}';
  Result := (Length(Version) > 1) and (Version[1] = 'v');
end;

function RunServiceManagement(Operation: String; RuntimeRoot: String; DataRoot: String; InstallRoot: String): Boolean;
var
  ScriptPath: String;
  Parameters: String;
  ResultCode: Integer;
  ExecutionPolicy: String;
begin
  ScriptPath := AddBackslash(RuntimeRoot) + 'windows-service-management.ps1';
  if not FileExists(ScriptPath) then
  begin
    Result := False;
    Exit;
  end;
  ExecutionPolicy := 'Bypass';
  if ProductionPayloadRequired() then
    ExecutionPolicy := 'AllSigned';
  Parameters := '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy ' + ExecutionPolicy +
    ' -File "' + ScriptPath + '" -Operation ' + Operation +
    ' -RuntimeRoot "' + RuntimeRoot + '" -DataRoot "' + DataRoot +
    '" -InstallRoot "' + InstallRoot + '"';
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
    Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

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
  CleanupReady := True;
  KeepUserData();
  if FileExists(ExpandConstant('{app}\CorePayload\windows-service-management.ps1')) then
  begin
    CleanupReady := RunServiceManagement('UninstallPrepare',
      ExpandConstant('{app}\CorePayload'), ExpandConstant('{commonappdata}\chuzi\data'),
      ExpandConstant('{app}'));
    if not CleanupReady then
    begin
      ShowSetupMessage('无法停止 Chuzi 服务，卸载已停止。请等待会话排空后重试。', mbError);
      Result := False;
      Exit;
    end;
  end;
  Result := True;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ExistingLauncher: String;
  ExistingManifest: String;
  ExistingManagement: String;
  DataRoot: String;
begin
  NeedsRestart := False;
  Result := '';
  ExistingLauncher := ExpandConstant('{app}\CorePayload\chuzi-launcher.exe');
  ExistingManifest := ExpandConstant('{app}\CorePayload\release-manifest.json');
  ExistingManagement := ExpandConstant('{app}\CorePayload\windows-service-management.ps1');
  DataRoot := ExpandConstant('{commonappdata}\chuzi\data');
  if FileExists(ExistingManagement) then
  begin
    if not RunServiceManagement('UpgradePrepare', ExpandConstant('{app}\CorePayload'),
      DataRoot, ExpandConstant('{app}')) then
    begin
      Result := '无法停止 Chuzi 服务；请先排空会话后重试。';
      Exit;
    end;
    if not RunServiceManagement('Backup', ExpandConstant('{app}\CorePayload'),
      DataRoot, ExpandConstant('{app}')) then
    begin
      Result := '无法创建升级回滚点；安装已停止。';
      Exit;
    end;
  end;
  if (not FileExists(ExistingLauncher)) or (not FileExists(ExistingManifest)) then
    Exit;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Launcher: String;
  Manifest: String;
  DataRoot: String;
  Parameters: String;
  ResultCode: Integer;
  RuntimeRoot: String;
begin
  if CurStep <> ssPostInstall then
    Exit;
  Launcher := ExpandConstant('{app}\CorePayload\chuzi-launcher.exe');
  Manifest := ExpandConstant('{app}\CorePayload\release-manifest.json');
  DataRoot := ExpandConstant('{commonappdata}\chuzi\data');
  RuntimeRoot := ExpandConstant('{app}\CorePayload');
  if (not FileExists(Launcher)) or (not FileExists(Manifest)) then
  begin
    ShowSetupMessage('安装包缺少 Core 文件，无法完成安装。请重新下载最新安装包。', mbError);
    Abort;
  end;
  Parameters := '-root "' + DataRoot + '" -source-root "' + ExpandConstant('{app}\CorePayload') +
    '" -manifest "' + Manifest + '" -command component-install -item service';
  if (not Exec(Launcher, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode)) or
     (ResultCode <> 0) then
  begin
    ShowSetupMessage('Core 文件安装失败，已停止安装。请关闭 Chuzi 后重试。', mbError);
    Abort;
  end;
  if not RunServiceManagement('Install', RuntimeRoot, DataRoot, ExpandConstant('{app}')) then
  begin
    ShowSetupMessage('Chuzi 服务注册失败，安装已停止。请保留安装目录并重试 Repair。', mbError);
    Abort;
  end;
end;
