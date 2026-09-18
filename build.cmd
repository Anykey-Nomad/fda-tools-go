@echo off
setlocal

REM ============================================================
REM  fda-tools-go build script
REM  Builds 64-bit binaries for Windows and Linux into .\dist\
REM  Usage: build.cmd
REM ============================================================

REM Check that Go is available
where go >nul 2>nul
if errorlevel 1 (
    echo Error: Go toolchain not found in PATH.
    echo Install it from https://go.dev/dl/ and try again.
    exit /b 1
)

for /f "delims=" %%i in ('go env GOVERSION') do set "GOVERSION=%%i"
echo Building fda-tools-go with %GOVERSION% ...
echo.

REM Clean output directory
if exist dist rmdir /s /q dist
mkdir dist

REM --- Windows 64-bit ---
echo [1/2] Building windows/amd64 ...
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o dist\fda-tools-go_windows_amd64.exe .
if errorlevel 1 goto :fail

REM --- Linux 64-bit ---
echo [2/2] Building linux/amd64 ...
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o dist\fda-tools-go_linux_amd64 .
if errorlevel 1 goto :fail

REM Reset environment for the current shell
set GOOS=
set GOARCH=
set CGO_ENABLED=

echo.
echo Done! Output files:
dir /b dist
exit /b 0

:fail
echo.
echo Build FAILED.
exit /b 1
