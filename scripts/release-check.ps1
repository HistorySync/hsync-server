param(
    [string]$ReportPath = (Join-Path "build" "release-report-ce.json"),
    [string]$HumanSummaryPath = (Join-Path "build" "release-summary-ce.txt"),
    [switch]$DryRunReport,
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

$resolvedHumanSummaryPath = if ([System.IO.Path]::IsPathRooted($HumanSummaryPath)) {
    $HumanSummaryPath
} else {
    Join-Path $RepoRoot $HumanSummaryPath
}
$summaryParent = Split-Path -Parent $resolvedHumanSummaryPath
if ($summaryParent) {
    New-Item -ItemType Directory -Force -Path $summaryParent | Out-Null
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

    $sensitiveFlags = @(
        "-admin-key",
        "--admin-key",
        "-token",
        "--token",
        "-password",
        "--password",
        "-secret",
        "--secret",
        "-license-key",
        "--license-key"
    )
    $redacted = @()
    $redactNext = $false
    foreach ($part in $Parts) {
        if ($part -match 'HSYNC_.*(KEY|TOKEN|SECRET|PASSWORD)') {
            $redacted += "[redacted-script]"
            $redactNext = $false
            continue
        }
        if ($redactNext) {
            $redacted += "[redacted]"
            $redactNext = $false
            continue
        }
        $matchedFlag = $false
        foreach ($flag in $sensitiveFlags) {
            if ($part -eq $flag) {
                $matchedFlag = $true
                $redacted += $part
                $redactNext = $true
                break
            }
            if ($part.StartsWith("$flag=")) {
                $matchedFlag = $true
                $redacted += "$flag=[redacted]"
                break
            }
        }
        if (-not $matchedFlag) {
            $redacted += $part
        }
    }

    return ($redacted | ForEach-Object {
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

    if ($Step.status -ne "passed") {
        throw "Step $($Step.name) failed."
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

function Test-CEReleaseLoadReport {
    param([Parameter(Mandatory = $true)][string]$Path)

    $json = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    $failures = @()
    $scenarios = @{}
    foreach ($scenario in @($json.scenarios)) {
        $scenarios[[string]$scenario.name] = $scenario
    }

    foreach ($name in @("ce_register_login", "ce_bundle_snapshot_sync", "ce_notification_outbox_drain")) {
        $scenario = $scenarios[$name]
        if ($null -eq $scenario) {
            $failures += "missing scenario $name"
            continue
        }
        if ([int64]$scenario.errors -ne 0) {
            $failures += "$name errors=$($scenario.errors)"
        }
    }

    foreach ($name in @("ce_rate_limit_fallback", "ce_ws_connect_cap")) {
        $scenario = $scenarios[$name]
        if ($null -eq $scenario) {
            $failures += "missing scenario $name"
            continue
        }
        if ([int64]$scenario.errors -ne 0) {
            $failures += "$name errors=$($scenario.errors)"
        }
        if ([int64]$scenario.rejections -le 0) {
            $failures += "$name rejections=$($scenario.rejections)"
        }
    }

    foreach ($field in @("http_5xx", "other")) {
        if ([int64]$json.status_classes.$field -ne 0) {
            $failures += "status_classes.$field=$($json.status_classes.$field)"
        }
    }

    foreach ($mode in @("memory", "deny", "disable")) {
        if ($json.rate_limit_fallback.$mode -eq $true) {
            $failures += "rate_limit_fallback.$mode=true"
        }
    }

    if ([int64]$json.quota_rollback_count -ne 0) {
        $failures += "quota_rollback_count=$($json.quota_rollback_count)"
    }

    if ($null -eq $json.notification_summary) {
        $failures += "missing notification_summary"
    }
    else {
        if ([int64]$json.notification_summary.failed -ne 0) {
            $failures += "notification_summary.failed=$($json.notification_summary.failed)"
        }
        if ([int64]$json.notification_summary.sent -le 0) {
            $failures += "notification_summary.sent=$($json.notification_summary.sent)"
        }
    }

    if ($failures.Count -eq 0) {
        return [PSCustomObject]@{ Status = "passed"; Detail = "load thresholds met" }
    }
    return [PSCustomObject]@{ Status = "failed"; Detail = ($failures -join "; ") }
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

function Convert-StepStatusLevel {
    param([string]$Status)

    switch ($Status) {
        "passed" { return "ok" }
        "failed" { return "error" }
        "pending" { return "warn" }
        default { return "warn" }
    }
}

function Convert-OverallLevel {
    param([string]$Status)

    switch ($Status) {
        "ok" { return "ok" }
        "passed" { return "ok" }
        "success" { return "ok" }
        "warn" { return "warn" }
        "warning" { return "warn" }
        "degraded" { return "warn" }
        "" { return "warn" }
        default { return "error" }
    }
}

function Merge-StatusLevel {
    param([string[]]$Statuses)

    if ($Statuses -contains "error") {
        return "error"
    }
    if ($Statuses -contains "warn") {
        return "warn"
    }
    return "ok"
}

function Find-Step {
    param(
        [array]$Steps,
        [Parameter(Mandatory = $true)][string]$Name
    )

    return ($Steps | Where-Object { $_.name -eq $Name } | Select-Object -First 1)
}

function Get-StepLevel {
    param(
        [array]$Steps,
        [Parameter(Mandatory = $true)][string]$Name
    )

    $step = Find-Step -Steps $Steps -Name $Name
    if ($null -eq $step) {
        return "warn"
    }
    return Convert-StepStatusLevel -Status $step.status
}

function Read-StepJSON {
    param($Step)

    if ($null -eq $Step -or [string]::IsNullOrWhiteSpace($Step.stdout_path) -or -not (Test-Path -LiteralPath $Step.stdout_path)) {
        return $null
    }
    try {
        return (Get-Content -LiteralPath $Step.stdout_path -Raw | ConvertFrom-Json)
    }
    catch {
        return $null
    }
}

function Get-PropertyArrayCount {
    param(
        $Object,
        [Parameter(Mandatory = $true)][string]$Name
    )

    if ($null -eq $Object -or $null -eq $Object.PSObject.Properties[$Name]) {
        return 0
    }
    return @($Object.PSObject.Properties[$Name].Value).Count
}

function Get-RelativePathOrNull {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) {
        return $null
    }
    return Get-RelativePath -Path $Path
}

function Clear-PreviousReleaseOutputs {
    foreach ($path in @($resolvedReportPath, $resolvedHumanSummaryPath, $ArtifactManifestPath, $VulnJSONPath, $VulnTextPath)) {
        if (-not [string]::IsNullOrWhiteSpace($path) -and (Test-Path -LiteralPath $path)) {
            Remove-Item -LiteralPath $path -Force
        }
    }
}

function Get-NamedCheckStatus {
    param(
        $Json,
        [Parameter(Mandatory = $true)][string[]]$IDs
    )

    if ($null -eq $Json -or $null -eq $Json.PSObject.Properties["checks"]) {
        return "warn"
    }
    foreach ($check in @($Json.PSObject.Properties["checks"].Value)) {
        if ($IDs -contains [string]$check.id) {
            return Convert-OverallLevel -Status ([string]$check.severity)
        }
    }
    return "warn"
}

function Get-RehearsalStepStatus {
    param(
        $Json,
        [Parameter(Mandatory = $true)][string[]]$IDs
    )

    if ($null -eq $Json -or $null -eq $Json.PSObject.Properties["steps"]) {
        return "warn"
    }
    foreach ($step in @($Json.PSObject.Properties["steps"].Value)) {
        if ($IDs -contains [string]$step.id) {
            return Convert-OverallLevel -Status ([string]$step.status)
        }
    }
    return "warn"
}

function New-CEEvidenceSummary {
    param(
        [array]$Steps,
        $ArtifactManifest,
        [bool]$ReleasePassed,
        $ReleaseError,
        [string]$SetupStdoutPath,
        [string]$SetupStderrPath,
        [string]$ReportRelativePath,
        [string]$HumanSummaryRelativePath,
        [string]$ArtifactManifestRelativePath,
        [string]$VulnJSONRelativePath,
        [string]$VulnTextRelativePath
    )

    $migrationStep = Find-Step -Steps $Steps -Name "migration-status"
    $doctorStep = Find-Step -Steps $Steps -Name "doctor"
    $rehearsalStep = Find-Step -Steps $Steps -Name "rehearsal"
    $loadStep = Find-Step -Steps $Steps -Name "load"

    $migration = Read-StepJSON -Step $migrationStep
    $doctor = Read-StepJSON -Step $doctorStep
    $rehearsal = Read-StepJSON -Step $rehearsalStep
    $load = Read-StepJSON -Step $loadStep

    $blockingFailures = @($Steps | Where-Object { $_.status -eq "failed" } | ForEach-Object {
        [ordered]@{
            step = $_.name
            status = "error"
            detail = $_.detail
            stdout_path = $(if ($_.stdout_path) { Get-RelativePath -Path $_.stdout_path } else { $null })
            stderr_path = $(if ($_.stderr_path) { Get-RelativePath -Path $_.stderr_path } else { $null })
        }
    })
    if ($null -ne $ReleaseError -and $blockingFailures.Count -eq 0) {
        $blockingFailures += [ordered]@{
            step = "release-setup"
            status = "error"
            detail = $ReleaseError.Exception.Message
            stdout_path = $(if ($SetupStdoutPath -and (Test-Path -LiteralPath $SetupStdoutPath)) { Get-RelativePath -Path $SetupStdoutPath } else { $null })
            stderr_path = $(if ($SetupStderrPath -and (Test-Path -LiteralPath $SetupStderrPath)) { Get-RelativePath -Path $SetupStderrPath } else { $null })
        }
    }

    $vulnReports = [ordered]@{
        govulncheck_json = $VulnJSONRelativePath
        govulncheck_text = $VulnTextRelativePath
    }
    $vulnStatus = $(if ($vulnReports.govulncheck_json -and $vulnReports.govulncheck_text) { Get-StepLevel -Steps $Steps -Name "vulnerability-check" } else { "warn" })
    $sbomStatus = $(if ($ArtifactManifest -and $ArtifactManifest.sbom -and $ArtifactManifestRelativePath) { Get-StepLevel -Steps $Steps -Name "artifact-verification" } else { "warn" })
    $buildStatus = $(if ($ArtifactManifest -and $ArtifactManifest.build_info) { "ok" } else { "warn" })
    $migrationStatus = $(if ($migration) {
        if (-not [bool]$migration.consistent -or -not [bool]$migration.tracking_table_ok) {
            "error"
        }
        elseif ((Get-PropertyArrayCount -Object $migration -Name "pending") -gt 0 -or (Get-PropertyArrayCount -Object $migration -Name "problems") -gt 0) {
            "warn"
        }
        else {
            "ok"
        }
    } else { "warn" })
    $schemaDriftStatus = Merge-StatusLevel -Statuses @(
        (Get-NamedCheckStatus -Json $doctor -IDs @("schema_drift")),
        (Get-RehearsalStepStatus -Json $rehearsal -IDs @("schema.drift"))
    )
    $doctorStatus = $(if ($doctor) { Convert-OverallLevel -Status ([string]$doctor.overall) } else { "warn" })
    $rehearsalStatus = $(if ($rehearsal) { Convert-OverallLevel -Status ([string]$rehearsal.overall) } else { "warn" })
    $smokeStatus = Get-StepLevel -Steps $Steps -Name "smoke"
    $loadStatus = $(if ($load) {
        if ([int64]$load.status_classes.http_5xx -gt 0 -or [int64]$load.status_classes.other -gt 0 -or [int64]$load.quota_rollback_count -gt 0) {
            "error"
        }
        elseif ([int64]$load.status_classes.http_403 -gt 0 -or [int64]$load.status_classes.http_429 -gt 0) {
            "warn"
        }
        else {
            Get-StepLevel -Steps $Steps -Name "load"
        }
    } else { "warn" })
    $overallStatus = Merge-StatusLevel -Statuses @(
        $(if ($ReleasePassed -and $blockingFailures.Count -eq 0) { "ok" } else { "error" }),
        $buildStatus,
        $migrationStatus,
        $schemaDriftStatus,
        $doctorStatus,
        $rehearsalStatus,
        $smokeStatus,
        $loadStatus,
        $vulnStatus,
        $sbomStatus
    )

    return [ordered]@{
        schema_version = 1
        overall_status = $overallStatus
        report_path = $ReportRelativePath
        human_summary_path = $HumanSummaryRelativePath
        commit = $gitCommit
        version = $gitVersion
        edition = "community"
        build = [ordered]@{
            status = $buildStatus
            build_info = $(if ($ArtifactManifest) { $ArtifactManifest.build_info } else { $null })
            artifact_manifest_path = $ArtifactManifestRelativePath
        }
        migration = [ordered]@{
            status = $migrationStatus
            consistent = $(if ($migration) { $migration.consistent } else { $null })
            tracking_table_ok = $(if ($migration) { $migration.tracking_table_ok } else { $null })
            pending_count = $(if ($migration) { Get-PropertyArrayCount -Object $migration -Name "pending" } else { $null })
            rollback_available_count = $(if ($migration) { Get-PropertyArrayCount -Object $migration -Name "rollback_available" } else { $null })
        }
        schema_drift = [ordered]@{
            status = $schemaDriftStatus
            doctor_status = Get-NamedCheckStatus -Json $doctor -IDs @("schema_drift")
            rehearsal_status = Get-RehearsalStepStatus -Json $rehearsal -IDs @("schema.drift")
        }
        doctor = [ordered]@{
            status = $doctorStatus
            overall = $(if ($doctor) { $doctor.overall } else { $null })
        }
        ops_rehearsal = [ordered]@{
            status = $rehearsalStatus
            overall = $(if ($rehearsal) { $rehearsal.overall } else { $null })
        }
        smoke_load = [ordered]@{
            smoke_status = $smokeStatus
            load_status = $loadStatus
            load_status_classes = $(if ($load) { $load.status_classes } else { $null })
            load_quota_rollback_count = $(if ($load) { $load.quota_rollback_count } else { $null })
        }
        supply_chain = [ordered]@{
            sbom_status = $sbomStatus
            vuln_status = $vulnStatus
            artifact_manifest_path = $ArtifactManifestRelativePath
            sbom = $(if ($ArtifactManifest) { $ArtifactManifest.sbom } else { $null })
            vulnerability_reports = $vulnReports
        }
        blocking_failures = $blockingFailures
        operator_next_action = $(if ($overallStatus -eq "ok") { "Archive this evidence bundle with the release tag, then proceed with the planned upgrade and rollback rehearsal." } elseif ($overallStatus -eq "warn") { "Review the warning sections, confirm the warnings are intentional, and only then promote the release." } else { "Do not release. Inspect blocking_failures and the referenced stdout/stderr logs, fix the failing gate, and rerun make release-check." })
    }
}

function Write-HumanReleaseSummary {
    param(
        [Parameter(Mandatory = $true)]$Summary,
        [Parameter(Mandatory = $true)][string]$Path
    )

    $lines = @(
        "HistorySync CE Release Evidence",
        "overall_status: $($Summary.overall_status)",
        "version: $($Summary.version)",
        "commit: $($Summary.commit)",
        "migration_status: $($Summary.migration.status)",
        "schema_drift_status: $($Summary.schema_drift.status)",
        "doctor_status: $($Summary.doctor.status)",
        "ops_rehearsal_status: $($Summary.ops_rehearsal.status)",
        "smoke_status: $($Summary.smoke_load.smoke_status)",
        "load_status: $($Summary.smoke_load.load_status)",
        "sbom_status: $($Summary.supply_chain.sbom_status)",
        "vuln_status: $($Summary.supply_chain.vuln_status)",
        "artifact_manifest_path: $($Summary.supply_chain.artifact_manifest_path)",
        "govulncheck_json: $($Summary.supply_chain.vulnerability_reports.govulncheck_json)",
        "govulncheck_text: $($Summary.supply_chain.vulnerability_reports.govulncheck_text)",
        "blocking_failures: $(@($Summary.blocking_failures).Count)",
        "operator_next_action: $($Summary.operator_next_action)"
    )
    Set-Content -LiteralPath $Path -Value ($lines -join [Environment]::NewLine)
}

function Complete-DryRunStep {
    param(
        [Parameter(Mandatory = $true)]$Step,
        [string]$StdoutJSON = ""
    )

    $stdoutFile = Join-Path $ArtifactsDir ($Step.name + ".stdout.log")
    $stderrFile = Join-Path $ArtifactsDir ($Step.name + ".stderr.log")
    $Step.stdout_path = $stdoutFile
    $Step.stderr_path = $stderrFile
    Start-Step -Step $Step
    if ($StdoutJSON) {
        Set-Content -LiteralPath $stdoutFile -Value $StdoutJSON
    }
    else {
        Set-Content -LiteralPath $stdoutFile -Value "dry-run"
    }
    Set-Content -LiteralPath $stderrFile -Value ""
    Complete-StepFromResult -Step $Step -ExitCode 0 -Status "passed" -Detail "dry-run report construction"
}

function Invoke-DryRunReport {
    param([array]$Steps)

    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ArtifactManifestPath) | Out-Null
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $VulnJSONPath) | Out-Null

    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "test")
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "openapi-compatibility")
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "migration-status") -StdoutJSON @"
{
  "scope": "community",
  "tracking_table": "schema_migrations",
  "tracking_table_ok": true,
  "consistent": true,
  "applied": [],
  "pending": [],
  "rollback_available": [],
  "problems": []
}
"@
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "doctor") -StdoutJSON @"
{
  "overall": "ok",
  "checks": [
    {"id": "schema_drift", "severity": "ok"}
  ]
}
"@
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "rehearsal") -StdoutJSON @"
{
  "overall": "ok",
  "steps": [
    {"id": "schema.drift", "status": "ok"}
  ]
}
"@
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "smoke")
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "load") -StdoutJSON @"
{
  "status_classes": {"http_403": 0, "http_429": 0, "http_5xx": 0, "other": 0},
  "quota_rollback_count": 0,
  "scenarios": []
}
"@
    Set-Content -LiteralPath $VulnJSONPath -Value "{}"
    Set-Content -LiteralPath $VulnTextPath -Value "dry-run"
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "vulnerability-check")
    $manifest = [ordered]@{
        build_info = [ordered]@{
            version = $gitVersion
            commit = $gitCommit
            build_time = $buildTime
            edition = "community"
            schema_version = 1
        }
        binary = [ordered]@{ path = "build/artifacts/hsync-server"; sha256 = "dry-run"; size_bytes = 0 }
        image = [ordered]@{ tag = "historysync/server:dry-run"; digest = "dry-run"; repo_digests = @() }
        sbom = [ordered]@{
            go_modules = [ordered]@{ path = "build/sbom/go-modules-ce.cdx.json"; format = "cyclonedx-json" }
            docker_image = [ordered]@{ path = "build/sbom/image-ce.cdx.json"; format = "cyclonedx-json" }
        }
    }
    $manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ArtifactManifestPath
    Complete-DryRunStep -Step (Find-Step -Steps $Steps -Name "artifact-verification")
}

