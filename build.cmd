@echo off
setlocal

if not defined GO_EXE set "GO_EXE=go"
if exist "%GO_EXE%" goto go_found
where %GO_EXE% >nul 2>nul
if not errorlevel 1 goto go_found
echo Go not found. Install Go or set GO_EXE to the full path of go.exe.
exit /b 1

:go_found

taskkill /F /IM PortPilot.exe >nul 2>nul

if not exist assets\portpilot.ico (
    "%GO_EXE%" run .\cmd\makeicon
    if errorlevel 1 exit /b 1
)

"%GO_EXE%" run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest assets\portpilot.exe.manifest -ico assets\portpilot.ico -o cmd\portpilot\rsrc_windows_amd64.syso
if errorlevel 1 exit /b 1

rem Version: prefer PORTPILOT_VERSION, then git describe, else dev.
rem Release builds pass the exact v* tag through PORTPILOT_VERSION.
if not defined PORTPILOT_VERSION (
    for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set "PORTPILOT_VERSION=%%v"
)
if not defined PORTPILOT_VERSION set "PORTPILOT_VERSION=dev"

set "LDFLAGS=-H windowsgui -s -w -X github.com/BeyondXinXin/portpilot/internal/version.Version=%PORTPILOT_VERSION%"
"%GO_EXE%" build -trimpath -ldflags="%LDFLAGS%" -o PortPilot.exe .\cmd\portpilot
if errorlevel 1 exit /b 1

echo Built: %CD%\PortPilot.exe (%PORTPILOT_VERSION%)
