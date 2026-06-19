# Release Artifact Verification

This repository writes a machine-readable artifact manifest during release
preparation so operators can verify that a binary or container image matches a
specific source commit without relying on an external supply-chain platform.

## Generate release artifacts

From the CE repository root:

```powershell
make vuln-check
make sbom
```

The main outputs are:

- `build/release-artifact-manifest-ce.json`
- `build/release-report-ce.json`
- `build/release-summary-ce.txt`
- `build/sbom/go-modules-ce.cdx.json`
- `build/sbom/image-ce.cdx.json`
- `build/vuln/govulncheck-ce.json`
- `build/vuln/govulncheck-ce.txt`

## Read the CE release evidence

Open `build/release-report-ce.json` and start with
`release_evidence.overall_status`.

- `ok`: archive the JSON, human summary, SBOM, vulnerability reports, and
  artifact manifest with the release tag.
- `warn`: review the named warning sections and record operator sign-off before
  promotion.
- `error`: do not release; inspect `release_evidence.blocking_failures` and the
  referenced logs under `build/release-check/`.

`release_evidence.operator_next_action` is the short operator-facing decision.
The evidence bundle records paths, status, hashes, and counts only and should
not contain raw secrets, DSNs, tokens, or provider credentials.

`build/release-artifact-manifest-ce.json` records:

- `commit`
- `build_info`
- `binary.sha256`
- `image.digest`
- `sbom.go_modules.path`
- `sbom.docker_image.path`

## Verify a CE binary against a commit

1. Compare the binary hash with the manifest:

```powershell
Get-FileHash .\build\artifacts\hsync-server.exe -Algorithm SHA256
```

The printed SHA-256 must match `binary.sha256` in
`build/release-artifact-manifest-ce.json`.

2. Read the embedded build metadata directly from the binary:

```powershell
.\build\artifacts\hsync-server.exe version --format json
```

Confirm that `build_info.commit` matches the commit you expect and that the
other fields line up with the manifest's `build_info`.

## Verify a CE image against a commit

1. Compare the local image digest with the manifest:

```powershell
docker image inspect historysync/server:release-<short-commit> --format '{{.Id}}'
```

That digest must match `image.digest` in the artifact manifest.

2. Check the OCI revision label:

```powershell
docker image inspect historysync/server:release-<short-commit> --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'
```

The revision label must match the manifest `commit`.

3. If the image has been pushed to a registry, compare any populated
`RepoDigests` output with `image.repo_digests`.
