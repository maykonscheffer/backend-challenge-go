$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
$brokerStopped = $false
$databaseStopped = $false
function Docker-Checked([string[]]$Arguments) {
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed: $($Arguments -join ' ')" }
}
function Wait-Ready {
    for ($i=0; $i -lt 45; $i++) {
        try { Invoke-RestMethod 'http://localhost:8080/health/ready' -TimeoutSec 3 | Out-Null; return }
        catch { Start-Sleep -Seconds 1 }
    }
    throw 'API did not recover readiness'
}
try {
    function Token([string]$client) {
        (Invoke-RestMethod -Method Post -Uri 'http://localhost:8180/realms/wallet/protocol/openid-connect/token' `
            -Body @{grant_type='client_credentials';client_id=$client;client_secret="local-$client-secret"}).access_token
    }
    $internal = Token 'internal-service'
    $provider = Token 'provider-a'
    $wallet = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wallets' `
        -Headers @{Authorization="Bearer $internal"} -ContentType 'application/json' `
        -Body (@{playerId=[guid]::NewGuid().ToString();initialBalance=@{amount='100.00';currency='BRL'}} | ConvertTo-Json)
    $operation = @{
        providerId='provider-a';externalTransactionId=[guid]::NewGuid().ToString()
        playerId=$wallet.playerId;walletId=$wallet.id;roundId='outage';gameId='demo';kind='BET'
        money=@{amount='10.00';currency='BRL'}
    }
    $headers = @{Authorization="Bearer $provider";'Idempotency-Key'=[guid]::NewGuid().ToString()}
    Docker-Checked @('compose','stop','ministack')
    $brokerStopped = $true
    $first = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wagering/transactions' `
        -Headers $headers -ContentType 'application/json' -Body ($operation | ConvertTo-Json) -TimeoutSec 15
    if ($first.balance.amount -ne '90.00') { throw 'HTTP transaction failed during broker outage' }
    # UUID comes from the API, and is parsed before being included in SQL.
    $transactionId = [guid]::Parse($first.transactionId).ToString()
    $pending = & docker compose exec -T postgres psql -U wallet_owner -d wallet -Atc "SELECT count(*) FROM outbox WHERE transaction_id='$transactionId' AND published_at IS NULL"
    if ($LASTEXITCODE -ne 0 -or [int]$pending -ne 2) { throw 'Events were not preserved during broker outage' }
    Docker-Checked @('compose','start','ministack')
    $brokerStopped = $false
    Wait-Ready
    $published = 0
    for ($i=0; $i -lt 90; $i++) {
        $published = & docker compose exec -T postgres psql -U wallet_owner -d wallet -Atc "SELECT count(*) FROM outbox WHERE transaction_id='$transactionId' AND published_at IS NOT NULL"
        if ($LASTEXITCODE -ne 0) { throw 'Could not inspect recovered outbox' }
        if ([int]$published -eq 2) { break }
        Start-Sleep -Seconds 1
    }
    if ([int]$published -ne 2) { throw 'Outbox did not recover after broker restart' }
    Write-Output 'PASS: broker stopped; financial commit and two events preserved; publication recovered after restart.'
    $operation.externalTransactionId = [guid]::NewGuid().ToString()
    $operation.money.amount = '5.00'
    $headers['Idempotency-Key'] = [guid]::NewGuid().ToString()
    Docker-Checked @('compose','stop','postgres')
    $databaseStopped = $true
    $status = 0
    try {
        Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wagering/transactions' `
            -Headers $headers -ContentType 'application/json' -Body ($operation | ConvertTo-Json) -TimeoutSec 15 | Out-Null
    } catch { if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode } }
    if ($status -ne 503) { throw "Expected HTTP 503 during database outage, received $status" }
    Docker-Checked @('compose','start','postgres')
    $databaseStopped = $false
    Wait-Ready
    $recovered = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wagering/transactions' `
        -Headers $headers -ContentType 'application/json' -Body ($operation | ConvertTo-Json)
    $replay = Invoke-RestMethod -Method Post -Uri 'http://localhost:8082/wagering/transactions' `
        -Headers $headers -ContentType 'application/json' -Body ($operation | ConvertTo-Json)
    $reconciliation = Invoke-RestMethod -Method Post -Uri "http://localhost:8080/wallets/$($wallet.id)/reconciliation" `
        -Headers @{Authorization="Bearer $internal"}
    if ($recovered.balance.amount -ne '85.00' -or !$replay.idempotentReplay -or
        $replay.transactionId -ne $recovered.transactionId -or !$reconciliation.consistent -or
        $reconciliation.storedBalance.amount -ne '85.00' -or $reconciliation.checkedEntries -ne 3) {
        throw 'Database outage recovery violated financial invariants'
    }
    Write-Output 'PASS: database stopped; HTTP 503; same operation recovered once after restart; balance 85.00 and three ledger entries.'
} finally {
    if ($databaseStopped) { & docker compose start postgres }
    if ($brokerStopped) { & docker compose start ministack }
    Pop-Location
}