function Start-ReleaseDataStack {
    param(
        [Parameter(Mandatory = $true)][string]$StdoutFile,
        [Parameter(Mandatory = $true)][string]$StderrFile
    )

    if ($script:releaseEnvironmentStarted) {
        return
    }
    docker compose --env-file $EnvFile -f $ComposeFile up -d 1> $StdoutFile 2> $StderrFile
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose up failed with exit code $LASTEXITCODE"
    }
    Wait-ForDockerHealth -Service "postgres"
    Wait-ForDockerHealth -Service "redis"
    Wait-ForDockerHealth -Service "minio"
    & $BinaryPath "migrate" "up" 1>> $StdoutFile 2>> $StderrFile
    if ($LASTEXITCODE -ne 0) {
        throw "$BinaryPath migrate up failed with exit code $LASTEXITCODE"
    }
    $script:releaseEnvironmentStarted = $true
}

function Start-ReleaseServer {
    if ($null -ne $script:serverProcess -and -not $script:serverProcess.HasExited) {
        return
    }
    $script:serverProcess = Invoke-CapturedCommand -FilePath $BinaryPath -StdoutPath $ServerStdoutPath -StderrPath $ServerStderrPath
    Wait-ForHTTP -Url "$baseUrl/readyz" -TimeoutSeconds 120
}

