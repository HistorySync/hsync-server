param(
    [ValidateSet(
        "help",
        "deps",
        "build",
        "run",
        "test",
        "test-no-race",
        "test-smoke",
        "test-integration",
        "lint",
        "clean",
        "dev",
        "docker-build",
        "docker-up-simple",
        "docker-down-simple",
        "docker-up",
        "docker-down",
        "migrate-up",
        "migrate-down",
        "migrate-create",
        "gen-key"
    )]
    [string]$Task = "help",

    [string]$Name = ""
)

$ErrorActionPreference = "Stop"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$BuildDir = Join-Path $RepoRoot "build"
$Binary = Join-Path $BuildDir "hsync-server.exe"
$CmdDir = "./cmd/hsync-server"

Set-Location $RepoRoot

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$Arguments = @()
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath exited with code $LASTEXITCODE"
    }
}

function Get-GitValue {
    param(
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$Fallback
    )

    $output = & git @Arguments 2>$null
    if ($LASTEXITCODE -eq 0 -and $output) {
        return ($output | Select-Object -First 1).Trim()
    }

    return $Fallback
}

function Invoke-Build {
    New-Item -ItemType Directory -Force -Path $BuildDir | Out-Null

    $version = Get-GitValue -Arguments @("describe", "--tags", "--always", "--dirty") -Fallback "dev"
    $commit = Get-GitValue -Arguments @("rev-parse", "--short", "HEAD") -Fallback "unknown"
    $buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = "-s -w -X main.version=$version -X main.commit=$commit -X main.buildDate=$buildDate"

    Invoke-External "go" @("build", "-ldflags", $ldflags, "-o", $Binary, $CmdDir)
}

function Show-Help {
    @"
HistorySync CE PowerShell helper

Usage:
  .\scripts\dev.ps1 <task> [-Name <migration_name>]

Tasks:
  deps                 go mod download; go mod tidy
  build                build .\build\hsync-server.exe
  run                  build and run the server
  test                 run unit tests with -race
  test-no-race         run unit tests without -race
  test-smoke           run production readiness smoke checks
  test-integration     run DB-backed integration tests
  lint                 run golangci-lint
  clean                remove .\build
  dev                  run air hot reload
  docker-build         build the Docker image
  docker-up-simple     start the simple compose stack
  docker-down-simple   stop the simple compose stack
  docker-up            start the full compose stack
  docker-down          stop the full compose stack
  migrate-up           apply pending migrations
  migrate-down         roll back one migration
  migrate-create       create a migration pair; pass -Name <name>
  gen-key              print a base64 32-byte secret
"@
}

switch ($Task) {
    "help" { Show-Help }
    "deps" {
        Invoke-External "go" @("mod", "download")
        Invoke-External "go" @("mod", "tidy")
    }
    "build" { Invoke-Build }
    "run" {
        Invoke-Build
        Invoke-External $Binary
    }
    "test" { Invoke-External "go" @("test", "-race", "-count=1", "-timeout", "60s", "./...") }
    "test-no-race" { Invoke-External "go" @("test", "-count=1", "-timeout", "60s", "./...") }
    "test-smoke" { Invoke-External "go" @("test", "-tags=smoke", "-count=1", "-timeout", "300s", "./cmd/hsync-server") }
    "test-integration" { Invoke-External "go" @("test", "-tags=integration", "-count=1", "-timeout", "300s", "./pkg/repository/...") }
    "lint" { Invoke-External "golangci-lint" @("run", "./...") }
    "clean" {
        if (Test-Path -LiteralPath $BuildDir) {
            Remove-Item -LiteralPath $BuildDir -Recurse -Force
        }
    }
    "dev" { Invoke-External "air" @("-c", ".air.toml") }
    "docker-build" { Invoke-External "docker" @("build", "-t", "historysync/server:latest", "-f", "Dockerfile", ".") }
    "docker-up-simple" { Invoke-External "docker" @("compose", "-f", "deployments/docker-compose.simple.yml", "up", "-d") }
    "docker-down-simple" { Invoke-External "docker" @("compose", "-f", "deployments/docker-compose.simple.yml", "down") }
    "docker-up" { Invoke-External "docker" @("compose", "-f", "deployments/docker-compose.full.yml", "up", "-d") }
    "docker-down" { Invoke-External "docker" @("compose", "-f", "deployments/docker-compose.full.yml", "down") }
    "migrate-up" { Invoke-External "go" @("run", $CmdDir, "migrate", "up") }
    "migrate-down" { Invoke-External "go" @("run", $CmdDir, "migrate", "down", "1") }
    "migrate-create" {
        if ([string]::IsNullOrWhiteSpace($Name)) {
            throw "Pass -Name <migration_name> when using migrate-create."
        }
        Invoke-External "go" @("run", $CmdDir, "migrate", "create", $Name)
    }
    "gen-key" {
        $bytes = New-Object byte[] 32
        $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
        try {
            $rng.GetBytes($bytes)
            [Convert]::ToBase64String($bytes)
        }
        finally {
            $rng.Dispose()
        }
    }
}
