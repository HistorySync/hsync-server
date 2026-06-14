param(
    [string]$ManifestPath = (Join-Path "build" "release-artifact-manifest-ce.json"),
    [string]$BinaryPath = "",
    [string]$GoSBOMPath = (Join-Path "build" "sbom" "go-modules-ce.cdx.json"),
    [string]$ImageSBOMPath = (Join-Path "build" "sbom" "image-ce.cdx.json"),
    [string]$ImageTag = "",
    [string]$Version = "",
    [string]$Commit = "",
    [string]$BuildTime = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$BuildPkg = "github.com/historysync/hsync-server/pkg/buildinfo"
$DockerImage = "historysync/server"
$CycloneDXVersion = "v1.10.0"
$SyftVersion = "v1.45.1"
$ToolBinDir = Join-Path $RepoRoot "build\tools\bin"
$ScratchDir = Join-Path $RepoRoot "build\release-artifacts"

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

function Get-RelativePath {
    param([Parameter(Mandatory = $true)][string]$Path)

    return [System.IO.Path]::GetRelativePath($RepoRoot, $Path).Replace("\", "/")
}

function Invoke-Required {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$Arguments = @()
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath exited with code $LASTEXITCODE"
    }
}

function Read-JSONFile {
    param([Parameter(Mandatory = $true)][string]$Path)

    return (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json)
}

Push-Location $RepoRoot
try {
    if ([string]::IsNullOrWhiteSpace($Commit)) {
        $Commit = (git -C $RepoRoot rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw "git rev-parse HEAD failed"
        }
    }
    if ([string]::IsNullOrWhiteSpace($Version)) {
        $Version = (git -C $RepoRoot describe --tags --always --dirty).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($Version)) {
            $Version = "dev"
        }
    }
    if ([string]::IsNullOrWhiteSpace($BuildTime)) {
        $BuildTime = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
    }

    $resolvedManifestPath = Resolve-OutputPath -Path $ManifestPath
    $resolvedGoSBOMPath = Resolve-OutputPath -Path $GoSBOMPath
    $resolvedImageSBOMPath = Resolve-OutputPath -Path $ImageSBOMPath
    if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
        $BinaryPath = Join-Path "build\artifacts" ("hsync-server" + (Get-ExecutableSuffix))
    }
    $resolvedBinaryPath = Resolve-OutputPath -Path $BinaryPath
    if ([string]::IsNullOrWhiteSpace($ImageTag)) {
        $shortCommit = $Commit.Substring(0, [Math]::Min(12, $Commit.Length))
        $ImageTag = "${DockerImage}:release-$shortCommit"
    }

    foreach ($path in @($resolvedManifestPath, $resolvedGoSBOMPath, $resolvedImageSBOMPath, $resolvedBinaryPath)) {
        $parent = Split-Path -Parent $path
        if ($parent) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
    }
    New-Item -ItemType Directory -Force -Path $ScratchDir | Out-Null

    $cycloneDX = Install-GoTool -Module "github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod" -Version $CycloneDXVersion -BinaryName "cyclonedx-gomod"
    $syft = Install-GoTool -Module "github.com/anchore/syft/cmd/syft" -Version $SyftVersion -BinaryName "syft"

    $ldflags = @(
        "-s",
        "-w",
        "-X", "$BuildPkg.version=$Version",
        "-X", "$BuildPkg.commit=$Commit",
        "-X", "$BuildPkg.buildTime=$BuildTime",
        "-X", "$BuildPkg.edition=community"
    ) -join " "

    Invoke-Required -FilePath "go" -Arguments @("build", "-ldflags", $ldflags, "-o", $resolvedBinaryPath, "./cmd/hsync-server")

    $versionJSONPath = Join-Path $ScratchDir "binary-version-ce.json"
    & $resolvedBinaryPath "version" "--format" "json" 1> $versionJSONPath
    if ($LASTEXITCODE -ne 0) {
        throw "built binary version command failed with exit code $LASTEXITCODE"
    }
    $versionPayload = Read-JSONFile -Path $versionJSONPath
    $buildInfo = $versionPayload.build_info

    Invoke-Required -FilePath $cycloneDX -Arguments @("mod", "-licenses", "-json", "-output", $resolvedGoSBOMPath)

    $iidFile = Join-Path $ScratchDir "image-ce.iid"
    Invoke-Required -FilePath "docker" -Arguments @(
        "build",
        "--iidfile", $iidFile,
        "--build-arg", "VERSION=$Version",
        "--build-arg", "COMMIT=$Commit",
        "--build-arg", "BUILD_TIME=$BuildTime",
        "-t", $ImageTag,
        "-f", "Dockerfile",
        "."
    )

    $imageDigest = (Get-Content -LiteralPath $iidFile -Raw).Trim()
    if ([string]::IsNullOrWhiteSpace($imageDigest)) {
        throw "docker build did not write an image digest to $iidFile"
    }

    $imageArchivePath = Join-Path $ScratchDir "image-ce.tar"
    try {
        Invoke-Required -FilePath "docker" -Arguments @("save", "--output", $imageArchivePath, $ImageTag)
        & $syft "docker-archive:$imageArchivePath" "-o" "cyclonedx-json" 1> $resolvedImageSBOMPath
        if ($LASTEXITCODE -ne 0) {
            throw "syft exited with code $LASTEXITCODE"
        }
    }
    finally {
        if (Test-Path -LiteralPath $imageArchivePath) {
            Remove-Item -LiteralPath $imageArchivePath -Force
        }
    }

    $repoDigestsRaw = (docker image inspect $ImageTag --format "{{json .RepoDigests}}").Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($repoDigestsRaw)) {
        throw "docker image inspect repo digests failed"
    }
    $repoDigests = @()
    $repoDigestsParsed = $repoDigestsRaw | ConvertFrom-Json
    if ($null -ne $repoDigestsParsed) {
        $repoDigests = @($repoDigestsParsed)
    }

    $labelJSON = (docker image inspect $ImageTag --format "{{json .Config.Labels}}").Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($labelJSON)) {
        throw "docker image inspect labels failed"
    }
    $labels = $labelJSON | ConvertFrom-Json

    $binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $resolvedBinaryPath).Hash.ToLowerInvariant()
    $binaryInfo = Get-Item -LiteralPath $resolvedBinaryPath

    $manifest = [ordered]@{
        schema_version = 1
        generated_at = [DateTime]::UtcNow.ToString("o")
        edition = "community"
        commit = $Commit
        version = $Version
        build_info = [ordered]@{
            version = $buildInfo.version
            commit = $buildInfo.commit
            build_time = $buildInfo.build_time
            edition = $buildInfo.edition
            schema_version = $buildInfo.schema_version
        }
        binary = [ordered]@{
            path = (Get-RelativePath -Path $resolvedBinaryPath)
            sha256 = $binaryHash
            size_bytes = $binaryInfo.Length
        }
        image = [ordered]@{
            tag = $ImageTag
            digest = $imageDigest
            repo_digests = $repoDigests
            labels = [ordered]@{
                title = $labels."org.opencontainers.image.title"
                version = $labels."org.opencontainers.image.version"
                revision = $labels."org.opencontainers.image.revision"
                created = $labels."org.opencontainers.image.created"
            }
        }
        sbom = [ordered]@{
            go_modules = [ordered]@{
                path = (Get-RelativePath -Path $resolvedGoSBOMPath)
                format = "cyclonedx-json"
            }
            docker_image = [ordered]@{
                path = (Get-RelativePath -Path $resolvedImageSBOMPath)
                format = "cyclonedx-json"
            }
        }
    }

    $manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resolvedManifestPath
}
finally {
    Pop-Location
}
