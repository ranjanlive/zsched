param([switch]$All)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Static checks failed' }
New-Item -ItemType Directory -Path dist -Force | Out-Null
$originalOS = $env:GOOS
$originalArch = $env:GOARCH
$originalCGO = $env:CGO_ENABLED
try {
 $env:CGO_ENABLED='0'
 $env:GOOS='windows'; $env:GOARCH='amd64'
 go build -trimpath -ldflags '-s -w' -o dist/zsched.exe ./cmd/zsched
 if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
 go build -trimpath -ldflags '-s -w -H=windowsgui' -o dist/zsched-tray.exe ./cmd/zsched
 if ($LASTEXITCODE -ne 0) { throw 'Tray build failed' }
 if ($All) {
  foreach ($target in @('windows/arm64','linux/amd64','linux/arm64','darwin/amd64','darwin/arm64')) {
   $parts=$target.Split('/'); $env:GOOS=$parts[0]; $env:GOARCH=$parts[1]
   $suffix='';if ($env:GOOS -eq 'windows') { $suffix='.exe' }
   go build -trimpath -ldflags '-s -w' -o "dist/zsched-$($env:GOOS)-$($env:GOARCH)$suffix" ./cmd/zsched
   if ($LASTEXITCODE -ne 0) { throw "Build failed: $target" }
  }
 }
 Get-ChildItem dist -File | Where-Object { $_.Name -match '^zsched.*\.exe$|^zsched-(linux|darwin)-(amd64|arm64)$' } | Get-FileHash -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLower())  $(Split-Path $_.Path -Leaf)" } | Set-Content dist/SHA256SUMS.txt
} finally { $env:GOOS=$originalOS; $env:GOARCH=$originalArch; $env:CGO_ENABLED=$originalCGO }
