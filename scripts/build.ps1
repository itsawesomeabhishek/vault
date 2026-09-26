$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$Bin = Join-Path $Root 'app\resources\bin'
New-Item -ItemType Directory -Force -Path $Bin | Out-Null

Write-Host 'Building vault-node.exe...'
$env:CGO_ENABLED = '0'
Push-Location (Join-Path $Root 'engine')
try {
  go build -trimpath -ldflags '-s -w' -o (Join-Path $Bin 'vault-node.exe') ./cmd/vault-node
} finally {
  Pop-Location
}
if (-not (Test-Path (Join-Path $Bin 'vault-node.exe'))) { throw 'vault-node.exe was not produced' }

Write-Host 'Building Windows installer...'
Push-Location (Join-Path $Root 'app')
try {
  if (-not (Test-Path 'node_modules')) { npm ci --no-audit --no-fund }
  npm run typecheck
  npm test
  npm run lint
  npm run dist
} finally {
  Pop-Location
}

Get-ChildItem (Join-Path $Root 'app\dist\Vault-Setup.exe') -ErrorAction SilentlyContinue | Format-List FullName, Length, LastWriteTime
