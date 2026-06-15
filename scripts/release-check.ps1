param(
    [string]$ReportPath = (Join-Path "build" "release-report-ce.json"),
    [switch]$KeepEnvironment
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$ArtifactsDir = Join-Path $RepoRoot "build\release-check"
$ComposeFile = Join-Path $RepoRoot "deployments\docker-compose.release-check.yml"
$EnvFile = Join-Path $ArtifactsDir ".env.release-check"
$ReleaseConfigPath = Join-Path $RepoRoot "config.release-check.yaml"
$ArtifactManifestPath = Join-Path $RepoRoot "build\release-artifact-manifest-ce.json"
$VulnJSONPath = Join-Path $RepoRoot "build\vuln\govulncheck-ce.json"
$VulnTextPath = Join-Path $RepoRoot "build\vuln\govulncheck-ce.txt"
$OpsWebhookURL = "https://ops.example.invalid/historysync"
$ServerStdoutPath = Join-Path $ArtifactsDir "server.stdout.log"
$ServerStderrPath = Join-Path $ArtifactsDir "server.stderr.log"

function Get-ExecutableSuffix {
    if ($IsWindows) {
        return ".exe"
    }
    return ""
}

$BinaryPath = Join-Path $RepoRoot ("build\artifacts\hsync-server" + (Get-ExecutableSuffix))

New-Item -ItemType Directory -Force -Path $ArtifactsDir | Out-Null

$resolvedReportPath = if ([System.IO.Path]::IsPathRooted($ReportPath)) {
    $ReportPath
} else {
    Join-Path $RepoRoot $ReportPath
}
$reportParent = Split-Path -Parent $resolvedReportPath
if ($reportParent) {
    New-Item -ItemType Directory -Force -Path $reportParent | Out-Null
}

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$Arguments = @(),
        [switch]$AllowFailure
    )

    & $FilePath @Arguments
    $exitCode = $LASTEXITCODE
    if (-not $AllowFailure -and $exitCode -ne 0) {
        throw "$FilePath exited with code $exitCode"
    }
    return $exitCode
}

function Invoke-CapturedCommand {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$Arguments = @(),
        [Parameter(Mandatory = $true)][string]$StdoutPath,
        [Parameter(Mandatory = $true)][string]$StderrPath
    )

    $stdout = [System.IO.Path]::GetFullPath($StdoutPath)
    $stderr = [System.IO.Path]::GetFullPath($StderrPath)
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $stdout) | Out-Null
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $stderr) | Out-Null

    $startParams = @{
        FilePath               = $FilePath
        WorkingDirectory       = $RepoRoot
        RedirectStandardOutput = $stdout
        RedirectStandardError  = $stderr
        PassThru               = $true
    }
    if ($Arguments.Count -gt 0) {
        $startParams["ArgumentList"] = $Arguments
    }
    if ($IsWindows) {
        $startParams["WindowStyle"] = "Hidden"
    }
    $process = Start-Process @startParams
    return $process
}

function Stop-ProcessTree {
    param([System.Diagnostics.Process]$Process)

    if ($null -eq $Process) {
        return
    }
    try {
        if (-not $Process.HasExited) {
            if ($IsWindows) {
                Get-CimInstance Win32_Process -Filter "ParentProcessId = $($Process.Id)" -ErrorAction SilentlyContinue |
                    ForEach-Object {
                        Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
                    }
            }
            Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
            $Process.WaitForExit(5000) | Out-Null
        }
    }
    catch {
    }
}

function New-Base64Secret([int]$Bytes = 32) {
    $buffer = New-Object byte[] $Bytes
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($buffer)
        return [Convert]::ToBase64String($buffer)
    }
    finally {
        $rng.Dispose()
    }
}

function Wait-ForHTTP {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [int]$TimeoutSeconds = 90
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5
            if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) {
                return
            }
        }
        catch {
        }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for $Url"
}

