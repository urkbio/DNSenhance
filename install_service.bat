@echo off
echo Installing DNSenhance service...

REM 获取当前目录
set CURRENT_DIR=%~dp0

REM 检查是否以管理员权限运行
net session >nul 2>&1
if %errorLevel% neq 0 (
    echo Please run this script as Administrator
    pause
    exit /b 1
)

REM 检查 nssm 是否已安装
where nssm >nul 2>&1
if %errorLevel% neq 0 (
    echo NSSM not found. Please install NSSM first.
    echo Download from: https://nssm.cc/download
    pause
    exit /b 1
)

REM 安装服务
nssm install DNSenhance "%CURRENT_DIR%DNSenhance.exe"
nssm set DNSenhance AppDirectory "%CURRENT_DIR%"
nssm set DNSenhance Description "DNS服务器增强版"
nssm set DNSenhance Start SERVICE_AUTO_START

REM 启动服务
nssm start DNSenhance

echo Service installed and started successfully!
pause 