; GTasks NSIS installer（首装 / 覆盖安装用，自升级走 internal/update 原地换 exe）。
;
; 参考 svc 的 build/windows/installer/project.nsi，但去掉 wails_tools.nsh 依赖：
; GTasks 的 Windows 产物是 scripts/build-windows.sh 里裸 go build 出来的，
; 不经过 wails CLI，所以这里自成一体，makensis 直接吃本文件。
;
; 路径约定：makensis 的 File / OutFile 相对路径按「启动 makensis 时的 cwd」解析，
; 而 __FILEDIR__ 是「nsi 相对启动 cwd 的目录」，两者同锚 —— 所以本文件里所有
; 相对路径都写成 ${__FILEDIR__}\... 形式，从仓库根或从 installer 目录调都对。
;
; 本地调试（仓库根执行）：
;   makensis -DAPP_VERSION=1.0.1 \
;     -DBIN_PATH=build/bin/gtasks-1.0.1-windows-x64.exe \
;     -DOUT_NAME=gtasks-1.0.1-windows-x64-installer.exe \
;     build/windows/installer/project.nsi
Unicode true

!ifndef APP_VERSION
  !define APP_VERSION "0.0.0"
!endif
!ifndef BIN_PATH
  !define BIN_PATH "${__FILEDIR__}\..\..\bin\gtasks.exe"
!endif
!ifndef OUT_NAME
  !define OUT_NAME "gtasks-installer.exe"
!endif

!define PRODUCT_NAME      "GTasks"
!define PRODUCT_EXECUTABLE "GTasks.exe"
!define PUBLISHER         "mose-x"
!define UNINST_KEY        "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME}"
!define DATA_DIR_NAME     "gtasks"


# VIProductVersion / VIFileVersion 必须是四段数字。
VIProductVersion "${APP_VERSION}.0"
VIFileVersion    "${APP_VERSION}.0"

VIAddVersionKey "CompanyName"     "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${PRODUCT_NAME} Installer"
VIAddVersionKey "ProductVersion"  "${APP_VERSION}"
VIAddVersionKey "FileVersion"     "${APP_VERSION}"
VIAddVersionKey "LegalCopyright"  "MIT License"
VIAddVersionKey "ProductName"     "${PRODUCT_NAME}"

ManifestDPIAware true

# TEMP-DEBUG（验证完删）：摸清 makensis 编译期 cwd 与相对路径基准。
!echo "TEMP-DEBUG FILEDIR=${__FILEDIR__}"
!system "cd"
!system "dir /b build\bin 2>nul || echo NO_ROOT_BIN"
!system "dir /b ..\..\bin 2>nul || echo NO_RELBLD_BIN"
!system "dir /b ..\icon.ico 2>nul || echo NO_REL_ICON"
!system "dir /b build\windows\icon.ico 2>nul || echo NO_ROOT_ICON"

!include "MUI2.nsh"
!include "FileFunc.nsh"

Name "${PRODUCT_NAME} ${APP_VERSION}"
OutFile "${__FILEDIR__}\..\..\bin\${OUT_NAME}"
# per-user 安装：不弹 UAC，且自升级（internal/update 原地换 exe）要写安装目录，
# 装到 Program Files 会让普通权限的自升级写不进去；$LOCALAPPDATA 没这问题。
InstallDir "$LOCALAPPDATA\Programs\${PRODUCT_NAME}"
RequestExecutionLevel user
SetCompressor /SOLID lzma

!define MUI_ICON "${__FILEDIR__}\..\icon.ico"
!define MUI_UNICON "${__FILEDIR__}\..\icon.ico"
!define MUI_FINISHPAGE_NOAUTOCLOSE
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
# 已装过就直接复用上次的目录（读注册表），跳过目录页。
!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipDirIfInstalled

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Var InstalledDir

Function SkipDirIfInstalled
  ReadRegStr $InstalledDir HKCU "${UNINST_KEY}" "InstallLocation"
  ${If} $InstalledDir != ""
    StrCpy $INSTDIR $InstalledDir
    Abort
  ${EndIf}
FunctionEnd

Section "Install"
  SetShellVarContext current

  # 覆盖安装 / 自升级残留进程：先静默停掉，否则 exe 被占用写不进去。
  nsExec::ExecToLog 'taskkill /IM "${PRODUCT_EXECUTABLE}" /F'
  Pop $0

  SetOutPath $INSTDIR
  File /oname=${PRODUCT_EXECUTABLE} "${BIN_PATH}"
  File "${__FILEDIR__}\..\icon.ico"

  CreateShortcut "$SMPROGRAMS\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\icon.ico"
  CreateShortcut "$DESKTOP\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\icon.ico"

  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName"     "${PRODUCT_NAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion"  "${APP_VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon"     "$INSTDIR\${PRODUCT_EXECUTABLE}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher"       "${PUBLISHER}"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0
SectionEnd

Section "Uninstall"
  SetShellVarContext current

  nsExec::ExecToLog 'taskkill /IM "${PRODUCT_EXECUTABLE}" /F'
  Pop $0

  Delete "$DESKTOP\${PRODUCT_NAME}.lnk"
  Delete "$SMPROGRAMS\${PRODUCT_NAME}.lnk"
  # 通配兜住重命名 / 复制出来的变体（例如「GTasks (2).lnk」）。
  Delete "$DESKTOP\${PRODUCT_NAME}*.lnk"
  Delete "$SMPROGRAMS\${PRODUCT_NAME}*.lnk"

  DeleteRegKey HKCU "${UNINST_KEY}"

  # WebView2 的用户数据（主题 / 语言 / 登录态都在这），卸载一并清掉。
  RMDir /r "$AppData\${DATA_DIR_NAME}"
  RMDir /r "$INSTDIR"
SectionEnd