function Wait-ForDockerHealth {
    param(
        [Parameter(Mandatory = $true)][string]$Service,
    [int]$TimeoutSeconds = 120
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $containerId = (docker compose --env-file $EnvFile -f $ComposeFile ps -q $Service 2>$null | Select-Object -First 1).Trim()
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($containerId)) {
            $health = (docker inspect --format "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}" $containerId 2>$null | Select-Object -First 1).Trim()
            if ($LASTEXITCODE -eq 0 -and ($health -eq "healthy" -or $health -eq "running")) {
                return
            }
        }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for docker service $Service"
}

function Convert-CommandForDisplay {
    param([string[]]$Parts)

    return ($Parts | ForEach-Object {
        if ($_ -match '\s') { '"' + $_ + '"' } else { $_ }
    }) -join ' '
}

function Get-RelativePath {
    param([Parameter(Mandatory = $true)][string]$Path)

    return [System.IO.Path]::GetRelativePath($RepoRoot, $Path).Replace("\", "/")
}

function New-ReleaseStep {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string[]]$Command
    )

    [PSCustomObject]@{
        name = $Name
        command = $Command
        started_at = $null
        finished_at = $null
        duration_ms = 0
        status = "pending"
        exit_code = $null
        detail = $null
        stdout_path = $null
        stderr_path = $null
    }
}

function Start-Step {
    param([Parameter(Mandatory = $true)]$Step)

    $Step.started_at = (Get-Date).ToUniversalTime()
}

function Complete-StepFromResult {
    param(
        [Parameter(Mandatory = $true)]$Step,
        [int]$ExitCode,
        [string]$Status,
        [string]$Detail
    )

    $Step.finished_at = (Get-Date).ToUniversalTime()
    $Step.duration_ms = [int64](New-TimeSpan -Start $Step.started_at -End $Step.finished_at).TotalMilliseconds
    $Step.exit_code = $ExitCode
    $Step.status = $Status
    if ($Detail) {
        $Step.detail = $Detail
    }
}

function Fail-Step {
    param(
        [Parameter(Mandatory = $true)]$Step,
        [string]$Detail,
        [int]$ExitCode = 1
    )

    if (-not $Step.started_at) {
        $Step.started_at = (Get-Date).ToUniversalTime()
    }
    Complete-StepFromResult -Step $Step -ExitCode $ExitCode -Status "failed" -Detail $Detail
}

function Invoke-ReleaseStep {
    param(
        [Parameter(Mandatory = $true)]$Step,
        [scriptblock]$Inspector
    )

    $stdoutFile = Join-Path $ArtifactsDir ($Step.name + ".stdout.log")
    $stderrFile = Join-Path $ArtifactsDir ($Step.name + ".stderr.log")
    $Step.stdout_path = $stdoutFile
    $Step.stderr_path = $stderrFile
    $Step.started_at = (Get-Date).ToUniversalTime()

    $arguments = @()
    if ($Step.command.Length -gt 1) {
        $arguments = @($Step.command[1..($Step.command.Length - 1)])
    }
    & $Step.command[0] @arguments 1> $stdoutFile 2> $stderrFile
    $exitCode = $LASTEXITCODE

    if ($null -ne $Inspector) {
        $inspection = & $Inspector $stdoutFile $stderrFile $exitCode
        Complete-StepFromResult -Step $Step -ExitCode $exitCode -Status $inspection.Status -Detail $inspection.Detail
    }
    elseif ($exitCode -eq 0) {
        Complete-StepFromResult -Step $Step -ExitCode $exitCode -Status "passed" -Detail ""
    }
    else {
        Complete-StepFromResult -Step $Step -ExitCode $exitCode -Status "failed" -Detail "Command exited with code $exitCode."
    }
}

function Invoke-EnvironmentStep {
    param(
        [Parameter(Mandatory = $true)]$Step,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )

    $stdoutFile = Join-Path $ArtifactsDir ($Step.name + ".stdout.log")
    $stderrFile = Join-Path $ArtifactsDir ($Step.name + ".stderr.log")
    $Step.stdout_path = $stdoutFile
    $Step.stderr_path = $stderrFile
    Start-Step -Step $Step

    try {
        & $Action $stdoutFile $stderrFile
        Complete-StepFromResult -Step $Step -ExitCode 0 -Status "passed" -Detail ""
    }
    catch {
        $message = $_.Exception.Message
        if (-not [string]::IsNullOrWhiteSpace($_.ScriptStackTrace)) {
            $message = $message + [Environment]::NewLine + $_.ScriptStackTrace
        }
        Set-Content -LiteralPath $stderrFile -Value $message
        Fail-Step -Step $Step -Detail $_.Exception.Message
        throw
    }
}