$gitCommit = (git -C $RepoRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "git rev-parse HEAD failed"
}
$gitVersion = (git -C $RepoRoot describe --tags --always --dirty).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($gitVersion)) {
    $gitVersion = "dev"
}
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

function Build-ReleaseBinary {
    $ldflags = @(
        "-s",
        "-w",
        "-X", "github.com/historysync/hsync-server/pkg/buildinfo.version=$gitVersion",
        "-X", "github.com/historysync/hsync-server/pkg/buildinfo.commit=$gitCommit",
        "-X", "github.com/historysync/hsync-server/pkg/buildinfo.buildTime=$buildTime",
        "-X", "github.com/historysync/hsync-server/pkg/buildinfo.edition=community"
    ) -join " "
    & go build -ldflags $ldflags -o $BinaryPath ./cmd/hsync-server
    if ($LASTEXITCODE -ne 0) {
        throw "go build ./cmd/hsync-server failed with exit code $LASTEXITCODE"
    }
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
    (New-ReleaseStep -Name "test" -Command @("go", "test", "-count=1", "-timeout", "60s", "./...")),
    (New-ReleaseStep -Name "openapi-compatibility" -Command @("go", "test", "./docs/api")),
    (New-ReleaseStep -Name "migration-status" -Command @($BinaryPath, "migrate", "status", "--json")),
    (New-ReleaseStep -Name "doctor" -Command @($BinaryPath, "doctor", "--format", "json")),
    (New-ReleaseStep -Name "rehearsal" -Command @($BinaryPath, "ops", "rehearsal", "--format", "json")),
    (New-ReleaseStep -Name "smoke" -Command @("go", "test", "-tags=smoke", "-count=1", "-timeout", "300s", "./cmd/hsync-server")),
    (New-ReleaseStep -Name "load" -Command @("go", "run", "./cmd/loadtest", "-json", "-base-url", $baseUrl, "-admin-key", $adminKey)),
    (New-ReleaseStep -Name "vulnerability-check" -Command @("pwsh", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $RepoRoot "scripts\vuln-check.ps1"), "-JSONReportPath", $VulnJSONPath, "-TextReportPath", $VulnTextPath)),
    (New-ReleaseStep -Name "artifact-verification" -Command @("pwsh", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $RepoRoot "scripts\supply-chain.ps1"), "-ManifestPath", $ArtifactManifestPath, "-BinaryPath", $BinaryPath, "-Version", $gitVersion, "-Commit", $gitCommit, "-BuildTime", $buildTime))
)

