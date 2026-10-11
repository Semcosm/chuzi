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
; Runtime and control files are read/execute for operators. The service owns
; mutable state; no Users-modify grant is applied to the whole ProgramData tree.
Name: "{commonappdata}\chuzi"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data\.chuzi"; Permissions: users-modify
Name: "{commonappdata}\chuzi\data\db"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data\profiles"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data\runtime"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data\journal"; Permissions: users-readexec
Name: "{commonappdata}\chuzi\data\secrets"; Permissions: users-readexec

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
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File ""{app}\CorePayload\windows-service-management.ps1"" -Operation Uninstall -RuntimeRoot ""{app}\CorePayload"" -DataRoot ""{commonappdata}\chuzi\data"" -InstallRoot ""{app}"""; Flags: runhidden waituntilterminated skipifdoesntexist; Check: InstalledManagementScript; RunOnceId: RemoveChuziServices
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

function ProductionPayloadRequired(): Boolean;
var
  Version: String;
begin
  { Release build scripts accept only v<major>.<minor>.<patch>. Treat any
    v-prefixed installer as production so malformed stable metadata fails
    closed in the real payload validator instead of falling back to the
    unsigned-development path. }
  Version := '{#AppVersion}';
  Result := False;
  if Length(Version) <= 1 then
    Exit;
  Result := Version[1] = 'v';
end;

function RunServiceManagementAt(ScriptPath: String; Operation: String; RuntimeRoot: String; DataRoot: String; InstallRoot: String): Boolean;
var
  Parameters: String;
  ResultCode: Integer;
begin
  if not FileExists(ScriptPath) then
  begin
    Result := False;
    Exit;
  end;
  Parameters := '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File "' +
    ScriptPath + '" -Operation ' + Operation + ' -RuntimeRoot "' + RuntimeRoot +
    '" -DataRoot "' + DataRoot + '" -InstallRoot "' + InstallRoot + '"';
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'), Parameters,
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

function RunServiceManagement(Operation: String; RuntimeRoot: String; DataRoot: String; InstallRoot: String): Boolean;
begin
  Result := RunServiceManagementAt(
    ExpandConstant('{app}\CorePayload\windows-service-management.ps1'),
    Operation, RuntimeRoot, DataRoot, InstallRoot);
end;

function InstalledManagementScript(): Boolean;
begin
  Result := FileExists(ExpandConstant('{app}\CorePayload\windows-service-management.ps1'));
end;

function ValidateInstalledPayload(PayloadRoot: String; DataRoot: String; Production: Boolean): Boolean;
var
  ScriptPath: String;
  Parameters: String;
  ResultCode: Integer;
begin
  ScriptPath := AddBackslash(PayloadRoot) + 'validate_windows_payload.ps1';
  if not FileExists(ScriptPath) then
  begin
    Result := False;
    Exit;
  end;
  Parameters := '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File "' +
    ScriptPath + '" -PayloadDir "' + PayloadRoot + '" -Offline';
  if Production then
    Parameters := Parameters + ' -Production -DataRoot "' + DataRoot + '"';
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'), Parameters,
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

function RollbackUpgrade(): Boolean;
var
  PreviousManagement: String;
begin
  if not FileExists(ExpandConstant('{commonappdata}\chuzi\data\.chuzi\installer-backups\upgrade-state.json')) then
  begin
    Result := True;
    Exit;
  end;
  Result := RunServiceManagement('Rollback', ExpandConstant('{app}\CorePayload'),
    ExpandConstant('{commonappdata}\chuzi\data'), ExpandConstant('{app}'));
  if not Result then
  begin
    { The new payload may have failed signature validation. Use the copy of
      the previously signed management entrypoint saved with the rollback. }
    PreviousManagement := ExpandConstant('{commonappdata}\chuzi\data\.chuzi\rollback-management.ps1');
    Result := RunServiceManagementAt(PreviousManagement, 'Rollback',
      ExpandConstant('{app}\CorePayload'), ExpandConstant('{commonappdata}\chuzi\data'), ExpandConstant('{app}'));
  end;
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
  Result := UninstallContext and CleanupReady and not KeepUserData();
end;

function InitializeUninstall(): Boolean;
begin
  UninstallContext := True;
  CleanupReady := True;
  KeepUserData();
  if InstalledManagementScript() then
  begin
    if ProductionPayloadRequired() and
       not ValidateInstalledPayload(ExpandConstant('{app}\CorePayload'), '', True) then
    begin
      MsgBox('已安装的 Core 文件未通过生产签名检查，卸载已停止。请由管理员检查安装目录后重试。', mbError, MB_OK);
      Result := False;
      Exit;
    end;
    CleanupReady := RunServiceManagement('UninstallPrepare', ExpandConstant('{app}\CorePayload'),
      ExpandConstant('{commonappdata}\chuzi\data'), ExpandConstant('{app}'));
    if not CleanupReady then
    begin
      MsgBox('无法停止 Chuzi 服务，卸载已停止。请等待会话排空后重试。', mbError, MB_OK);
      Result := False;
      Exit;
    end;
    if not KeepUserData() then
    begin
      CleanupReady := RunServiceManagement('ConfirmDataOwnership', ExpandConstant('{app}\CorePayload'),
        ExpandConstant('{commonappdata}\chuzi\data'), ExpandConstant('{app}'));
      if not CleanupReady then
      begin
        MsgBox('无法确认 Chuzi 数据目录归属，卸载已停止。请由管理员检查数据目录后重试。', mbError, MB_OK);
        Result := False;
        Exit;
      end;
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
  Parameters: String;
  ResultCode: Integer;
begin
  NeedsRestart := False;
  Result := '';
  ExistingLauncher := ExpandConstant('{app}\CorePayload\chuzi-launcher.exe');
  ExistingManifest := ExpandConstant('{app}\CorePayload\release-manifest.json');
  ExistingManagement := ExpandConstant('{app}\CorePayload\windows-service-management.ps1');
  DataRoot := ExpandConstant('{commonappdata}\chuzi\data');
  { Drain any existing managed installation before replacement, including
    packages whose metadata predates the current production policy. }
  if FileExists(ExistingManagement) then
  begin
    if not RunServiceManagement('UpgradePrepare', ExpandConstant('{app}\CorePayload'), DataRoot, ExpandConstant('{app}')) then
    begin
      Result := '无法停止 Chuzi 服务；请先排空会话后重试。';
      Exit;
    end;
    if not RunServiceManagement('Backup', ExpandConstant('{app}\CorePayload'), DataRoot, ExpandConstant('{app}')) then
    begin
      Result := '无法创建升级回滚点；安装已停止。';
      Exit;
    end;
  end;
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
    if not RollbackUpgrade() then
      MsgBox('安装失败且无法恢复上一版本；请保留当前目录并联系管理员。', mbError, MB_OK);
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
  { The data tree is service-owned and read-only to Users; install the
    manifest-declared component with the elevated installer token. }
  if (not Exec(Launcher, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ResultCode)) or
     (ResultCode <> 0) then
  begin
    if not RollbackUpgrade() then
      MsgBox('Core 文件安装失败且无法恢复上一版本；请保留当前目录并联系管理员。', mbError, MB_OK);
    MsgBox('Core 文件安装失败，已停止安装。请关闭 Chuzi 后重试。', mbError, MB_OK);
    Abort;
  end;
  if not ValidateInstalledPayload(RuntimeRoot, '', ProductionPayloadRequired()) then
  begin
    if not RollbackUpgrade() then
      MsgBox('安装失败且无法恢复上一版本；请保留当前目录并联系管理员。', mbError, MB_OK);
    MsgBox('已安装的 Core 文件未通过签名和完整性检查，安装已停止。', mbError, MB_OK);
    Abort;
  end;
  if ProductionPayloadRequired() then
  begin
    if not RunServiceManagement('Install', RuntimeRoot, DataRoot, ExpandConstant('{app}')) then
    begin
      if not RollbackUpgrade() then
        MsgBox('服务注册失败且无法恢复上一版本；请保留当前目录并联系管理员。', mbError, MB_OK);
      MsgBox('生产签名包缺少可用的服务或 Session Broker，安装已停止。', mbError, MB_OK);
      Abort;
    end;
    if not ValidateInstalledPayload(RuntimeRoot, DataRoot, True) then
    begin
      if not RollbackUpgrade() then
        MsgBox('安装健康检查失败且无法恢复上一版本；请保留当前目录并联系管理员。', mbError, MB_OK);
      MsgBox('生产服务配置未通过完整性检查，安装已停止。', mbError, MB_OK);
      Abort;
    end;
    if not RunServiceManagement('CleanupBackup', RuntimeRoot, DataRoot, ExpandConstant('{app}')) then
      MsgBox('安装已完成，但升级回滚点清理失败；请使用 Repair 完成清理。', mbInformation, MB_OK);
  end;
end;
