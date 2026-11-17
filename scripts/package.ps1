param([string]$Version = '0.1.0')
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'Invalid release version' }
$taskRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$taskDist = Join-Path $taskRoot 'dist'
New-Item -ItemType Directory -Force $taskDist | Out-Null
$taskStageRoot = Join-Path $taskDist ('.package-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $taskStageRoot | Out-Null
$originalOS = $env:GOOS; $originalArch = $env:GOARCH; $originalCGO = $env:CGO_ENABLED
$sums = @()
Push-Location $taskRoot
try {
    $env:CGO_ENABLED = '0'
    # Build package-tar helper for Unix archive creation
    $env:GOOS = (go env GOHOSTOS).Trim(); $env:GOARCH = (go env GOHOSTARCH).Trim()
    $archiveHelper = Join-Path $taskStageRoot 'package-tar.exe'
    go build -trimpath -o $archiveHelper ./scripts/package-tar
    if ($LASTEXITCODE -ne 0) { throw 'Archive helper build failed' }

    # 1. Build Windows AMD64 (Console + Tray)
    $winAmdStage = Join-Path $taskStageRoot "zsched-$Version-windows-amd64"
    New-Item -ItemType Directory $winAmdStage | Out-Null
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    Write-Host "building windows/amd64 ($Version)"
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $winAmdStage 'zsched.exe') ./cmd/zsched
    go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o (Join-Path $winAmdStage 'zsched-tray.exe') ./cmd/zsched
    Copy-Item -LiteralPath (Join-Path $winAmdStage 'zsched.exe'), (Join-Path $winAmdStage 'zsched-tray.exe') -Destination $taskDist
    foreach ($file in @('README.md','LICENSE')) { if (Test-Path -LiteralPath $file) { Copy-Item -LiteralPath $file -Destination $winAmdStage } }
    Copy-Item -LiteralPath docs -Destination $winAmdStage -Recurse
    $winZip = Join-Path $taskDist "zsched-$Version-windows-amd64.zip"
    Compress-Archive -LiteralPath $winAmdStage -DestinationPath $winZip -Force
    $sums += "$((Get-FileHash -LiteralPath $winZip -Algorithm SHA256).Hash.ToLower())  $(Split-Path $winZip -Leaf)"

    # 2. Build Other Targets
    $targets = @('windows/arm64','linux/amd64','linux/arm64','darwin/amd64','darwin/arm64')
    foreach ($target in $targets) {
        $targetOS, $targetArch = $target.Split('/')
        $name = "zsched-$Version-$targetOS-$targetArch"
        $stage = Join-Path $taskStageRoot $name
        New-Item -ItemType Directory $stage | Out-Null
        $binary = if ($targetOS -eq 'windows') { 'zsched.exe' } else { 'zsched' }
        $env:GOOS = $targetOS; $env:GOARCH = $targetArch
        Write-Host "building $target ($Version)"
        go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $stage $binary) ./cmd/zsched
        if ($LASTEXITCODE -ne 0) { throw "Build failed for $target" }
        foreach ($file in @('README.md','LICENSE')) { if (Test-Path -LiteralPath $file) { Copy-Item -LiteralPath $file -Destination $stage } }
        Copy-Item -LiteralPath docs -Destination $stage -Recurse
        if ($targetOS -eq 'windows') {
            $archive = Join-Path $taskDist "$name.zip"
            Compress-Archive -LiteralPath $stage -DestinationPath $archive -Force
        } else {
            $archive = Join-Path $taskDist "$name.tar.gz"
            & $archiveHelper $stage $archive
            if ($LASTEXITCODE -ne 0) { throw "Archive failed for $target" }
        }
        $sums += "$((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLower())  $(Split-Path $archive -Leaf)"
    }

    # 3. Source Archive
    $srcStage = Join-Path $taskStageRoot "zsched-$Version-source"
    New-Item -ItemType Directory $srcStage | Out-Null
    Get-ChildItem -Path $taskRoot -Exclude "dist", ".git" | ForEach-Object {
        Copy-Item -Path $_.FullName -Destination $srcStage -Recurse -Force
    }
    $srcZip = Join-Path $taskDist "zsched-$Version-source.zip"
    Compress-Archive -Path $srcStage -DestinationPath $srcZip -Force
    $sums += "$((Get-FileHash -LiteralPath $srcZip -Algorithm SHA256).Hash.ToLower())  $(Split-Path $srcZip -Leaf)"

    $sums | Set-Content -LiteralPath (Join-Path $taskDist 'SHA256SUMS.txt') -Encoding ascii
    Write-Host "Packaged all release archives into $taskDist"
} finally {
    $env:GOOS = $originalOS; $env:GOARCH = $originalArch; $env:CGO_ENABLED = $originalCGO
    Pop-Location
    $resolvedStage = [IO.Path]::GetFullPath($taskStageRoot)
    $resolvedDist = [IO.Path]::GetFullPath($taskDist) + [IO.Path]::DirectorySeparatorChar
    if (!$resolvedStage.StartsWith($resolvedDist, [StringComparison]::OrdinalIgnoreCase)) { throw 'Stage cleanup escaped dist' }
    Remove-Item -LiteralPath $resolvedStage -Recurse -Force
}