$serverProcess = $null
$releaseEnvironmentStarted = $false
$releaseStartedAt = (Get-Date).ToUniversalTime()
$releasePassed = $false
$releaseError = $null
$setupStdout = Join-Path $ArtifactsDir "_setup.stdout.log"
$setupStderr = Join-Path $ArtifactsDir "_setup.stderr.log"

try {
    Push-Location $RepoRoot
    $previousExtraFiles = $env:HSYNC_CONFIG_EXTRA_FILES
    $env:HSYNC_CONFIG_EXTRA_FILES = "config.release-check"
    Clear-PreviousReleaseOutputs

    if ($DryRunReport) {
        Invoke-DryRunReport -Steps $steps
    }
    else {
        Build-ReleaseBinary
        Start-ReleaseDataStack -StdoutFile $setupStdout -StderrFile $setupStderr
        Start-ReleaseServer

        Invoke-ReleaseStep -Step $steps[0]
        Invoke-ReleaseStep -Step $steps[1]
        Invoke-ReleaseStep -Step $steps[2] -Inspector {
            param($stdout, $stderr, $exitCode)
            if ($exitCode -ne 0) {
                return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
            }
            return Test-MigrateStatusJSON -Path $stdout
        }
        Invoke-ReleaseStep -Step $steps[3] -Inspector {
            param($stdout, $stderr, $exitCode)
            if ($exitCode -ne 0) {
                return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
            }
            return Test-JSONOverallOK -Path $stdout -PropertyName "overall"
        }
        Invoke-ReleaseStep -Step $steps[4] -Inspector {
            param($stdout, $stderr, $exitCode)
            if ($exitCode -ne 0) {
                return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
            }
            return Test-JSONOverallOK -Path $stdout -PropertyName "overall"
        }
        Invoke-ReleaseStep -Step $steps[5]
        Invoke-ReleaseStep -Step $steps[6] -Inspector {
            param($stdout, $stderr, $exitCode)
            if ($exitCode -ne 0) {
                return [PSCustomObject]@{ Status = "failed"; Detail = "Command exited with code $exitCode." }
            }
            return Test-CEReleaseLoadReport -Path $stdout
        }
        Stop-ProcessTree -Process $serverProcess
        $serverProcess = $null
        Invoke-ReleaseStep -Step $steps[7]
        Invoke-ReleaseStep -Step $steps[8]
    }

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
        schema_version = 1
        edition = "community"
        commit = $gitCommit
        version = $gitVersion
        build_info = $(if ($artifactManifest) { $artifactManifest.build_info } else { $null })
        artifact_manifest_path = $(if (Test-Path -LiteralPath $ArtifactManifestPath) { Get-RelativePath -Path $ArtifactManifestPath } else { $null })
        started_at = $releaseStartedAt.ToString("o")
        finished_at = $releaseFinishedAt.ToString("o")
        duration_ms = $durationMs
        overall_status = $(if ($releasePassed) { "ok" } else { "error" })
        summary = [ordered]@{
            passed = $summary.passed
            failed = $summary.failed
        }
        steps = @($steps | ForEach-Object {
            [ordered]@{
                name = $_.name
                command = (Convert-CommandForDisplay -Parts $_.command)
                status = $_.status
                status_level = (Convert-StepStatusLevel -Status $_.status)
                exit_code = $_.exit_code
                detail = $_.detail
                started_at = $(if ($_.started_at) { $_.started_at.ToString("o") } else { $null })
                finished_at = $(if ($_.finished_at) { $_.finished_at.ToString("o") } else { $null })
                duration_ms = $_.duration_ms
                stdout_path = $_.stdout_path
                stderr_path = $_.stderr_path
            }
        })
        release_evidence = New-CEEvidenceSummary -Steps $steps -ArtifactManifest $artifactManifest -ReleasePassed $releasePassed -ReleaseError $releaseError -SetupStdoutPath $setupStdout -SetupStderrPath $setupStderr -ReportRelativePath (Get-RelativePath -Path $resolvedReportPath) -HumanSummaryRelativePath (Get-RelativePath -Path $resolvedHumanSummaryPath) -ArtifactManifestRelativePath (Get-RelativePathOrNull -Path $ArtifactManifestPath) -VulnJSONRelativePath (Get-RelativePathOrNull -Path $VulnJSONPath) -VulnTextRelativePath (Get-RelativePathOrNull -Path $VulnTextPath)
    }
    $report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $resolvedReportPath
    Write-HumanReleaseSummary -Summary $report.release_evidence -Path $resolvedHumanSummaryPath

    Stop-ProcessTree -Process $serverProcess
    if ($null -ne $previousExtraFiles) {
        $env:HSYNC_CONFIG_EXTRA_FILES = $previousExtraFiles
    }
    else {
        Remove-Item Env:\HSYNC_CONFIG_EXTRA_FILES -ErrorAction SilentlyContinue
    }
    if (-not $KeepEnvironment -and -not $DryRunReport) {
        try {
            Push-Location $RepoRoot
            Invoke-External -FilePath "docker" -Arguments @("compose", "--env-file", $EnvFile, "-f", $ComposeFile, "down", "-v") -AllowFailure | Out-Null
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
