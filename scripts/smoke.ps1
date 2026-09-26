#Requires -Version 5.1
# End-to-end smoke test: starts 3 real vault-node processes on this machine
# (mutual-TLS gRPC + encrypted gossip), joins them with invite codes, uploads
# a file, kills a node, and verifies the file is still readable and repaired.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root 'app\resources\bin\vault-node.exe'
$work = Join-Path ([IO.Path]::GetTempPath()) ("vault-smoke-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
$env:VAULT_API_TOKEN = 'smoke-test-token-0123456789'
$headers = @{ Authorization = "Bearer $env:VAULT_API_TOKEN" }
$procs = @{}

function Start-Node([int]$i, [string[]]$extra) {
    $dir = Join-Path $work "n$i"
    New-Item -ItemType Directory -Force $dir | Out-Null
    $nodeArgs = @('--data-dir', $dir, '--zone', "zone-$($i % 2)", '--advertise', '127.0.0.1',
        '--rpc-port', (19100 + $i), '--gossip-port', (17100 + $i), '--api-addr', "127.0.0.1:$(18100 + $i)",
        '--enable-chaos', '--anti-entropy-interval', '2s', '--dead-timeout', '10s') + $extra
    $procs[$i] = Start-Process -FilePath $bin -ArgumentList $nodeArgs -PassThru -WindowStyle Hidden `
        -RedirectStandardError (Join-Path $work "n$i.err") -RedirectStandardOutput (Join-Path $work "n$i.out")
    Wait-Api $i
}

function Wait-Api([int]$i) {
    for ($t = 0; $t -lt 60; $t++) {
        try { Invoke-RestMethod "http://127.0.0.1:$(18100 + $i)/v1/health" | Out-Null; return } catch { Start-Sleep -Milliseconds 250 }
    }
    throw "node $i did not start: $(Get-Content (Join-Path $work "n$i.err") -Raw)"
}

function Api([int]$i, [string]$method, [string]$path, $body = $null) {
    $uri = "http://127.0.0.1:$(18100 + $i)$path"
    if ($null -ne $body) { return Invoke-RestMethod -Method $method -Uri $uri -Headers $headers -Body $body -ContentType 'application/octet-stream' }
    return Invoke-RestMethod -Method $method -Uri $uri -Headers $headers
}

try {
    Start-Node 1 @('--init')
    foreach ($i in 2, 3, 4) {
        $code = (Api 1 POST '/v1/invites').code
        Start-Node $i @('--join', $code)
    }
    for ($t = 0; $t -lt 40 -and (Api 1 GET '/v1/status').members.Count -lt 4; $t++) { Start-Sleep -Milliseconds 250 }
    $members = (Api 1 GET '/v1/status').members.Count
    Write-Host "cluster members: $members"
    if ($members -ne 4) { throw "expected 4 members" }

    for ($t = 0; $t -lt 40 -and -not ((Api 3 GET '/v1/buckets') | Where-Object name -eq 'scans'); $t++) { Start-Sleep -Milliseconds 250 }
    $payload = [byte[]]::new(9MB); (New-Object Random 42).NextBytes($payload)
    Api 2 PUT '/v1/buckets/scans/objects/patients/P-1/ct.dcm' $payload | Out-Null
    $tmp = Join-Path $work 'download.bin'
    Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18103/v1/buckets/scans/objects/patients/P-1/ct.dcm' -Headers $headers -OutFile $tmp
    if ((Get-FileHash $tmp).Hash -ne (Get-FileHash -InputStream ([IO.MemoryStream]::new($payload))).Hash) { throw 'download mismatch' }
    Write-Host 'upload/download across nodes: OK'

    $ins = Api 1 GET '/v1/buckets/scans/inspect/patients/P-1/ct.dcm'
    Write-Host "healthy slots: $($ins.healthySlots)/$($ins.width)"
    $victim = ($ins.slots | Where-Object { $_.node -ne ((Api 1 GET '/v1/status').id) } | Select-Object -First 1).node
    $victimIdx = (1..4 | Where-Object { (Api $_ GET '/v1/status').id -eq $victim })[0]
    Api $victimIdx POST '/v1/chaos/corrupt' '{"bucket":"scans","key":"patients/P-1/ct.dcm"}' | Out-Null
    Write-Host "corrupted a replica on node $victimIdx"
    Api $victimIdx POST '/v1/maintenance/scrub' | Out-Null
    for ($t = 0; $t -lt 40; $t++) {
        $ins = Api 1 GET '/v1/buckets/scans/inspect/patients/P-1/ct.dcm'
        if ($ins.healthySlots -eq $ins.width) { break }
        Start-Sleep -Milliseconds 500
    }
    Write-Host "after scrub + repair: $($ins.healthySlots)/$($ins.width) healthy"
    if ($ins.healthySlots -ne $ins.width) { throw 'corruption was not repaired' }

    Stop-Process -Id $procs[$victimIdx].Id -Force
    Write-Host "killed node $victimIdx"
    $reader = (1..4 | Where-Object { $_ -ne $victimIdx })[0]
    Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$(18100 + $reader)/v1/buckets/scans/objects/patients/P-1/ct.dcm" -Headers $headers -OutFile $tmp
    if ((Get-FileHash $tmp).Hash -ne (Get-FileHash -InputStream ([IO.MemoryStream]::new($payload))).Hash) { throw 'read after node loss failed' }
    Write-Host 'read with a node down: OK'
    Api $reader PUT '/v1/buckets/scans/objects/patients/P-2/mri.dcm' ([byte[]](1..200)) | Out-Null
    Write-Host 'write with a node down (sloppy quorum): OK'
    Write-Host 'SMOKE TEST PASSED'
} finally {
    $procs.Values | ForEach-Object { if (-not $_.HasExited) { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue } }
    Start-Sleep -Milliseconds 500
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
