# Set error action preference to stop
$ErrorActionPreference = "Stop"

Write-Host "=== Building DNSenhance ===" -ForegroundColor Green

# Define directories
$buildDir = "build"
$configsDir = "$buildDir/configs"
$dataDir = "$buildDir/data"
$logsDir = "$buildDir/logs"
$webDir = "$buildDir/web"

# Create necessary directories
@(
    $buildDir,
    $configsDir,
    $dataDir,
    $logsDir,
    "$webDir/static",
    "$webDir/templates"
) | ForEach-Object {
    if (-not (Test-Path $_)) {
        New-Item -ItemType Directory -Path $_ -Force | Out-Null
        Write-Host "Created directory: $_" -ForegroundColor Gray
    }
}

# Copy configuration files
Write-Host "Copying config files..." -ForegroundColor Gray
if (Test-Path "configs") {
    Copy-Item "configs/*" -Destination $configsDir -Recurse -Force
}

# Copy web files
Write-Host "Copying web files..." -ForegroundColor Gray
if (Test-Path "web") {
    Copy-Item "web/templates/*" -Destination "$webDir/templates" -Recurse -Force
    Copy-Item "web/static/*" -Destination "$webDir/static" -Recurse -Force
}

# Copy data files
Write-Host "Copying data files..." -ForegroundColor Gray
if (Test-Path "data") {
    Copy-Item "data/*" -Destination $dataDir -Recurse -Force
}

# Copy start script
Write-Host "Copying start script..." -ForegroundColor Gray
Copy-Item "start_simple.vbs" -Destination $buildDir -Force

# Build program
Write-Host "Building..." -ForegroundColor Yellow
try {
    Push-Location cmd
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o "../$buildDir/dnsenhance.exe"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Build failed!" -ForegroundColor Red
        exit 1
    }
    Pop-Location
} catch {
    Write-Host "Build failed: $_" -ForegroundColor Red
    exit 1
}

# Create empty files if they don't exist
if (!(Test-Path "configs/block.txt")) {
    New-Item -ItemType File -Path "configs/block.txt" -Force | Out-Null
    Write-Host "Created empty block.txt file" -ForegroundColor Gray
}
if (!(Test-Path "configs/domains.txt")) {
    New-Item -ItemType File -Path "configs/domains.txt" -Force | Out-Null
    Write-Host "Created empty domains.txt file" -ForegroundColor Gray
}

# Create README file
$readme = @"
DNSenhance

Instructions:
1. Run with administrator privileges: sudo ./build/dnsenhance.exe
2. Visit http://localhost:8080 for the web interface
3. Configuration files are in the configs directory
4. Log files are in the logs directory
5. Statistics data is in the data directory

Note: Administrator/root privileges are required to run the program as it uses port 53.
"@
Set-Content -Path "$buildDir/README.txt" -Value $readme -Encoding UTF8

Write-Host "=== Build Complete ===" -ForegroundColor Green
Write-Host "Program has been built in the $buildDir directory" -ForegroundColor Cyan
Write-Host "Run command: sudo ./build/dnsenhance.exe" -ForegroundColor Cyan