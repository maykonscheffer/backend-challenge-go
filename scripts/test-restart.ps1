$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $tokenURL = 'http://localhost:8180/realms/wallet/protocol/openid-connect/token'
    function Get-Token([string]$client) {
        (Invoke-RestMethod -Method Post -Uri $tokenURL -Body @{
            grant_type = 'client_credentials'; client_id = $client
            client_secret = "local-$client-secret"
        }).access_token
    }
    $internal = Get-Token 'internal-service'
    $provider = Get-Token 'provider-a'
    $wallet = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wallets' `
        -Headers @{Authorization="Bearer $internal"} -ContentType 'application/json' `
        -Body (@{playerId=[guid]::NewGuid().ToString();initialBalance=@{amount='100.00';currency='BRL'}} | ConvertTo-Json)
    $key = [guid]::NewGuid().ToString()
    $operation = @{
        providerId='provider-a';externalTransactionId=[guid]::NewGuid().ToString()
        playerId=$wallet.playerId;walletId=$wallet.id;roundId='restart';gameId='demo';kind='BET'
        money=@{amount='25.00';currency='BRL'}
    } | ConvertTo-Json
    $headers = @{Authorization="Bearer $provider";'Idempotency-Key'=$key}
    $first = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wagering/transactions' `
        -Headers $headers -ContentType 'application/json' -Body $operation
    $pendingBetId = [guid]::NewGuid().ToString()
    $pendingKey = [guid]::NewGuid().ToString()
    $pendingOperation = @{
        providerId='provider-a';externalTransactionId=[guid]::NewGuid().ToString()
        playerId=$wallet.playerId;walletId=$wallet.id;roundId='pending-restart';gameId='demo';kind='REFUND'
        money=@{amount='5.00';currency='BRL'};referenceExternalTransactionId=$pendingBetId
    }
    $pendingHeaders = @{Authorization="Bearer $provider";'Idempotency-Key'=$pendingKey}
    $pending = Invoke-RestMethod -Method Post -Uri 'http://localhost:8080/wagering/transactions' `
        -Headers $pendingHeaders -ContentType 'application/json' -Body ($pendingOperation | ConvertTo-Json)
    if ($pending.status -ne 'PENDING_REFERENCE') { throw 'Expected durable pending reference' }
    # Kill all application processes; preserve PostgreSQL and the IdP.
    docker compose kill app1 app2 app3
    if ($LASTEXITCODE -ne 0) { throw 'Could not terminate application containers' }
    docker compose up -d app1 app2 app3
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart application containers' }
    $ready = $false
    for ($i=0; $i -lt 30; $i++) {
        try { Invoke-RestMethod 'http://localhost:8082/health/ready' | Out-Null; $ready=$true; break }
        catch { Start-Sleep -Seconds 1 }
    }
    if (!$ready) { throw 'Restarted API did not become ready' }
    $replay = Invoke-RestMethod -Method Post -Uri 'http://localhost:8082/wagering/transactions' `
        -Headers $headers -ContentType 'application/json' -Body $operation
    $reconciliation = Invoke-RestMethod -Method Post -Uri "http://localhost:8082/wallets/$($wallet.id)/reconciliation" `
        -Headers @{Authorization="Bearer $internal"}
    if ($first.transactionId -ne $replay.transactionId -or !$replay.idempotentReplay -or
        $replay.balance.amount -ne '75.00' -or !$reconciliation.consistent -or $reconciliation.checkedEntries -ne 2) {
        throw 'Restart/replay invariant failed'
    }
    Write-Output 'PASS: all three processes killed; original transaction replayed, balance 75.00, exactly two ledger entries.'
    $originalBet = @{
        providerId='provider-a';externalTransactionId=$pendingBetId
        playerId=$wallet.playerId;walletId=$wallet.id;roundId='pending-restart';gameId='demo';kind='BET'
        money=@{amount='5.00';currency='BRL'}
    }
    Invoke-RestMethod -Method Post -Uri 'http://localhost:8081/wagering/transactions' `
        -Headers @{Authorization="Bearer $provider";'Idempotency-Key'=[guid]::NewGuid().ToString()} `
        -ContentType 'application/json' -Body ($originalBet | ConvertTo-Json) | Out-Null
    $resolved = $null
    for ($i=0; $i -lt 30; $i++) {
        $resolved = Invoke-RestMethod -Uri "http://localhost:8082/wagering/transactions/$($pending.transactionId)" `
            -Headers @{Authorization="Bearer $provider"}
        if ($resolved.status -ne 'PENDING_REFERENCE') { break }
        Start-Sleep -Seconds 1
    }
    $reconciliation = Invoke-RestMethod -Method Post -Uri "http://localhost:8082/wallets/$($wallet.id)/reconciliation" `
        -Headers @{Authorization="Bearer $internal"}
    if ($resolved.status -ne 'PROCESSED' -or $resolved.transactionId -ne $pending.transactionId -or
        !$reconciliation.consistent -or $reconciliation.storedBalance.amount -ne '75.00' -or $reconciliation.checkedEntries -ne 4) {
        throw 'Pending reference recovery after restart failed'
    }
    Write-Output 'PASS: pending refund survived all three processes being killed and resolved once after original bet arrived.'
} finally { Pop-Location }
