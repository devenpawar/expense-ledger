@echo off
setlocal
cd /d "%~dp0"
title The Expense Ledger
set "LEDGER_EXECUTABLE=expense-ledger.exe"
if exist ".cache\expense-ledger-next.exe" set "LEDGER_EXECUTABLE=.cache\expense-ledger-next.exe"
if exist ".cache\expense-ledger-labels.exe" set "LEDGER_EXECUTABLE=.cache\expense-ledger-labels.exe"
if not exist "%LEDGER_EXECUTABLE%" (
  echo The application executable is missing. Rebuild the app before starting.
  pause
  exit /b 1
)
if not exist "frontend\dist\index.html" (
  echo The frontend build is missing. Run start.ps1 to build the app first.
  pause
  exit /b 1
)
echo Starting The Expense Ledger...
echo Open http://127.0.0.1:8080 in your browser once the server starts.
echo Keep this window open while using the app.
echo.
"%LEDGER_EXECUTABLE%"
echo.
echo The server has stopped. Any error is shown above.
pause