function Test-JSONOverallOK {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$PropertyName
    )

    $raw = Get-Content -LiteralPath $Path -Raw
    $json = $raw | ConvertFrom-Json
    $overall = [string]$json.$PropertyName
    if ($overall -eq "ok") {
        return [PSCustomObject]@{ Status = "passed"; Detail = "$PropertyName=ok" }
    }
    return [PSCustomObject]@{ Status = "failed"; Detail = "$PropertyName=$overall" }
}

function Test-MigrateStatusJSON {
    param([string]$Path)

    $raw = Get-Content -LiteralPath $Path -Raw
    $json = $raw | ConvertFrom-Json
    $consistent = [bool]$json.consistent
    $pendingCount = @($json.pending).Count
    $trackingTableOk = [bool]$json.tracking_table_ok
    if ($consistent -and $trackingTableOk -and $pendingCount -eq 0) {
        return [PSCustomObject]@{ Status = "passed"; Detail = "consistent=$consistent pending=$pendingCount tracking_table_ok=$trackingTableOk" }
    }
    return [PSCustomObject]@{ Status = "failed"; Detail = "consistent=$consistent pending=$pendingCount tracking_table_ok=$trackingTableOk" }
}

function Get-ReportSummary {
    param([array]$Steps)

    $passed = @($Steps | Where-Object { $_.status -eq "passed" } | ForEach-Object { $_.name })
    $failed = @($Steps | Where-Object { $_.status -eq "failed" } | ForEach-Object { $_.name })
    return [PSCustomObject]@{
        passed = $passed
        failed = $failed
    }
}

$gitCommit = (git -C $RepoRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "git rev-parse HEAD failed"
}
$gitVersion = (git -C $RepoRoot describe --tags --always --dirty).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($gitVersion)) {
    $gitVersion = "dev"
}

$jwtKey = New-Base64Secret
$securitySecret = New-Base64Secret
$adminKey = New-Base64Secret
$dbPassword = "hsync"
$s3AccessKey = "minioadmin"
$s3SecretKey = "minioadmin"
$baseUrl = "http://127.0.0.1:18080"
$opsAlertSecret = New-Base64Secret

@"
listen_addr: ":18080"
public_url: "$baseUrl"
database_url: "postgres://hsync:$dbPassword@127.0.0.1:15432/hsync?sslmode=disable"
redis_url: "redis://127.0.0.1:16379/0"
s3_endpoint: "127.0.0.1:19000"
s3_bucket: "hsync-bundles"
s3_access_key: "$s3AccessKey"
s3_secret_key: "$s3SecretKey"
s3_use_ssl: false
jwt_private_key: "$jwtKey"
security_secret: "$securitySecret"
admin_key: "$adminKey"
metrics_enabled: true
metrics_path: "/metrics"
metrics_allowed_cidrs:
  - "127.0.0.1/32"
rate_limit_fail_mode: "fail_closed"
rate_limit_public_auth_fail_mode: "fail_closed"
rate_limit_enterprise_admin_fail_mode: "fail_closed"
rate_limit_enterprise_billing_fail_mode: "fail_closed"
rate_limit_redis_unavailable_fallback: "deny"
websocket_origin_check_disabled: false
websocket_allowed_origins:
  - "$baseUrl"
websocket_max_connections: 8
websocket_max_connections_per_user: 2
background_tasks_enabled: true
notifications_enabled: true
notification_outbox_interval: "1s"
quota_warning_threshold: 1
quota_exhausted_threshold: 95
ops_alert_email: "ops@example.com"
ops_alert_webhook_url: "$OpsWebhookURL"
ops_alert_webhook_secret: "$opsAlertSecret"
stripe_disabled: true
"@ | Set-Content -LiteralPath $ReleaseConfigPath

