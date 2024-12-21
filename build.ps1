# 设置错误时停止执行
$ErrorActionPreference = "Stop"

Write-Host "=== 开始构建 DNSenhance ===" -ForegroundColor Green

# 定义目录
$buildDir = "build"
$configsDir = "$buildDir/configs"
$dataDir = "$buildDir/data"
$logsDir = "$buildDir/logs"
$webDir = "$buildDir/web"
$redisDir = "$buildDir/redis"

# 创建必要的目录
@(
    $buildDir,
    $configsDir,
    $dataDir,
    $logsDir,
    "$webDir/static/images",
    $redisDir
) | ForEach-Object {
    if (-not (Test-Path $_)) {
        New-Item -ItemType Directory -Path $_ -Force | Out-Null
        Write-Host "创建目录: $_" -ForegroundColor Gray
    }
}

# 复制配置文件
if (-not (Test-Path "$configsDir/config.json")) {
    Copy-Item "examples/config.json" -Destination $configsDir -ErrorAction SilentlyContinue
    Write-Host "复制配置文件: config.json" -ForegroundColor Gray
}
if (-not (Test-Path "$configsDir/domains.txt")) {
    Copy-Item "examples/domains.txt" -Destination $configsDir -ErrorAction SilentlyContinue
    Write-Host "复制配置文件: domains.txt" -ForegroundColor Gray
}
if (-not (Test-Path "$configsDir/block.txt")) {
    Copy-Item "examples/block.txt" -Destination $configsDir -ErrorAction SilentlyContinue
    Write-Host "复制配置文件: block.txt" -ForegroundColor Gray
}

# 复制Web文件
Write-Host "复制Web文件..." -ForegroundColor Gray
Copy-Item "web/templates/*" -Destination "$webDir/templates" -Recurse -Force
Copy-Item "web/static/*" -Destination "$webDir/static" -Recurse -Force

# 复制Redis文件
Write-Host "复制Redis文件..." -ForegroundColor Gray
Copy-Item "redis/*" -Destination "$redisDir" -Recurse -Force

# 编译程序
Write-Host "正在编译..." -ForegroundColor Yellow
Push-Location cmd
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o "../$buildDir/dnsenhance.exe"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "编译失败！" -ForegroundColor Red
        exit 1
    }
} finally {
    Pop-Location
}

# 创建启动脚本
$startScript = @"
@echo off
chcp 65001 > nul
cd /d "%~dp0"
dnsenhance.exe
pause
"@
$startScript | Out-File -FilePath "$buildDir/start.bat" -Encoding utf8

# 创建 README 文件
$readme = @"
DNSenhance

使用说明：
1. 运行 start.bat 启动服务器
2. 访问 http://localhost:8080 查看Web界面
3. 配置文件在 configs 目录下
4. 日志文件在 logs 目录下
5. 统计数据在 data 目录下

注意：首次运行时需要以管理员身份运行，因为需要使用53端口。
"@
$readme | Out-File -FilePath "$buildDir/README.txt" -Encoding utf8

# 在 build.ps1 中添加创建空文件的命令
if (!(Test-Path "configs/block.txt")) {
    New-Item -ItemType File -Path "configs/block.txt" -Force | Out-Null
    Write-Host "创建空的 block.txt 文件" -ForegroundColor Gray
}
if (!(Test-Path "configs/domains.txt")) {
    New-Item -ItemType File -Path "configs/domains.txt" -Force | Out-Null
    Write-Host "创建空的 domains.txt 文件" -ForegroundColor Gray
}

Write-Host "=== 构建完成 ===" -ForegroundColor Green
Write-Host "程序已生成在 $buildDir 目录下" -ForegroundColor Cyan
Write-Host "运行 start.bat 启动服务器" -ForegroundColor Cyan 