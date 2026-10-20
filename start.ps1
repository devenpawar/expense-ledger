$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$env:GOCACHE = Join-Path $PSScriptRoot '.cache/go-build'
New-Item -ItemType Directory -Path $env:GOCACHE -Force | Out-Null
Push-Location (Join-Path $PSScriptRoot 'frontend')
try {
    if (-not (Test-Path -LiteralPath 'node_modules')) {
        & npm.cmd ci --cache (Join-Path $PSScriptRoot '.cache/npm') --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
    }
    & npm.cmd run build
    if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
} finally {
    Pop-Location
}
& go run .
if ($LASTEXITCODE -ne 0) { throw 'Backend stopped with an error.' }
