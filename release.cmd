@echo off
setlocal

call build.cmd
if errorlevel 1 exit /b 1

set "OUT=dist\PortPilot"
if exist "%OUT%" rmdir /s /q "%OUT%"
mkdir "%OUT%"
copy /y PortPilot.exe "%OUT%\PortPilot.exe" >nul

echo Released: %CD%\%OUT%
