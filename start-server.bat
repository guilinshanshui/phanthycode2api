@echo off
rem phanthycode2api launcher: double-click to run.
rem Logs show up in this window and are appended to logs\server.out.log at the same time.
chcp 65001 >nul
cd /d "%~dp0"
if not exist "logs" mkdir "logs"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Continue'; [Console]::OutputEncoding=[System.Text.Encoding]::UTF8; & '.\phanthycode2api.exe' -config 'config.json' 2>&1 | ForEach-Object { $line = [string]$_ + ''; $line | Out-File -FilePath 'logs\server.out.log' -Append -Encoding utf8; Write-Host $line }"
echo.
echo phanthycode2api exited. Full log: logs\server.out.log
pause