@"
DB_PASSWORD=$dbPassword
S3_ACCESS_KEY=$s3AccessKey
S3_SECRET_KEY=$s3SecretKey
"@ | Set-Content -LiteralPath $EnvFile

$steps = @(
    (New-ReleaseStep -Name "vuln-check" -Command @("pwsh", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $RepoRoot "scripts\vuln-check.ps1"))),
    (New-ReleaseStep -Name "artifact-manifest" -Command @("pwsh", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $RepoRoot "scripts\supply-chain.ps1"), "-ManifestPath", $ArtifactManifestPath, "-BinaryPath", $BinaryPath)),
    (New-ReleaseStep -Name "environment-setup" -Command @("docker", "compose", "up")),
    (New-ReleaseStep -Name "server-ready" -Command @($BinaryPath)),
    (New-ReleaseStep -Name "go-test" -Command @("go", "test", "-count=1", "-timeout", "60s", "./...")),
    (New-ReleaseStep -Name "openapi-compatibility" -Command @("go", "test", "./docs/api")),
    (New-ReleaseStep -Name "migrate-status" -Command @($BinaryPath, "migrate", "status", "--json")),
    (New-ReleaseStep -Name "doctor-json" -Command @($BinaryPath, "doctor", "--format", "json")),
    (New-ReleaseStep -Name "ops-rehearsal" -Command @($BinaryPath, "ops", "rehearsal", "--format", "json")),
    (New-ReleaseStep -Name "smoke" -Command @("go", "test", "-tags=smoke", "-count=1", "-timeout", "300s", "./cmd/hsync-server"))
)

$serverProcess = $null
$releaseStartedAt = (Get-Date).ToUniversalTime()
$releasePassed = $false
$releaseError = $null

