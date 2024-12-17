@echo off
setlocal

REM 设置DNS服务器程序路径（可以修改为实际安装路径）
set DNS_PATH=C:\Users\zyg\Portable\DNSenhance\DNSenhance.exe

REM 检查程序是否存在
if not exist "%DNS_PATH%" (
    echo Error: DNSenhance.exe not found at: %DNS_PATH%
    echo Please make sure the program is in the same directory as this script
    pause
    exit /b 1
)

REM 检查是否以管理员权限运行
net session >nul 2>&1
if %errorLevel% neq 0 (
    echo Please run this script as Administrator
    pause
    exit /b 1
)

REM 启动程序
echo Starting DNSenhance...
start /b "" "%DNS_PATH%"

REM 等待程序启动
timeout /t 2 /nobreak > nul

REM 检查程序是否成功启动
tasklist /FI "IMAGENAME eq DNSenhance.exe" 2>NUL | find /I /N "DNSenhance.exe">NUL
if "%ERRORLEVEL%"=="0" (
    echo DNSenhance is running successfully!
) else (
    echo Failed to start DNSenhance!
    pause
    exit /b 1
)

exit