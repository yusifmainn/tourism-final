@echo off
rem ================================================================
rem  Aze-Tourism - serveri komputerde qaldirir ve sayti brauzerde acir
rem  Go ve PostgreSQL lazimdir. Etrafli: DEPLOY.md
rem  Dayandirmaq ucun acilan server penceresini baglayin.
rem ================================================================
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
  echo.
  echo   Go tapilmadi. Qurasdirmaq ucun: winget install GoLang.Go
  echo.
  pause
  exit /b 1
)

if not exist ".env" (
  echo.
  echo   .env fayli yoxdur. .env.example faylini .env adi ile kopyalayib doldurun.
  echo   Etrafli: DEPLOY.md
  echo.
  pause
  exit /b 1
)

echo.
echo   Aze-Tourism serveri qaldirilir...
echo   Sayt:  http://localhost:5500/
echo   Admin: http://localhost:5500/admin/
echo.

start "Aze-Tourism Server" cmd /k "set PORT=5500&& go run ."

rem ilk build bir nece saniye cekir
ping -n 8 127.0.0.1 >nul

start "" "http://localhost:5500/"
exit