try {
    Push-Location $RepoRoot
    $previousExtraFiles = $env:HSYNC_CONFIG_EXTRA_FILES
    $env:HSYNC_CONFIG_EXTRA_FILES = "config.release-check"

    Invoke-ReleaseStep -Step $steps[0]
    Invoke-ReleaseStep -Step $steps[1]

    Invoke-EnvironmentStep -Step $steps[2] -Action {
        param($stdoutFile, $stderrFile)
        docker compose --env-file $EnvFile -f $ComposeFile up -d 1> $stdoutFile 2> $stderrFile
        if ($LASTEXITCODE -ne 0) {
            throw "docker compose up failed with exit code $LASTEXITCODE"
        }
        Wait-ForDockerHealth -Service "postgres"
        Wait-ForDockerHealth -Service "redis"
        Wait-ForDockerHealth -Service "minio"
        & $BinaryPath "migrate" "up" 1>> $stdoutFile 2>> $stderrFile
        if ($LASTEXITCODE -ne 0) {
            throw "$BinaryPath migrate up failed with exit code $LASTEXITCODE"
        }
    }
    $steps[2].detail = "Docker dependencies started and migrations applied."

    Invoke-EnvironmentStep -Step $steps[3] -Action {
        param($stdoutFile, $stderrFile)
        $script:serverProcess = Invoke-CapturedCommand -FilePath $BinaryPath -StdoutPath $ServerStdoutPath -StderrPath $ServerStderrPath
        Wait-ForHTTP -Url "$baseUrl/readyz" -TimeoutSeconds 120
        Set-Content -LiteralPath $stdoutFile -Value "Server ready at $baseUrl/readyz"
    }
    $steps[3].detail = "Server reached /readyz."

    Invoke-ReleaseStep -Step $steps[4]
    Invoke-ReleaseStep -Step $steps[5]
    Invoke-ReleaseStep -Step $steps[6] -Inspector {
        param($stdout, $stderr, $exitCode)
        if ($exitCode -ne 0) {
            return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
        }
        return Test-MigrateStatusJSON -Path $stdout
    }
    Invoke-ReleaseStep -Step $steps[7] -Inspector {
        param($stdout, $stderr, $exitCode)
        if ($exitCode -ne 0) {
            return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
        }
        return Test-JSONOverallOK -Path $stdout -PropertyName "overall"
    }
    Invoke-ReleaseStep -Step $steps[8] -Inspector {
        param($stdout, $stderr, $exitCode)
        if ($exitCode -ne 0) {
            return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
        }
        return Test-JSONOverallOK -Path $stdout -PropertyName "overall"
    }
    Invoke-ReleaseStep -Step $steps[9]

    $summary = Get-ReportSummary -Steps $steps
    $releasePassed = ($summary.failed.Count -eq 0)
    if (-not $releasePassed) {
        throw "Release gate failed."
    }
}
catch {
    $releaseError = $_
}
finally {
    $releaseFinishedAt = (Get-Date).ToUniversalTime()
    $durationMs = [int64](New-TimeSpan -Start $releaseStartedAt -End $releaseFinishedAt).TotalMilliseconds
    $summary = Get-ReportSummary -Steps $steps
    $artifactManifest = $null
    if (Test-Path -LiteralPath $ArtifactManifestPath) {
        $artifactManifest = Get-Content -LiteralPath $ArtifactManifestPath -Raw | ConvertFrom-Json
    }

    $report = [ordered]@{
        commit = $gitCommit
        version = $gitVersion
        edition = "community"
        build_info = $(if ($artifactManifest) { $artifactManifest.build_info } else { $null })
        artifact_manifest_path = $(if (Test-Path -LiteralPath $ArtifactManifestPath) { Get-RelativePath -Path $ArtifactManifestPath } else { $null })
        vulnerability_reports = [ordered]@{
            govulncheck_json = $(if (Test-Path -LiteralPath $VulnJSONPath) { Get-RelativePath -Path $VulnJSONPath } else { $null })
            govulncheck_text = $(if (Test-Path -LiteralPath $VulnTextPath) { Get-RelativePath -Path $VulnTextPath } else { $null })
        }
        artifacts = $(if ($artifactManifest) {
            [ordered]@{
                binary = $artifactManifest.binary
                image = $artifactManifest.image
                sbom = $artifactManifest.sbom
            }
        } else {
            $null
        })
        started_at = $releaseStartedAt.ToString("o")
        finished_at = $releaseFinishedAt.ToString("o")
        duration_ms = $durationMs
        overall = $(if ($releasePassed) { "passed" } else { "failed" })
        passed_steps = $summary.passed
        failed_steps = $summary.failed
        steps = @($steps | ForEach-Object {
            [ordered]@{
                name = $_.name
                command = (Convert-CommandForDisplay -Parts $_.command)
                status = $_.status
                exit_code = $_.exit_code
                detail = $_.detail
                started_at = $(if ($_.started_at) { $_.started_at.ToString("o") } else { $null })
                finished_at = $(if ($_.finished_at) { $_.finished_at.ToString("o") } else { $null })
                duration_ms = $_.duration_ms
                stdout_path = $_.stdout_path
                stderr_path = $_.stderr_path
            }
        })
    }
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resolvedReportPath

    Stop-ProcessTree -Process $serverProcess
    if ($null -ne $previousExtraFiles) {
        $env:HSYNC_CONFIG_EXTRA_FILES = $previousExtraFiles
    }
    else {
        Remove-Item Env:\HSYNC_CONFIG_EXTRA_FILES -ErrorAction SilentlyContinue
    }
    if (-not $KeepEnvironment) {
        try {
            Push-Location $RepoRoot
            Invoke-External -FilePath "docker" -Arguments @("compose", "--env-file", $EnvFile, "-f", $ComposeFile, "down", "-v") -AllowFailure
        }
        finally {
            Pop-Location
        }
    }

    if (Test-Path -LiteralPath $ReleaseConfigPath) {
        Remove-Item -LiteralPath $ReleaseConfigPath -Force
    }
    if (-not $KeepEnvironment -and (Test-Path -LiteralPath $EnvFile)) {
        Remove-Item -LiteralPath $EnvFile -Force
    }

    Pop-Location
}

if (-not $releasePassed) {
    exit 1
}
