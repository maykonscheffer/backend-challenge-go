$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
# A fresh, generated project owns only disposable resources from this execution.
$cleanProject = 'wallet-clean-' + [guid]::NewGuid().ToString('N').Substring(0, 10)
$composeArgs = @('compose', '-p', $cleanProject, '-f', 'compose.yaml', '-f', 'compose.clean.yaml')
try {
    Write-Output "Testing fresh project: $cleanProject"
    & docker @composeArgs up -d --build
    if ($LASTEXITCODE -ne 0) { throw 'Fresh Compose startup failed' }
    & docker @composeArgs --profile test run --rm tests go test -race -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Fresh integration suite failed' }
    & docker @composeArgs run --rm --entrypoint python broker-init /bootstrap/test-broker-iam.py
    if ($LASTEXITCODE -ne 0) { throw 'Fresh IAM test failed' }
    Write-Output 'PASS: fresh Compose, empty data/cache/credential volumes, full race suite and IAM tests.'
} finally {
    # Never use the main project name here: remove only this generated test project.
    & docker @composeArgs down --volumes --remove-orphans
    if ($LASTEXITCODE -ne 0) { Write-Warning "Cleanup failed for disposable project $cleanProject" }
    Pop-Location
}
