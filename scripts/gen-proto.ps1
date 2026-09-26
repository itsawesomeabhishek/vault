#Requires -Version 5.1
# Regenerates Go code from engine/proto. Requires tools installed by scripts/setup-tools.ps1.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$tools = Join-Path $root '.tools'
$env:PATH = "$(Join-Path $tools 'bin');$env:PATH"
$protoc = Join-Path $tools 'protoc\bin\protoc.exe'
$engine = Join-Path $root 'engine'
$out = Join-Path $engine 'gen\vaultpb'
New-Item -ItemType Directory -Force $out | Out-Null
Push-Location (Join-Path $engine 'proto')
try {
    & $protoc --go_out=$out --go_opt=paths=source_relative `
        --go-grpc_out=$out --go-grpc_opt=paths=source_relative `
        vault/v1/vault.proto
    if ($LASTEXITCODE -ne 0) { throw "protoc failed" }
    Move-Item -Force (Join-Path $out 'vault\v1\*.go') $out
    Remove-Item -Recurse -Force (Join-Path $out 'vault')
} finally {
    Pop-Location
}
