; Lemon Note 带界面版 Windows 安装包（NSIS 3）。按用户安装，不需要管理员权限。
; 由 scripts/package-windows.sh 调用：
;   makensis -DVERSION=0.1.0.42 -DVIVERSION=0.1.0.42 -DARCH=amd64 -DSRC=<LemonNote.exe> -DOUTFILE=<安装包> installer.nsi

Unicode true
ManifestDPIAware true
RequestExecutionLevel user
SetCompressor /SOLID lzma

!ifndef VERSION
  !error "缺少 -DVERSION"
!endif
!ifndef VIVERSION
  !define VIVERSION "0.0.0.0"
!endif
!ifndef ARCH
  !error "缺少 -DARCH（amd64 或 arm64）"
!endif
!ifndef SRC
  !error "缺少 -DSRC（LemonNote.exe 的路径）"
!endif
!ifndef OUTFILE
  !define OUTFILE "LemonNote-${VERSION}-windows-${ARCH}-setup.exe"
!endif

!define PRODUCT "Lemon Note"
!define EXE "LemonNote.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\LemonNote"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${PRODUCT}"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
BrandingText "${PRODUCT} ${VERSION}"

VIProductVersion "${VIVERSION}"
VIFileVersion "${VIVERSION}"
VIAddVersionKey /LANG=0 "ProductName" "${PRODUCT}"
VIAddVersionKey /LANG=0 "CompanyName" "LemonIT.CN"
VIAddVersionKey /LANG=0 "FileDescription" "${PRODUCT} Setup"
VIAddVersionKey /LANG=0 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "LegalCopyright" "Copyright (c) LemonIT.CN"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

LangString MsgRunning ${LANG_SIMPCHINESE} "Lemon Note 正在运行。请在系统托盘的 Lemon Note 图标菜单里选择「退出」，然后点「重试」。"
LangString MsgRunning ${LANG_ENGLISH} "Lemon Note is running. Choose Quit from the Lemon Note tray icon menu, then click Retry."
LangString MsgArch ${LANG_SIMPCHINESE} "这个安装包是 ${ARCH} 版本，与这台电脑的处理器不匹配，请下载对应的安装包。"
LangString MsgArch ${LANG_ENGLISH} "This installer is for ${ARCH} and does not match this computer. Please download the matching installer."
LangString MsgWebView2 ${LANG_SIMPCHINESE} "Lemon Note 需要 Microsoft Edge WebView2 运行时（Windows 11 自带）。现在打开下载页面吗？"
LangString MsgWebView2 ${LANG_ENGLISH} "Lemon Note needs the Microsoft Edge WebView2 Runtime (included with Windows 11). Open the download page now?"

; 安装或卸载前确认程序没有在运行（文件被占用会装不上）
!macro WaitNotRunning
  ${Do}
    nsExec::ExecToStack 'cmd /c tasklist /FI "IMAGENAME eq ${EXE}" /NH | find /I "${EXE}"'
    Pop $0
    Pop $1
    ${If} $0 != 0
      ${Break}
    ${EndIf}
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$(MsgRunning)" IDRETRY +2
    Abort
  ${Loop}
!macroend

Function .onInit
  !if "${ARCH}" == "arm64"
    ${IfNot} ${IsNativeARM64}
      MessageBox MB_OK|MB_ICONSTOP "$(MsgArch)"
      Abort
    ${EndIf}
  !else
    ${IfNot} ${RunningX64}
      MessageBox MB_OK|MB_ICONSTOP "$(MsgArch)"
      Abort
    ${EndIf}
  !endif
  !insertmacro WaitNotRunning
FunctionEnd

Function un.onInit
  !insertmacro WaitNotRunning
FunctionEnd

; WebView2 运行时：按微软文档检查三个位置的版本号
Function CheckWebView2
  SetRegView 64
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  SetRegView lastused
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    MessageBox MB_YESNO|MB_ICONQUESTION "$(MsgWebView2)" IDNO +2
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/"
  ${EndIf}
FunctionEnd

Section "${PRODUCT}" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "/oname=${EXE}" "${SRC}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortCut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${EXE}"

  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "LemonIT.CN"
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://github.com/LemonITCN/lemon-note"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  Call CheckWebView2
SectionEnd

; 卸载只删程序本身；数据目录、缓存与配置文件保留（里面是用户的笔记）
Section "Uninstall"
  DeleteRegValue HKCU "${RUN_KEY}" "LemonNote"
  Delete "$SMPROGRAMS\${PRODUCT}.lnk"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd
