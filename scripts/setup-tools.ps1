#Requires -Version 5.1
# Installs protoc and the Go protobuf plugins into .tools (only needed to regenerate engine/gen).
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$tools = Join-Path $root '.tools'
New-Item -ItemType Directory -Force $tools | Out-Null
$zip = Join-Path $tools 'protoc.zip'
Invoke-WebRequest -UseBasicParsing 'https://github.com/protocolbuffers/protobuf/releases/download/v28.3/protoc-28.3-win64.zip' -OutFile $zip
Expand-Archive -Force $zip (Join-Path $tools 'protoc')
$env:GOBIN = Join-Path $tools 'bin'
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
