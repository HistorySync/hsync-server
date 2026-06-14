param(
    [string]$JSONReportPath = (Join-Path "build" "vuln" "govulncheck-ce.json"),
    [string]$TextReportPath = (Join-Path "build" "vuln" "govulncheck-ce.txt")
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$GovulncheckVersion = "v1.3.0"
$ToolBinDir = Join-Path $RepoRoot "build\tools\bin"

function Resolve-OutputPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return $Path
    }
    return (Join-Path $RepoRoot $Path)
}

function Get-ExecutableSuffix {
    if ($IsWindows) {
        return ".exe"
    }
    return ""
}

function Install-GoTool {
    param(
        [Parameter(Mandatory = $true)][string]$Module,
        [Parameter(Mandatory = $true)][string]$Version,
        [Parameter(Mandatory = $true)][string]$BinaryName
    )

    New-Item -ItemType Directory -Force -Path $ToolBinDir | Out-Null
    $toolPath = Join-Path $ToolBinDir ($BinaryName + (Get-ExecutableSuffix))
    if (Test-Path -LiteralPath $toolPath) {
        return $toolPath
    }

    $previousGobin = $env:GOBIN
    try {
        $env:GOBIN = $ToolBinDir
        & go install "$Module@$Version"
        if ($LASTEXITCODE -ne 0) {
            throw "go install $Module@$Version failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        if ($null -ne $previousGobin) {
            $env:GOBIN = $previousGobin
        }
        else {
            Remove-Item Env:\GOBIN -ErrorAction SilentlyContinue
        }
    }

    if (-not (Test-Path -LiteralPath $toolPath)) {
        throw "expected tool $BinaryName at $toolPath"
    }
    return $toolPath
}

$resolvedJSONReportPath = Resolve-OutputPath -Path $JSONReportPath
$resolvedTextReportPath = Resolve-OutputPath -Path $TextReportPath
foreach ($path in @($resolvedJSONReportPath, $resolvedTextReportPath)) {
    $parent = Split-Path -Parent $path
    if ($parent) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
}

$govulncheck = Install-GoTool -Module "golang.org/x/vuln/cmd/govulncheck" -Version $GovulncheckVersion -BinaryName "govulncheck"

Push-Location $RepoRoot
try {
    & $govulncheck "-json" "./..." 1> $resolvedJSONReportPath
    $jsonExitCode = $LASTEXITCODE
    & $govulncheck "./..." 1> $resolvedTextReportPath
    $textExitCode = $LASTEXITCODE
}
finally {
    Pop-Location
}

if ($jsonExitCode -ne 0) {
    exit $jsonExitCode
}
if ($textExitCode -ne 0) {
    exit $textExitCode
}
